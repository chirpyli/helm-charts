package main

// =============================================================================
// 配置类型（由 Helm ConfigMap 渲染的 config.json 反序列化）
// =============================================================================

// Config 控制面运行配置。
//
// 设计约束：这里**不再**保存 pageserver / safekeeper 节点清单，也没有 default_tenant_id。
// 原因：
//  1. 节点由各组件自注册进 storage controller（pageserver 走 re-attach 携带 metadata.json，
//     safekeeper 走 sk-register sidecar 的 upsert + 激活），控制面再存一份必然与真相源漂移，
//     且同 id 不同地址会被 SC 判为 Mismatched 返回 409（storage_controller/src/service.rs）；
//  2. 静态清单无法感知副本数变化（扩容漏注册、缩容留僵尸节点），node id 还与子 chart 的
//     nodeIdBase + ordinal 规则重复定义；
//  3. tenant / timeline 一律由 POST /projects 按需创建，不再有"默认租户"。
//
// 控制面只在运行时从 SC 查询节点：GET /control/v1/node、GET /control/v1/safekeeper、
// GET /debug/v1/tenant/{id}/locate。
type Config struct {
	StorageControllerURL string `json:"storage_controller_url"`
	ListenPort           int    `json:"listen_port"`
	ComputeImage         string `json:"compute_image"`
	// ComputeServiceType 动态拉起的 compute Service 类型：ClusterIP（默认）或 NodePort。
	// NodePort 用于集群外直连（无 proxy / LoadBalancer 场景）。
	// 说明：compute 只能由控制面通过 K8s API 动态拉起（创建 Deployment + Service），
	// 不存在静态部署形态，因此这里没有"是否启用 K8s compute"之类的开关。
	ComputeServiceType string `json:"compute_service_type"`
	// NodePortExternalHost NodePort 模式下对外的可达主机（节点 IP / 域名 / 负载均衡器 VIP）。
	// 拼接到返回给用户的连接串 <NodePortExternalHost>:<nodePort>；为空时回退 localhost。
	NodePortExternalHost string `json:"node_port_external_host"`
	Domain               string `json:"domain"`
}

// NodeInfo SC `GET /control/v1/node` 返回的 pageserver 节点信息（NodeDescribeResponse 的子集）。
//
// 仅用于启动诊断日志与错误信息增强（例如"SC 中当前没有 Active 的 pageserver"），
// 控制面不据此注册任何节点——SC 才是唯一真相源。
//
// 注意 scheduling 字段的取值：上游 SkSchedulingPolicy / NodeSchedulingPolicy 的 serde 用的是
// 变体名（首字母大写，如 "Active"），只有 FromStr / 落库才是小写（"active"），
// 因此这里按大写比较。
type NodeInfo struct {
	ID             int    `json:"id"`
	ListenPgAddr   string `json:"listen_pg_addr"`
	ListenPgPort   int    `json:"listen_pg_port"`
	ListenHTTPAddr string `json:"listen_http_addr"`
	ListenHTTPPort int    `json:"listen_http_port"`
	Scheduling     string `json:"scheduling"`
}

// =============================================================================
// 业务实体类型
// =============================================================================

// Project 对应一个 Neon project，映射到一个 storage-controller tenant。
// 每个 project 拥有独立的 tenant，实现完全的数据隔离。
type Project struct {
	ProjectID string `json:"project_id"`
	TenantID  string `json:"tenant_id"`
	Name      string `json:"name,omitempty"`       // project 名称（用户可指定，对齐 Neon Cloud）
	CreatedAt string `json:"created_at,omitempty"` // 创建时间 ISO8601（对齐 Neon Cloud）
}

