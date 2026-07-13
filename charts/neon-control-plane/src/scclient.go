package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

// scClient 调用 storage_controller 的 /control/v1/* 与 /v1/* REST 接口。
// 每个请求按路径前缀携带正确 scope 的 JWT（control|debug/* -> admin；v1/* -> pageserverapi）。
type scClient struct {
	base   string
	http   *http.Client
	signer *jwtSigner
}

func newSCClient(base string, signer *jwtSigner) *scClient {
	return &scClient{base: strings.TrimRight(base, "/"), http: &http.Client{}, signer: signer}
}

// do 发送带 Bearer 鉴权的请求，返回响应体、状态码。
func (c *scClient) do(ctx context.Context, method, path, scope string, body interface{}) ([]byte, int, error) {
	tok, err := c.signer.sign(scope)
	if err != nil {
		return nil, 0, err
	}
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return data, resp.StatusCode, nil
}

// =============================================================================
// SC 查询 API（用于 endpoint 创建和 notify 回调）
// =============================================================================

// tenantLocateResult SC debug/v1/tenant/{tid}/locate 的响应结构。
// SC 返回 Vec<TenantLocateResponseShard>，每个 shard 包含 shard_id (TenantShardId) 和节点信息。
type tenantLocateResult struct {
	Shards []struct {
		ShardID       string `json:"shard_id"`
		NodeID        int    `json:"node_id"`
		ListenPg      string `json:"listen_pg_addr"`
		ListenPgPort  int    `json:"listen_pg_port"`
		ListenHTTP    string `json:"listen_http_addr"`
		ListenHTTPPort int   `json:"listen_http_port"`
	} `json:"shards"`
	ShardParams struct {
		StripeSize int `json:"stripe_size"`
		ShardCount int `json:"shard_count"`
	} `json:"shard_params"`
}

// tenantLocate 查询租户的 pageserver 位置（debug/v1 路径，需 Admin scope）。
// 用于在创建 endpoint 或 notify-attach 时获取最新的 pageserver 连接信息。
func (c *scClient) tenantLocate(ctx context.Context, tenantID string) (*tenantLocateResult, error) {
	data, code, err := c.do(ctx, "GET", fmt.Sprintf("/debug/v1/tenant/%s/locate", tenantID), "admin", nil)
	if err != nil {
		return nil, fmt.Errorf("locate tenant %s: %w", tenantID, err)
	}
	if code >= 400 {
		return nil, fmt.Errorf("locate tenant %s -> HTTP %d: %s", tenantID, code, string(data))
	}
	var result tenantLocateResult
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("parse locate response: %w", err)
	}
	return &result, nil
}

// safekeeperListResult SC control/v1/safekeeper 的响应。
type safekeeperListResult []struct {
	ID               int    `json:"id"`
	Host             string `json:"host"`
	Port             int    `json:"port"`
	SchedulingPolicy string `json:"scheduling_policy"`
}

// listSafekeepers 列出所有 Active 状态的 safekeeper 节点（control/v1 路径，需 Admin scope）。
// 用于在创建 endpoint 时获取 safekeeper 连接串列表。
func (c *scClient) listSafekeepers(ctx context.Context) ([]SafekeeperInfo, error) {
	data, code, err := c.do(ctx, "GET", "/control/v1/safekeeper", "admin", nil)
	if err != nil {
		return nil, fmt.Errorf("list safekeepers: %w", err)
	}
	if code >= 400 {
		return nil, fmt.Errorf("list safekeepers -> HTTP %d: %s", code, string(data))
	}
	var raw safekeeperListResult
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse safekeeper list: %w", err)
	}
	// 仅保留 Active 状态的节点
	var active []SafekeeperInfo
	for _, sk := range raw {
		if sk.SchedulingPolicy == "Active" {
			active = append(active, SafekeeperInfo{ID: sk.ID, Host: sk.Host, Port: sk.Port})
		}
	}
	return active, nil
}

// =============================================================================
// SC 删除操作
// =============================================================================

// deleteTimeline 向 SC 请求删除指定时间线（branch 清理时调用）。
// 路径：DELETE /v1/tenant/{tenant_id}/timeline/{timeline_id}，scope 为 pageserverapi。
// 注意：开源版 SC 可能没有实现该端点，失败时仅记录日志不阻塞（best-effort）。
func (c *scClient) deleteTimeline(ctx context.Context, tenantID, timelineID string) error {
	path := fmt.Sprintf("/v1/tenant/%s/timeline/%s", tenantID, timelineID)
	data, code, err := c.do(ctx, http.MethodDelete, path, "pageserverapi", nil)
	if err != nil {
		return fmt.Errorf("delete timeline %s (tenant=%s): %w", timelineID, tenantID, err)
	}
	if code >= 300 && code != 404 {
		return fmt.Errorf("delete timeline %s (tenant=%s) -> HTTP %d: %s", timelineID, tenantID, code, string(data))
	}
	return nil
}

