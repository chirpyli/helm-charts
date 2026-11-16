package main

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"testing"
	"time"
)

// PG 集成测试：通过 NEON_CP_TEST_DATABASE_URL 指定「控制面独立库」连接串。
// 未提供时跳过；严禁指向 storage_controller 库（其中保存 generation numbers）。
func TestPostgresStateStoreIntegration(t *testing.T) {
	dsn := os.Getenv("NEON_CP_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 NEON_CP_TEST_DATABASE_URL，跳过外部库集成测试")
	}
	ctx := context.Background()
	s, err := newPostgresStateStore(ctx, dsn, StateStoreConfig{
		Backend: "postgres", ConnectTimeoutSeconds: 5, MaxConnections: 4,
	})
	if err != nil {
		t.Fatalf("连接控制面独立库失败：%v", err)
	}
	defer s.Close(ctx)

	// 迁移必须幂等：连续执行两次不报错，且 checksum 一致。
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("首次迁移：%v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("重复迁移应幂等：%v", err)
	}
	if err := s.Health(ctx); err != nil {
		t.Fatalf("Health：%v", err)
	}

	suffix := fmt.Sprintf("%d", rand.Int63n(1<<62))
	projectID, tenantID := "proj-pg-"+suffix, "tenant-pg-"+suffix
	branchID, timelineID := "br-pg-"+suffix, "tl-pg-"+suffix
	endpointID := "ep-pg-" + suffix
	p := &Project{ProjectID: projectID, TenantID: tenantID, Name: "集成测试",
		CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	b := &Branch{BranchID: branchID, ProjectID: projectID, TenantID: tenantID,
		TimelineID: timelineID, Name: "main", Default: true}
	ep := &Endpoint{EndpointID: endpointID, ProjectID: projectID, BranchID: branchID,
		TenantID: tenantID, TimelineID: timelineID, ComputeImage: "compute:test",
		Status: EndpointStatusProvisioning}
	cred := &EndpointCredential{EndpointID: endpointID, RoleName: "cloud_admin",
		DatabaseName: "neondb", Owner: "cloud_admin",
		SecretName: credentialSecretName(endpointID), SecretKey: defaultCredentialSecretKey}
	bundle := &ProjectBundle{Project: p, Branch: b, Endpoint: ep, Credential: cred}
	defer func() { _ = s.DeleteProject(ctx, projectID) }()

	if err := s.CreateProjectBundle(ctx, bundle); err != nil {
		t.Fatalf("CreateProjectBundle：%v", err)
	}
	if err := s.CreateProjectBundle(ctx, bundle); err != nil {
		t.Fatalf("重复创建应幂等：%v", err)
	}
	snap, err := s.Load(ctx)
	if err != nil {
		t.Fatalf("Load：%v", err)
	}
	if snap.Projects[projectID] == nil || snap.Branches[branchID] == nil || snap.Endpoints[endpointID] == nil {
		t.Fatal("加载结果与写入不一致")
	}
	// 完整 ComputeSpec 与观测字段不得落库，重启后由对账重建。
	if snap.Endpoints[endpointID].Spec != nil || snap.Endpoints[endpointID].ComputeStatus != "" {
		t.Fatal("派生字段不应从库中加载")
	}
	if snap.Endpoints[endpointID].RoleName != "cloud_admin" || snap.Credentials[endpointID] == nil {
		t.Fatal("凭据引用未随端点恢复")
	}

	fp, pending, gen := "fp-pg", true, int64(9)
	if err := s.UpdateRecoveryState(ctx, endpointID, RecoveryPatch{
		RouteFingerprint: &fp, PendingReconfigure: &pending, SafekeeperGeneration: &gen,
	}); err != nil {
		t.Fatalf("UpdateRecoveryState：%v", err)
	}
	snap, _ = s.Load(ctx)
	got := snap.Endpoints[endpointID]
	if got.RouteFingerprint != fp || !got.PendingReconfigure || got.SafekeeperGeneration != gen {
		t.Fatalf("恢复字段未落库：%+v", got)
	}
	if err := s.UpdateObservedStatus(ctx, []StatusUpdate{
		{EndpointID: endpointID, Status: EndpointStatusRunning},
	}); err != nil {
		t.Fatalf("UpdateObservedStatus：%v", err)
	}
	if err := s.DeleteProject(ctx, projectID); err != nil {
		t.Fatalf("DeleteProject：%v", err)
	}
	snap, _ = s.Load(ctx)
	if snap.Projects[projectID] != nil || snap.Branches[branchID] != nil || snap.Endpoints[endpointID] != nil {
		t.Fatal("删除项目后仍有残留实体")
	}
}
