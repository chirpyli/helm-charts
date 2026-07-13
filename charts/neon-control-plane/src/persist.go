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
// 返回 nil 表示首次启动（ConfigMap 不存在）。
func loadPersistedState() *PersistedState {
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

	// 2) 如果启用 K8s compute，与现有 compute Deployment 对账
	if cfg.EnableK8sCompute && kubePersist != nil {
		reconcileWithK8s()
	}

	// 3) 确保默认分支存在（若既无持久化也无任何分支）
	st.mu.Lock()
	if st.defaultBranchID != "" {
		if _, ok := st.branches[st.defaultBranchID]; !ok {
			st.branches[st.defaultBranchID] = &Branch{
				BranchID:   st.defaultBranchID,
				ProjectID:  "", // 默认分支不属于特定 project
				TenantID:   st.tenantID,
				TimelineID: st.defaultTimelineID,
				Name:       "main",
				Default:    true,
			}
		}
	}
	st.mu.Unlock()
}

// reconcileWithK8s 与 K8s 实际 compute Deployment 对账。
// 修正 endpoint.status：K8s 中存在的 Deployment 标记为 running，不存在的标记为 stopped。
func reconcileWithK8s() {
	names, err := kubePersist.listComputeDeployments()
	if err != nil {
		log.Printf("WARN reconcile: list compute deployments: %v", err)
		return
	}
	log.Printf("reconcile: found %d compute deployments in K8s", len(names))

	existing := make(map[string]bool)
	for _, name := range names {
		existing[name] = true
	}

	st.mu.Lock()
	defer st.mu.Unlock()

	for _, ep := range st.endpoints {
		depName := computeDeploymentName(ep.EndpointID)
		if existing[depName] {
			ep.Status = "running"
		} else {
			ep.Status = "stopped"
		}
	}
	log.Printf("reconcile: state reconciled with K8s")
}
