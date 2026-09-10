package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// strPtr 返回字符串指针，用于构造 GenericOption 的 Value 字段
func strPtr(s string) *string { return &s }

// =============================================================================
// 全局变量
// =============================================================================

var (
	cfg    *Config
	signer *jwtSigner
	sc     *scClient
	st     *state
)

// =============================================================================
// 进程内状态（真相源）
// =============================================================================

// state 进程内真相源（phase-1 仅内存；重启由 ConfigMap + SC + K8s 真相对账恢复）。
// 根据 plan：
//   - project_id → tenant_id
//   - branch_id → (tenant_id, timeline_id)
//   - endpoint_id → (tenant_id, timeline_id, compute_pod_name, status)
type state struct {
	mu        sync.RWMutex
	projects  map[string]*Project
	branches  map[string]*Branch // 新增：branch_id → Branch
	endpoints map[string]*Endpoint
}

// =============================================================================
// main 入口
// =============================================================================

func main() {
	configPath := flag.String("config", "/etc/neon/config.json", "path to config.json")
	flag.Parse()

	data, err := os.ReadFile(*configPath)
	if err != nil {
		log.Fatalf("read config: %v", err)
	}
	cfg = &Config{}
	if err := json.Unmarshal(data, cfg); err != nil {
		log.Fatalf("parse config: %v", err)
	}
	if cfg.ListenPort == 0 {
		cfg.ListenPort = 8080
	}
	// compute_ctl 端口 / 对账周期 / 推送超时：未配置时填默认值。
	// 取值统一走 computeCtlPort() / reconcileInterval() / reconfigureTimeout()，
	// 这里补默认只是为了启动日志里能看到实际生效值。
	if cfg.ComputeCtlPort == 0 {
		cfg.ComputeCtlPort = defaultComputeCtlPort
	}
	if cfg.ReconcileIntervalSeconds == 0 {
		cfg.ReconcileIntervalSeconds = defaultReconcileIntervalSeconds
	}
	if cfg.ReconfigureTimeoutSeconds == 0 {
		cfg.ReconfigureTimeoutSeconds = defaultReconfigureTimeoutSeconds
	}

	// 初始化 JWT 签发器（含公钥用于校验）
	signer, err = newJWTSigner(os.Getenv("JWT_PRIVATE_KEY_PATH"))
	if err != nil {
		log.Fatalf("jwt signer: %v", err)
	}

	// 初始化 compute_ctl 鉴权用的 JWKS。
	// compute_ctl 外部 HTTP 服务的鉴权路由（/configure、/status）用它校验控制面请求，
	// JWKS 缺失会导致推送重配置时 401（故障转移闭环断在最后一公里）。
	initComputeJWKS()

	// 初始化 SC 客户端
	sc = newSCClient(cfg.StorageControllerURL, signer)

	// 初始化进程内状态
	st = &state{
		projects:  map[string]*Project{},
		branches:  map[string]*Branch{},
		endpoints: map[string]*Endpoint{},
	}

	// 尝试 K8s 客户端初始化（持久化 + compute 编排用）
	if kc, err := newKubeClient(); err == nil {
		kubePersist = kc
		log.Printf("kube: in-cluster client ready (namespace=%s)", kubePersist.namespace)
	} else {
		log.Printf("kube: not in cluster (%v) — persistence and compute management disabled", err)
	}

	// 只读诊断：打印一次 SC 中已注册的节点快照。
	// 节点由各组件自注册（pageserver re-attach / safekeeper sk-register sidecar），控制面不注册、不等待。
	logStorageNodes()

	// 启动对账：从 ConfigMap 恢复状态 + K8s 真相对账
	reconcileOnStartup()

	// 启动与 storage controller 的周期对账 + compute 重配置推送 worker。
	// 必须先于 HTTP 服务启动：故障转移发生在控制面停机期间时，只有这里能补上路由。
	startReconciler()

	// 启动 endpoint 运行时状态对账（K8s + compute_ctl /status 周期探测）。
	// 必须先于 HTTP 服务启动：控制面重启后内存/ConfigMap 里的 status 可能已落后于 K8s 真相。
	startStatusReconciler()

	// 注册 HTTP 路由
	mux := http.NewServeMux()
	mux.HandleFunc("/", apiHandler)

	addr := fmt.Sprintf(":%d", cfg.ListenPort)
	log.Printf("neon-control-plane listening on %s", addr)
	log.Printf("  storage-controller: %s", cfg.StorageControllerURL)
	log.Printf("  compute_ctl port=%d reconcile_interval=%s reconfigure_timeout=%s status_reconcile_interval=%s",
		cfg.ComputeCtlPort, reconcileInterval(), reconfigureTimeout(), statusReconcileInterval())
	// 节点信息不在本地配置中：运行时从 SC 查询（见 logStorageNodes 与 resolvePageserverInfo/resolveSafekeeperConns）

	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("server: %v", err)
	}
}

// =============================================================================
// HTTP 路由
// =============================================================================

func apiHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(r.URL.Path, "/")
	parts := strings.Split(path, "/")

	switch {
	// ---- 健康检查 ----
	case path == "healthz":
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))

	// ---- Project API ----
	case path == "projects" && r.Method == http.MethodPost:
		createProject(w, r)
	case path == "projects" && r.Method == http.MethodGet:
		listProjects(w, r)
	case len(parts) == 2 && parts[0] == "projects" && r.Method == http.MethodDelete:
		deleteProject(w, r, parts[1])
	case len(parts) == 2 && parts[0] == "projects" && r.Method == http.MethodGet:
		getProject(w, r, parts[1])

	// ---- Branch API ----
	case len(parts) == 3 && parts[0] == "projects" && parts[2] == "branches" && r.Method == http.MethodPost:
		createBranch(w, r, parts[1])
	case len(parts) == 3 && parts[0] == "projects" && parts[2] == "branches" && r.Method == http.MethodGet:
		listBranches(w, r, parts[1])
	case len(parts) == 4 && parts[0] == "projects" && parts[2] == "branches" && r.Method == http.MethodDelete:
		deleteBranch(w, r, parts[1], parts[3])

	// ---- Endpoint API ----
	case len(parts) == 3 && parts[0] == "projects" && parts[2] == "endpoints" && r.Method == http.MethodPost:
		createEndpoint(w, r, parts[1])
	case len(parts) == 3 && parts[0] == "projects" && parts[2] == "endpoints" && r.Method == http.MethodGet:
		listEndpoints(w, r, parts[1])
	case len(parts) == 4 && parts[0] == "projects" && parts[2] == "endpoints" && r.Method == http.MethodDelete:
		deleteEndpoint(w, r, parts[1], parts[3])

	// ---- Compute Spec API ----
	case len(parts) == 6 && parts[0] == "compute" && parts[1] == "api" && parts[2] == "v2" &&
		parts[3] == "computes" && parts[5] == "spec" && r.Method == http.MethodGet:
		getComputeSpec(w, r, parts[4])

	// ---- Notify 回调（SC → control plane） ----
	// 支持 PUT（SC 实际使用）和 POST（兼容测试）
	case path == "notify-attach" && (r.Method == http.MethodPut || r.Method == http.MethodPost):
		handleNotifyAttach(w, r)
	case path == "notify-safekeepers" && (r.Method == http.MethodPut || r.Method == http.MethodPost):
		handleNotifySafekeepers(w, r)

	// ---- Proxy 兼容接口（phase-2 启用 proxy 时使用） ----
	case path == "get_endpoint_access_control":
		getEndpointAccessControl(w, r)
	case path == "wake_compute":
		wakeCompute(w, r)
	case len(parts) == 3 && parts[0] == "endpoints" && parts[2] == "jwks":
		getJwks(w, r, parts[1])

	default:
		http.NotFound(w, r)
	}
}

