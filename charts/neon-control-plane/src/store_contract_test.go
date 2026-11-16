package main

import (
	"context"
	"testing"
)

// StateStore 契约测试：所有实现必须通过同一组用例（阶段 1 验收）。
func TestStateStoreContract(t *testing.T) {
	stores := map[string]StateStore{
		"memory": newMemoryStateStore(),
	}
	for name, s := range stores {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			if err := s.Migrate(ctx); err != nil {
				t.Fatalf("Migrate: %v", err)
			}
			snap, err := s.Load(ctx)
			if err != nil {
				t.Fatalf("Load(空库): %v", err)
			}
			if len(snap.Projects) != 0 || len(snap.Branches) != 0 || len(snap.Endpoints) != 0 {
				t.Fatalf("空库应返回空状态，实际 projects=%d branches=%d endpoints=%d",
					len(snap.Projects), len(snap.Branches), len(snap.Endpoints))
			}

			p := &Project{ProjectID: "proj-contract", TenantID: "tenant-contract", Name: "契约测试"}
			b := &Branch{BranchID: "br-contract", ProjectID: p.ProjectID, TenantID: p.TenantID,
				TimelineID: "tl-contract", Name: "main", Default: true}
			ep := &Endpoint{EndpointID: "ep-contract", ProjectID: p.ProjectID, BranchID: b.BranchID,
				TenantID: p.TenantID, TimelineID: b.TimelineID, ComputeImage: "compute:test",
				Status: EndpointStatusProvisioning}
			cred := &EndpointCredential{EndpointID: ep.EndpointID, RoleName: "cloud_admin",
				DatabaseName: "neondb", Owner: "cloud_admin",
				SecretName: credentialSecretName(ep.EndpointID), SecretKey: defaultCredentialSecretKey}
			bundle := &ProjectBundle{Project: p, Branch: b, Endpoint: ep, Credential: cred}

			if err := s.CreateProjectBundle(ctx, bundle); err != nil {
				t.Fatalf("CreateProjectBundle: %v", err)
			}
			// 重复执行必须幂等，不产生重复实体。
			if err := s.CreateProjectBundle(ctx, bundle); err != nil {
				t.Fatalf("CreateProjectBundle(重复): %v", err)
			}
			snap, err = s.Load(ctx)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if len(snap.Projects) != 1 || len(snap.Branches) != 1 || len(snap.Endpoints) != 1 || len(snap.Credentials) != 1 {
				t.Fatalf("实体计数不符：projects=%d branches=%d endpoints=%d credentials=%d",
					len(snap.Projects), len(snap.Branches), len(snap.Endpoints), len(snap.Credentials))
			}

			fp, pending, gen := "fingerprint-1", true, int64(7)
			if err := s.UpdateRecoveryState(ctx, ep.EndpointID, RecoveryPatch{
				RouteFingerprint: &fp, PendingReconfigure: &pending, SafekeeperGeneration: &gen,
			}); err != nil {
				t.Fatalf("UpdateRecoveryState: %v", err)
			}
			snap, _ = s.Load(ctx)
			got := snap.Endpoints[ep.EndpointID]
			if got.RouteFingerprint != fp || !got.PendingReconfigure || got.SafekeeperGeneration != gen {
				t.Fatalf("恢复字段未正确落库：%+v", got)
			}

			if err := s.UpdateObservedStatus(ctx, []StatusUpdate{
				{EndpointID: ep.EndpointID, Status: EndpointStatusRunning},
			}); err != nil {
				t.Fatalf("UpdateObservedStatus: %v", err)
			}
			snap, _ = s.Load(ctx)
			if snap.Endpoints[ep.EndpointID].Status != EndpointStatusRunning {
				t.Fatal("状态批量更新未生效")
			}

			if err := s.DeleteEndpoint(ctx, ep.EndpointID); err != nil {
				t.Fatalf("DeleteEndpoint: %v", err)
			}
			snap, _ = s.Load(ctx)
			if len(snap.Endpoints) != 0 {
				t.Fatal("端点删除后端点仍存在")
			}
			if err := s.DeleteProject(ctx, p.ProjectID); err != nil {
				t.Fatalf("DeleteProject: %v", err)
			}
			snap, _ = s.Load(ctx)
			if len(snap.Projects) != 0 || len(snap.Branches) != 0 {
				t.Fatal("项目删除后关联分支未清理")
			}
			if err := s.Health(ctx); err != nil {
				t.Fatalf("Health: %v", err)
			}
			if err := s.Flush(ctx); err != nil {
				t.Fatalf("Flush: %v", err)
			}
			if err := s.Close(ctx); err != nil {
				t.Fatalf("Close: %v", err)
			}
		})
	}
}

// 分支删除必须级联清理其端点（子→父顺序由 DeleteProject 保证）。
func TestMemoryStateStoreDeleteBranchCascade(t *testing.T) {
	s := newMemoryStateStore()
	ctx := context.Background()
	p := &Project{ProjectID: "proj-cascade", TenantID: "tenant-cascade"}
	b := &Branch{BranchID: "br-cascade", ProjectID: p.ProjectID, TenantID: p.TenantID, TimelineID: "tl-cascade"}
	ep := &Endpoint{EndpointID: "ep-cascade", ProjectID: p.ProjectID, BranchID: b.BranchID,
		TenantID: p.TenantID, TimelineID: b.TimelineID}
	if err := s.CreateProjectBundle(ctx, &ProjectBundle{Project: p, Branch: b, Endpoint: ep}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteBranch(ctx, b.BranchID); err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Load(ctx)
	if len(snap.Branches) != 0 || len(snap.Endpoints) != 0 {
		t.Fatal("DeleteBranch 未级联清理端点")
	}
}
