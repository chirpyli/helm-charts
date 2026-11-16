package main

import "log"

// =============================================================================
// 持久化生命周期
//
// 旧版在此处维护全量 JSON 快照（见 git 历史）。
// 按 docs/develop.md，状态已改为外部 PostgreSQL 独立库：
//   - initStateStore 负责迁移、加载与内存态装配；
//   - 各写路径直接调用 StateStore 的实体级方法；
//   - SIGTERM 时由 main.go 调用 flushStateStore 完成优雅退出。
// =============================================================================

// kubePersist 全局 K8s 客户端：compute 编排与凭据 Secret 读写共用。
// nil 表示不在集群内（本地调试 / 单测），此时 compute 管理与 Secret 读写跳过。
var kubePersist *kubeClient

// reconcileOnStartup 启动时初始化持久层，并用 K8s 真相对账修正 endpoint 状态。
// PG 连接 / 迁移 / 加载失败一律 fail fast，绝不退化为空状态继续提供服务。
func reconcileOnStartup() {
	if err := initStateStore(); err != nil {
		log.Fatalf("state store: 初始化失败：%v", err)
	}
	if kubePersist != nil {
		reconcileWithK8s()
	}
}

// reconcileWithK8s 复用周期状态对账逻辑，覆盖副本就绪度 / Pod 失败 / compute_ctl 状态。
func reconcileWithK8s() {
	reconcileEndpointStatuses()
}

// flushStateStore 优雅退出时输出计数、刷新并关闭持久层。
func flushStateStore() {
	logStoreMetrics()
	closeStateStore()
}