// =============================================================================
// Project API 实现
// =============================================================================

// computeEndpointAddress 返回 endpoint 对外的 host:port 地址字符串。
//
//   - ClusterIP（默认）：返回集群内 DNS 地址 <svc>.<ns>.svc.cluster.local:5432，
//     适用于集群内或 proxy 访问。
//   - NodePort：返回 <外部主机>:<nodePort>。nodePort 由 K8s 在创建 Service 时
//     自动分配（30000-32767），需通过 kc.getServiceNodePort 回读；外部主机取自
//     cfg.NodePortExternalHost（节点 IP / 域名 / 负载均衡器 VIP）。
//
// 当 kc 为 nil（控制面未运行在集群内）或 NodePort 回读失败时，
// 自动回退到 ClusterIP 形式，保证至少返回一个可用地址。
//
// 注意：NodePort 模式下必须回读**对外 Service**（neon-compute-<ep>-ext）的 nodePort，
// 主 Service 已固定为 ClusterIP，不再分配 nodePort（见 buildComputeService 的说明）。
func computeEndpointAddress(kc *kubeClient, endpointID string) string {
	svcName := computeServiceName(endpointID)
	if needsExternalService() && kc != nil {
		if nodePort, err := kc.getServiceNodePort(computeExternalServiceName(endpointID)); err == nil && nodePort > 0 {
			host := cfg.NodePortExternalHost
			if host == "" {
				// 未配置外部主机时回退本机地址，便于同机直连排查。
				host = "localhost"
			}
			return fmt.Sprintf("%s:%d", host, nodePort)
		}
		logWarn("computeEndpointAddress: NodePort 回读失败，回退 ClusterIP 形式")
	}
	return fmt.Sprintf("%s.%s.svc.cluster.local:5432", svcName, podNamespace())
}

// createProject 创建新 project。
//
// 对齐 Neon Cloud API 行为：
//  1. 解析请求体（project.name、project.branch.name/role_name/database_name）
//  2. 为每个 project 独立在 SC 创建 tenant（不再共享默认 tenant）
//  3. 为每个 project 独立在 SC 创建 timeline（main 分支）
//  4. 创建本地 Project + main Branch 记录
//  5. 自动创建 primary read_write endpoint（含 SCRAM 密码）
//  6. 返回完整创建响应（project、connection_uris、roles、databases、operations）
func createProject(w http.ResponseWriter, r *http.Request) {
	// 1) 解析请求体（对齐 Neon Cloud ProjectCreateRequest）
	var req ProjectCreateRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			// 请求体格式错误，使用默认值
			req = ProjectCreateRequest{}
		}
	}
	// 设置默认值（对齐 Neon Cloud 行为）
	projectName := req.Project.Name
	if projectName == "" {
		projectName = "project-" + randHex(6)
	}
	branchName := req.Project.Branch.Name
	if branchName == "" {
		branchName = "main"
	}
	databaseName := req.Project.Branch.DatabaseName
	if databaseName == "" {
		databaseName = "neondb"
	}
	roleName := req.Project.Branch.RoleName
	if roleName == "" {
		roleName = "cloud_admin"
	}

	// 2) 生成 project_id 和独立的 tenant_id（每个 project 独立隔离）
	projectID := "proj-" + randHex(8)
	tenantID := randHex(16) // 32 hex 字符，生成独立 tenant ID

	// 3) 向 SC 创建独立 tenant（每个 project 独占，不再共享）
	//    注意：SC 调用在加锁之前完成，避免持锁 I/O 阻塞
	operations := make([]map[string]string, 0)
	_, code, err := sc.do(context.Background(), http.MethodPost, "/v1/tenant", "pageserverapi",
		map[string]interface{}{
			"new_tenant_id":    tenantID,
			"shard_parameters": map[string]interface{}{"count": 1, "stripe_size": 1024},
		})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError,
			map[string]string{"error": "create tenant in storage controller: " + err.Error()})
		return
	}
	if code >= 400 && code != 409 {
		writeJSON(w, http.StatusInternalServerError,
			map[string]string{"error": fmt.Sprintf("create tenant -> HTTP %d", code)})
		return
	}
	operations = append(operations, map[string]string{"action": "create_tenant", "status": "finished"})

	// 4) 向 SC 创建初始 timeline（main 分支的时间线）
	timelineID := randHex(16)
	_, code, err = sc.do(context.Background(), http.MethodPost,
		fmt.Sprintf("/v1/tenant/%s/timeline", tenantID), "pageserverapi",
		map[string]interface{}{"new_timeline_id": timelineID})
	if err != nil {
		logWarn("createProject %s: create timeline: %v (tenant %s already created)", projectID, err, tenantID)
		operations = append(operations, map[string]string{
			"action": "create_timeline", "status": "error", "error": err.Error(),
		})
	} else if code >= 400 && code != 409 {
		logWarn("createProject %s: create timeline -> HTTP %d", projectID, code)
		operations = append(operations, map[string]string{
			"action": "create_timeline", "status": "error",
		})
	} else {
		operations = append(operations, map[string]string{"action": "create_timeline", "status": "finished"})
	}

	// 5) 创建本地 Project + main Branch 记录
	p := &Project{
		ProjectID: projectID,
		TenantID:  tenantID,
		Name:      projectName,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	mainBranch := &Branch{
		BranchID:   "br-" + randHex(8),
		ProjectID:  projectID,
		TenantID:   tenantID,
		TimelineID: timelineID,
		Name:       branchName,
		Default:    true,
	}

	st.mu.Lock()
	st.projects[projectID] = p
	st.branches[mainBranch.BranchID] = mainBranch
	st.mu.Unlock()

	// 6) 自动创建 primary read_write endpoint（对齐 Neon Cloud 行为）
	//    存储节点地址只从 SC 动态获取；若 SC 尚未完成调度或无 Active safekeeper，
	//    这里返回 503（而不是下发一份指向不存在节点的坏 spec）。
	ep, scramPassword, err := prepareEndpointForBranch(projectID, tenantID,
		timelineID, mainBranch.BranchID, "read_write", roleName, databaseName)
	if err != nil {
		logWarn("createProject %s: prepare endpoint: %v", projectID, err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}

	st.mu.Lock()
	st.endpoints[ep.EndpointID] = ep
	st.mu.Unlock()

	// 7) K8s 动态拉起 compute
	// compute 节点没有静态部署形态：只能由控制面通过 in-cluster K8s API 动态创建
	// Deployment + Service，compute_ctl 启动后再回调本服务拉取 ComputeSpec。
	// kc 声明在函数作用域，便于下方构造连接串时回读 NodePort。
	kc, kcErr := newKubeClient()
	if kcErr != nil {
		// 不在集群内（或 ServiceAccount 不可用）：仅记录告警，保持 best-effort，
		// 仍返回已生成的 ComputeSpec 供排障，不把 201 变成 5xx。
		logWarn("createProject %s: kube client 不可用，跳过 compute 拉起: %v", projectID, kcErr)
	} else {
		if err := kc.createDeployment(buildComputeDeployment(ep)); err != nil {
			logWarn("createProject %s: create compute deployment %s: %v",
				projectID, ep.EndpointID, err)
		} else {
			ep.Status = EndpointStatusProvisioning
			log.Printf("createProject %s: compute deployment %s created",
				projectID, ep.EndpointID)
		}
		if err := kc.createService(buildComputeService(ep)); err != nil {
			logWarn("createProject %s: create compute service %s: %v",
				projectID, ep.EndpointID, err)
		}
		// NodePort 模式额外创建只暴露 5432 的对外 Service（主 Service 恒为 ClusterIP）。
		if needsExternalService() {
			if err := kc.createService(buildComputeExternalService(ep)); err != nil {
				logWarn("createProject %s: create compute external service %s: %v",
					projectID, ep.EndpointID, err)
			}
		}
	}

	// 8) 持久化
	triggerPersist()

	log.Printf("createProject: %s name=%q tenant=%s timeline=%s branch=%s endpoint=%s",
		projectID, projectName, tenantID, timelineID, mainBranch.BranchID, ep.EndpointID)

	// 返回对齐 Neon Cloud 的完整响应
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"project": p,
		"connection_uris": []map[string]string{
			{
				"connection_uri": fmt.Sprintf(
					"postgresql://%s@%s/%s",
					roleName, computeEndpointAddress(kc, ep.EndpointID), databaseName,
				),
				"connection_parameters": fmt.Sprintf(
					"{\"database\":\"%s\",\"role\":\"%s\"}", databaseName, roleName,
				),
			},
		},
		"roles": []map[string]string{
			{"role_name": roleName, "password": scramPassword},
		},
		"databases": []map[string]string{
			{"database_name": databaseName, "owner_name": roleName},
		},
		"default_branch": map[string]interface{}{
			"branch_id":   mainBranch.BranchID,
			"name":        mainBranch.Name,
			"timeline_id": mainBranch.TimelineID,
		},
		"operations": operations,
	})
}

