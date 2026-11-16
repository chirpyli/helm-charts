package main

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// =============================================================================
// PostgresStateStore：生产持久化实现
//
// 设计约束（docs/develop.md 第 3、4 章）：
//   - 事务 + 行级 UPSERT/DELETE，不写整份 JSON；
//   - 只持久化权威 / 恢复字段，完整 ComputeSpec 与观测字段不落库；
//   - 不引入行级版本列，阶段 1-3 靠单副本 + 唯一约束 + 幂等写保证正确性；
//   - 连接失败 fail fast，运行期写失败返回可行动错误，禁止静默降级。
// =============================================================================

type postgresStateStore struct {
	pool *pgxpool.Pool
}

func newPostgresStateStore(ctx context.Context, dsn string, c StateStoreConfig) (*postgresStateStore, error) {
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("解析数据库连接串：%w", err)
	}
	timeout := c.ConnectTimeoutSeconds
	if timeout <= 0 {
		timeout = 5
	}
	maxConns := c.MaxConnections
	if maxConns <= 0 {
		maxConns = 8
	}
	poolCfg.MaxConns = int32(maxConns)
	poolCfg.ConnConfig.ConnectTimeout = time.Duration(timeout) * time.Second
	// 语句级防护：避免控制面慢查询拖垮同实例的 storage_controller 库。
	if poolCfg.ConnConfig.RuntimeParams == nil {
		poolCfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	poolCfg.ConnConfig.RuntimeParams["statement_timeout"] = "10000"
	poolCfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = "30000"

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("创建连接池：%w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("数据库探活失败（请检查 NEON_CP_DATABASE_URL 指向的独立库）：%w", err)
	}
	return &postgresStateStore{pool: pool}, nil
}

func (s *postgresStateStore) Migrate(ctx context.Context) error {
	return runMigrations(ctx, s.pool)
}

