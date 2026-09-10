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

	// ComputeCtlPort compute_ctl 的外部 HTTP 端口（默认 3080）。
	// 控制面通过它向 compute 推送重配置（POST /configure），必须与
	// buildComputeDeployment 里 --external-http-port 传的值一致。
	ComputeCtlPort int `json:"compute_ctl_port"`
	// ReconcileIntervalSeconds 与 storage controller 对账的周期（秒，默认 30）。
	// 每轮对每个 endpoint 做一次 tenant_locate，用路由指纹判断 spec 是否落后于 SC 真相；
	// 这是 notify 丢失 / 控制面重启后的兜底自愈手段。
	ReconcileIntervalSeconds int `json:"reconcile_interval_seconds"`
	// ReconfigureTimeoutSeconds 单次 /configure 推送的 HTTP 超时（秒，默认 120）。
	// compute_ctl 的 /configure 会阻塞到 compute 进入 Running 或 Failed，
	// neon_local 使用 120s 超时（control_plane/src/endpoint.rs:1058），这里保持一致。
	ReconfigureTimeoutSeconds int `json:"reconfigure_timeout_seconds"`
	// StatusReconcileIntervalSeconds endpoint 运行时状态对账周期（秒，默认 15）。
	// 每轮对每个 endpoint 探测 K8s Deployment/Pod 就绪度 + compute_ctl /status，
	// 修正 endpoint.Status（compute 崩溃/未就绪能被正确反映，而不是永久停在 running）。
	// 与 SC 路由对账（reconcile_interval_seconds）解耦：状态探测更轻量，可更频繁。
	StatusReconcileIntervalSeconds int `json:"status_reconcile_interval_seconds"`
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

// EndpointStatus* 是控制面对外暴露的 endpoint 聚合状态枚举（endpoint.Status 的取值）。
// 与 neon_local 的 EndpointStatus(Running/Stopped/Crashed) 及 Neon Cloud 的 compute_ctl
// ComputeStatus 对齐：本控制面综合"K8s 存活信号 + compute_ctl /status 内部信号"得出。
const (
	EndpointStatusProvisioning = "provisioning" // Deployment 存在但 Pod 未就绪（创建/重建/配置中）
	EndpointStatusRunning      = "running"      // Pod Ready 且 compute_ctl /status=running
	EndpointStatusStopped      = "stopped"      // K8s 中不存在对应 Deployment（未拉起/已删除）
	EndpointStatusFailed       = "failed"       // Pod 处于失败终端态 或 compute_ctl /status=failed/termination
	EndpointStatusUnknown      = "unknown"      // 探测异常（理论上不会出现）
)

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

	// RouteFingerprint 最近一次"已下发/待下发"的存储路由指纹。
	// 指纹由 SC locate 结果（shard_id + node_id + libpq 地址）计算，随 endpoint 持久化。
	// 用途：判断本地缓存的 spec 是否已经落后于 SC 真相——这是 spec 自愈与周期对账的依据，
	// 否则 notify 丢失或控制面重启时，compute 会一直拿到指向已死 pageserver 的旧 spec。
	RouteFingerprint string `json:"route_fingerprint,omitempty"`
	// SafekeeperGeneration 最近一次生效的 safekeeper 成员配置 generation。
	// SC 的 /notify-safekeepers 可能乱序或重放，只有 generation 更大才允许覆盖。
	SafekeeperGeneration int64 `json:"safekeeper_generation,omitempty"`
	// PendingReconfigure 是否有尚未成功推送给 compute 的 spec 变更。
	// 置 true 后由后台 worker 持续重试；推送成功后清除。持久化保证控制面重启不丢任务。
	PendingReconfigure bool `json:"pending_reconfigure,omitempty"`

	// ComputeStatus 最近一次探测到的 compute_ctl /status 原始值（snake_case 枚举字符串，如 "running"）。
	// 仅排障用途；对外聚合状态一律看 Status。
	ComputeStatus string `json:"compute_status,omitempty"`
	// StatusMessage 状态附加说明（如失败原因 "CrashLoopBackOff" / "compute status: failed"）。
	StatusMessage string `json:"status_message,omitempty"`
	// ReadyReplicas K8s Deployment 当前 ready 副本数（0 表示未就绪）。
	ReadyReplicas int `json:"ready_replicas,omitempty"`
	// LastStatusCheck 最近一次状态探测的 Unix 时间戳（秒）。
	LastStatusCheck int64 `json:"last_status_check,omitempty"`
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

// NotifyAttachRequestShard 对应上游 compute_hook.rs 的 NotifyAttachRequestShard。
//
//	pub struct NotifyAttachRequestShard { pub shard_number: ShardNumber, pub node_id: NodeId }
type NotifyAttachRequestShard struct {
	ShardNumber int `json:"shard_number"`
	NodeID      int `json:"node_id"`
}

// NotifyReAttachRequest SC 在 pageserver shard 迁移后发送的重附着通知。
//
// 对齐上游 storage_controller/src/compute_hook.rs:426-455 的 NotifyAttachRequest：
//
//	pub struct NotifyAttachRequest {
//	    pub tenant_id: TenantId,
//	    pub shards: Vec<NotifyAttachRequestShard>,
//	    pub stripe_size: Option<ShardStripeSize>,
//	    pub preferred_az: Option<AvailabilityZone>,
//	}
//
// 上游没有 timeline_id / generation 字段（旧实现里这两个字段恒为零值，日志具有误导性），
// 这里保留为可选字段仅用于兼容历史报文与日志，不参与业务判断。
//
// 控制面收到后应以 SC 的 tenant_locate 为准重新获取 pageserver 地址并重建 ComputeSpec：
// 通知只说明"该租户的 attach 状态变了"，具体位置必须以 SC 真相源为准。
type NotifyReAttachRequest struct {
	TenantID    string                     `json:"tenant_id"`
	Shards      []NotifyAttachRequestShard `json:"shards"`
	StripeSize  *int                       `json:"stripe_size,omitempty"`
	PreferredAz *string                    `json:"preferred_az,omitempty"`
	// 兼容字段：老版本报文 / 手工测试可能携带，仅用于日志。
	TimelineID string `json:"timeline_id,omitempty"`
	Generation int    `json:"generation,omitempty"`
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
