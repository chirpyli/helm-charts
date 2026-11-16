package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestEndpointCreateRequestNormalize 校验官方嵌套形态与旧扁平形态都能解析，
// 且 type 缺失/非法时显式报错（旧实现会静默按 read_write 处理）。
func TestEndpointCreateRequestNormalize(t *testing.T) {
	nested := EndpointCreateRequest{
		Endpoint: &EndpointCreateSpec{BranchID: "br-nested", Type: EndpointTypeReadOnly},
	}
	branchID, epType, err := nested.normalize()
	if err != nil || branchID != "br-nested" || epType != EndpointTypeReadOnly {
		t.Fatalf("官方嵌套形态解析错误：branch=%q type=%q err=%v", branchID, epType, err)
	}

	flat := EndpointCreateRequest{BranchID: "br-flat", Type: EndpointTypeReadWrite}
	branchID, epType, err = flat.normalize()
	if err != nil || branchID != "br-flat" || epType != EndpointTypeReadWrite {
		t.Fatalf("旧扁平形态解析错误：branch=%q type=%q err=%v", branchID, epType, err)
	}

	if _, _, err := (&EndpointCreateRequest{}).normalize(); err == nil {
		t.Fatal("缺少 type 时必须报错，不能默认成 read_write")
	}
	if _, _, err := (&EndpointCreateRequest{Type: "readonly"}).normalize(); err == nil {
		t.Fatal("非法 type 必须报错")
	}
}

// TestBranchReadWriteUniquenessAndIdentity 覆盖：
//   - 分支 read_write 唯一性判断（含迁移前 Type 为空的历史记录）；
//   - 新分支没有 endpoint 时，连接身份沿 parent_id 从父分支继承。
func TestBranchReadWriteUniquenessAndIdentity(t *testing.T) {
	oldState := st
	st = &state{
		projects:  map[string]*Project{},
		branches:  map[string]*Branch{},
		endpoints: map[string]*Endpoint{},
	}
	defer func() { st = oldState }()

	st.branches["br-main"] = &Branch{BranchID: "br-main", ProjectID: "proj-1", Name: "main", Default: true}
	st.branches["br-child"] = &Branch{BranchID: "br-child", ProjectID: "proj-1", Name: "child", ParentID: "br-main"}
	st.endpoints["ep-primary"] = &Endpoint{
		EndpointID:           "ep-primary",
		ProjectID:            "proj-1",
		BranchID:             "br-main",
		Type:                 EndpointTypeReadWrite,
		RoleName:             "sally",
		DatabaseName:         "mydb",
		Owner:                "sally",
		CredentialSecretName: "neon-cp-cred-ep-primary",
		CredentialSecretKey:  defaultCredentialSecretKey,
	}

	if !branchHasReadWriteLocked("br-main") {
		t.Fatal("main 分支已有 read_write，应判定为冲突")
	}
	if branchHasReadWriteLocked("br-child") {
		t.Fatal("child 分支尚无 endpoint，不应判定冲突")
	}

	id := findBranchIdentityLocked("br-child")
	if id == nil || id.RoleName != "sally" || id.DatabaseName != "mydb" {
		t.Fatalf("未从父分支继承连接身份：%+v", id)
	}
	if id.SecretName != "neon-cp-cred-ep-primary" {
		t.Fatalf("应共享父分支角色的 Secret，实际 %q", id.SecretName)
	}

	// 只读副本不占用 read_write 名额；没有 read_write 时应回退到只读副本的身份。
	st.endpoints["ep-ro"] = &Endpoint{
		EndpointID:   "ep-ro",
		ProjectID:    "proj-1",
		BranchID:     "br-child",
		Type:         EndpointTypeReadOnly,
		RoleName:     "sally",
		DatabaseName: "mydb",
	}
	if branchHasReadWriteLocked("br-child") {
		t.Fatal("只有 read_only 时不应判定 read_write 冲突")
	}
	if id := findBranchIdentityLocked("br-child"); id == nil || id.RoleName != "sally" {
		t.Fatalf("应回退到只读副本的连接身份：%+v", id)
	}

	// 历史记录 Type 为空时按 read_write 处理（迁移回填前的数据）。
	st.endpoints["ep-legacy"] = &Endpoint{
		EndpointID: "ep-legacy", ProjectID: "proj-1", BranchID: "br-child", RoleName: "sally",
	}
	if !branchHasReadWriteLocked("br-child") {
		t.Fatal("Type 为空的历史记录应按 read_write 参与唯一性判断")
	}
}

// TestAttachReferenceSharesSecret 确保创建副本不会生成独立 Secret，
// 而是复用分支角色的 SCRAM 凭据引用（官方语义：endpoint 不创建 role）。
func TestAttachReferenceSharesSecret(t *testing.T) {
	ep := &Endpoint{EndpointID: "ep-shared-ref", Type: EndpointTypeReadOnly}
	defer credStore.Invalidate(ep.EndpointID)

	cred := credStore.AttachReference(ep, "sally", "mydb", "", "neon-cp-cred-ep-primary", defaultCredentialSecretKey)
	if cred.SecretName != "neon-cp-cred-ep-primary" {
		t.Fatalf("应共享源 Secret，实际 %q", cred.SecretName)
	}
	if cred.SecretName == credentialSecretName(ep.EndpointID) {
		t.Fatal("不应为副本创建独立 Secret")
	}
	if cred.Owner != "sally" {
		t.Fatalf("owner 应回退为 roleName：%+v", cred)
	}
	if ep.RoleName != "sally" || ep.DatabaseName != "mydb" {
		t.Fatalf("endpoint 身份未回填：%+v", ep)
	}
}

// TestEndpointJSONShape 校验对外 JSON：包含 type/current_state，且不泄漏凭据。
func TestEndpointJSONShape(t *testing.T) {
	ep := &Endpoint{
		EndpointID:           "ep-json",
		ProjectID:            "proj-json",
		BranchID:             "br-json",
		Type:                 EndpointTypeReadOnly,
		Status:               EndpointStatusRunning,
		RoleName:             "sally",
		DatabaseName:         "mydb",
		CredentialSecretName: "neon-cp-cred-ep-json",
		ScramVerifier:        "SCRAM-SHA-256$4096:c2FsdA$stored:server",
	}
	data, err := json.Marshal(ep)
	if err != nil {
		t.Fatalf("序列化失败：%v", err)
	}
	s := string(data)
	for _, want := range []string{`"type":"read_only"`, `"current_state":"active"`} {
		if !strings.Contains(s, want) {
			t.Errorf("缺少字段 %s：%s", want, s)
		}
	}
	for _, forbidden := range []string{"SCRAM", "sally", "neon-cp-cred"} {
		if strings.Contains(s, forbidden) {
			t.Errorf("对外 JSON 不应包含 %q：%s", forbidden, s)
		}
	}
}
