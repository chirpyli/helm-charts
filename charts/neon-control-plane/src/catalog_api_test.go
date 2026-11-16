package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// setupCatalogFixture 构造一个带初始角色/数据库的分支。
func setupCatalogFixture(t *testing.T) {
	t.Helper()
	oldState := st
	st = &state{
		projects:  map[string]*Project{},
		branches:  map[string]*Branch{},
		endpoints: map[string]*Endpoint{},
		roles:     map[string]*Role{},
		databases: map[string]*Database{},
	}
	t.Cleanup(func() { st = oldState })

	st.projects["proj-cat"] = &Project{ProjectID: "proj-cat", TenantID: "tenant-cat", Name: "cat"}
	st.branches["br-main"] = &Branch{
		BranchID: "br-main", ProjectID: "proj-cat", TenantID: "tenant-cat",
		TimelineID: "tl-main", Name: "main", Default: true,
	}
	st.endpoints["ep-rw"] = &Endpoint{
		EndpointID: "ep-rw", ProjectID: "proj-cat", BranchID: "br-main",
		Type: EndpointTypeReadWrite, Status: EndpointStatusRunning,
	}

	role := &Role{
		BranchID: "br-main", Name: "sally", Protected: true,
		SecretName: roleSecretName("br-main", "sally"), SecretKey: defaultCredentialSecretKey,
	}
	db := &Database{BranchID: "br-main", Name: "mydb", OwnerName: "sally"}
	st.roles[catalogKey("br-main", "sally")] = role
	st.databases[catalogKey("br-main", "mydb")] = db

	// 预置 verifier，避免 handler 走到 K8s/SC。
	if err := credStore.PutSecret(role.SecretName, role.SecretKey, "SCRAM-SHA-256$4096:c2FsdA$stored:server"); err != nil {
		t.Fatalf("预置角色 Secret 失败：%v", err)
	}
	t.Cleanup(func() { _ = credStore.DeleteSecret(role.SecretName, role.SecretKey) })
}

func callCatalogAPI(t *testing.T, method, path, body string) (int, map[string]interface{}) {
	t.Helper()
	var reader *bytes.Reader
	if body == "" {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader([]byte(body))
	}
	req := httptest.NewRequest(method, path, reader)
	rec := httptest.NewRecorder()
	apiHandler(rec, req)

	var resp map[string]interface{}
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("响应不是 JSON 对象：%s", rec.Body.String())
		}
	}
	return rec.Code, resp
}

func TestCreateRoleValidation(t *testing.T) {
	setupCatalogFixture(t)

	cases := []struct {
		name     string
		body     string
		wantCode int
	}{
		{"非法角色名", `{"role":{"name":"1-bad"}}`, http.StatusBadRequest},
		{"空角色名", `{"role":{"name":""}}`, http.StatusBadRequest},
		{"no_login 不支持", `{"role":{"name":"app","no_login":true}}`, http.StatusBadRequest},
		{"重名", `{"role":{"name":"sally"}}`, http.StatusConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, resp := callCatalogAPI(t, http.MethodPost,
				"/projects/proj-cat/branches/br-main/roles", tc.body)
			if code != tc.wantCode {
				t.Fatalf("期望 %d，实际 %d（%v）", tc.wantCode, code, resp)
			}
			if resp["error"] == nil {
				t.Fatalf("错误响应应包含 error：%v", resp)
			}
		})
	}

	// 不存在的 project/branch 统一 404。
	code, _ := callCatalogAPI(t, http.MethodPost,
		"/projects/proj-missing/branches/br-main/roles", `{"role":{"name":"app"}}`)
	if code != http.StatusNotFound {
		t.Fatalf("project 不存在期望 404，实际 %d", code)
	}
}

func TestRoleReadAPIs(t *testing.T) {
	setupCatalogFixture(t)

	// 列表不应包含 password，且包含初始角色。
	code, resp := callCatalogAPI(t, http.MethodGet,
		"/projects/proj-cat/branches/br-main/roles", "")
	if code != http.StatusOK {
		t.Fatalf("list roles 期望 200，实际 %d（%v）", code, resp)
	}
	roles, _ := resp["roles"].([]interface{})
	if len(roles) != 1 {
		t.Fatalf("期望 1 个角色，实际 %v", resp)
	}
	first := roles[0].(map[string]interface{})
	if _, hasPassword := first["password"]; hasPassword {
		t.Fatalf("list roles 不应返回 password：%v", first)
	}
	if first["protected"] != true {
		t.Fatalf("初始角色应为 protected：%v", first)
	}

	// 单查存在/不存在。
	code, resp = callCatalogAPI(t, http.MethodGet,
		"/projects/proj-cat/branches/br-main/roles/sally", "")
	if code != http.StatusOK || resp["role"] == nil {
		t.Fatalf("get role 期望 200 + role，实际 %d（%v）", code, resp)
	}
	code, _ = callCatalogAPI(t, http.MethodGet,
		"/projects/proj-cat/branches/br-main/roles/app", "")
	if code != http.StatusNotFound {
		t.Fatalf("不存在角色期望 404，实际 %d", code)
	}

	// 受保护角色不可删除、不可重置密码。
	code, _ = callCatalogAPI(t, http.MethodDelete,
		"/projects/proj-cat/branches/br-main/roles/sally", "")
	if code != http.StatusForbidden {
		t.Fatalf("保护角色删除期望 403，实际 %d", code)
	}
	code, _ = callCatalogAPI(t, http.MethodPost,
		"/projects/proj-cat/branches/br-main/roles/sally/reset_password", "")
	if code != http.StatusForbidden {
		t.Fatalf("保护角色重置密码期望 403，实际 %d", code)
	}

	// reveal_password 明确 501。
	code, resp = callCatalogAPI(t, http.MethodGet,
		"/projects/proj-cat/branches/br-main/roles/sally/reveal_password", "")
	if code != http.StatusNotImplemented || resp["error"] == nil {
		t.Fatalf("reveal_password 期望 501 + error，实际 %d（%v）", code, resp)
	}
}

