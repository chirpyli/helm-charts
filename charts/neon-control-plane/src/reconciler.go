package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
)

// =============================================================================
// 与 storage controller 的对账 + compute 重配置队列
//
// 为什么需要对账（而不是只依赖 SC 的 notify）：
//  1. SC 只有在"shard 成功 attach 到某个 pageserver"时才发 /notify-attach；
//     没有可用落点时不会发（compute_hook.rs 只在 intent.attached 非空时通知）。
//  2. SC 对同一 node_id 的通知有去重（"remote state already matches" 直接跳过）。
//  3. 控制面一旦对 notify 返回 2xx，SC 就标记 applied=true 不再重发；
//     若此时推送 compute 失败，SC 不会再补发 —— 必须由控制面自己重试。
//  4. 控制面重启后从 ConfigMap 恢复的 spec 可能是故障转移前的旧路由。
//
// 因此这里用"路由指纹 + 周期对账"兜底：指纹记录"当前 spec 对应的 SC 路由"，
// 每轮对账重新 locate 并比对，不一致就重建 spec 并入队推送。
// =============================================================================

// 重配置推送的退避参数：失败后按 2 倍退避，上限 60s。
const (
	initialReconfigureBackoff = 5 * time.Second
	maxReconfigureBackoff     = 60 * time.Second
)

// reconfigureSignal 用于唤醒重配置 worker（带 1 缓冲，合并重复信号）。
var reconfigureSignal = make(chan struct{}, 1)

// signalReconfigurer 唤醒重配置 worker；已有待处理信号时直接返回（合并）。
func signalReconfigurer() {
	select {
	case reconfigureSignal <- struct{}{}:
	default:
	}
}

// startReconciler 启动周期对账与重配置 worker，并先同步对账一次。
//
// 启动即对账的原因：控制面重启后内存/ConfigMap 里的 spec 可能已经落后于 SC 真相
// （例如故障转移发生在控制面停机期间），必须第一时间补上。
func startReconciler() {
	reconcileWithSC()
	go reconcileLoop()
	go reconfigureWorker()
}

// -----------------------------------------------------------------------------
// 路由指纹
// -----------------------------------------------------------------------------

// routeFingerprint 计算"已经写进 spec 的存储路由"的指纹。
//
// 输入是 PageserverConnInfo（即最终下发给 compute 的内容）而不是 locate 原始响应，
// 保证指纹与实际下发的路由严格同源，不会因为解析差异产生"指纹没变但路由变了"的漏判。
//
// 指纹覆盖三要素：shard 划分（key = ShardIndex）、所在 pageserver 节点 id、libpq 地址。
// 任一变化（典型场景：pageserver 故障后 shard 迁到别的节点）都会导致指纹变化，
// 据此重建 spec 并推送给 compute。
func routeFingerprint(connInfo PageserverConnInfo) string {
	keys := make([]string, 0, len(connInfo.Shards))
	for shardKey, info := range connInfo.Shards {
		if len(info.Pageservers) == 0 {
			keys = append(keys, fmt.Sprintf("%s|none", shardKey))
			continue
		}
		ps := info.Pageservers[0]
		nodeID := -1
		if ps.ID != nil {
			nodeID = *ps.ID
		}
		keys = append(keys, fmt.Sprintf("%s|%d|%s", shardKey, nodeID, ps.LibpqURL))
	}
	// 排序保证指纹与 map 遍历顺序无关。
	sort.Strings(keys)
	sum := sha256.Sum256([]byte(strings.Join(keys, "\n")))
	return hex.EncodeToString(sum[:])
}

// -----------------------------------------------------------------------------
// 与 SC 的对账
// -----------------------------------------------------------------------------

// reconcileWithSC 对每个 endpoint 重新 locate，路由指纹变化时重建 spec 并入队推送。
//
// 关键约束：locate 失败时**保留现有 spec**并只打 WARN，绝不下发空 shards 或静态清单
// —— 宁可让 compute 暂时用旧路由，也不能把它指向不存在的 pageserver。
func reconcileWithSC() {
	ctx := context.Background()

	st.mu.RLock()
	ids := make([]string, 0, len(st.endpoints))
	for id := range st.endpoints {
		ids = append(ids, id)
	}
	st.mu.RUnlock()
	sort.Strings(ids)

	for _, id := range ids {
		st.mu.RLock()
		ep, ok := st.endpoints[id]
		tenantID := ""
		if ok && ep != nil {
			tenantID = ep.TenantID
		}
		st.mu.RUnlock()
		if !ok || ep == nil || tenantID == "" {
			continue
		}

		locate, err := sc.tenantLocate(ctx, tenantID)
		if err != nil {
			logWarn("reconcile: locate tenant=%s endpoint=%s 失败，保留现有 spec: %v", tenantID, id, err)
			continue
		}
		connInfo := buildPageserverConnInfo(locate)
		if len(connInfo.Shards) == 0 {
			logWarn("reconcile: locate tenant=%s endpoint=%s 未返回 shard，保留现有 spec", tenantID, id)
			continue
		}
		fp := routeFingerprint(connInfo)

		st.mu.Lock()
		cur, ok := st.endpoints[id]
		if !ok || cur == nil {
			st.mu.Unlock()
			continue
		}
		if cur.RouteFingerprint == fp {
			st.mu.Unlock()
			continue
		}
		// 路由已变化：重建 spec 的 pageserver 部分并标记待推送。
		// spec 尚未生成时只更新指纹（compute 启动时自会拉取最新 spec）。
		if cur.Spec != nil {
			cur.Spec.PageserverConnectionInfo = connInfo
			cur.PendingReconfigure = true
		}
		cur.RouteFingerprint = fp
		st.mu.Unlock()

		log.Printf("reconcile: endpoint=%s tenant=%s 存储路由已变更(fingerprint=%s)，已重建 spec 并入队推送",
			id, tenantID, fp)
		triggerPersist()
	}

	signalReconfigurer()
}