func listProjects(w http.ResponseWriter, r *http.Request) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := make([]*Project, 0, len(st.projects))
	for _, p := range st.projects {
		out = append(out, p)
	}
	writeJSON(w, http.StatusOK, out)
}

func getProject(w http.ResponseWriter, r *http.Request, id string) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	p, ok := st.projects[id]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// deleteProject 删除指定 project 及其所有关联的 branches 和 endpoints。
// 删除流程（参考 Neon API DELETE /projects/{project_id}）：
//  1. 校验 project 存在
//  2. 收集该 project 下所有 branch（含默认分支）
//  3. 对每个 branch：删除其所有 endpoint（含 K8s 资源清理）、删除 SC timeline（best-effort）
//  4. 删除 SC tenant（best-effort）— 每个 project 独占一个 tenant
//  5. 从 state 中移除所有 branch、endpoint 和 project
//  6. 持久化
//
// 返回被删除的 project 信息（HTTP 200）。
func deleteProject(w http.ResponseWriter, r *http.Request, projectID string) {
	// 1) 校验 project 是否存在（先读锁，确认存在后再加写锁）
	st.mu.RLock()
	p, pok := st.projects[projectID]
	st.mu.RUnlock()
	if !pok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}

	// 复制一份 project 用于返回响应（在删除前获取）
	projectCopy := *p

	// 2) 收集该 project 下的所有 branch
	st.mu.RLock()
	var projectBranches []*Branch
	for _, b := range st.branches {
		if b.ProjectID == projectID {
			projectBranches = append(projectBranches, b)
		}
	}
	// 也收集该 project 下的所有 endpoint
	var projectEndpoints []*Endpoint
	for _, ep := range st.endpoints {
		if ep.ProjectID == projectID {
			projectEndpoints = append(projectEndpoints, ep)
		}
	}
	st.mu.RUnlock()

	// 3) 先清理所有 endpoint 的 K8s 资源（在加写锁之前做 IO 操作）
	for _, ep := range projectEndpoints {
		cleanupK8sComputeResources(ep.EndpointID)
	}

	// 4) 对每个 branch 删除 SC timeline（best-effort）
	type branchOpResult struct {
		BranchID   string `json:"branch_id"`
		TimelineID string `json:"timeline_id"`
		Deleted    bool   `json:"timeline_deleted"`
		Error      string `json:"error,omitempty"`
	}
	branchResults := make([]branchOpResult, 0, len(projectBranches))

	for _, b := range projectBranches {
		opResult := branchOpResult{BranchID: b.BranchID, TimelineID: b.TimelineID}
		if err := sc.deleteTimeline(context.Background(), b.TenantID, b.TimelineID); err != nil {
			logWarn("deleteProject %s: delete timeline %s (branch=%s): %v",
				projectID, b.TimelineID, b.BranchID, err)
			opResult.Error = err.Error()
		} else {
			opResult.Deleted = true
		}
		branchResults = append(branchResults, opResult)
	}

	// 4.5) 删除该 project 独占的 tenant（best-effort）
	//      每个 project 拥有独立 tenant，project 删除后 tenant 也应清理
	tenantDeleted := false
	tenantErr := ""
	if p.TenantID != "" {
		if err := sc.deleteTenant(context.Background(), p.TenantID); err != nil {
			logWarn("deleteProject %s: delete tenant %s: %v", projectID, p.TenantID, err)
			tenantErr = err.Error()
		} else {
			tenantDeleted = true
			log.Printf("deleteProject %s: tenant %s deleted from SC", projectID, p.TenantID)
		}
	}

	// 5) 加写锁，从 state 中移除所有相关记录
	st.mu.Lock()
	for _, ep := range projectEndpoints {
		delete(st.endpoints, ep.EndpointID)
	}
	for _, b := range projectBranches {
		delete(st.branches, b.BranchID)
	}
	delete(st.projects, projectID)
	st.mu.Unlock()

	// 6) 持久化
	triggerPersist()

	log.Printf("deleteProject %s: removed 1 project, %d branches, %d endpoints, tenant_deleted=%v",
		projectID, len(projectBranches), len(projectEndpoints), tenantDeleted)

	// 返回 Neon 兼容响应（含被删除 project 信息、各分支操作状态、tenant 状态）
	resp := map[string]interface{}{
		"project":         &projectCopy,
		"branches":        branchResults,
		"endpoints_count": len(projectEndpoints),
		"tenant_deleted":  tenantDeleted,
	}
	if tenantErr != "" {
		resp["tenant_delete_error"] = tenantErr
	}
	writeJSON(w, http.StatusOK, resp)
}

