-- 控制面独立库初始化脚本（由集群侧在部署前执行，含中文注释）
-- 目标：在同一外部 PostgreSQL 实例上创建 neon_control_plane 库与独立账号，
-- 与 storage_controller 库（保存 pageserver generation numbers）完全隔离。
--
-- 执行方式（开发示例，管理员口令按实际环境替换）：
--   psql "postgres://postgres:<管理员口令>@192.168.232.128:5432/postgres" \
--     -f docs/sql/init_control_plane_db.sql
--
-- 注意：
--   1. 绝不修改 storage_controller 库或其账号；
--   2. 下方口令是开发环境示例值，与 docs/neon.yaml 的 Secret 示例保持一致；
--      生产必须替换为强口令，并通过 Secret 注入，不得写入仓库；
--   3. 业务表由控制面启动时的内嵌迁移自动创建，此脚本只负责库 / 账号 / 权限。

-- 创建独立账号（幂等：已存在时仅更新口令）。
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'neon_control_plane') THEN
        CREATE ROLE neon_control_plane LOGIN PASSWORD 'neon_control_plane';
    ELSE
        ALTER ROLE neon_control_plane WITH LOGIN PASSWORD 'neon_control_plane';
    END IF;
END
$$;

-- CREATE DATABASE 不能在事务块 / DO 块中执行；用 \gexec 保证库不存在时才创建。
SELECT 'CREATE DATABASE neon_control_plane OWNER neon_control_plane'
WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = 'neon_control_plane')
\gexec

-- 切换到新库授权；库 owner 与账号一致，迁移时即可创建业务表。
\connect neon_control_plane
GRANT CONNECT ON DATABASE neon_control_plane TO neon_control_plane;
GRANT USAGE, CREATE ON SCHEMA public TO neon_control_plane;
