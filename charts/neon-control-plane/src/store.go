package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"
)

// =============================================================================
// StateStore：本地持久化抽象
//
// 设计约束（见 docs/develop.md 第 4 章）：
//   - 运行时真相源仍是进程内 state，本接口只负责 project/branch/endpoint 的持久化；
//   - 不承载 SC / K8s / Secret 的业务编排，跨系统一致性由 orchestration.go 处理；
//   - 完整 ComputeSpec 与观测字段不落库，重启后由既有对账重建；
//   - 阶段 1-3 单副本，不引入行级版本列。
// =============================================================================

// StateStoreConfig 持久化后端配置，由 Helm 渲染的 config.json 提供，绝不包含连接串。
type StateStoreConfig struct {
	Backend               string `json:"backend"` // postgres | memory
	ConnectTimeoutSeconds int    `json:"connect_timeout_seconds"`
	MaxConnections        int    `json:"max_connections"`
}

// EndpointCredential 端点凭据引用：库中只保存引用，不保存 SCRAM 验证器本体。
type EndpointCredential struct {
	EndpointID   string
	RoleName     string
	DatabaseName string
	Owner        string
	SecretName   string
	SecretKey    string
}

// RecoveryPatch 恢复字段的局部更新；指针为 nil 表示该列不更新。
type RecoveryPatch struct {
	RouteFingerprint     *string
	SafekeeperGeneration *int64
	PendingReconfigure   *bool
}

// StatusUpdate 对外聚合状态的批量更新项。
type StatusUpdate struct {
	EndpointID string
	Status     string
}

// StateSnapshot 启动时从持久层一次性加载的全部状态。
type StateSnapshot struct {
	Projects    map[string]*Project
	Branches    map[string]*Branch
	Endpoints   map[string]*Endpoint
	Credentials map[string]*EndpointCredential
	Roles       []*Role
	Databases   []*Database
}

// ProjectBundle 创建 project 时在同一事务内落库的实体集合。
type ProjectBundle struct {
	Project    *Project
	Branch     *Branch
	Roles      []*Role
	Databases  []*Database
	Endpoint   *Endpoint
	Credential *EndpointCredential
}

// StateStore 本地持久化接口。所有写操作都是同步的，失败必须返回错误，禁止静默丢弃。
type StateStore interface {
	Migrate(ctx context.Context) error
	Load(ctx context.Context) (*StateSnapshot, error)
	CreateProjectBundle(ctx context.Context, b *ProjectBundle) error
	UpsertBranch(ctx context.Context, b *Branch) error
	DeleteBranch(ctx context.Context, branchID string) error
	// UpsertBranchCatalog 在同一事务内写 branch 及其 roles/databases（分支创建时使用）。
	UpsertBranchCatalog(ctx context.Context, b *Branch, roles []*Role, dbs []*Database) error
	UpsertEndpoint(ctx context.Context, ep *Endpoint, cred *EndpointCredential) error
	DeleteEndpoint(ctx context.Context, endpointID string) error
	DeleteProject(ctx context.Context, projectID string) error
	// 分支级 catalog 读写。
	ListRoles(ctx context.Context, branchID string) ([]*Role, error)
	GetRole(ctx context.Context, branchID, name string) (*Role, error)
	UpsertRole(ctx context.Context, r *Role) error
	DeleteRole(ctx context.Context, branchID, name string) error
	ListDatabases(ctx context.Context, branchID string) ([]*Database, error)
	GetDatabase(ctx context.Context, branchID, name string) (*Database, error)
	UpsertDatabase(ctx context.Context, d *Database) error
	DeleteDatabase(ctx context.Context, branchID, name string) error
	UpdateRecoveryState(ctx context.Context, endpointID string, patch RecoveryPatch) error
	UpdateObservedStatus(ctx context.Context, updates []StatusUpdate) error
	ResolveCredentials(ctx context.Context, ep *Endpoint) (string, error)
	Health(ctx context.Context) error
	Flush(ctx context.Context) error
	Close(ctx context.Context) error
}

var (
	stateStore StateStore
	storeMu    sync.RWMutex
)

// getStore 返回当前持久化实现；单测未显式初始化时懒加载内存实现，避免 nil 解引用。
func getStore() StateStore {
	storeMu.RLock()
	s := stateStore
	storeMu.RUnlock()
	if s != nil {
		return s
	}
	storeMu.Lock()
	defer storeMu.Unlock()
	if stateStore == nil {
		stateStore = newMemoryStateStore()
	}
	return stateStore
}