// =============================================================================
// Branch API 实现
// =============================================================================

// createBranch 在指定 project 下创建新分支（新 timeline）。
//
// 流程：
//  1. 解析请求体（name、可选 parent_branch_id）
//  2. 从 parent_branch_id 解析 parent timeline id
//  3. 向 SC 的 /v1/tenant/{tid}/timeline 创建新 timelines
//  4. 追踪 branch_id → timeline_id 映射
func createBranch(w http.ResponseWriter, r *http.Request, projectID string) {
	// 解析请求体
	var req BranchCreateRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			// 若无请求体或解析失败，使用默认值
			req = BranchCreateRequest{Name: "branch-" + randHex(6)}
		}
	}
	if req.Name == "" {
		req.Name = "branch-" + randHex(6)
	}

	// 查找 project
	st.mu.RLock()
	p, pok := st.projects[projectID]
	st.mu.RUnlock()
	if !pok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}

	// 确定父分支的时间线 ID
	var parentTimelineID string
	if req.ParentBranchID != "" {
		st.mu.RLock()
		if parentBranch, ok := st.branches[req.ParentBranchID]; ok {
			parentTimelineID = parentBranch.TimelineID
		}
		st.mu.RUnlock()
	}
	if parentTimelineID == "" {
		// 使用该 project 的 main（default）分支作为父分支
		st.mu.RLock()
		for _, b := range st.branches {
			if b.ProjectID == projectID && b.Default && b.TimelineID != "" {
				parentTimelineID = b.TimelineID
				break
			}
		}
		st.mu.RUnlock()
	}
	if parentTimelineID == "" {
		writeJSON(w, http.StatusBadRequest,
			map[string]string{"error": "no parent branch found; project has no main branch"})
		return
	}

	// 向 SC 创建新时间线（branch = 新 timeline）
	// TimelineId 为 16 字节（32 个 hex 字符），randHex(16) 恰好生成 32 hex 字符
	timelineID := randHex(16)
	_, code, err := sc.do(context.Background(), http.MethodPost,
		fmt.Sprintf("/v1/tenant/%s/timeline", p.TenantID), "pageserverapi",
		map[string]interface{}{
			"new_timeline_id":      timelineID,
			"ancestor_timeline_id": parentTimelineID,
		})
	if err != nil {
		logWarn("create branch timeline: %v", err)
	} else if code >= 400 && code != 409 {
		logWarn("create branch timeline -> HTTP %d", code)
	}

	// 追踪分支
	branchID := "br-" + randHex(8)
	branch := &Branch{
		BranchID:   branchID,
		ProjectID:  projectID,
		TenantID:   p.TenantID,
		TimelineID: timelineID,
		Name:       req.Name,
		ParentID:   req.ParentBranchID,
	}

	st.mu.Lock()
	st.branches[branchID] = branch
	st.mu.Unlock()

	triggerPersist()

	writeJSON(w, http.StatusCreated, branch)
}

// listBranches 列出指定 project 下的所有分支。
func listBranches(w http.ResponseWriter, r *http.Request, projectID string) {
	st.mu.RLock()
	defer st.mu.RUnlock()

	// 验证 project 存在
	if _, ok := st.projects[projectID]; !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}

	// 收集该 project 下的所有分支
	out := make([]*Branch, 0)
	for _, b := range st.branches {
		if b.ProjectID == projectID {
			out = append(out, b)
		}
	}

	// 该 project 下的分支（不再回退到全局默认分支）
	writeJSON(w, http.StatusOK, out)
}

