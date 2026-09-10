package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// =============================================================================
// compute 重配置推送（控制面 → compute_ctl）
//
// Neon Cloud / neon_local 的做法（本次实现的对标对象）：
// 控制面在存储路由变化后，构造完整 ComputeSpec，向 compute 外部 HTTP 端口的
// /configure 端点 POST ConfigurationRequest{spec, compute_ctl_config}，并携带
// compute_id 声明的 JWT（control_plane/src/endpoint.rs:1062-1078）；
// compute_ctl 收到后重写 postgresql.conf + pg_reload_conf，无需重启 Postgres
// 即可完成 pageserver / safekeeper 路由热更新（compute.rs:reconfigure）。
//
// 为什么不用内部端口的 /refresh_configuration：
//   1. 它是 Hadron/Lakebase 的"compute 主动刷新"路径，不是 Cloud 主路径；
//   2. 它只"戳"compute 回拉 spec，控制面无法确认结果，且 spec 未变化时直接 no-op；
//   3. 3081 未在任何 Service 暴露。
// =============================================================================

const (
	// defaultComputeCtlPort compute_ctl 外部 HTTP 端口默认值，与 buildComputeDeployment
	// 里 --external-http-port 3080 保持一致。
	defaultComputeCtlPort = 3080
	// defaultReconcileIntervalSeconds 与 SC 对账的默认周期。
	defaultReconcileIntervalSeconds = 30
	// defaultReconfigureTimeoutSeconds 单次 /configure 推送超时。
	// neon_local 使用 120s（endpoint.rs:1058），因为 /configure 会阻塞到 compute
	// 进入 Running / Failed 才返回。
	defaultReconfigureTimeoutSeconds = 120
)

// computeCtlPort 返回 compute_ctl 外部 HTTP 端口（未配置时用默认值）。
func computeCtlPort() int {
	if cfg != nil && cfg.ComputeCtlPort > 0 {
		return cfg.ComputeCtlPort
	}
	return defaultComputeCtlPort
}

// reconcileInterval 返回与 SC 对账的周期。
func reconcileInterval() time.Duration {
	sec := defaultReconcileIntervalSeconds
	if cfg != nil && cfg.ReconcileIntervalSeconds > 0 {
		sec = cfg.ReconcileIntervalSeconds
	}
	return time.Duration(sec) * time.Second
}

// reconfigureTimeout 返回单次 /configure 推送的超时。
func reconfigureTimeout() time.Duration {
	sec := defaultReconfigureTimeoutSeconds
	if cfg != nil && cfg.ReconfigureTimeoutSeconds > 0 {
		sec = cfg.ReconfigureTimeoutSeconds
	}
	return time.Duration(sec) * time.Second
}

// computeCtlBaseURL 返回 compute_ctl 外部 HTTP 服务在集群内的可达地址。
//
// 使用主 Service 的 DNS 名而不是 Pod IP：compute Pod 重建后 IP 会变，而 Service 名固定，
// 控制面无需维护 endpoint → PodIP 的映射表。主 Service 恒为 ClusterIP（见 buildComputeService），
// 因此 /configure 只在集群内可达，不会被 NodePort 暴露到集群外。
func computeCtlBaseURL(endpointID string) string {
	return fmt.Sprintf("http://%s.%s.svc.cluster.local:%d",
		computeServiceName(endpointID), podNamespace(), computeCtlPort())
}

// configurationRequest 对应 compute_api::requests::ConfigurationRequest。
type configurationRequest struct {
	Spec             *ComputeSpec     `json:"spec"`
	ComputeCtlConfig computeCtlConfig `json:"compute_ctl_config"`
}

// computeCtlConfig 对应 compute_api::responses::ComputeCtlConfig。
// 注意 jwks 必填：为空时 compute_ctl 的鉴权路由（含 /configure）全部 401。
type computeCtlConfig struct {
	JWKS jwkSet     `json:"jwks"`
	TLS  *TlsConfig `json:"tls,omitempty"`
}

