package main

import (
	"context"
	"testing"
)

// 补偿流程必须撤销已落库的 bundle；非集群环境下 Secret / K8s / SC 步骤安全退化。
func TestCompensateCreateRemovesBundle(t *testing.T) {
	storeMu.Lock()
	old := stateStore
	stateStore = newMemoryStateStore()
	storeMu.Unlock()
	defer func() {
		storeMu.Lock()
		stateStore = old
		storeMu.Unlock()
	}()

	ctx := context.Background()
	p := &Project{ProjectID: "proj-compensate", TenantID: "tenant-compensate"}
	b := &Branch{BranchID: "br-compensate", ProjectID: p.ProjectID, TenantID: p.TenantID, TimelineID: "tl-compensate"}
	ep := &Endpoint{EndpointID: "ep-compensate", ProjectID: p.ProjectID, BranchID: b.BranchID,
		TenantID: p.TenantID, TimelineID: b.TimelineID}
	bundle := &ProjectBundle{Project: p, Branch: b, Endpoint: ep}
	if err := persistCreateBundle(ctx, bundle); err != nil {
		t.Fatalf("persistCreateBundle: %v", err)
	}
	snap, _ := getStore().Load(ctx)
	if len(snap.Projects) != 1 {
		t.Fatal("前置条件失败：项目未落库")
	}

	compensateCreate(ctx, bundle)
	snap, _ = getStore().Load(ctx)
	if len(snap.Projects) != 0 || len(snap.Branches) != 0 || len(snap.Endpoints) != 0 {
		t.Fatalf("补偿后仍有残留实体：projects=%d branches=%d endpoints=%d",
			len(snap.Projects), len(snap.Branches), len(snap.Endpoints))
	}
}

// 待清理队列可记录补偿失败项，供后续重试/人工介入。
func TestQueueCleanup(t *testing.T) {
	before := pendingCleanupCount()
	queueCleanup("db", "proj-failed", "模拟补偿失败")
	if pendingCleanupCount() != before+1 {
		t.Fatal("补偿失败项未进入待清理队列")
	}
}
