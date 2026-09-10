package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
)

// =============================================================================
// Notify 回调端点处理（SC → control plane 的 compute_hook）
// =============================================================================

// handleNotifyAttach 处理 SC 发出的 PUT/POST /notify-attach 回调。
// SC 在 pageserver shard 迁移后调用此端点，通知控制面更新受影响 endpoint 的 pageserver 路由。
//
// 处理流程：
//  1. 校验 Authorization: Bearer <ControlPlane scope JWT>
//  2. 解析请求体 NotifyReAttachRequest
//  3. 向 SC 查询最新的 pageserver 位置（tenant_locate）—— SC 才是路由的真相源
//  4. 找到所有匹配 tenant_id 的 endpoint，重建其 pageserver_connection_info 与路由指纹
//  5. 标记待推送并**立即返回**：推送由后台 worker 异步完成
//
// 为什么必须尽快返回：SC 侧 NOTIFY_REQUEST_TIMEOUT 只有 10 秒
// （storage_controller/src/compute_hook.rs:28），而 /configure 会阻塞到 compute 完成重配置
// （可达数十秒）。若在本 handler 里同步推送，SC 必然超时并把通知判为失败。
func handleNotifyAttach(w http.ResponseWriter, r *http.Request) {
	// 1) JWT 鉴权
	if !validateNotifyAuth(w, r) {
		return
	}

	// 2) 解析请求体
	var req NotifyReAttachRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body: " + err.Error()})
		return
	}
	if req.TenantID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "tenant_id is required"})
		return
	}

	log.Printf("notify-attach: tenant=%s shards=%d timeline=%s generation=%d",
		req.TenantID, len(req.Shards), req.TimelineID, req.Generation)

	// 3) 向 SC 查询最新 pageserver 位置（通知只说明"attach 变了"，位置必须以 SC 为准）
	locate, err := sc.tenantLocate(r.Context(), req.TenantID)
	if err != nil {
		logWarn("notify-attach: locate tenant %s: %v", req.TenantID, err)
		// 返回 503：上游把 503 映射为 NotifyError::Unavailable（退避重试，直到成功）；
		// 而旧的 423(LOCKED) 会被映射为 Busy —— 注释里明确"视为 fatal"，只重试 3 次就放弃，
		// 且依赖后续 reconcile 才可能补发，不利于临时抖动场景的恢复。
		writeJSON(w, http.StatusServiceUnavailable,
			map[string]string{"error": "failed to locate tenant: " + err.Error()})
		return
	}
	connInfo := buildPageserverConnInfo(locate)
	if len(connInfo.Shards) == 0 {
		// 没有任何 shard：说明 SC 侧尚未完成调度，此时更新 spec 只会写入无效路由。
		logWarn("notify-attach: locate tenant %s 未返回任何 shard，暂不更新 spec", req.TenantID)
		writeJSON(w, http.StatusServiceUnavailable,
			map[string]string{"error": "locate returned no shards"})
		return
	}
	fp := routeFingerprint(connInfo)

	// 4) 找到所有受影响的 endpoint 并更新 spec（纯内存操作，保证在 SC 超时前返回）
	st.mu.Lock()
	var updated []string
	for _, ep := range st.endpoints {
		if ep.TenantID != req.TenantID {
			continue
		}
		// spec 尚未生成（创建时 SC 未完成调度）时跳过，避免空指针；
		// 该 endpoint 会在 compute_ctl 下一次拉取 spec 时由 getComputeSpec 重建并自愈。
		if ep.Spec == nil {
			log.Printf("notify-attach: skip endpoint=%s (spec not built yet)", ep.EndpointID)
			continue
		}
		// 重建 pageserver_connection_info（统一构造入口，与创建 endpoint 路径完全同源）
		ep.Spec.PageserverConnectionInfo = connInfo
		ep.RouteFingerprint = fp
		updated = append(updated, ep.EndpointID)
		log.Printf("notify-attach: updated endpoint=%s tenant=%s", ep.EndpointID, req.TenantID)
	}
	st.mu.Unlock()

	// 5) 入队推送（异步）+ 持久化。
	//    注意：一旦对 SC 返回 2xx，SC 就标记 applied=true 不再重发，
	//    因此这里的 pending 标记与后台 worker 的退避重试是唯一的可靠性保证。
	markPendingReconfigures(updated)

	if len(updated) == 0 {
		logWarn("notify-attach: no endpoints found for tenant=%s", req.TenantID)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":            "ok",
		"endpoints_updated": len(updated),
	})
}