// deleteBranch 删除指定 project 下的指定 branch。
// 删除流程（参考 Neon API DELETE /projects/{project_id}/branches/{branch_id}）：
//  1. 校验 project 存在
//  2. 校验 branch 存在且属于该 project
//  3. 检查该 branch 是否为 default 分支 — default 分支不可删除
//  4. 检查是否有子分支 — 有子分支的分支不可删除（需先删除子分支）
//  5. 收集并删除该 branch 下的所有 endpoint（含 K8s 资源清理）
//  6. 向 SC 请求删除对应 timeline（best-effort）
//  7. 从 state 中移除 branch
//  8. 持久化
//
// 支持查询参数 hard_delete=true（记录在响应中，当前无软删除/恢复窗口）。
func deleteBranch(w http.ResponseWriter, r *http.Request, projectID, branchID string) {
	// 1) 校验 project 存在
	st.mu.RLock()
	_, pok := st.projects[projectID]
	st.mu.RUnlock()
	if !pok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}

	// 2) 校验 branch 存在且属于该 project
	st.mu.RLock()
	branch, bok := st.branches[branchID]
	st.mu.RUnlock()
	if !bok || branch.ProjectID != projectID {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "branch not found"})
		return
	}

	// 复制 branch 用于返回响应
	branchCopy := *branch

	// 3) 检查是否为 default 分支 — 不可删除 default 分支
	if branch.Default {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "cannot delete the default branch of the project",
		})
		return
	}

	// 4) 检查是否有子分支 — 有子分支不可删除父分支
	st.mu.RLock()
	var childBranches []string
	for _, b := range st.branches {
		if b.ParentID == branchID {
			childBranches = append(childBranches, b.BranchID)
		}
	}
	st.mu.RUnlock()

	if len(childBranches) > 0 {
		writeJSON(w, http.StatusConflict, map[string]interface{}{
			"error":          "cannot delete a branch that has child branches",
			"child_branches": childBranches,
			"hint":           "delete all child branches first before deleting this branch",
		})
		return
	}

	// 5) 收集该 branch 下的所有 endpoint 并清理 K8s 资源
	st.mu.RLock()
	var branchEndpoints []*Endpoint
	for _, ep := range st.endpoints {
		if ep.BranchID == branchID {
			branchEndpoints = append(branchEndpoints, ep)
		}
	}
	st.mu.RUnlock()

	// 逐个清理 K8s 资源（在加写锁之前做 IO 操作）
	for _, ep := range branchEndpoints {
		cleanupK8sComputeResources(ep.EndpointID)
	}

	// 6) 向 SC 请求删除该分支对应的 timeline（best-effort）
	// 如果 SC 不支持删除 timeline 端点，仅记录日志不阻塞流程
	hardDelete := r.URL.Query().Get("hard_delete") == "true"
	operations := make([]map[string]string, 0)

	// 操作 1：挂起/删除 timeline
	timelineOp := map[string]string{
		"action": "delete_timeline",
		"status": "scheduling",
	}
	if err := sc.deleteTimeline(context.Background(), branch.TenantID, branch.TimelineID); err != nil {
		logWarn("deleteBranch %s: delete timeline %s (tenant=%s): %v",
			branchID, branch.TimelineID, branch.TenantID, err)
		timelineOp["status"] = "error"
		timelineOp["error"] = err.Error()
	} else {
		timelineOp["status"] = "finished"
		log.Printf("deleteBranch %s: timeline %s deleted from SC", branchID, branch.TimelineID)
	}
	operations = append(operations, timelineOp)

	// 操作 2：挂起 endpoints（所有关联的 compute 资源已在上方清理）
	if len(branchEndpoints) > 0 {
		operations = append(operations, map[string]string{
			"action": "suspend_compute",
			"status": "finished",
		})
	}

	// 7) 加写锁，从 state 中移除
	st.mu.Lock()
	// 移除该 branch 的所有 endpoint
	for _, ep := range branchEndpoints {
		delete(st.endpoints, ep.EndpointID)
	}
	// 移除 branch
	delete(st.branches, branchID)
	st.mu.Unlock()

	// 8) 持久化
	triggerPersist()

	log.Printf("deleteBranch %s: removed branch '%s' (%s), %d endpoints",
		branchID, branchCopy.Name, branchCopy.TimelineID, len(branchEndpoints))

	// 返回 Neon 兼容响应（含分支信息和操作列表）
	resp := map[string]interface{}{
		"branch":     &branchCopy,
		"operations": operations,
	}
	if hardDelete {
		resp["hard_delete"] = true
	}

	writeJSON(w, http.StatusOK, resp)
}

// =============================================================================
// Endpoint API 实现
// =============================================================================

// prepareEndpointForBranch 为指定 branch 准备 endpoint（不注册 state，不持久化）。
// 这是 createProject（自动创建 endpoint）和 createEndpoint API 的共享实现。
//
// 流程：
//
//	定位 pageserver → 获取 safekeeper 列表 → 签发 storage JWT → 生成 SCRAM 验证器
//	→ 组装 ComputeSpec → 返回 Endpoint 对象 + 明文密码
//
// pageserver / safekeeper 的地址**只来自 storage controller**（locate / safekeeper list），
// 没有静态清单兜底：查不到就直接返回 error，由调用方回 503。
// 宁可让请求失败，也不能下发指向不存在节点的 ComputeSpec（compute 起来后连不上存储，且只有 WARN 日志，极难排查）。
//
// 返回的 Endpoint 和明文 SCRAM 密码由调用方负责注册到 state 和持久化。
func prepareEndpointForBranch(projectID, tenantID, timelineID, branchID,
	epType, roleName, databaseName string) (*Endpoint, string, error) {

	epID := "ep-" + randHex(8)
	ctx := context.Background()

	// 1) 定位 pageserver：只能通过 SC 查询最新位置（失败即返回错误）
	pageserverInfo, err := resolvePageserverInfo(ctx, tenantID)
	if err != nil {
		return nil, "", fmt.Errorf("prepareEndpoint %s: %w", epID, err)
	}
	log.Printf("prepareEndpoint %s: located pageserver via SC for tenant=%s", epID, tenantID)

	// 2) 取 safekeeper 列表：动态从 SC 获取 Active 节点（失败即返回错误）
	skConns, err := resolveSafekeeperConns(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("prepareEndpoint %s: %w", epID, err)
	}
	log.Printf("prepareEndpoint %s: %d active safekeepers from SC", epID, len(skConns))

	// 3) 签发 storage JWT（tenant scope 同时满足 pageserver 和 safekeeper 的鉴权需求）
	tok, err := signer.signWithTenant("tenant", tenantID)
	if err != nil {
		return nil, "", fmt.Errorf("prepareEndpoint %s: sign storage token: %w", epID, err)
	}

	// 4) 生成 SCRAM 验证器，返回明文密码给调用方
	scramPassword := randHex(16)
	verifier, err := generateScramVerifier(scramPassword)
	if err != nil {
		logWarn("prepareEndpoint %s: scram generation failed: %v", epID, err)
	}

	// 5) 组装 ComputeSpec
	mode := "Primary"
	if epType == "read_only" {
		mode = "Replica"
	}

	spec := &ComputeSpec{
		FormatVersion:            1.0,
		TenantID:                 tenantID,
		TimelineID:               timelineID,
		Mode:                     mode,
		PageserverConnectionInfo: pageserverInfo,
		SafekeeperConnstrings:    skConns,
		StorageAuthToken:         tok,
		ProjectID:                projectID,
		BranchID:                 branchID,
		EndpointID:               epID,
		SuspendTimeoutSeconds:    0, // 0 表示不自动挂起
		LocalProxyConfig:         &LocalProxySpec{Jwks: []JwksSettings{}},
		Cluster: &ClusterSpec{
			Roles:     []RoleSpec{{Name: roleName, EncryptedPassword: verifier}},
			Databases: []DatabaseSpec{{Name: databaseName, Owner: roleName}},
			// 必须设置 shared_preload_libraries='neon'，否则 neon SMGR 扩展不会加载
			// PostgreSQL 回退到标准 md.c 存储 → 找不到 pageserver 上的数据文件 → 崩溃
			// neon config.rs 中 write_postgres_conf 会在 ComputeAudit::Disabled 时不写该设置
			Settings: &[]GenericOption{
				{Name: "shared_preload_libraries", Value: strPtr("neon"), Vartype: "string"},
				// 必须监听所有网卡地址（*），否则 compute Pod 内 Postgres 仅监听 127.0.0.1，
				// 外部/节点通过 NodePort 或 podIP 访问 5432 会被拒绝（connection refused）
				{Name: "listen_addresses", Value: strPtr("*"), Vartype: "string"},
			},
		},
	}

	return &Endpoint{
		EndpointID:    epID,
		ProjectID:     projectID,
		TenantID:      tenantID,
		TimelineID:    timelineID,
		BranchID:      branchID,
		ComputeImage:  cfg.ComputeImage,
		ScramVerifier: verifier,
		ScramPassword: scramPassword,
		Spec:          spec,
		Status:        EndpointStatusProvisioning,
		// 记录创建时的存储路由指纹，作为后续"spec 是否落后于 SC 真相"的基线。
		RouteFingerprint: routeFingerprint(spec.PageserverConnectionInfo),
	}, scramPassword, nil
}

