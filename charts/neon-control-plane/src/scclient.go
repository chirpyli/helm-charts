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
		ShardID        string `json:"shard_id"`
		NodeID         int    `json:"node_id"`
		ListenPg       string `json:"listen_pg_addr"`
		ListenPgPort   int    `json:"listen_pg_port"`
		ListenHTTP     string `json:"listen_http_addr"`
		ListenHTTPPort int    `json:"listen_http_port"`
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
//
// 重要：SC 的 get_safekeepers() 只返回 scheduling_policy == Active 的节点；
// 而 safekeeper upsert 落库时默认是 Activating，必须由 sk-register sidecar 显式激活后才会出现在这里。
// 上游 SkSchedulingPolicy 的 serde 用的是变体名（首字母大写），故这里按 "Active" 比较。
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

// listNodes 列出 SC 中已注册的全部 pageserver 节点（control/v1 路径，需 Admin scope）。
//
// 只读接口：控制面**不注册**任何节点（节点由 pageserver re-attach 自注册），
// 这里只用于在启动时打印节点快照、以及在 locate 失败时给出更明确的错误提示。
func (c *scClient) listNodes(ctx context.Context) ([]NodeInfo, error) {
	data, code, err := c.do(ctx, "GET", "/control/v1/node", "admin", nil)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	if code >= 400 {
		return nil, fmt.Errorf("list nodes -> HTTP %d: %s", code, string(data))
	}
	var nodes []NodeInfo
	if err := json.Unmarshal(data, &nodes); err != nil {
		return nil, fmt.Errorf("parse node list: %w", err)
	}
	return nodes, nil
}

// =============================================================================
// 启动诊断（只读）
// =============================================================================

// logStorageNodes 启动时 best-effort 打印一次 SC 中的节点快照，便于排障。
//
// 与控制面旧版 bootstrap 的区别：这里**不注册节点、不创建默认 tenant/timeline**，
// 全部依赖各组件自注册（pageserver re-attach / safekeeper sk-register sidecar）。
// 因此失败时只打 WARN，不阻塞启动：组件可能晚于控制面启动，SC 自身也有 reconciler 兜底。
func logStorageNodes() {
	ctx := context.Background()

	nodes, err := sc.listNodes(ctx)
	if err != nil {
		logWarn("startup: list pageserver nodes from storage controller: %v", err)
	} else {
		var active int
		for _, n := range nodes {
			if n.Scheduling == "Active" {
				active++
			}
		}
		log.Printf("startup: storage controller has %d pageserver node(s), %d active", len(nodes), active)
		for _, n := range nodes {
			log.Printf("  pageserver node=%d scheduling=%s pg=%s:%d http=%s:%d",
				n.ID, n.Scheduling, n.ListenPgAddr, n.ListenPgPort, n.ListenHTTPAddr, n.ListenHTTPPort)
		}
	}

	sks, err := sc.listSafekeepers(ctx)
	if err != nil {
		logWarn("startup: list safekeepers from storage controller: %v", err)
	} else {
		log.Printf("startup: storage controller has %d active safekeeper(s)", len(sks))
		for _, sk := range sks {
			log.Printf("  safekeeper node=%d host=%s port=%d", sk.ID, sk.Host, sk.Port)
		}
		if len(sks) == 0 {
			logWarn("startup: no active safekeeper yet (safekeeper 需由 sk-register sidecar 注册并激活)")
		}
	}
}
