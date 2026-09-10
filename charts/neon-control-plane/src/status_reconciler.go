package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"time"
)

// =============================================================================
// endpoint 运行时状态对账（控制面 → K8s / compute_ctl）
//
// 为什么需要：进程内 endpoint.Status 是对外（Endpoint API）暴露的 compute 状态唯一来源，
// 也是 proxy / 调用方判断 compute 是否可用的依据。compute 只能由控制面动态拉起，
// 其真实健康度由两层信号决定：
//   1. K8s Deployment/Pod 状态（廉价、Pod 存活的权威信号）：readyReplicas、容器失败原因；
//   2. compute_ctl 外部 HTTP /status（精确、compute 进程内部状态）：Empty/Init/Running/Failed...
//
// 仅看"Deployment 对象存在"会漏报大量故障（Pod 崩溃、镜像拉取失败、节点驱逐时
// Deployment 对象仍在），因此这里用"周期探测 + 两层判断"持续修正 endpoint.Status。
//
// 设计对齐 Neon：
//   - neon_local 用 Endpoint::status()（pidfile + TCP）区分 Running/Stopped/Crashed；
//   - Neon Cloud 用 compute_ctl /status 的 ComputeStatus 枚举（本文件即采用该路径）。
// =============================================================================

const (
	// defaultStatusReconcileIntervalSeconds endpoint 状态对账的默认周期（秒）。
	// 与 SC 路由对账（reconcileIntervalSeconds，默认 30s）解耦：状态探测更轻量，可更频繁。
	defaultStatusReconcileIntervalSeconds = 15
)

// statusReconcileInterval 返回 endpoint 状态对账的周期。
func statusReconcileInterval() time.Duration {
	sec := defaultStatusReconcileIntervalSeconds
	if cfg != nil && cfg.StatusReconcileIntervalSeconds > 0 {
		sec = cfg.StatusReconcileIntervalSeconds
	}
	return time.Duration(sec) * time.Second
}

// startStatusReconciler 启动 endpoint 状态对账的周期循环。
// 同步对账已在 reconcileOnStartup → reconcileWithK8s 中完成（控制面重启后立即修正一次），
// 这里只负责持续周期探测。非 K8s 环境（kubePersist==nil）不启动。
func startStatusReconciler() {
	if kubePersist == nil {
		log.Printf("status-reconcile: 非集群内运行，跳过 endpoint 状态对账")
		return
	}
	go statusReconcileLoop()
}

// statusReconcileLoop 周期对账循环。
func statusReconcileLoop() {
	ticker := time.NewTicker(statusReconcileInterval())
	defer ticker.Stop()
	for range ticker.C {
		reconcileEndpointStatuses()
	}
}

// reconcileEndpointStatuses 对每个 endpoint 重新探测其运行时状态并更新 ep.Status。
// 非 K8s 环境（kubePersist==nil）不触碰 status，保持创建时的值。
// 返回 true 表示有 endpoint 的聚合状态发生过变更，需要触发持久化。
func reconcileEndpointStatuses() bool {
	if kubePersist == nil {
		return false
	}

	st.mu.RLock()
	ids := make([]string, 0, len(st.endpoints))
	for id := range st.endpoints {
		ids = append(ids, id)
	}
	st.mu.RUnlock()
	// 排序保证日志/处理顺序稳定（与 reconcileWithSC 保持一致风格）。
	sort.Strings(ids)

	changed := false
	for _, id := range ids {
		if reconcileSingleEndpoint(id) {
			changed = true
		}
	}
	// 有状态变更时触发去抖持久化（write-through ConfigMap），保证控制面重启后能恢复最新状态。
	if changed {
		triggerPersist()
	}
	return changed
}