// handleNotifySafekeepers 处理 SC 发出的 PUT/POST /notify-safekeepers 回调。
// SC 在 safekeeper 集合变更后调用此端点，通知控制面更新受影响 endpoint 的 safekeeper 连接串。
//
// 处理流程：
//  1. 校验 Authorization: Bearer <ControlPlane scope JWT>
//  2. 解析请求体 NotifySafekeepersRequest
//  3. 按 generation 做新旧比较后，更新匹配 endpoint 的 safekeeper_connstrings
//  4. 标记待推送并立即返回（推送由后台 worker 异步完成）
//
// generation 比较的必要性：网络乱序或 SC 重放都可能让"旧集合"后到，
// 盲目覆盖会把 compute 指向已经被淘汰的 safekeeper。上游用它做
// neon.safekeepers 的 g#<generation>: 前缀，walproposer 也据此判断成员配置新旧。
func handleNotifySafekeepers(w http.ResponseWriter, r *http.Request) {
	// 1) JWT 鉴权
	if !validateNotifyAuth(w, r) {
		return
	}

	// 2) 解析请求体
	var req NotifySafekeepersRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body: " + err.Error()})
		return
	}
	if req.TenantID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "tenant_id is required"})
		return
	}

	log.Printf("notify-safekeepers: tenant=%s timeline=%s generation=%d safekeepers=%d",
		req.TenantID, req.TimelineID, req.Generation, len(req.Safekeepers))

	// 3) 构建 safekeeper 连接串列表（host:5454 格式）
	skConns := make([]string, 0, len(req.Safekeepers))
	for _, sk := range req.Safekeepers {
		skConns = append(skConns, fmt.Sprintf("%s:%d", sk.Host, sk.Port))
	}

	// 4) 找到所有受影响的 endpoint 并更新其 spec（纯内存操作，保证在 SC 超时前返回）
	st.mu.Lock()
	var updated []string
	for _, ep := range st.endpoints {
		if ep.TenantID != req.TenantID {
			continue
		}
		// 指定了 timeline 时只更新该 timeline 的 endpoint。
		if req.TimelineID != "" && ep.TimelineID != "" && ep.TimelineID != req.TimelineID {
			continue
		}
		// spec 尚未生成时跳过，避免空指针；后续由 getComputeSpec 重建自愈。
		if ep.Spec == nil {
			log.Printf("notify-safekeepers: skip endpoint=%s (spec not built yet)", ep.EndpointID)
			continue
		}
		// generation 单调递增校验：只有更新的成员配置才允许覆盖。
		if req.Generation > 0 && int64(req.Generation) < ep.SafekeeperGeneration {
			logWarn("notify-safekeepers: skip endpoint=%s (stale generation %d <= 已知的 %d)",
				ep.EndpointID, req.Generation, ep.SafekeeperGeneration)
			continue
		}
		ep.Spec.SafekeeperConnstrings = skConns
		if req.Generation > 0 {
			// 下发到 spec：compute_ctl 会写成 neon.safekeepers 的 g#<generation>: 前缀。
			ep.Spec.SafekeepersGeneration = &req.Generation
			ep.SafekeeperGeneration = int64(req.Generation)
		}
		updated = append(updated, ep.EndpointID)
		log.Printf("notify-safekeepers: updated endpoint=%s tenant=%s safekeepers=%d generation=%d",
			ep.EndpointID, req.TenantID, len(skConns), req.Generation)
	}
	st.mu.Unlock()

	// 5) 入队推送（异步）+ 持久化
	markPendingReconfigures(updated)

	if len(updated) == 0 {
		logWarn("notify-safekeepers: no endpoints found for tenant=%s", req.TenantID)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":            "ok",
		"endpoints_updated": len(updated),
	})
}

// =============================================================================
// Notify 鉴权辅助
// =============================================================================

