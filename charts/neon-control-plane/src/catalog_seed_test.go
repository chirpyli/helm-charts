package main

import (
	"context"
	"testing"
)

func TestSeedCatalogRegistryFromEndpointAndParent(t *testing.T) {
	oldState := st
	oldStore := stateStore
	st = &state{
		projects:  map[string]*Project{},
		branches:  map[string]*Branch{},
		endpoints: map[string]*Endpoint{},
		roles:     map[string]*Role{},
		databases: map[string]*Database{},
	}
	storeMu.Lock()
	stateStore = newMemoryStateStore()
	storeMu.Unlock()
	t.Cleanup(func() {
		st = oldState
		storeMu.Lock()
		stateStore = oldStore
		storeMu.Unlock()
	})

	st.projects["proj-seed"] = &Project{ProjectID: "proj-seed", TenantID: "tenant-seed"}
	st.branches["br-main"] = &Branch{
		BranchID: "br-main", ProjectID: "proj-seed", TenantID: "tenant-seed",
		TimelineID: "tl-main", Name: "main", Default: true,
	}
	st.branches["br-child"] = &Branch{
		BranchID: "br-child", ProjectID: "proj-seed", TenantID: "tenant-seed",
		TimelineID: "tl-child", Name: "child", ParentID: "br-main",
	}
	// 升级前的 endpoint：只有 endpoint 级凭据引用。
	st.endpoints["ep-legacy"] = &Endpoint{
		EndpointID: "ep-legacy", ProjectID: "proj-seed", BranchID: "br-main",
		Type: EndpointTypeReadWrite, Status: EndpointStatusRunning,
		RoleName: "sally", DatabaseName: "mydb", Owner: "sally",
		CredentialSecretName: "neon-cp-cred-ep-legacy",
		CredentialSecretKey:  defaultCredentialSecretKey,
		ScramVerifier:        "SCRAM-SHA-256$4096:c2FsdA$stored:server",
	}
	t.Cleanup(func() { credStore.Invalidate("ep-legacy") })

	seedCatalogRegistry()

	mainRoles, mainDBs := catalogForBranch("br-main")
	if len(mainRoles) != 1 || mainRoles[0].Name != "sally" || !mainRoles[0].Protected {
		t.Fatalf("根分支回填错误：%+v", mainRoles)
	}
	if len(mainDBs) != 1 || mainDBs[0].Name != "mydb" || mainDBs[0].OwnerName != "sally" {
		t.Fatalf("根分支数据库回填错误：%+v", mainDBs)
	}
	if mainRoles[0].SecretName == "neon-cp-cred-ep-legacy" {
		t.Fatalf("应把 endpoint Secret 复制到独立角色 Secret，实际 %q", mainRoles[0].SecretName)
	}
	if _, err := credStore.GetSecret(mainRoles[0].SecretName, mainRoles[0].SecretKey); err != nil {
		t.Fatalf("角色 Secret 未写入：%v", err)
	}

	childRoles, childDBs := catalogForBranch("br-child")
	if len(childRoles) != 1 || len(childDBs) != 1 {
		t.Fatalf("子分支未继承 catalog：roles=%+v dbs=%+v", childRoles, childDBs)
	}
	if childRoles[0].Name != "sally" || childRoles[0].BranchID != "br-child" {
		t.Fatalf("子分支角色错误：%+v", childRoles[0])
	}
	if childRoles[0].SecretName == mainRoles[0].SecretName {
		t.Fatalf("子分支必须使用独立 Secret，实际与父分支相同：%s", childRoles[0].SecretName)
	}
	if childDBs[0].Name != "mydb" || childDBs[0].OwnerName != "sally" {
		t.Fatalf("子分支数据库错误：%+v", childDBs[0])
	}

	// 幂等：再次执行不应新增行或改变 Secret。
	beforeMainSecret := mainRoles[0].SecretName
	seedCatalogRegistry()
	mainRoles2, _ := catalogForBranch("br-main")
	if len(mainRoles2) != 1 || mainRoles2[0].SecretName != beforeMainSecret {
		t.Fatalf("seed 不幂等：%+v", mainRoles2)
	}

	// 持久层也应包含 catalog（内存 store 的 Load 结果）。
	snap, err := getStore().Load(context.Background())
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	if len(snap.Roles) != 2 || len(snap.Databases) != 2 {
		t.Fatalf("持久层 catalog 数量错误：roles=%d dbs=%d", len(snap.Roles), len(snap.Databases))
	}
	t.Cleanup(func() {
		_ = credStore.DeleteSecret(mainRoles[0].SecretName, mainRoles[0].SecretKey)
		_ = credStore.DeleteSecret(childRoles[0].SecretName, childRoles[0].SecretKey)
	})
}
