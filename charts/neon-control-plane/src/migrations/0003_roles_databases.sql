-- 分支级 catalog：roles / databases（desired state）。
--
-- 设计要点（docs/dev/control-plane-api-step1-step2.md）：
--   - 角色与数据库属于 branch，不属于 endpoint；
--   - SCRAM verifier 存在独立 K8s Secret，本表只保存 Secret 名称/键名；
--   - 明文密码不落库，只在创建角色 / reset_password 的响应中返回一次；
--   - 角色名/库名限制为无需引号的 PG 标识符，避免下发 spec 时出现注入与转义问题。

CREATE TABLE IF NOT EXISTS roles (
    branch_id   VARCHAR(64) NOT NULL REFERENCES branches (branch_id),
    role_name   VARCHAR(63) NOT NULL
        CHECK (role_name ~ '^[A-Za-z_][A-Za-z0-9_$]{0,62}$'),
    secret_name VARCHAR(253) NOT NULL,
    secret_key  VARCHAR(253) NOT NULL DEFAULT 'scram_verifier',
    protected   BOOLEAN NOT NULL DEFAULT false,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (branch_id, role_name)
);
CREATE INDEX IF NOT EXISTS idx_roles_branch ON roles (branch_id);

CREATE TABLE IF NOT EXISTS databases (
    branch_id     VARCHAR(64) NOT NULL REFERENCES branches (branch_id),
    database_name VARCHAR(63) NOT NULL
        CHECK (database_name ~ '^[A-Za-z_][A-Za-z0-9_$]{0,62}$'),
    owner_name    VARCHAR(63) NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (branch_id, database_name)
);
CREATE INDEX IF NOT EXISTS idx_databases_branch ON databases (branch_id);
