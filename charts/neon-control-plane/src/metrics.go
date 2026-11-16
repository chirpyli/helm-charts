package main

import (
	"log"
	"sync/atomic"
)

// =============================================================================
// 持久化层可观测性（阶段 1+2 轻量实现）：
// 只记录进程内计数并输出日志，不引入额外监控框架。
// 详细指标与告警阈值属阶段 3 范围。
// =============================================================================

var (
	metricStoreWrites     atomic.Int64 // 持久化写次数
	metricStoreWriteFails atomic.Int64 // 持久化写失败次数
	metricCompensations   atomic.Int64 // 触发补偿次数
	metricCleanupPending  atomic.Int64 // 补偿失败进入待清理队列的次数
	metricCredCacheHit    atomic.Int64 // 凭据缓存命中次数
	metricCredCacheMiss   atomic.Int64 // 凭据缓存未命中次数
)

// recordStoreWrite 记录一次持久化写的结果；错误只打印类型，不打印连接串或凭据。
func recordStoreWrite(err error) {
	metricStoreWrites.Add(1)
	if err != nil {
		metricStoreWriteFails.Add(1)
		logWarn("state store: 写入失败：%v", err)
	}
}

// logStoreMetrics 输出一次当前计数，供启动日志与优雅退出时排障。
func logStoreMetrics() {
	log.Printf("state store metrics: writes=%d fails=%d compensations=%d cleanup_pending=%d cred_cache(hit=%d miss=%d)",
		metricStoreWrites.Load(), metricStoreWriteFails.Load(),
		metricCompensations.Load(), metricCleanupPending.Load(),
		metricCredCacheHit.Load(), metricCredCacheMiss.Load())
}