// validateNotifyAuth 校验 notify 回调的 JWT 令牌（必须是 ControlPlane scope）。
// 校验失败时直接向 ResponseWriter 写入错误并返回 false。
func validateNotifyAuth(w http.ResponseWriter, r *http.Request) bool {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing Authorization header"})
		return false
	}

	// 提取 Bearer token
	token := strings.TrimPrefix(authHeader, "Bearer ")
	if token == authHeader {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid Authorization header format"})
		return false
	}

	// 校验签名并提取 scope
	scope, err := signer.verify(token)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid token: " + err.Error()})
		return false
	}

	// 必须是 ControlPlane scope（对应 SC 的 --control-plane-jwt-token）
	// 也接受 admin scope 以确保兼容性
	if scope != "controlplane" && scope != "admin" {
		log.Printf("WARN notify auth: rejected scope=%s", scope)
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "scope must be controlplane or admin, got " + scope})
		return false
	}

	return true
}

// =============================================================================
// 辅助：从 SC locate 响应构建 pageserver shard 映射
// =============================================================================

// buildShardsFromLocate 将 SC 的 tenant_locate 结果转换为 ComputeSpec 所需的 Shards 结构。
//
// shard key 必须是 Rust ShardIndex 的 JSON 表示："{shard_number:02x}{shard_count:02x}"
// （例如 13 号分片 / 共 17 个分片 → "0d11"）。compute_ctl 按下列逻辑查表
// （compute_tools/src/config.rs）：
//
//	num_shards = if conninfo.shard_count.0 == 0 { 1 } else { conninfo.shard_count.0 }
//	for shard_number in 0..num_shards:
//	    shard_index = ShardIndex { shard_number, shard_count: conninfo.shard_count }
//	    info = conninfo.shards.get(&shard_index)   ← 缺失即硬报错
//
// 因此：
//   - unsharded (shard_count=0)：只有 ShardIndex(0,0) → key "0000"；
//   - sharded (shard_count=2)：ShardIndex(0,2) → "0002"，ShardIndex(1,2) → "0102"。
//
// 实现要点：key 一律由 parseShardIndex 从 shard_id 的真实后缀解析（TenantShardId 的
// ShardSlug 就是 ShardIndex 的 hex 表示），不再依赖数组下标，避免 locate 返回顺序变化导致错位。
func buildShardsFromLocate(locate *tenantLocateResult) map[string]ShardInfo {
	shardCount := locate.shardCount()
	shards := make(map[string]ShardInfo)
	for i, sh := range locate.Shards {
		shardKey := parseShardIndex(sh.ShardID, i, shardCount)

		id := sh.NodeID
		ps := PageserverShard{
			ID: &id,
			// libpq_url 是 compute 实际使用的连接串（compute 取 pageservers[0].libpq_url）。
			LibpqURL: fmt.Sprintf("postgresql://%s:%d", sh.ListenPg, sh.ListenPgPort),
		}
		// grpc_url 上游为 Option<String>：仅在 pageserver 上报了 gRPC 地址时下发。
		// 旧实现拿 http 地址冒充 grpc 地址，会给出错误的协议端点。
		if sh.ListenGRPC != "" && sh.ListenGRPCPort > 0 {
			grpcURL := fmt.Sprintf("http://%s:%d", sh.ListenGRPC, sh.ListenGRPCPort)
			ps.GRPCURL = &grpcURL
		}
		shards[shardKey] = ShardInfo{
			Pageservers: []PageserverShard{ps},
		}
	}
	return shards
}

// buildPageserverConnInfo 由 SC locate 结果构造 ComputeSpec 的 pageserver_connection_info。
//
// 这是**唯一**的构造入口：创建 endpoint、getComputeSpec 自愈、notify-attach、周期对账
// 四条路径都调用它，保证 shard_count / stripe_size / shards key 语义一致，
// 避免各处各写一份导致字段漂移（历史上 shard_count 字段解析错误就源于此）。
func buildPageserverConnInfo(locate *tenantLocateResult) PageserverConnInfo {
	shardCount := locate.shardCount()
	return PageserverConnInfo{
		ShardCount: shardCount,
		// 上游不变量：shard_count == 0 时 stripe_size 必须为 null。
		StripeSize: stripeSizePtr(shardCount, locate.ShardParams.StripeSize),
		Shards:     buildShardsFromLocate(locate),
	}
}