// createEndpoint 在指定 project 下创建 endpoint（运行中的 compute 实例）。
//
// 流程：
//  1. 解析请求体（branch_id、type）
//  2. 定位目标 branch → tenant/timeline
//  3. 共享函数 prepareEndpointForBranch 完成所有 SC 交互和 spec 组装
//  4. 注册 state + K8s 资源（若启用）+ 持久化
func createEndpoint(w http.ResponseWriter, r *http.Request, projectID string) {
	// 1) 解析请求体
	var req EndpointCreateRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	if req.Type == "" {
		req.Type = "read_write"
	}

	// 2) 查找 project 与目标 branch → 获取 tenant/timeline
	st.mu.RLock()
	p, pok := st.projects[projectID]
	st.mu.RUnlock()
	if !pok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}

	// 确定 target branch 的 tenant 和 timeline_id
	var tenantID, timelineID, branchID string
	var roleName, databaseName string

	st.mu.RLock()
	if req.BranchID != "" {
		if b, ok := st.branches[req.BranchID]; ok && b.ProjectID == projectID {
			branchID = b.BranchID
			tenantID = b.TenantID
			timelineID = b.TimelineID
		}
	}
	if branchID == "" {
		// 使用该 project 的默认分支
		for _, b := range st.branches {
			if b.ProjectID == projectID && b.Default {
				branchID = b.BranchID
				tenantID = b.TenantID
				timelineID = b.TimelineID
				break
			}
		}
	}
	st.mu.RUnlock()

	if branchID == "" || tenantID == "" || timelineID == "" {
		writeJSON(w, http.StatusNotFound,
			map[string]string{"error": "no branch found for this project, create a branch first"})
		return
	}

	// 使用 project 自身的 tenant（而非全局默认）
	if p.TenantID != "" && tenantID != p.TenantID {
		logWarn("createEndpoint: branch tenant %s != project tenant %s, using branch tenant",
			tenantID, p.TenantID)
	}

	// 默认角色和数据库名
	roleName = "cloud_admin"
	databaseName = "neondb"

	// 3) 共享函数准备 endpoint
	//    存储节点地址只从 SC 动态获取，失败直接 503，不再回退静态清单。
	ep, scramPassword, err := prepareEndpointForBranch(projectID, tenantID,
		timelineID, branchID, req.Type, roleName, databaseName)
	if err != nil {
		logWarn("createEndpoint: prepare endpoint: %v", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}

	// 4) 注册到 state
	st.mu.Lock()
	st.endpoints[ep.EndpointID] = ep
	st.mu.Unlock()

	// 5) K8s 动态拉起 compute
	// compute 节点没有静态部署形态：只能由控制面通过 in-cluster K8s API 动态创建
	// Deployment + Service。kc 声明在函数作用域，便于下方构造连接串时回读 NodePort。
	kc, kcErr := newKubeClient()
	if kcErr != nil {
		// 不在集群内（或 ServiceAccount 不可用）：best-effort，仅记录告警，
		// 仍返回已生成的 ComputeSpec 供排障。
		logWarn("createEndpoint %s: kube client 不可用，跳过 compute 拉起: %v", ep.EndpointID, kcErr)
	} else {
		if err := kc.createDeployment(buildComputeDeployment(ep)); err != nil {
			logWarn("createEndpoint %s: create compute deployment: %v", ep.EndpointID, err)
		} else {
			ep.Status = EndpointStatusProvisioning
			log.Printf("createEndpoint %s: compute deployment created", ep.EndpointID)
		}
		if err := kc.createService(buildComputeService(ep)); err != nil {
			logWarn("createEndpoint %s: create compute service: %v", ep.EndpointID, err)
		}
		// NodePort 模式额外创建只暴露 5432 的对外 Service。
		if needsExternalService() {
			if err := kc.createService(buildComputeExternalService(ep)); err != nil {
				logWarn("createEndpoint %s: create compute external service: %v", ep.EndpointID, err)
			}
		}
	}

	// 6) 持久化
	triggerPersist()

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"endpoint": ep,
		"connection_uri": fmt.Sprintf(
			"postgresql://%s@%s/%s",
			roleName, computeEndpointAddress(kc, ep.EndpointID), databaseName,
		),
		"password": scramPassword,
	})
}

// listEndpoints 列出指定 project 下的所有 endpoints。
func listEndpoints(w http.ResponseWriter, r *http.Request, projectID string) {
	st.mu.RLock()
	defer st.mu.RUnlock()

	if _, ok := st.projects[projectID]; !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}

	out := make([]*Endpoint, 0)
	for _, e := range st.endpoints {
		if e.ProjectID == projectID {
			out = append(out, e)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// deleteEndpoint 删除指定 endpoint。
// compute 只能动态拉起，因此删除时总是同步清理对应的 Deployment 和 Service。
func deleteEndpoint(w http.ResponseWriter, r *http.Request, projectID, endpointID string) {
	st.mu.Lock()
	ep, ok := st.endpoints[endpointID]
	if !ok {
		st.mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "endpoint not found"})
		return
	}
	if ep.ProjectID != projectID {
		st.mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "endpoint not found in this project"})
		return
	}
	delete(st.endpoints, endpointID)
	st.mu.Unlock()

	// 清理 K8s 资源（compute 只能动态拉起，其 Deployment/Service 由控制面负责回收）
	cleanupK8sComputeResources(endpointID)

	triggerPersist()

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":      "deleted",
		"endpoint_id": endpointID,
	})
}

// cleanupK8sComputeResources 删除 endpoint 对应的 K8s Deployment 和 Service。
// 失败不阻塞返回（best-effort）。
func cleanupK8sComputeResources(endpointID string) {
	kc, err := newKubeClient()
	if err != nil {
		logWarn("deleteEndpoint %s: kube client: %v", endpointID, err)
		return
	}

	depName := computeDeploymentName(endpointID)
	if err := kc.deleteDeployment(depName); err != nil {
		logWarn("deleteEndpoint %s: delete deployment: %v", endpointID, err)
	} else {
		log.Printf("deleteEndpoint %s: deployment %s deleted", endpointID, depName)
	}

	svcName := computeServiceName(endpointID)
	if err := kc.deleteService(svcName); err != nil {
		logWarn("deleteEndpoint %s: delete service: %v", endpointID, err)
	}

	// 对外 Service（NodePort）可能不存在（ClusterIP 模式下不创建），删除失败只记日志。
	extSvcName := computeExternalServiceName(endpointID)
	if err := kc.deleteService(extSvcName); err != nil {
		logWarn("deleteEndpoint %s: delete external service: %v", endpointID, err)
	}
}