// initStateStore 按配置初始化持久层、执行 DDL 迁移并把状态装载进内存。
// PG 不可用时直接返回错误（fail fast），不会降级为内存实现。
func initStateStore() error {
	backend := cfg.StateStore.Backend
	if backend == "" {
		backend = "postgres"
	}
	dsn := os.Getenv("NEON_CP_DATABASE_URL")

	switch backend {
	case "memory":
		// 仅供单测与本地调试：重启即丢，禁止用于集群内部署。
		log.Printf("state store: backend=memory（重启丢失，仅用于本地调试）")
		stateStore = newMemoryStateStore()
	case "postgres":
		if dsn == "" {
			return fmt.Errorf("缺少控制面数据库连接串：请通过 Secret 注入 NEON_CP_DATABASE_URL")
		}
		s, err := newPostgresStateStore(context.Background(), dsn, cfg.StateStore)
		if err != nil {
			return fmt.Errorf("连接控制面数据库失败：%w", err)
		}
		stateStore = s
	default:
		return fmt.Errorf("未知的 stateStore.backend %q（仅支持 postgres / memory）", backend)
	}

	ctx := context.Background()
	if err := stateStore.Migrate(ctx); err != nil {
		return fmt.Errorf("DDL 迁移失败：%w", err)
	}
	snap, err := stateStore.Load(ctx)
	if err != nil {
		return fmt.Errorf("加载控制面状态失败：%w", err)
	}
	applySnapshot(snap)
	log.Printf("state store: backend=%s 已就绪（projects=%d branches=%d endpoints=%d）",
		backend, len(snap.Projects), len(snap.Branches), len(snap.Endpoints))
	return nil
}

// applySnapshot 用持久层快照覆盖内存态；派生状态与 spec 留空，由后续对账重建。
func applySnapshot(snap *StateSnapshot) {
	if snap == nil {
		return
	}
	st.mu.Lock()
	if snap.Projects == nil {
		snap.Projects = map[string]*Project{}
	}
	st.projects = snap.Projects
	st.branches = snap.Branches
	st.endpoints = snap.Endpoints
	if st.branches == nil {
		st.branches = map[string]*Branch{}
	}
	if st.endpoints == nil {
		st.endpoints = map[string]*Endpoint{}
	}
	st.roles = make(map[string]*Role, len(snap.Roles))
	for _, r := range snap.Roles {
		st.roles[catalogKey(r.BranchID, r.Name)] = r
	}
	st.databases = make(map[string]*Database, len(snap.Databases))
	for _, d := range snap.Databases {
		st.databases[catalogKey(d.BranchID, d.Name)] = d
	}
	st.mu.Unlock()
	// 预热凭据引用缓存，避免首次鉴权请求打到 apiserver。
	credStore.Warm(snap.Credentials, snap.Endpoints)
}

// closeStateStore 优雅退出时刷新并关闭持久层。
func closeStateStore() {
	s := getStore()
	ctx := context.Background()
	if err := s.Flush(ctx); err != nil {
		logWarn("state store: flush 失败：%v", err)
	}
	if err := s.Close(ctx); err != nil {
		logWarn("state store: close 失败：%v", err)
	}
}

// -----------------------------------------------------------------------------
// 便捷函数：统一携带后台 context，避免各 handler 重复构造。
// -----------------------------------------------------------------------------

func storeCreateBundle(b *ProjectBundle) error {
	return getStore().CreateProjectBundle(context.Background(), b)
}
func storeUpsertBranch(b *Branch) error {
	return getStore().UpsertBranch(context.Background(), b)
}
func storeUpsertBranchCatalog(b *Branch, roles []*Role, dbs []*Database) error {
	return getStore().UpsertBranchCatalog(context.Background(), b, roles, dbs)
}
func storeDeleteBranch(id string) error {
	return getStore().DeleteBranch(context.Background(), id)
}
func storeUpsertEndpoint(ep *Endpoint, cred *EndpointCredential) error {
	return getStore().UpsertEndpoint(context.Background(), ep, cred)
}
func storeDeleteEndpoint(id string) error {
	return getStore().DeleteEndpoint(context.Background(), id)
}
func storeDeleteProject(id string) error {
	return getStore().DeleteProject(context.Background(), id)
}
func storeRecoveryState(id string, patch RecoveryPatch) {
	if err := getStore().UpdateRecoveryState(context.Background(), id, patch); err != nil {
		logWarn("state store: 更新恢复字段失败 endpoint=%s：%v", id, err)
	}
}
func storeObservedStatuses(updates []StatusUpdate) {
	if err := getStore().UpdateObservedStatus(context.Background(), updates); err != nil {
		logWarn("state store: 批量更新状态失败：%v", err)
	}
}

func ptrBool(v bool) *bool    { return &v }
func ptrInt64(v int64) *int64 { return &v }