func TestDatabaseValidation(t *testing.T) {
	setupCatalogFixture(t)

	cases := []struct {
		name     string
		body     string
		wantCode int
	}{
		{"非法库名", `{"database":{"name":"1-bad","owner_name":"sally"}}`, http.StatusBadRequest},
		{"owner 不存在", `{"database":{"name":"app_db","owner_name":"ghost"}}`, http.StatusBadRequest},
		{"重名", `{"database":{"name":"mydb","owner_name":"sally"}}`, http.StatusConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, resp := callCatalogAPI(t, http.MethodPost,
				"/projects/proj-cat/branches/br-main/databases", tc.body)
			if code != tc.wantCode {
				t.Fatalf("期望 %d，实际 %d（%v）", tc.wantCode, code, resp)
			}
		})
	}

	// 列表与单查。
	code, resp := callCatalogAPI(t, http.MethodGet,
		"/projects/proj-cat/branches/br-main/databases", "")
	if code != http.StatusOK || resp["databases"] == nil {
		t.Fatalf("list databases 期望 200，实际 %d（%v）", code, resp)
	}
	code, _ = callCatalogAPI(t, http.MethodGet,
		"/projects/proj-cat/branches/br-main/databases/mydb", "")
	if code != http.StatusOK {
		t.Fatalf("get database 期望 200，实际 %d", code)
	}
	code, _ = callCatalogAPI(t, http.MethodGet,
		"/projects/proj-cat/branches/br-main/databases/missing", "")
	if code != http.StatusNotFound {
		t.Fatalf("不存在数据库期望 404，实际 %d", code)
	}
	code, _ = callCatalogAPI(t, http.MethodPatch,
		"/projects/proj-cat/branches/br-main/databases/missing", `{"database":{"name":"x"}}`)
	if code != http.StatusNotFound {
		t.Fatalf("patch 不存在数据库期望 404，实际 %d", code)
	}
	code, _ = callCatalogAPI(t, http.MethodDelete,
		"/projects/proj-cat/branches/br-main/databases/missing", "")
	if code != http.StatusNotFound {
		t.Fatalf("delete 不存在数据库期望 404，实际 %d", code)
	}
}

func TestRoleSecretNameDeterministic(t *testing.T) {
	a := roleSecretName("br-1", "sally")
	b := roleSecretName("br-1", "sally")
	c := roleSecretName("br-2", "sally")
	if a != b {
		t.Fatalf("同名输入应得到稳定 Secret 名：%s vs %s", a, b)
	}
	if a == c {
		t.Fatalf("不同分支同名角色必须使用不同 Secret：%s", a)
	}
	if len(a) > 253 {
		t.Fatalf("Secret 名超长：%d", len(a))
	}
}

func TestBuildSpecCatalogMultiRole(t *testing.T) {
	setupCatalogFixture(t)

	// 追加第二个角色与数据库，验证 spec 目录完整且顺序稳定。
	role2 := &Role{
		BranchID: "br-main", Name: "app_user",
		SecretName: roleSecretName("br-main", "app_user"), SecretKey: defaultCredentialSecretKey,
	}
	st.roles[catalogKey("br-main", "app_user")] = role2
	st.databases[catalogKey("br-main", "app_db")] = &Database{BranchID: "br-main", Name: "app_db", OwnerName: "app_user"}
	if err := credStore.PutSecret(role2.SecretName, role2.SecretKey, "SCRAM-SHA-256$4096:c2FsdA$stored2:server2"); err != nil {
		t.Fatalf("预置第二个角色 Secret 失败：%v", err)
	}
	t.Cleanup(func() { _ = credStore.DeleteSecret(role2.SecretName, role2.SecretKey) })

	roles, dbs, err := buildSpecCatalog("br-main")
	if err != nil {
		t.Fatalf("buildSpecCatalog: %v", err)
	}
	if len(roles) != 2 || len(dbs) != 2 {
		t.Fatalf("期望 2 roles / 2 dbs，实际 %d / %d", len(roles), len(dbs))
	}
	if roles[0].Name != "app_user" || roles[1].Name != "sally" {
		t.Fatalf("roles 应按名称升序：%+v", roles)
	}
	if dbs[0].Name != "app_db" || dbs[1].Name != "mydb" {
		t.Fatalf("databases 应按名称升序：%+v", dbs)
	}
}