// reconcileSingleEndpoint 探测单个 endpoint 的运行时状态并就地更新 ep 的 Status 子字段。
//
// 探测分两层，任何一层失败都不致命：
//   - K8s 不可达时保留旧 status（返回 false，不修改）；
//   - compute_ctl /status 不可达时，对已有 running 状态采用"粘性"策略（不因一次探测失败而
//     降级），避免网络抖动导致状态反复横跳；其它状态则保守地停留在 provisioning。
//
// 互斥：读旧值/写新值均加锁，避免与 API 处理并发读写 endpoint 产生数据竞争。
func reconcileSingleEndpoint(endpointID string) bool {
	if kubePersist == nil {
		return false
	}

	// 先在读锁内抓取旧值，避免与 API 处理并发读写产生数据竞争。
	st.mu.RLock()
	ep := st.endpoints[endpointID]
	var oldStatus, oldComputeStatus string
	if ep != nil {
		oldStatus = ep.Status
		oldComputeStatus = ep.ComputeStatus
	}
	st.mu.RUnlock()
	if ep == nil {
		return false
	}

	ds, err := kubePersist.getDeploymentStatus(computeDeploymentName(endpointID))
	if err != nil {
		logWarn("status-reconcile: endpoint=%s getDeploymentStatus: %v", endpointID, err)
		return false
	}

	newStatus := oldStatus
	computeStatus := oldComputeStatus
	message := ""
	ready := int(ds.ReadyReplicas)

	switch {
	case !ds.Exists:
		// Deployment 不存在：compute 未拉起或已被删除。
		newStatus = EndpointStatusStopped
		computeStatus = ""
		message = "no compute deployment"
	case ds.PodFailed:
		// Pod 处于失败终端态：不会被 K8s 自愈，直接报 failed，并附原因。
		newStatus = EndpointStatusFailed
		computeStatus = ""
		message = ds.PodReason
	case ds.ReadyReplicas == 0:
		// Deployment 存在但副本未就绪：仍在启动中（镜像下载 / 初始化）。
		newStatus = EndpointStatusProvisioning
		computeStatus = ""
		message = "deployment exists, 0 ready replicas"
	default:
		// Pod 已 Ready：进一步问 compute_ctl 的实际内部状态（精确信号）。
		cs, cerr := queryComputeStatus(endpointID)
		if cerr != nil {
			// /status 不可达：对已有 running 状态保持粘性，避免抖动；其它状态保守停留在 provisioning。
			if oldStatus != EndpointStatusRunning {
				newStatus = EndpointStatusProvisioning
				message = "compute_ctl /status unreachable: " + cerr.Error()
			}
			computeStatus = ""
		} else {
			computeStatus = cs
			switch cs {
			case "running":
				newStatus = EndpointStatusRunning
			case "failed", "termination_pending_fast", "termination_pending_immediate":
				newStatus = EndpointStatusFailed
				message = "compute status: " + cs
			default:
				// empty / configuration_pending / init / configuration / refresh_* 等：尚未就绪。
				newStatus = EndpointStatusProvisioning
				message = "compute status: " + cs
			}
		}
	}

	// 真正落库前再次加锁，确认 endpoint 仍在且未被替换。
	st.mu.Lock()
	cur := st.endpoints[endpointID]
	if cur == nil {
		st.mu.Unlock()
		return false
	}
	cur.ReadyReplicas = ready
	cur.LastStatusCheck = time.Now().Unix()
	cur.ComputeStatus = computeStatus
	cur.StatusMessage = message
	changed := false
	if newStatus != cur.Status {
		prev := cur.Status
		cur.Status = newStatus
		changed = true
		log.Printf("status-reconcile: endpoint=%s status %s -> %s (compute_status=%q)",
			endpointID, prev, newStatus, computeStatus)
	}
	st.mu.Unlock()

	// compute 刚进入 running：唤醒重配置 worker，把 pending 的 spec 推下去
	// （此时 /configure 才会返回成功，否则会被 compute 以 412 拒绝）。
	if changed && newStatus == EndpointStatusRunning {
		signalReconfigurer()
	}
	return changed
}

// computeStatusResponse compute_ctl /status 的响应结构（仅取需要的字段）。
// 上游 ComputeStatusResponse 经 #![serde(rename_all = "snake_case")] 序列化，
// status 字段为小写蛇形枚举字符串（"running" / "failed" / "empty" / "init" / ...）。
type computeStatusResponse struct {
	Status string `json:"status"`
}

// queryComputeStatus 向 compute_ctl 外部 HTTP 的 /status 查询 compute 内部状态。
// 鉴权方式与 /configure 一致：携带 compute_id 声明的 JWT（由 signComputeToken 签发）。
// 返回 ComputeStatus 的 snake_case 字符串（如 "running"）。超时很短（2s），
// 因为本探测高频执行，且失败不应阻塞对账主循环。
func queryComputeStatus(endpointID string) (string, error) {
	tok, err := signer.signComputeToken(endpointID)
	if err != nil {
		return "", fmt.Errorf("签发 compute token: %w", err)
	}
	u := computeCtlBaseURL(endpointID) + "/status"
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return "", fmt.Errorf("构造请求: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("请求 compute_ctl: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
	var parsed computeStatusResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("解析 /status 响应: %w", err)
	}
	if parsed.Status == "" {
		return "", fmt.Errorf("compute_ctl /status 返回空 status")
	}
	return parsed.Status, nil
}
