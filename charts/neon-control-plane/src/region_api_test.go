package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// setupReadAPIFixture 构造 Step 1 只读接口所需的最小状态。
func setupReadAPIFixture(t *testing.T) {
	t.Helper()
	oldState := st
	st = &state{
		projects:  map[string]*Project{},
		branches:  map[string]*Branch{},
		endpoints: map[string]*Endpoint{},
	}
	t.Cleanup(func() { st = oldState })

	st.projects["proj-read"] = &Project{ProjectID: "proj-read", TenantID: "tenant-read", Name: "read"}
	st.branches["br-main"] = &Branch{
		BranchID: "br-main", ProjectID: "proj-read", TenantID: "tenant-read",
		TimelineID: "tl-main", Name: "main", Default: true,
	}
	st.branches["br-child"] = &Branch{
		BranchID: "br-child", ProjectID: "proj-read", TenantID: "tenant-read",
		TimelineID: "tl-child", Name: "child", ParentID: "br-main",
	}
	st.endpoints["ep-rw"] = &Endpoint{
		EndpointID: "ep-rw", ProjectID: "proj-read", BranchID: "br-main",
		Type: EndpointTypeReadWrite, Status: EndpointStatusRunning,
	}
	st.endpoints["ep-ro"] = &Endpoint{
		EndpointID: "ep-ro", ProjectID: "proj-read", BranchID: "br-main",
		Type: EndpointTypeReadOnly, Status: EndpointStatusStopped,
	}
	st.endpoints["ep-child"] = &Endpoint{
		EndpointID: "ep-child", ProjectID: "proj-read", BranchID: "br-child",
		Type: EndpointTypeReadWrite, Status: EndpointStatusRunning,
	}
}

// callReadAPI 走真实路由，覆盖路径解析与归属校验。
func callReadAPI(t *testing.T, method, path string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	apiHandler(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func TestListRegions(t *testing.T) {
	oldCfg := cfg
	cfg = &Config{RegionID: "zone-local"}
	t.Cleanup(func() { cfg = oldCfg })
	setupReadAPIFixture(t)

	code, body := callReadAPI(t, http.MethodGet, "/regions")
	if code != http.StatusOK {
		t.Fatalf("GET /regions 期望 200，实际 %d（%s）", code, body)
	}

	var resp struct {
		Regions []Region `json:"regions"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("解析 regions 响应失败：%v（%s）", err, body)
	}
	if len(resp.Regions) != 1 {
		t.Fatalf("期望 1 个 region，实际 %d", len(resp.Regions))
	}
	r := resp.Regions[0]
	if r.RegionID != "zone-local" || !r.Default || r.Name == "" {
		t.Fatalf("region 字段不符合预期：%+v", r)
	}
}

func TestListRegionsDefaultsWhenConfigMissing(t *testing.T) {
	oldCfg := cfg
	cfg = nil
	t.Cleanup(func() { cfg = oldCfg })
	setupReadAPIFixture(t)

	code, body := callReadAPI(t, http.MethodGet, "/regions")
	if code != http.StatusOK {
		t.Fatalf("GET /regions 期望 200，实际 %d（%s）", code, body)
	}
	var resp struct {
		Regions []Region `json:"regions"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("解析 regions 响应失败：%v", err)
	}
	if len(resp.Regions) != 1 || resp.Regions[0].RegionID != "default" {
		t.Fatalf("未配置 region_id 时应回退 default：%+v", resp.Regions)
	}
}

func TestGetBranch(t *testing.T) {
	setupReadAPIFixture(t)

	code, body := callReadAPI(t, http.MethodGet, "/projects/proj-read/branches/br-main")
	if code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d（%s）", code, body)
	}
	var b Branch
	if err := json.Unmarshal(body, &b); err != nil {
		t.Fatalf("解析 branch 失败：%v", err)
	}
	if b.BranchID != "br-main" || b.TimelineID != "tl-main" {
		t.Fatalf("branch 字段不符合预期：%+v", b)
	}

	for _, tc := range []struct {
		name string
		path string
	}{
		{"branch 不存在", "/projects/proj-read/branches/br-missing"},
		{"跨 project", "/projects/proj-other/branches/br-main"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, body := callReadAPI(t, http.MethodGet, tc.path)
			if code != http.StatusNotFound {
				t.Fatalf("期望 404，实际 %d（%s）", code, body)
			}
		})
	}
}

func TestGetEndpoint(t *testing.T) {
	setupReadAPIFixture(t)

	code, body := callReadAPI(t, http.MethodGet, "/projects/proj-read/endpoints/ep-rw")
	if code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d（%s）", code, body)
	}
	var ep Endpoint
	if err := json.Unmarshal(body, &ep); err != nil {
		t.Fatalf("解析 endpoint 失败：%v", err)
	}
	if ep.EndpointID != "ep-rw" || ep.Type != EndpointTypeReadWrite || ep.Status != EndpointStatusRunning {
		t.Fatalf("endpoint 字段不符合预期：%+v", ep)
	}
	// MarshalJSON 应派生 current_state。
	if !json.Valid(body) {
		t.Fatalf("endpoint 响应不是合法 JSON：%s", body)
	}

	code, body = callReadAPI(t, http.MethodGet, "/projects/proj-read/endpoints/ep-missing")
	if code != http.StatusNotFound {
		t.Fatalf("不存在 endpoint 期望 404，实际 %d（%s）", code, body)
	}
}

func TestListBranchEndpoints(t *testing.T) {
	setupReadAPIFixture(t)

	code, body := callReadAPI(t, http.MethodGet, "/projects/proj-read/branches/br-main/endpoints")
	if code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d（%s）", code, body)
	}
	var eps []Endpoint
	if err := json.Unmarshal(body, &eps); err != nil {
		t.Fatalf("解析 endpoint 数组失败：%v", err)
	}
	if len(eps) != 2 {
		t.Fatalf("main 分支应有 2 个 endpoint，实际 %d：%s", len(eps), body)
	}
	// 按 endpoint_id 排序：ep-ro < ep-rw。
	if eps[0].EndpointID != "ep-ro" || eps[1].EndpointID != "ep-rw" {
		t.Fatalf("endpoint 顺序不符合预期：%+v", eps)
	}

	code, body = callReadAPI(t, http.MethodGet, "/projects/proj-read/branches/br-child/endpoints")
	if code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d（%s）", code, body)
	}
	if err := json.Unmarshal(body, &eps); err != nil {
		t.Fatalf("解析 endpoint 数组失败：%v", err)
	}
	if len(eps) != 1 || eps[0].EndpointID != "ep-child" {
		t.Fatalf("child 分支 endpoint 列表错误：%s", body)
	}

	code, body = callReadAPI(t, http.MethodGet, "/projects/proj-read/branches/br-missing/endpoints")
	if code != http.StatusNotFound {
		t.Fatalf("branch 不存在期望 404，实际 %d（%s）", code, body)
	}
}
