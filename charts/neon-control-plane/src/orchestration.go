package main

import (
	"context"
	"fmt"
	"log"
	"sync"
)

// =============================================================================
// 跨系统写入编排（Saga）
//
// PG 事务只覆盖本地业务表；SC、K8s、Secret 属外部副作用，无法原子提交。
// 本文件集中管理创建流程的逆序补偿与补偿失败的待清理队列
// （docs/develop.md 4.3.1）。StateStore 不参与编排，只提供本地事务。
// =============================================================================

// cleanupTask 补偿失败后需要人工或后续重试清理的资源记录。
type cleanupTask struct {
	Kind       string // db | secret | compute | tenant | timeline
	ResourceID string
	Reason     string
}

var cleanupState = struct {
	sync.Mutex
	tasks []cleanupTask
}{tasks: []cleanupTask{}}

// persistCreateBundle 在 Saga 的 PG 步骤把 project/branch/endpoint/credential 一次性提交到本地库。
func persistCreateBundle(ctx context.Context, b *ProjectBundle) error {
	if err := getStore().CreateProjectBundle(ctx, b); err != nil {
		return fmt.Errorf("Saga 步骤 pg-transaction 失败：%w", err)
	}
	return nil
}

// compensateCreate 按逆序（K8s → PG → Secret → SC timeline → SC tenant）撤销已完成的副作用，
// 并同步撤销内存态，避免「回滚后 list/get 仍能看到资源、重启后消失」。
func compensateCreate(ctx context.Context, b *ProjectBundle) {
	if b == nil || b.Project == nil {
		return
	}
	metricCompensations.Add(1)
	projectID, tenantID := b.Project.ProjectID, b.Project.TenantID
	endpointID := ""
	if b.Endpoint != nil {
		endpointID = b.Endpoint.EndpointID
	}

	// 1) 撤销可能已创建的 compute Deployment / Service。
	if endpointID != "" {
		if err := cleanupK8sComputeResources(endpointID); err != nil {
			queueCleanup("compute", endpointID, err.Error())
		}
	}
	// 2) 撤销本地库事务结果。
	if err := getStore().DeleteProject(ctx, projectID); err != nil {
		queueCleanup("db", projectID, err.Error())
	}
	// 3) 撤销凭据 Secret 与缓存。
	if endpointID != "" {
		if err := credStore.Delete(endpointID); err != nil {
			queueCleanup("secret", endpointID, err.Error())
		}
	}
	// 4) 撤销 SC 侧 timeline 与 tenant。
	if sc != nil {
		if b.Branch != nil && b.Branch.TimelineID != "" {
			if err := sc.deleteTimeline(ctx, tenantID, b.Branch.TimelineID); err != nil {
				queueCleanup("timeline", b.Branch.TimelineID, err.Error())
			}
		}
		if err := sc.deleteTenant(ctx, tenantID); err != nil {
			queueCleanup("tenant", tenantID, err.Error())
		}
	}
	// 5) 撤销内存态：与本地库保持一致。
	if st != nil {
		st.mu.Lock()
		delete(st.projects, projectID)
		for id, br := range st.branches {
			if br != nil && br.ProjectID == projectID {
				delete(st.branches, id)
			}
		}
		for id, ep := range st.endpoints {
			if ep != nil && ep.ProjectID == projectID {
				delete(st.endpoints, id)
			}
		}
		st.mu.Unlock()
	}
	log.Printf("compensate: project=%s 创建流程已回滚（待清理项=%d）", projectID, pendingCleanupCount())
}

// queueCleanup 记录补偿失败项；阶段 1+2 只做内存记录 + 告警，重试 worker 属阶段 3。
func queueCleanup(kind, resourceID, reason string) {
	cleanupState.Lock()
	cleanupState.tasks = append(cleanupState.tasks, cleanupTask{
		Kind: kind, ResourceID: resourceID, Reason: reason,
	})
	cleanupState.Unlock()
	metricCleanupPending.Add(1)
	logWarn("compensate: 补偿失败需人工清理 kind=%s resource=%s: %s", kind, resourceID, reason)
}

// pendingCleanupCount 返回当前待清理项数量，供测试与日志使用。
func pendingCleanupCount() int {
	cleanupState.Lock()
	defer cleanupState.Unlock()
	return len(cleanupState.tasks)
}