// reconcileLoop 周期对账循环。
func reconcileLoop() {
	ticker := time.NewTicker(reconcileInterval())
	defer ticker.Stop()
	for range ticker.C {
		reconcileWithSC()
	}
}

// -----------------------------------------------------------------------------
// 重配置推送队列（单 worker + 指数退避）
// -----------------------------------------------------------------------------

// reconfigureWorker 消费待推送队列：一次信号后反复重试直到清空，失败按指数退避。
func reconfigureWorker() {
	backoff := initialReconfigureBackoff
	for range reconfigureSignal {
		for {
			remaining := pushAllPendingReconfigures()
			if remaining == 0 {
				break
			}
			logWarn("reconfigure: 仍有 %d 个 endpoint 待推送，%v 后重试", remaining, backoff)
			time.Sleep(backoff)
			backoff *= 2
			if backoff > maxReconfigureBackoff {
				backoff = maxReconfigureBackoff
			}
		}
		backoff = initialReconfigureBackoff
	}
}

// pushAllPendingReconfigures 推送所有 PendingReconfigure 的 endpoint，返回仍未成功数。
func pushAllPendingReconfigures() int {
	st.mu.RLock()
	ids := make([]string, 0)
	for id, ep := range st.endpoints {
		if ep != nil && ep.PendingReconfigure {
			ids = append(ids, id)
		}
	}
	st.mu.RUnlock()
	sort.Strings(ids)

	remaining := 0
	for _, id := range ids {
		// 在锁内做 spec 深拷贝，避免持锁做网络 IO，也避免与 notify 并发改 spec 产生数据竞争。
		st.mu.RLock()
		spec := snapshotSpec(id)
		epStatus := ""
		if e := st.endpoints[id]; e != nil {
			epStatus = e.Status
		}
		st.mu.RUnlock()

		// compute 尚未就绪（provisioning/stopped/failed）时跳过本次推送，保留 PendingReconfigure：
		// 避免对未就绪的 compute 徒劳打 /configure（也会被 412 拒绝），
		// 待状态对账把 endpoint 置为 running 后由 signalReconfigurer 唤醒再推。
		// 历史持久化数据若 Status 为空（旧格式），仍 best-effort 尝试推送。
		if epStatus != "" && epStatus != EndpointStatusRunning {
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), reconfigureTimeout()+10*time.Second)
		err := configureCompute(ctx, id, spec)
		cancel()

		if err == nil {
			log.Printf("reconfigure: endpoint=%s spec 已推送到 compute", id)
			setPendingReconfigure(id, false)
			continue
		}
		ce, ok := err.(*configureError)
		if ok && !ce.Retryable {
			// 不可重试（spec 尚未生成 / 被 compute 拒绝 / JWKS 缺失）：清除标记，避免无限重试。
			logWarn("reconfigure: endpoint=%s 放弃推送: %v", id, err)
			setPendingReconfigure(id, false)
			continue
		}
		logWarn("reconfigure: endpoint=%s 推送失败，稍后重试: %v", id, err)
		remaining++
	}
	return remaining
}

// snapshotSpec 返回 endpoint 当前 spec 的深拷贝（通过 JSON 往返实现）。
// 返回 nil 表示 endpoint 不存在或 spec 尚未生成。
func snapshotSpec(endpointID string) *ComputeSpec {
	ep, ok := st.endpoints[endpointID]
	if !ok || ep == nil || ep.Spec == nil {
		return nil
	}
	b, err := json.Marshal(ep.Spec)
	if err != nil {
		logWarn("snapshotSpec %s: marshal spec: %v", endpointID, err)
		return nil
	}
	var out ComputeSpec
	if err := json.Unmarshal(b, &out); err != nil {
		logWarn("snapshotSpec %s: unmarshal spec: %v", endpointID, err)
		return nil
	}
	return &out
}

// setPendingReconfigure 设置（或清除）endpoint 的待推送标记并触发持久化。
func setPendingReconfigure(endpointID string, pending bool) {
	st.mu.Lock()
	if ep, ok := st.endpoints[endpointID]; ok && ep != nil {
		ep.PendingReconfigure = pending
	}
	st.mu.Unlock()
	triggerPersist()
}

// markPendingReconfigures 批量标记待推送并唤醒 worker。
// 由 notify-attach / notify-safekeepers 在更新 spec 后调用。
func markPendingReconfigures(endpointIDs []string) {
	if len(endpointIDs) == 0 {
		return
	}
	st.mu.Lock()
	for _, id := range endpointIDs {
		if ep, ok := st.endpoints[id]; ok && ep != nil {
			ep.PendingReconfigure = true
		}
	}
	st.mu.Unlock()
	triggerPersist()
	signalReconfigurer()
}