// deleteTenant 向 SC 请求删除指定租户（project 清理时调用）。
// 路径：DELETE /v1/tenant/{tenant_id}，scope 为 pageserverapi。
// 注意：开源版 SC 可能未实现该端点，失败时仅记录日志不阻塞（best-effort）。
func (c *scClient) deleteTenant(ctx context.Context, tenantID string) error {
	path := fmt.Sprintf("/v1/tenant/%s", tenantID)
	data, code, err := c.do(ctx, http.MethodDelete, path, "pageserverapi", nil)
	if err != nil {
		return fmt.Errorf("delete tenant %s: %w", tenantID, err)
	}
	if code >= 300 && code != 404 {
		return fmt.Errorf("delete tenant %s -> HTTP %d: %s", tenantID, code, string(data))
	}
	return nil
}

// =============================================================================
// Bootstrap 流程（启动时幂等执行一次）
// =============================================================================

// bootstrap 幂等地把节点注册进 SC 并创建默认租户/时间线。
// 注意：此处的默认租户/时间线仅供系统级兼容（旧 project 的回退）。
//       新 project 通过 createProject 独立创建专属 tenant + timeline。
func bootstrap() error {
	ctx := context.Background()

	// 1) 注册 pageserver 节点
	for _, ps := range cfg.Pageservers {
		body := map[string]interface{}{
			"node_id":             ps.ID,
			"listen_pg_addr":      ps.Host,
			"listen_pg_port":      ps.PGPort,
			"listen_http_addr":    ps.Host,
			"listen_http_port":    ps.HTTPPort,
			"availability_zone_id": "az1",
		}
		_, code, err := sc.do(ctx, "POST", "/control/v1/node", "admin", body)
		if err != nil {
			return fmt.Errorf("register pageserver %d: %w", ps.ID, err)
		}
		if code >= 400 && code != 409 {
			logWarn("register pageserver %d -> HTTP %d (continuing)", ps.ID, code)
		}
	}

	// 2) 注册 safekeeper 节点并设为 Active
	for _, sk := range cfg.Safekeepers {
		body := map[string]interface{}{
			"id":                   sk.ID,
			"host":                 sk.Host,
			"port":                 sk.PGPort,
			"http_port":            sk.HTTPPort,
			"https_port":           0,
			"region_id":            "local",
			"availability_zone_id": "az1",
			"version":              1,
		}
		_, code, err := sc.do(ctx, "POST", fmt.Sprintf("/control/v1/safekeeper/%d", sk.ID), "admin", body)
		if err != nil {
			return fmt.Errorf("register safekeeper %d: %w", sk.ID, err)
		}
		if code >= 400 && code != 409 {
			logWarn("register safekeeper %d -> HTTP %d (continuing)", sk.ID, code)
		}
		// 设为 Active
		_, code, _ = sc.do(ctx, "POST", fmt.Sprintf("/control/v1/safekeeper/%d/scheduling_policy", sk.ID), "admin",
			map[string]string{"scheduling_policy": "Active"})
	}

	// 3) 创建默认租户（幂等）
	tid := cfg.DefaultTenantID
	_, code, err := sc.do(ctx, "POST", "/v1/tenant", "pageserverapi", map[string]interface{}{
		"new_tenant_id": tid,
		"shard_parameters": map[string]interface{}{"count": 1, "stripe_size": 1024},
	})
	if err != nil {
		return fmt.Errorf("create tenant: %w", err)
	}
	if code >= 400 && code != 409 {
		logWarn("create tenant -> HTTP %d (continuing)", code)
	}

	// 4) 创建初始时间线（生成合法的 16 字节 / 32 hex TimelineId，幂等）
	initialTimeline := randHex(16)
	_, code, err = sc.do(ctx, "POST", fmt.Sprintf("/v1/tenant/%s/timeline", tid), "pageserverapi",
		map[string]interface{}{"new_timeline_id": initialTimeline})
	if err != nil {
		return fmt.Errorf("create timeline: %w", err)
	}
	if code >= 400 && code != 409 {
		logWarn("create timeline -> HTTP %d (continuing)", code)
	}
	st.setDefaultTimeline(initialTimeline)
	log.Printf("bootstrap: default tenant=%s timeline=%s", tid, initialTimeline)
	return nil
}