// =============================================================================
// Compute Spec API 实现
// =============================================================================

// getComputeSpec 处理 GET /compute/api/v2/computes/{compute_id}/spec。
// compute_ctl 周期性调用此端点拉取 ComputeSpec（路径源自 compute_tools/src/spec.rs）。
// 返回 ComputeConfig { spec, compute_ctl_config }。
func getComputeSpec(w http.ResponseWriter, r *http.Request, computeID string) {
	st.mu.RLock()
	ep, ok := st.endpoints[computeID]
	st.mu.RUnlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "compute not found"})
		return
	}

	// 确保 spec 中的页面服务器信息是最新的
	// 始终确保返回的 spec 含最新的 cluster/settings（尤其是 listen_addresses）。
	// 触发重建的条件（任一）：
	//   1) 缓存的 spec 为空 / 无 cluster 配置；
	//   2) 缺少 listen_addresses（老 spec）；
	//   3) **没有任何 pageserver 分片信息** —— 说明上次创建时 SC 尚未完成调度，
	//      这里每次轮询都重试一次 locate，实现自愈（compute_ctl 本就周期性拉取 spec）；
	//   4) 缺少路由指纹 —— 升级前持久化的老 endpoint，指纹未知时先重建一次补齐。
	//
	// 注意：这里**不做**每请求的 locate 对账。路由是否落后由后台周期对账（reconcileWithSC）
	// 用指纹判断，避免 compute_ctl 的轮询把 locate 打爆。
	needRebuild := ep.Spec == nil || ep.Spec.Cluster == nil ||
		len(ep.Spec.PageserverConnectionInfo.Shards) == 0 ||
		ep.RouteFingerprint == ""
	if !needRebuild && ep.Spec.Cluster.Settings != nil {
		hasListen := false
		for _, o := range *ep.Spec.Cluster.Settings {
			if o.Name == "listen_addresses" {
				hasListen = true
				break
			}
		}
		needRebuild = !hasListen
	}
	if needRebuild {
		log.Printf("getComputeSpec %s: rebuilding spec", computeID)
		spec, err := buildSpecFromEndpoint(ep)
		if err != nil {
			// SC 尚未完成调度 / 无 Active safekeeper：返回 503 让 compute_ctl 继续轮询重试，
			// 绝不返回一份不完整的 spec（那会让 compute 连上错误的存储地址）。
			logWarn("getComputeSpec %s: rebuild spec: %v", computeID, err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
			return
		}
		st.mu.Lock()
		ep.Spec = spec
		// 重建后同步指纹，保证后续对账能正确判断"spec 是否已跟上 SC 真相"。
		ep.RouteFingerprint = routeFingerprint(spec.PageserverConnectionInfo)
		st.mu.Unlock()
	}

	// 对齐 compute_ctl 的 ControlPlaneConfigResponse 反序列化结构：
	//   - status: 必需，"attached" 表示 compute 已绑定到 endpoint
	//   - compute_ctl_config.jwks: 必需 —— 这里下发控制面自己的 Ed25519 公钥（OKP/Ed25519）。
	//     compute_ctl 用它校验 /configure、/status 等鉴权路由的请求；
	//     下发空 keys 会让这些路由全部 401，控制面就无法推送重配置（见 jwks.go 说明）。
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"spec":               ep.Spec,
		"status":             "attached",
		"compute_ctl_config": computeCtlConfigFor(),
	})
}

// =============================================================================
// Proxy 兼容接口（phase-2 启用 proxy 时由 proxy 调用）
// =============================================================================

// getEndpointAccessControl 处理 GET /get_endpoint_access_control?endpointish=...&role=...&session_id=...
// proxy 调用来获取角色 SCRAM 密钥和访问控制信息。
func getEndpointAccessControl(w http.ResponseWriter, r *http.Request) {
	endpointish := r.URL.Query().Get("endpointish")
	st.mu.RLock()
	ep, ok := st.endpoints[endpointish]
	st.mu.RUnlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "endpoint not found"})
		return
	}
	// role_secret 取 SCRAM 验证器。ScramVerifier 刻意不持久化（避免密钥落 ConfigMap），
	// 因此控制面重启后内存值为空；此时回退到已持久化的 spec.cluster.roles[].encrypted_password
	// —— 两者本就是同一个验证器，避免 proxy 拿到空密钥导致鉴权失败。
	roleSecret := ep.ScramVerifier
	if roleSecret == "" && ep.Spec != nil && ep.Spec.Cluster != nil && len(ep.Spec.Cluster.Roles) > 0 {
		roleSecret = ep.Spec.Cluster.Roles[0].EncryptedPassword
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"role_secret":              roleSecret,
		"allowed_ips":              []string{},
		"allowed_vpc_endpoint_ids": []string{},
		"block_public_connections": false,
		"block_vpc_connections":    false,
		"project_id":               ep.ProjectID,
		"account_id":               nil,
		"rate_limits":              map[string]string{},
	})
}

// wakeCompute 处理 GET /wake_compute?endpointish=...&session_id=...&application_name=...
// proxy 调用来获取 compute 地址，将其路由到对应 compute Service。
func wakeCompute(w http.ResponseWriter, r *http.Request) {
	endpointish := r.URL.Query().Get("endpointish")

	// 查找 endpoint 信息以获取 project_id / branch_id
	st.mu.RLock()
	ep, ok := st.endpoints[endpointish]
	st.mu.RUnlock()

	projectID := ""
	branchID := ""
	if ok {
		projectID = ep.ProjectID
		branchID = ep.BranchID
	}

	// 构造对外地址：NodePort 模式回读节点端口，否则返回集群内 DNS。
	kc, _ := newKubeClient()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"address":     computeEndpointAddress(kc, endpointish),
		"server_name": nil,
		"aux": map[string]string{
			"endpoint_id":     endpointish,
			"project_id":      projectID,
			"branch_id":       branchID,
			"compute_id":      endpointish,
			"cold_start_info": "unknown",
		},
	})
}

// getJwks 处理 GET /endpoints/{id}/jwks?session_id=...
// proxy 调用来获取 JWKS 公钥（v1 返回空列表即可，proxy 会 fallback）。
func getJwks(w http.ResponseWriter, r *http.Request, endpoint string) {
	writeJSON(w, http.StatusOK, map[string]interface{}{"jwks": []string{}})
}

// =============================================================================
// 辅助函数
// =============================================================================