// Branch 跟踪项目下的每个分支（branch_id → timeline_id 映射）。
// 根据 plan：branch_id → (tenant_id, timeline_id) 关系由控制面维护，
// 真相源是 SC 的 tenant→timeline，启动时向 SC 对账恢复。
type Branch struct {
	BranchID   string `json:"branch_id"`
	ProjectID  string `json:"project_id"`
	TenantID   string `json:"tenant_id"`
	TimelineID string `json:"timeline_id"`
	Name       string `json:"name"`
	ParentID   string `json:"parent_id,omitempty"`
	Default    bool   `json:"default"` // 是否为项目的默认分支（main）
}

// Endpoint 对应一个运行中的 compute 端点。
type Endpoint struct {
	EndpointID    string       `json:"endpoint_id"`
	ProjectID     string       `json:"project_id"`
	TenantID      string       `json:"tenant_id"`
	TimelineID    string       `json:"timeline_id"`
	BranchID      string       `json:"branch_id"`
	ComputeImage  string       `json:"compute_image"`
	ScramVerifier string       `json:"-"` // SCRAM 验证器（不入持久化，见下方说明）
	ScramPassword string       `json:"-"` // 明文密码（仅在创建时返回给用户，不入持久化）
	Spec          *ComputeSpec `json:"spec"`
	Status        string       `json:"status"`
}

// =============================================================================
// API 请求/响应类型
// =============================================================================

// ProjectCreateRequest POST /projects 请求体（对齐 Neon Cloud API v2）。
// 示例：{"project":{"name":"myproject","branch":{"name":"main","role_name":"sally","database_name":"mydb"}}}
type ProjectCreateRequest struct {
	Project struct {
		Name   string `json:"name"` // project 名称（必填，Neon Cloud 中必填）
		Branch struct {
			Name         string `json:"name"`          // 初始分支名称（默认 "main"）
			RoleName     string `json:"role_name"`     // 初始角色名（默认 "cloud_admin"）
			DatabaseName string `json:"database_name"` // 初始数据库名（默认 "neondb"）
		} `json:"branch"`
	} `json:"project"`
}

// EndpointCreateRequest POST /projects/{id}/endpoints 请求体。
type EndpointCreateRequest struct {
	BranchID string `json:"branch_id"` // 目标分支 ID
	Type     string `json:"type"`      // "read_write" | "read_only"
}

// BranchCreateRequest POST /projects/{id}/branches 请求体。
type BranchCreateRequest struct {
	Name           string `json:"name"`                       // 分支名称
	ParentBranchID string `json:"parent_branch_id,omitempty"` // 父分支 ID，空则表示从 main 分支
}

// =============================================================================
// Notify 回调类型（SC → control plane 的 compute_hook）
// =============================================================================

// NotifyReAttachRequest SC 在 pageserver shard 迁移后发送的重附着通知。
// 控制面收到后应从 SC locate 重新获取 pageserver 地址并重建 ComputeSpec。
type NotifyReAttachRequest struct {
	TenantID   string `json:"tenant_id"`
	TimelineID string `json:"timeline_id"`
	Generation int    `json:"generation"`
}

// SafekeeperInfo safekeeper 节点信息（由 notify-safekeepers 请求体携带）。
type SafekeeperInfo struct {
	ID   int    `json:"id"`
	Host string `json:"host"`
	Port int    `json:"port"`
}

// NotifySafekeepersRequest SC 在 safekeeper 集合变更后发送的通知。
// 控制面收到后应直接使用请求体中的 safekeeper 列表更新受影响的 ComputeSpec。
type NotifySafekeepersRequest struct {
	TenantID    string           `json:"tenant_id"`
	TimelineID  string           `json:"timeline_id"`
	Generation  int              `json:"generation"`
	Safekeepers []SafekeeperInfo `json:"safekeepers"`
}

// =============================================================================
// 持久化类型
// =============================================================================

// PersistedState ConfigMap 中持久化的状态快照。
// 根据 plan：仅存 project/branch/endpoint 别名映射，tenant↔timeline 真相由 SC 维护。
type PersistedState struct {
	Projects  map[string]*Project  `json:"projects"`
	Branches  map[string]*Branch   `json:"branches"`
	Endpoints map[string]*Endpoint `json:"endpoints"`
}