// computeCtlConfigFor 构造随 spec 一起下发的 compute_ctl_config（含 JWKS）。
func computeCtlConfigFor() computeCtlConfig {
	return computeCtlConfig{JWKS: computeJWKS}
}

// configureError 描述一次 /configure 推送的结果，供调用方决定是否重试。
type configureError struct {
	// StatusCode HTTP 状态码（0 表示未收到响应）。
	StatusCode int
	// Retryable 是否值得重试。
	Retryable bool
	// Msg 人类可读原因（不打印 spec 与 token）。
	Msg string
}

func (e *configureError) Error() string {
	if e.StatusCode > 0 {
		return fmt.Sprintf("configure -> HTTP %d: %s", e.StatusCode, e.Msg)
	}
	return e.Msg
}

// configureCompute 把 endpoint 的 spec 推送给对应 compute 的 /configure。
//
// spec 由调用方传入快照：推送是耗时 IO（/configure 会阻塞到 compute 进入 Running），
// 期间不能持有 st.mu，因此调用方在锁内取深拷贝后传进来。
//
// 行为与上游一致：整份 spec 覆盖下发，天然幂等，重复推送安全。
// 返回 nil 表示 compute 已完成重配置。
func configureCompute(ctx context.Context, endpointID string, spec *ComputeSpec) error {
	if spec == nil {
		// spec 尚未生成（创建时 SC 未完成调度）：不推送，等待后续对账生成 spec。
		return &configureError{Retryable: false, Msg: "spec 尚未生成，跳过推送"}
	}
	if !jwksEnabled() {
		return &configureError{Retryable: false,
			Msg: "JWKS 未就绪（JWT_PUBLIC_KEY_PATH / privateKey.pem 不可用），/configure 会被 401"}
	}

	// compute-scoped JWT：claims 只带 compute_id（与 neon_local generate_jwt(None) 一致）。
	tok, err := signer.signComputeToken(endpointID)
	if err != nil {
		return &configureError{Retryable: true, Msg: "签发 compute token 失败: " + err.Error()}
	}

	body, err := json.Marshal(configurationRequest{
		Spec:             spec,
		ComputeCtlConfig: computeCtlConfigFor(),
	})
	if err != nil {
		return &configureError{Retryable: false, Msg: "序列化 spec 失败: " + err.Error()}
	}

	url := computeCtlBaseURL(endpointID) + "/configure"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return &configureError{Retryable: false, Msg: "构造请求失败: " + err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)

	client := &http.Client{Timeout: reconfigureTimeout()}
	resp, err := client.Do(req)
	if err != nil {
		// 网络错误 / 超时：compute 可能还在启动，值得重试。
		return &configureError{Retryable: true, Msg: err.Error()}
	}
	defer resp.Body.Close()
	// 只读取有限字节用于错误提示，避免异常响应体过大。
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil

	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		// 401/403：compute 侧 JWKS 未生效（旧 compute 未重建）或 compute_id 不匹配。
		// 认证层在 handler 之前就返回，开销极小，可以安全重试；真正的修法是重建 compute。
		return &configureError{
			StatusCode: resp.StatusCode,
			Retryable:  true,
			Msg:        "鉴权失败（旧 compute 需重建以加载 JWKS）: " + string(respBody),
		}

	case resp.StatusCode == http.StatusPreconditionFailed:
		// 412：compute 不在 Empty/Running（可能仍在启动或处于中间态），稍后重试即可。
		return &configureError{
			StatusCode: resp.StatusCode,
			Retryable:  true,
			Msg:        "compute 状态不允许重配置（invalid compute status）: " + string(respBody),
		}

	case resp.StatusCode == http.StatusBadRequest:
		// 400：spec 解析失败（ParsedSpec::try_from 报错），重试无意义，但需暴露到日志。
		return &configureError{
			StatusCode: resp.StatusCode,
			Retryable:  false,
			Msg:        "compute 拒绝了该 spec: " + string(respBody),
		}

	default:
		// 5xx 及其它：视为可重试。
		return &configureError{
			StatusCode: resp.StatusCode,
			Retryable:  true,
			Msg:        string(respBody),
		}
	}
}
