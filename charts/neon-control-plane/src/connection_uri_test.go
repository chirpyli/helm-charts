package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// setupConnectionURIFixture 构造 project + main 分支 + read_write endpoint 的内存态。
func setupConnectionURIFixture(t *testing.T) {
	t.Helper()
	oldState := st
	st = &state{
		projects:  map[string]*Project{},
		branches:  map[string]*Branch{},
		endpoints: map[string]*Endpoint{},
	}
	t.Cleanup(func() { st = oldState })

	st.projects["proj-1"] = &Project{ProjectID: "proj-1", TenantID: "tenant-1", Name: "demo"}
	st.branches["br-main"] = &Branch{
		BranchID: "br-main", ProjectID: "proj-1", TenantID: "tenant-1",
		TimelineID: "tl-main", Name: "main", Default: true,
	}
	st.branches["br-child"] = &Branch{
		BranchID: "br-child", ProjectID: "proj-1", TenantID: "tenant-1",
		TimelineID: "tl-child", Name: "child", ParentID: "br-main",
	}
	st.endpoints["ep-primary"] = &Endpoint{
		EndpointID: "ep-primary", ProjectID: "proj-1", BranchID: "br-main",
		Type: EndpointTypeReadWrite, RoleName: "sally", DatabaseName: "mydb",
	}
	st.endpoints["ep-ro"] = &Endpoint{
		EndpointID: "ep-ro", ProjectID: "proj-1", BranchID: "br-child",
		Type: EndpointTypeReadOnly, RoleName: "sally", DatabaseName: "mydb",
	}
}

// callConnectionURI 走真实路由，覆盖路径解析 + 参数校验。
func callConnectionURI(t *testing.T, projectID, rawQuery string) (int, map[string]string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet,
		"/projects/"+projectID+"/connection_uri?"+rawQuery, nil)
	rec := httptest.NewRecorder()
	apiHandler(rec, req)

	var body map[string]string
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("响应不是 JSON 对象：%s", rec.Body.String())
		}
	}
	return rec.Code, body
}

func TestGetConnectionURIValidation(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "")
	setupConnectionURIFixture(t)

	cases := []struct {
		name      string
		projectID string
		query     string
		wantCode  int
	}{
		{"缺少 role/database", "proj-1", "", http.StatusBadRequest},
		{"只给 database", "proj-1", "database_name=mydb", http.StatusBadRequest},
		{"project 不存在", "proj-missing", "database_name=mydb&role_name=sally", http.StatusNotFound},
		{"branch 不存在", "proj-1", "branch_id=br-missing&database_name=mydb&role_name=sally", http.StatusNotFound},
		{"endpoint 不存在", "proj-1", "endpoint_id=ep-missing&database_name=mydb&role_name=sally", http.StatusNotFound},
		{"branch 与 endpoint 不匹配", "proj-1",
			"branch_id=br-child&endpoint_id=ep-primary&database_name=mydb&role_name=sally", http.StatusBadRequest},
		{"pooled 非法", "proj-1", "database_name=mydb&role_name=sally&pooled=maybe", http.StatusBadRequest},
		{"pooled 不受支持", "proj-1", "database_name=mydb&role_name=sally&pooled=true", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body := callConnectionURI(t, tc.projectID, tc.query)
			if code != tc.wantCode {
				t.Fatalf("期望 HTTP %d，实际 %d（body=%v）", tc.wantCode, code, body)
			}
			if body["error"] == "" {
				t.Fatalf("错误响应应包含 error 字段：%v", body)
			}
		})
	}
}

func TestGetConnectionURISuccess(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "")
	setupConnectionURIFixture(t)

	// 1) 只给 role/database：默认 main 分支 + 该分支 read_write endpoint。
	code, body := callConnectionURI(t, "proj-1", "database_name=mydb&role_name=sally")
	if code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d（body=%v）", code, body)
	}
	want := "postgresql://sally@neon-compute-ep-primary.default.svc.cluster.local:5432/mydb"
	if body["uri"] != want {
		t.Fatalf("uri 不符合预期：\nwant %s\ngot  %s", want, body["uri"])
	}

	// 2) 指定只读副本：返回副本自身的地址。
	code, body = callConnectionURI(t, "proj-1",
		"branch_id=br-child&database_name=mydb&role_name=sally")
	if code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d（body=%v）", code, body)
	}
	want = "postgresql://sally@neon-compute-ep-ro.default.svc.cluster.local:5432/mydb"
	if body["uri"] != want {
		t.Fatalf("uri 不符合预期：\nwant %s\ngot  %s", want, body["uri"])
	}

	// 3) pooled=false 显式关闭应按普通 URI 返回。
	code, body = callConnectionURI(t, "proj-1",
		"database_name=mydb&role_name=sally&pooled=false")
	if code != http.StatusOK {
		t.Fatalf("pooled=false 期望 200，实际 %d（body=%v）", code, body)
	}
}
