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
// 对应上游 TenantLocateResponse：
//
//	pub struct TenantLocateResponse {
//	    pub shards: Vec<TenantLocateResponseShard>,
//	    pub shard_params: ShardParameters,
//	}
//
// 重要：ShardParameters 的字段名是 **count**，不是 shard_count（见
// libs/pageserver_api/src/models.rs:480-485）：
//
//	pub struct ShardParameters { pub count: ShardCount, pub stripe_size: ShardStripeSize }
//
// 旧实现按 shard_count 解析，导致分片数恒为 0，compute 端会把多分片租户当成 1 个分片处理
// （compute_tools/src/config.rs 中 num_shards = if shard_count.0 == 0 { 1 } else { shard_count.0 }），
// 分片映射整体错乱。
type tenantLocateResult struct {
	Shards []struct {
		ShardID        string `json:"shard_id"`
		NodeID         int    `json:"node_id"`
		ListenPg       string `json:"listen_pg_addr"`
		ListenPgPort   int    `json:"listen_pg_port"`
		ListenHTTP     string `json:"listen_http_addr"`
		ListenHTTPPort int    `json:"listen_http_port"`
		// gRPC 地址（可选）：对应 PageserverShardConnectionInfo.grpc_url。
		// 未启用 gRPC 的 pageserver 不会返回这两个字段，此时 grpc_url 置空。
		ListenGRPC     string `json:"listen_grpc_addr"`
		ListenGRPCPort int    `json:"listen_grpc_port"`
	} `json:"shards"`
	ShardParams struct {
		StripeSize int `json:"stripe_size"`
		// ShardCount 对应上游的 count 字段（0 表示 unsharded）。
		ShardCount int `json:"count"`
		// LegacyShardCount 兼容字段：个别 SC 版本仍返回 shard_count，作为兜底取值。
		LegacyShardCount int `json:"shard_count"`
	} `json:"shard_params"`
}

// shardCount 返回租户的分片数。
// 优先取上游标准字段 count；缺失时回退旧字段 shard_count；都没有则返回 0（unsharded）。
func (r *tenantLocateResult) shardCount() int {
	if r.ShardParams.ShardCount > 0 {
		return r.ShardParams.ShardCount
	}
	return r.ShardParams.LegacyShardCount
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

// parseShardIndex 从 TenantShardId 文本解析 ComputeSpec shards map 所需的 key。
//
// 上游事实（已逐条核对源码）：
//   - TenantShardId 形如 "<32hex tenant>-<4hex shard_slug>"，unsharded 时没有后缀（32 字符）；
//     ShardSlug = format!("-{:02x}{:02x}", shard_number, shard_count)
//     （libs/pageserver_api/src/shard.rs:258-264）。
//   - shards 的 key 类型是 ShardIndex，JSON 序列化即其 Display：
//     "{shard_number:02x}{shard_count:02x}"（13/17 → "0d11"），
//     见 libs/utils/src/shard.rs 的 shard_index_human_encoding 测试与 Serialize 实现（413-430）。
//
// 因此 shard_id 的 4 位 hex 后缀就是正确的 key；只有 shard_id 格式异常（无后缀 / 后缀非法）
// 时才用「编号 + 分片数」兜底拼装。注意 unsharded（shard_count=0）时
// ShardIndex(0,0) 的 key 也是 "0000"，与兜底结果一致。
func parseShardIndex(shardID string, shardNumber, shardCount int) string {
	if idx := strings.LastIndex(shardID, "-"); idx >= 0 {
		suffix := shardID[idx+1:]
		if len(suffix) == 4 && isHex4(suffix) {
			return suffix
		}
	}
	return fmt.Sprintf("%02x%02x", shardNumber, shardCount)
}

// isHex4 判断字符串是否为 4 位十六进制（ShardIndex 的 key 形态）。
func isHex4(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
			continue
		}
		return false
	}
	return true
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
