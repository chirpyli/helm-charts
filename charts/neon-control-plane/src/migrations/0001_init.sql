-- 控制面业务表初始化（中文注释，DDL 版本 0001）
-- 约束：迁移只做 additive 变更，保证 N-1 版本镜像仍可读写本库。

-- 已应用迁移版本记录（runner 也会以 IF NOT EXISTS 方式创建，这里保持幂等）。
CREATE TABLE IF NOT EXISTS schema_version (
    version     INTEGER PRIMARY KEY,      -- 单调递增的迁移版本号
    description TEXT NOT NULL,           -- 本次迁移的中文说明
    checksum    VARCHAR(64) NOT NULL,    -- 脚本内容 SHA-256
    applied_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 项目与 tenant 映射。
CREATE TABLE IF NOT EXISTS projects (
    project_id  VARCHAR(64) PRIMARY KEY, -- 对外项目 ID
    tenant_id   VARCHAR(64) NOT NULL,    -- 映射到的 storage-controller tenant
    name        VARCHAR(255),            -- 用户指定项目名
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at  TIMESTAMPTZ              -- 软删除时间，为空表示有效
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_projects_tenant_id
    ON projects (tenant_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_projects_deleted_at ON projects (deleted_at);

-- 分支与 timeline 映射、父子关系。
CREATE TABLE IF NOT EXISTS branches (
    branch_id   VARCHAR(64) PRIMARY KEY, -- 对外分支 ID
    project_id  VARCHAR(64) NOT NULL REFERENCES projects (project_id),
    tenant_id   VARCHAR(64) NOT NULL,    -- 冗余列，避免联表
    timeline_id VARCHAR(64) NOT NULL,
    name        VARCHAR(255) NOT NULL DEFAULT '',
    parent_id   VARCHAR(64) REFERENCES branches (branch_id) ON DELETE RESTRICT,
    is_default  BOOLEAN NOT NULL DEFAULT false,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at  TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_branches_tenant_timeline
    ON branches (tenant_id, timeline_id) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_branches_default_per_project
    ON branches (project_id) WHERE is_default AND deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_branches_project_id ON branches (project_id);
CREATE INDEX IF NOT EXISTS idx_branches_parent_id ON branches (parent_id);

-- 端点身份、恢复必需状态与对外聚合状态。
-- 完整 ComputeSpec 与观测字段不落库，重启后由 SC / K8s / compute_ctl 对账重建。
CREATE TABLE IF NOT EXISTS endpoints (
    endpoint_id           VARCHAR(64) PRIMARY KEY, -- 对外端点 ID
    project_id            VARCHAR(64) NOT NULL REFERENCES projects (project_id),
    branch_id             VARCHAR(64) NOT NULL REFERENCES branches (branch_id),
    tenant_id             VARCHAR(64) NOT NULL,    -- 供 notify-attach 按 tenant 批量定位
    timeline_id           VARCHAR(64) NOT NULL,
    compute_image         VARCHAR(512) NOT NULL,
    status                VARCHAR(16) NOT NULL DEFAULT 'provisioning'
        CHECK (status IN ('provisioning', 'running', 'stopped', 'failed', 'unknown')),
    route_fingerprint     VARCHAR(128),            -- SC 路由指纹
    safekeeper_generation BIGINT NOT NULL DEFAULT 0,
    pending_reconfigure   BOOLEAN NOT NULL DEFAULT false,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at            TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_endpoints_project_id ON endpoints (project_id);
CREATE INDEX IF NOT EXISTS idx_endpoints_branch_id ON endpoints (branch_id);
CREATE INDEX IF NOT EXISTS idx_endpoints_tenant_id ON endpoints (tenant_id);
CREATE INDEX IF NOT EXISTS idx_endpoints_pending
    ON endpoints (endpoint_id) WHERE pending_reconfigure;
CREATE INDEX IF NOT EXISTS idx_endpoints_deleted_at ON endpoints (deleted_at);

-- 端点凭据引用：只保存 Secret 名称与键，验证器本体不入库。
CREATE TABLE IF NOT EXISTS endpoint_credentials (
    endpoint_id            VARCHAR(64) PRIMARY KEY REFERENCES endpoints (endpoint_id) ON DELETE CASCADE,
    role_name              VARCHAR(128) NOT NULL,  -- 初始角色名，重建 spec 时不可回退默认值
    database_name          VARCHAR(128) NOT NULL,
    owner                  VARCHAR(128) NOT NULL,
    credential_secret_name VARCHAR(253) NOT NULL, -- 如 neon-cp-cred-<endpoint_id>
    credential_secret_key  VARCHAR(253) NOT NULL DEFAULT 'scram_verifier',
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 预留：异步操作与审计（后续版本启用）。
CREATE TABLE IF NOT EXISTS operations (
    operation_id  UUID PRIMARY KEY,
    project_id    VARCHAR(64),
    branch_id     VARCHAR(64),
    endpoint_id   VARCHAR(64),
    action        VARCHAR(64) NOT NULL,
    status        VARCHAR(32) NOT NULL,
    error_message TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_operations_project_created
    ON operations (project_id, created_at DESC);
