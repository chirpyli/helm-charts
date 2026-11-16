package main

import (
	"testing"
)

func TestCredentialCacheEnsureGetInvalidate(t *testing.T) {
	ep := &Endpoint{
		EndpointID:    "ep-credential-test",
		ScramVerifier: "SCRAM-SHA-256$4096:c2FsdA$stored:server",
		Spec: &ComputeSpec{Cluster: &ClusterSpec{
			Roles:     []RoleSpec{{Name: "sally", EncryptedPassword: "SCRAM-SHA-256$4096:c2FsdA$stored:server"}},
			Databases: []DatabaseSpec{{Name: "mydb", Owner: "sally"}},
		}},
	}
	defer credStore.Invalidate(ep.EndpointID)

	cred, err := credStore.Ensure(ep)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if cred.SecretName != credentialSecretName(ep.EndpointID) || cred.SecretKey != defaultCredentialSecretKey {
		t.Fatalf("凭据引用不符合约定：%+v", cred)
	}
	if cred.RoleName != "sally" || cred.DatabaseName != "mydb" || cred.Owner != "sally" {
		t.Fatalf("角色/库信息未按 spec 回填：%+v", cred)
	}

	v, err := credStore.Get(ep)
	if err != nil || v != ep.ScramVerifier {
		t.Fatalf("缓存未命中或值不一致：v=%q err=%v", v, err)
	}
	// 模拟重启：内存字段清空，仅靠缓存与引用恢复。
	ep.ScramVerifier = ""
	v, err = credStore.Get(ep)
	if err != nil || v == "" {
		t.Fatalf("缓存回退失败：v=%q err=%v", v, err)
	}
	credStore.Invalidate(ep.EndpointID)
	if _, err := credStore.Get(ep); err == nil {
		t.Fatal("非集群环境下缓存失效后应返回可行动错误，而不是空字符串")
	}
}

func TestCredentialCacheDeleteWithoutCluster(t *testing.T) {
	defer credStore.Invalidate("ep-delete-test")
	credStore.verifiers["ep-delete-test"] = "dummy"
	if err := credStore.Delete("ep-delete-test"); err != nil {
		t.Fatalf("非集群环境下删除凭据不应报错：%v", err)
	}
	if _, ok := credStore.verifiers["ep-delete-test"]; ok {
		t.Fatal("Delete 后缓存未清理")
	}
}
