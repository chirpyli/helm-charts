package main

import (
	"encoding/json"
	"log"
	"sync"
	"time"
)

// =============================================================================
// ConfigMap 持久化：write-through + 去抖
//
// 根据 plan 设计：
//   - 运行时真相源为进程内 RwLock<State>
//   - 持久化层为 K8s ConfigMap "neon-cp-state"，JSON 快照
//   - 采用 write-through + 去抖（1s 合并刷新）落盘
//   - 启动时先 load 快照，再与 K8s/SC 双向对账恢复
//   - 单写者约束：replicas=1 + Recreate 策略保证
// =============================================================================

var (
	persistMu       sync.Mutex
	persistDirty    bool
	persistTimer    *time.Timer
	persistInterval = 1 * time.Second // 去抖间隔
)

// triggerPersist 标记状态已变更，触发去抖持久化。
// 多次快速调用只会在最后一次调用的 persistInterval 后执行一次写入。
func triggerPersist() {
	persistMu.Lock()
	defer persistMu.Unlock()

	persistDirty = true
	if persistTimer == nil {
		persistTimer = time.AfterFunc(persistInterval, doPersist)
	}
	// timer 已存在时不重置（避免高频重写），之前标记的 dirty 标志已足够
}

// doPersist 将当前内存状态写入 ConfigMap。
func doPersist() {
	persistMu.Lock()
	if !persistDirty {
		persistMu.Unlock()
		persistTimer = nil
		return
	}
	persistDirty = false
	persistTimer = nil
	persistMu.Unlock()

	// 尝试 K8s 持久化
	if kubePersist == nil {
		return // 非 K8s 环境，跳过持久化
	}

	// 从 state 读取快照
	snapshot := st.snapshot()

	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		log.Printf("WARN persist: marshal state: %v", err)
		return
	}

	// 写入 ConfigMap（upsert）
	if err := kubePersist.upsertConfigMap(stateConfigMapName, map[string]interface{}{
		"state.json": string(data),
	}); err != nil {
		log.Printf("WARN persist: upsert configmap: %v", err)
		return
	}

	log.Printf("persist: saved state (%d projects, %d branches, %d endpoints) to ConfigMap %s",
		len(snapshot.Projects), len(snapshot.Branches), len(snapshot.Endpoints), stateConfigMapName)
}

// loadPersistedState 从 ConfigMap 加载持久化状态快照。
// 返回 nil 表示首次启动（ConfigMap 不存在）或未运行在集群内（无持久化可用）。
func loadPersistedState() *PersistedState {
	// 非集群内环境（本地调试 / 单元测试）kubePersist 为 nil：
	// 直接返回 nil，避免 nil 指针解引用导致进程启动即 panic。
	if kubePersist == nil {
		log.Printf("persist: 非集群内运行，跳过 ConfigMap 状态恢复")
		return nil
	}
	data, err := kubePersist.readConfigMap(stateConfigMapName)
	if err != nil {
		log.Printf("WARN persist: read configmap: %v", err)
		return nil
	}
	if data == nil {
		log.Printf("persist: no existing state ConfigMap, starting fresh")
		return nil
	}

	stateJSON, ok := data["state.json"]
	if !ok || stateJSON == "" {
		log.Printf("persist: state.json key not found in ConfigMap")
		return nil
	}

	var ps PersistedState
	if err := json.Unmarshal([]byte(stateJSON), &ps); err != nil {
		log.Printf("WARN persist: parse state.json: %v", err)
		return nil
	}

	log.Printf("persist: loaded state from ConfigMap (%d projects, %d branches, %d endpoints)",
		len(ps.Projects), len(ps.Branches), len(ps.Endpoints))
	return &ps
}

// flushPersist 强制立即持久化（用于优雅关闭，当前 phase-1 不实现信号处理）。
func flushPersist() {
	persistMu.Lock()
	if persistTimer != nil {
		persistTimer.Stop()
		persistTimer = nil
	}
	persistDirty = true
	persistMu.Unlock()
	doPersist()
}

// snapshot 获取当前内存状态的只读快照。
func (s *state) snapshot() PersistedState {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ps := PersistedState{
		Projects:  make(map[string]*Project, len(s.projects)),
		Branches:  make(map[string]*Branch, len(s.branches)),
		Endpoints: make(map[string]*Endpoint, len(s.endpoints)),
	}
	for k, v := range s.projects {
		ps.Projects[k] = v
	}
	for k, v := range s.branches {
		ps.Branches[k] = v
	}
	for k, v := range s.endpoints {
		ps.Endpoints[k] = v
	}
	return ps
}

// =============================================================================
// 启动对账
// =============================================================================

// kubePersist 全局 K8s 客户端（持久化用）。nil 表示非 K8s 环境。
var kubePersist *kubeClient

// reconcileOnStartup 启动双向对账：从 ConfigMap 恢复状态，与 K8s/SC 真相对账。
// 根据 plan：
//   - 先 load ConfigMap 快照
//   - 再 List 现有 compute Deployment 修正 endpoint 状态
//   - tenant↔timeline 真相由 SC 维护，不做持久化对账
func reconcileOnStartup() {
	// 1) 尝试从 ConfigMap 加载持久化状态
	persisted := loadPersistedState()
	if persisted != nil {
		st.mu.Lock()
		st.projects = persisted.Projects
		st.branches = persisted.Branches
		st.endpoints = persisted.Endpoints
		if len(st.branches) == 0 {
			// 无持久化分支记录 → 用 bootstrap 默认值补充默认分支
			st.branches = map[string]*Branch{}
		}
		st.mu.Unlock()
		log.Printf("reconcile: restored state from ConfigMap snapshot")
	}

	// 2) 与现有 compute Deployment 对账
	// compute 只能由控制面动态拉起，因此只要控制面运行在集群内（kubePersist != nil）
	// 就需要用 K8s 实际状态修正 endpoint.status。
	if kubePersist != nil {
		reconcileWithK8s()
	}

	// 说明：不再有"默认分支"概念。tenant 与 timeline 一律由 POST /projects 按需创建，
	// 每个 project 的主分支在创建时就已写入 branches，无需在此兜底补齐。
}

// reconcileWithK8s 与 K8s 实际 compute 运行态对账，修正 endpoint.Status。
// 直接复用 reconcileEndpointStatuses（周期对账同款逻辑）：覆盖副本就绪度 / Pod 失败 /
// compute_ctl 内部状态，而不是只看 Deployment 对象是否存在（否则 compute 崩溃后状态会
// 永久停在 running）。持久化由 reconcileEndpointStatuses 内部按变更去抖触发。
// 非 K8s 环境（kubePersist==nil）下无任何效果。
func reconcileWithK8s() {
	reconcileEndpointStatuses()
}