func (s *postgresStateStore) Load(ctx context.Context) (*StateSnapshot, error) {
	snap := &StateSnapshot{
		Projects:    map[string]*Project{},
		Branches:    map[string]*Branch{},
		Endpoints:   map[string]*Endpoint{},
		Credentials: map[string]*EndpointCredential{},
	}

	// 固定四次查询装配内存态，避免 N+1。
	rows, err := s.pool.Query(ctx, `
		SELECT project_id, tenant_id, COALESCE(name, ''), created_at
		FROM projects WHERE deleted_at IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("查询 projects：%w", err)
	}
	for rows.Next() {
		var p Project
		var created time.Time
		if err := rows.Scan(&p.ProjectID, &p.TenantID, &p.Name, &created); err != nil {
			rows.Close()
			return nil, fmt.Errorf("扫描 projects：%w", err)
		}
		p.CreatedAt = created.UTC().Format(time.RFC3339)
		snap.Projects[p.ProjectID] = &p
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("遍历 projects：%w", err)
	}
	rows.Close()

	rows, err = s.pool.Query(ctx, `
		SELECT branch_id, project_id, tenant_id, timeline_id, name,
		       COALESCE(parent_id, ''), is_default
		FROM branches WHERE deleted_at IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("查询 branches：%w", err)
	}
	for rows.Next() {
		var b Branch
		if err := rows.Scan(&b.BranchID, &b.ProjectID, &b.TenantID, &b.TimelineID,
			&b.Name, &b.ParentID, &b.Default); err != nil {
			rows.Close()
			return nil, fmt.Errorf("扫描 branches：%w", err)
		}
		snap.Branches[b.BranchID] = &b
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("遍历 branches：%w", err)
	}
	rows.Close()

	rows, err = s.pool.Query(ctx, `
		SELECT endpoint_id, project_id, branch_id, tenant_id, timeline_id,
		       compute_image, type, status, COALESCE(route_fingerprint, ''),
		       safekeeper_generation, pending_reconfigure
		FROM endpoints WHERE deleted_at IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("查询 endpoints：%w", err)
	}
	for rows.Next() {
		var ep Endpoint
		if err := rows.Scan(&ep.EndpointID, &ep.ProjectID, &ep.BranchID, &ep.TenantID,
			&ep.TimelineID, &ep.ComputeImage, &ep.Type, &ep.Status, &ep.RouteFingerprint,
			&ep.SafekeeperGeneration, &ep.PendingReconfigure); err != nil {
			rows.Close()
			return nil, fmt.Errorf("扫描 endpoints：%w", err)
		}
		// Spec 与观测字段留空，启动后由对账重建。
		snap.Endpoints[ep.EndpointID] = &ep
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("遍历 endpoints：%w", err)
	}
	rows.Close()

	rows, err = s.pool.Query(ctx, `
		SELECT endpoint_id, role_name, database_name, owner,
		       credential_secret_name, credential_secret_key
		FROM endpoint_credentials`)
	if err != nil {
		return nil, fmt.Errorf("查询 endpoint_credentials：%w", err)
	}
	for rows.Next() {
		var c EndpointCredential
		if err := rows.Scan(&c.EndpointID, &c.RoleName, &c.DatabaseName, &c.Owner,
			&c.SecretName, &c.SecretKey); err != nil {
			rows.Close()
			return nil, fmt.Errorf("扫描 endpoint_credentials：%w", err)
		}
		snap.Credentials[c.EndpointID] = &c
		if ep, ok := snap.Endpoints[c.EndpointID]; ok {
			ep.CredentialSecretName = c.SecretName
			ep.CredentialSecretKey = c.SecretKey
			ep.RoleName = c.RoleName
			ep.DatabaseName = c.DatabaseName
			ep.Owner = c.Owner
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("遍历 endpoint_credentials：%w", err)
	}
	rows.Close()

	return snap, nil
}

// execWrite 统一记录写操作结果与指标。
func (s *postgresStateStore) execWrite(ctx context.Context, sql string, args ...any) error {
	_, err := s.pool.Exec(ctx, sql, args...)
	recordStoreWrite(err)
	if err != nil {
		return fmt.Errorf("持久化写入失败：%w", err)
	}
	return nil
}

func (s *postgresStateStore) CreateProjectBundle(ctx context.Context, b *ProjectBundle) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		recordStoreWrite(err)
		return fmt.Errorf("开启创建事务：%w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	created := time.Now().UTC()
	if b.Project.CreatedAt != "" {
		if t, e := time.Parse(time.RFC3339, b.Project.CreatedAt); e == nil {
			created = t
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO projects (project_id, tenant_id, name, created_at, updated_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (project_id) DO UPDATE SET tenant_id = EXCLUDED.tenant_id,
		    name = EXCLUDED.name, updated_at = now()`,
		b.Project.ProjectID, b.Project.TenantID, b.Project.Name, created); err != nil {
		recordStoreWrite(err)
		return fmt.Errorf("写入 projects：%w", err)
	}
	var parent any
	if b.Branch.ParentID != "" {
		parent = b.Branch.ParentID
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO branches (branch_id, project_id, tenant_id, timeline_id, name, parent_id, is_default)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (branch_id) DO UPDATE SET timeline_id = EXCLUDED.timeline_id,
		    name = EXCLUDED.name, parent_id = EXCLUDED.parent_id,
		    is_default = EXCLUDED.is_default, updated_at = now()`,
		b.Branch.BranchID, b.Branch.ProjectID, b.Branch.TenantID, b.Branch.TimelineID,
		b.Branch.Name, parent, b.Branch.Default); err != nil {
		recordStoreWrite(err)
		return fmt.Errorf("写入 branches：%w", err)
	}
	if b.Endpoint != nil {
		if err := upsertEndpointTx(ctx, tx, b.Endpoint, b.Credential); err != nil {
			recordStoreWrite(err)
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		recordStoreWrite(err)
		return fmt.Errorf("提交创建事务：%w", err)
	}
	recordStoreWrite(nil)
	return nil
}

// upsertEndpointTx 在既有事务内写入端点与凭据引用。
func upsertEndpointTx(ctx context.Context, tx pgx.Tx, ep *Endpoint, cred *EndpointCredential) error {
	// 迁移前的调用方可能没有设置 Type，按 read_write 落库，避免违反 CHECK 约束。
	epType := ep.Type
	if epType == "" {
		epType = EndpointTypeReadWrite
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO endpoints (endpoint_id, project_id, branch_id, tenant_id, timeline_id,
		    compute_image, type, status, route_fingerprint, safekeeper_generation, pending_reconfigure)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (endpoint_id) DO UPDATE SET timeline_id = EXCLUDED.timeline_id,
		    compute_image = EXCLUDED.compute_image, type = EXCLUDED.type,
		    status = EXCLUDED.status, route_fingerprint = EXCLUDED.route_fingerprint,
		    safekeeper_generation = EXCLUDED.safekeeper_generation,
		    pending_reconfigure = EXCLUDED.pending_reconfigure, updated_at = now()`,
		ep.EndpointID, ep.ProjectID, ep.BranchID, ep.TenantID, ep.TimelineID,
		ep.ComputeImage, epType, ep.Status, nullIfEmpty(ep.RouteFingerprint),
		ep.SafekeeperGeneration, ep.PendingReconfigure); err != nil {
		return fmt.Errorf("写入 endpoints：%w", err)
	}
	if cred == nil {
		return nil
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO endpoint_credentials (endpoint_id, role_name, database_name, owner,
		    credential_secret_name, credential_secret_key)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (endpoint_id) DO UPDATE SET role_name = EXCLUDED.role_name,
		    database_name = EXCLUDED.database_name, owner = EXCLUDED.owner,
		    credential_secret_name = EXCLUDED.credential_secret_name,
		    credential_secret_key = EXCLUDED.credential_secret_key, updated_at = now()`,
		cred.EndpointID, cred.RoleName, cred.DatabaseName, cred.Owner,
		cred.SecretName, cred.SecretKey)
	if err != nil {
		return fmt.Errorf("写入 endpoint_credentials：%w", err)
	}
	return nil
}

func nullIfEmpty(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func (s *postgresStateStore) UpsertBranch(ctx context.Context, b *Branch) error {
	var parent any
	if b.ParentID != "" {
		parent = b.ParentID
	}
	return s.execWrite(ctx, `
		INSERT INTO branches (branch_id, project_id, tenant_id, timeline_id, name, parent_id, is_default)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (branch_id) DO UPDATE SET timeline_id = EXCLUDED.timeline_id,
		    name = EXCLUDED.name, parent_id = EXCLUDED.parent_id,
		    is_default = EXCLUDED.is_default, updated_at = now()`,
		b.BranchID, b.ProjectID, b.TenantID, b.TimelineID, b.Name, parent, b.Default)
}

func (s *postgresStateStore) DeleteBranch(ctx context.Context, branchID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		recordStoreWrite(err)
		return fmt.Errorf("开启删除分支事务：%w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// endpoint_credentials 由外键级联删除；先删端点再删分支。
	if _, err := tx.Exec(ctx,
		"DELETE FROM endpoints WHERE branch_id = $1", branchID); err != nil {
		recordStoreWrite(err)
		return fmt.Errorf("删除分支端点：%w", err)
	}
	if _, err := tx.Exec(ctx,
		"DELETE FROM branches WHERE branch_id = $1", branchID); err != nil {
		recordStoreWrite(err)
		return fmt.Errorf("删除分支：%w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		recordStoreWrite(err)
		return fmt.Errorf("提交删除分支事务：%w", err)
	}
	recordStoreWrite(nil)
	return nil
}

func (s *postgresStateStore) UpsertEndpoint(ctx context.Context, ep *Endpoint, cred *EndpointCredential) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		recordStoreWrite(err)
		return fmt.Errorf("开启端点事务：%w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := upsertEndpointTx(ctx, tx, ep, cred); err != nil {
		recordStoreWrite(err)
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		recordStoreWrite(err)
		return fmt.Errorf("提交端点事务：%w", err)
	}
	recordStoreWrite(nil)
	return nil
}

func (s *postgresStateStore) DeleteEndpoint(ctx context.Context, endpointID string) error {
	return s.execWrite(ctx, "DELETE FROM endpoints WHERE endpoint_id = $1", endpointID)
}

func (s *postgresStateStore) DeleteProject(ctx context.Context, projectID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		recordStoreWrite(err)
		return fmt.Errorf("开启删除项目事务：%w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// 顺序：端点（凭据级联）→ 子分支 → 父分支 → 项目。
	steps := []string{
		"DELETE FROM endpoints WHERE project_id = $1",
		"DELETE FROM branches WHERE project_id = $1 AND parent_id IS NOT NULL",
		"DELETE FROM branches WHERE project_id = $1",
		"DELETE FROM projects WHERE project_id = $1",
	}
	for _, sql := range steps {
		if _, err := tx.Exec(ctx, sql, projectID); err != nil {
			recordStoreWrite(err)
			return fmt.Errorf("删除项目关联数据：%w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		recordStoreWrite(err)
		return fmt.Errorf("提交删除项目事务：%w", err)
	}
	recordStoreWrite(nil)
	return nil
}

func (s *postgresStateStore) UpdateRecoveryState(ctx context.Context, endpointID string, patch RecoveryPatch) error {
	var fp, gen, pending any
	if patch.RouteFingerprint != nil {
		fp = *patch.RouteFingerprint
	}
	if patch.SafekeeperGeneration != nil {
		gen = *patch.SafekeeperGeneration
	}
	if patch.PendingReconfigure != nil {
		pending = *patch.PendingReconfigure
	}
	return s.execWrite(ctx, `
		UPDATE endpoints SET
		    route_fingerprint = COALESCE($2, route_fingerprint),
		    safekeeper_generation = COALESCE($3, safekeeper_generation),
		    pending_reconfigure = COALESCE($4, pending_reconfigure),
		    updated_at = now()
		WHERE endpoint_id = $1`, endpointID, fp, gen, pending)
}

func (s *postgresStateStore) UpdateObservedStatus(ctx context.Context, updates []StatusUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		recordStoreWrite(err)
		return fmt.Errorf("开启状态批量更新事务：%w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, u := range updates {
		if _, err := tx.Exec(ctx,
			"UPDATE endpoints SET status = $2, updated_at = now() WHERE endpoint_id = $1",
			u.EndpointID, u.Status); err != nil {
			recordStoreWrite(err)
			return fmt.Errorf("更新端点状态：%w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		recordStoreWrite(err)
		return fmt.Errorf("提交状态批量更新：%w", err)
	}
	recordStoreWrite(nil)
	return nil
}

func (s *postgresStateStore) ResolveCredentials(_ context.Context, ep *Endpoint) (string, error) {
	return credStore.Get(ep)
}

func (s *postgresStateStore) Health(ctx context.Context) error {
	if err := s.pool.Ping(ctx); err != nil {
		return fmt.Errorf("控制面数据库不可用：%w", err)
	}
	return nil
}

func (s *postgresStateStore) Flush(context.Context) error { return nil }
func (s *postgresStateStore) Close(context.Context) error {
	s.pool.Close()
	return nil
}
