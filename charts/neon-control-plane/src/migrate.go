package main

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// =============================================================================
// DDL 迁移 runner
//
// 方案要求（docs/develop.md 3.7）：
//   - 用标准库 embed.FS 打包版本化 SQL，不引入第二个迁移框架；
//   - 启动时用事务级 advisory lock 串行执行，多副本竞争时只有一个实例改表；
//   - checksum 校验防止已应用脚本被篡改；
//   - 迁移只做 additive 变更，保证 N-1 镜像可读新库。
// =============================================================================

//go:embed migrations/*.sql
var migrationFS embed.FS

// 固定的 advisory lock 键：同一控制面库内所有实例共用。
const migrationLockKey int64 = 0x4e454f4e4350

// runMigrations 在单个事务内按顺序执行未应用的迁移脚本。
func runMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("开启迁移事务：%w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 事务级 advisory lock：事务提交或回滚时自动释放。
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", migrationLockKey); err != nil {
		return fmt.Errorf("获取迁移互斥锁：%w", err)
	}

	// 版本表必须先于业务表存在。
	if _, err := tx.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_version (
			version INTEGER PRIMARY KEY,
			description TEXT NOT NULL,
			checksum VARCHAR(64) NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("创建 schema_version：%w", err)
	}

	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return fmt.Errorf("读取内嵌迁移目录：%w", err)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	for _, name := range files {
		version, err := migrationVersion(name)
		if err != nil {
			return err
		}
		body, err := fs.ReadFile(migrationFS, "migrations/"+name)
		if err != nil {
			return fmt.Errorf("读取迁移 %s：%w", name, err)
		}
		sum := sha256.Sum256(body)
		checksum := hex.EncodeToString(sum[:])

		var applied string
		err = tx.QueryRow(ctx,
			"SELECT checksum FROM schema_version WHERE version = $1", version).Scan(&applied)
		switch err {
		case nil:
			if applied != checksum {
				return fmt.Errorf("迁移 %s 的 checksum 不匹配（已应用 %s，当前 %s）", name, applied, checksum)
			}
			continue
		case pgx.ErrNoRows:
			// 未应用，继续执行
		default:
			return fmt.Errorf("查询迁移版本 %d：%w", version, err)
		}

		if _, err := tx.Exec(ctx, string(body)); err != nil {
			return fmt.Errorf("执行迁移 %s：%w", name, err)
		}
		if _, err := tx.Exec(ctx,
			"INSERT INTO schema_version (version, description, checksum) VALUES ($1, $2, $3)",
			version, "初始化控制面业务表", checksum); err != nil {
			return fmt.Errorf("记录迁移版本 %d：%w", version, err)
		}
		log.Printf("migrate: 已应用 %s (version=%d)", name, version)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("提交迁移事务：%w", err)
	}
	return nil
}

// migrationVersion 从文件名前缀解析版本号，例如 0001_init.sql -> 1。
func migrationVersion(name string) (int, error) {
	parts := strings.SplitN(name, "_", 2)
	if len(parts) != 2 {
		return 0, fmt.Errorf("迁移文件名格式错误：%s（应为 NNNN_描述.sql）", name)
	}
	v, err := strconv.Atoi(parts[0])
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("迁移版本号无效：%s", name)
	}
	return v, nil
}
