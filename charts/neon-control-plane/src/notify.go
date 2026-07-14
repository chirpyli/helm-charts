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
//  3. 向 SC 查询最新的 pageserver 位置（tenant_locate）
//  4. 找到所有匹配 tenant_id 的 endpoint，重建其 pageserver_connection_info
//  5. 写入持久化并返回 200
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

	log.Printf("notify-attach: tenant=%s timeline=%s generation=%d", req.TenantID, req.TimelineID, req.Generation)

	// 3) 向 SC 查询最新 pageserver 位置
	locate, err := sc.tenantLocate(r.Context(), req.TenantID)
	if err != nil {
		log.Printf("WARN notify-attach: locate tenant %s: %v", req.TenantID, err)
		// 定位失败时返回 423 LOCKED，让 SC 视为 Busy 继续 reconcile
		writeJSON(w, http.StatusLocked, map[string]string{"error": "failed to locate tenant: " + err.Error()})
		return
	}

	// 4) 找到所有受影响的 endpoint 并更新其 spec
	st.mu.Lock()
	defer st.mu.Unlock()

	var updated int
	for _, ep := range st.endpoints {
		if ep.TenantID != req.TenantID {
			continue
		}
		// 重建 pageserver_connection_info
		shards := buildShardsFromLocate(locate)
		ep.Spec.PageserverConnectionInfo = PageserverConnInfo{
			ShardCount: locate.ShardParams.ShardCount,
			StripeSize: locate.ShardParams.StripeSize,
			Shards:     shards,
		}
		updated++
		log.Printf("notify-attach: updated endpoint=%s tenant=%s", ep.EndpointID, req.TenantID)
	}

	// 5) 触发持久化
	triggerPersist()

	if updated == 0 {
		log.Printf("WARN notify-attach: no endpoints found for tenant=%s", req.TenantID)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":            "ok",
		"endpoints_updated": updated,
	})
}

// handleNotifySafekeepers 处理 SC 发出的 PUT/POST /notify-safekeepers 回调。
// SC 在 safekeeper 集合变更后调用此端点，通知控制面更新受影响 endpoint 的 safekeeper 连接串。
//
// 处理流程：
//  1. 校验 Authorization: Bearer <ControlPlane scope JWT>
//  2. 解析请求体 NotifySafekeepersRequest
//  3. 找到所有匹配 tenant_id 的 endpoint，更新 safekeeper_connstrings
//  4. 写入持久化并返回 200
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

	// 4) 找到所有受影响的 endpoint 并更新其 spec
	st.mu.Lock()
	defer st.mu.Unlock()

	var updated int
	for _, ep := range st.endpoints {
		if ep.TenantID != req.TenantID {
			continue
		}
		ep.Spec.SafekeeperConnstrings = skConns
		updated++
		log.Printf("notify-safekeepers: updated endpoint=%s tenant=%s safekeepers=%d", ep.EndpointID, req.TenantID, len(skConns))
	}

	// 5) 触发持久化
	triggerPersist()

	if updated == 0 {
		log.Printf("WARN notify-safekeepers: no endpoints found for tenant=%s", req.TenantID)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":            "ok",
		"endpoints_updated": updated,
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
// shard key 对齐 Rust ShardIndex::Display: "{:02x}{:02x}"（shard_number + shard_count，各 1 字节 hex）。
//
// compute_ctl 查找 shards map 时的 ShardIndex 构造（config.rs）：
//
//	num_shards = if conninfo.shard_count.0 == 0 { 1 } else { conninfo.shard_count.0 }
//	for shard_number in 0..num_shards:
//	    ShardIndex { shard_number, shard_count: conninfo.shard_count }  ← 用的是 RAW spec 值！
//
// 因此 unsharded (shard_count=0) 时：ShardIndex(0, 0) → key "0000"
// sharded (shard_count=2) 时：ShardIndex(0, 2)→"0002", ShardIndex(1, 2)→"0102"
func buildShardsFromLocate(locate *tenantLocateResult) map[string]ShardInfo {
	shardCount := locate.ShardParams.ShardCount
	shards := make(map[string]ShardInfo)
	for i, sh := range locate.Shards {
		// 从 shard_id (TenantShardId) 中提取 ShardIndex key
		// unsharded: shard_id = tenant_id (32 hex), key = "0000"（ShardIndex(0,0)）
		// sharded:   shard_id = tenant_id-0102 (37 chars), key = "0102"
		shardKey := "0000"
		if len(sh.ShardID) >= 38 {
			// 取后缀 4 位 hex 作为 ShardIndex key
			shardKey = sh.ShardID[len(sh.ShardID)-4:]
		} else if shardCount > 0 {
			// sharded 但 shard_id 格式不标准：用索引 + shardCount 生成 key
			shardKey = fmt.Sprintf("%02x%02x", i, shardCount)
		}
		id := sh.NodeID
		shards[shardKey] = ShardInfo{
			Pageservers: []PageserverShard{{
				ID:       &id,
				LibpqURL: fmt.Sprintf("postgresql://%s:%d", sh.ListenPg, sh.ListenPgPort),
				GRPCURL:  fmt.Sprintf("http://%s:%d", sh.ListenHTTP, sh.ListenHTTPPort),
			}},
		}
	}
	return shards
}