// buildSpecFromEndpoint 根据 endpoint 信息和当前配置重建 ComputeSpec。
// 用于重启后或 notify 回调后按需重建 spec。
//
// pageserver / safekeeper 地址只从 SC 动态获取；任一查询失败即返回 error，
// 调用方（getComputeSpec）返回 503 让 compute_ctl 继续轮询，而不是下发不完整的 spec。
func buildSpecFromEndpoint(ep *Endpoint) (*ComputeSpec, error) {
	ctx := context.Background()

	// 从 SC 动态获取最新信息（无静态兜底）
	pageserverInfo, err := resolvePageserverInfo(ctx, ep.TenantID)
	if err != nil {
		return nil, err
	}
	safekeeperConns, err := resolveSafekeeperConns(ctx)
	if err != nil {
		return nil, err
	}

	// 签发 storage token：失败时回退到 spec 里已有的 token（旧 token 通常仍在其有效期内）
	tok, err := signer.signWithTenant("tenant", ep.TenantID)
	if err != nil {
		if ep.Spec != nil && ep.Spec.StorageAuthToken != "" {
			tok = ep.Spec.StorageAuthToken
			logWarn("buildSpecFromEndpoint %s: sign storage token failed: %v (reuse existing token)", ep.EndpointID, err)
		} else {
			return nil, fmt.Errorf("sign storage token: %w", err)
		}
	}

	// 角色/数据库优先复用 endpoint 已持久化的 spec（含项目自定义的
	// role_name/database_name 与正确 SCRAM verifier），避免重建时被硬编码的
	// cloud_admin/postgres 覆盖（例如项目创建的 sally/mydb）。
	roles := []RoleSpec{{Name: "cloud_admin", EncryptedPassword: ep.ScramVerifier}}
	dbs := []DatabaseSpec{{Name: "postgres", Owner: "cloud_admin"}}
	if ep.Spec != nil && ep.Spec.Cluster != nil {
		if len(ep.Spec.Cluster.Roles) > 0 {
			roles = ep.Spec.Cluster.Roles
		}
		if len(ep.Spec.Cluster.Databases) > 0 {
			dbs = ep.Spec.Cluster.Databases
		}
	}

	return &ComputeSpec{
		FormatVersion:            1.0,
		TenantID:                 ep.TenantID,
		TimelineID:               ep.TimelineID,
		Mode:                     "Primary",
		PageserverConnectionInfo: pageserverInfo,
		SafekeeperConnstrings:    safekeeperConns,
		StorageAuthToken:         tok,
		ProjectID:                ep.ProjectID,
		BranchID:                 ep.BranchID,
		EndpointID:               ep.EndpointID,
		SuspendTimeoutSeconds:    0, // 0 表示不自动挂起
		LocalProxyConfig:         &LocalProxySpec{Jwks: []JwksSettings{}},
		Cluster: &ClusterSpec{
			Roles:     roles,
			Databases: dbs,
			// 必须设置 shared_preload_libraries='neon'，否则 neon SMGR 扩展不会加载
			Settings: &[]GenericOption{
				{Name: "shared_preload_libraries", Value: strPtr("neon"), Vartype: "string"},
				// 必须监听所有网卡地址（*），否则 compute Pod 内 Postgres 仅监听 127.0.0.1，
				// 外部/节点通过 NodePort 或 podIP 访问 5432 会被拒绝（connection refused）
				{Name: "listen_addresses", Value: strPtr("*"), Vartype: "string"},
			},
		},
	}, nil
}

// =============================================================================
// 存储节点动态解析（唯一来源：storage controller）
// =============================================================================

// resolvePageserverInfo 通过 SC 的 tenant_locate 获取该租户的 pageserver 连接信息。
//
// 设计要点：
//   - 不再有静态清单兜底。旧实现在 locate 失败时回落到 Helm 里写死的主机列表，
//     会生成指向不存在节点的 ComputeSpec（compute 起不来且只有 WARN 日志，极难排查）；
//   - locate 失败通常是"SC 尚未完成调度"，属于可重试的临时状态，调用方回 503 让客户端/compute_ctl 重试。
func resolvePageserverInfo(ctx context.Context, tenantID string) (PageserverConnInfo, error) {
	loc, err := sc.tenantLocate(ctx, tenantID)
	if err != nil {
		// 补充一次节点查询，帮助判断是"SC 不可用"还是"集群里根本没有 pageserver"
		if nodes, nerr := sc.listNodes(ctx); nerr == nil {
			var active int
			for _, n := range nodes {
				if n.Scheduling == "Active" {
					active++
				}
			}
			return PageserverConnInfo{}, fmt.Errorf(
				"locate tenant %s failed: %v (SC 中已注册 %d 个 pageserver，其中 %d 个 Active)",
				tenantID, err, len(nodes), active)
		}
		return PageserverConnInfo{}, fmt.Errorf("locate tenant %s failed: %w", tenantID, err)
	}
	// 统一走 buildPageserverConnInfo，保证与 notify-attach / 周期对账生成的 spec 完全一致。
	connInfo := buildPageserverConnInfo(loc)
	if len(connInfo.Shards) == 0 {
		return PageserverConnInfo{}, fmt.Errorf(
			"locate tenant %s 未返回任何 shard：SC 可能尚未完成调度（pageserver 是否已自注册且 Active）", tenantID)
	}
	return connInfo, nil
}

// resolveSafekeeperConns 从 SC 获取 Active safekeeper 的连接串列表（host:port）。
//
// 注意：safekeeper 必须由 sk-register sidecar upsert **并显式激活**后才会出现在这里
// （SC 的 upsert 默认把 scheduling_policy 置为 Activating，get_safekeepers 只返回 Active）。
// 没有 Active 节点时返回错误，而不是回退到静态清单。
func resolveSafekeeperConns(ctx context.Context) ([]string, error) {
	sks, err := sc.listSafekeepers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list active safekeepers failed: %w", err)
	}
	if len(sks) == 0 {
		return nil, fmt.Errorf("SC 中没有 Active 的 safekeeper（需由 sk-register sidecar 注册并激活，生产环境建议 >= 3 个）")
	}
	conns := make([]string, 0, len(sks))
	for _, sk := range sks {
		conns = append(conns, fmt.Sprintf("%s:%d", sk.Host, sk.Port))
	}
	return conns, nil
}

// writeJSON 写 JSON 响应。
func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// randHex 生成 n 字节的随机 hex 字符串（返回 2n 个 hex 字符）。
func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// podNamespace 返回当前 Pod 所在的命名空间。
func podNamespace() string {
	if ns := os.Getenv("POD_NAMESPACE"); ns != "" {
		return ns
	}
	return "default"
}

// logWarn 打印 WARN 级别日志。
func logWarn(format string, args ...interface{}) {
	log.Printf("WARN "+format, args...)
}
