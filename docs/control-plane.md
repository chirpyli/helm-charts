### neon_control_plane 库结构

control plane 使用独立库 `neon_control_plane`，库内只保存**控制面业务状态与外部资源引用**：
tenant/timeline 的真相源在 storage controller，明文密码与 SCRAM 验证器不在库中（验证器存放在
K8s Secret，库中仅保存 Secret 名称/键名）。

连接方式（密码在 `neon-control-plane-pg` Secret 的 `uri` 里）：

```sh
psql "postgres://neon_control_plane@192.168.232.128:5432/neon_control_plane"
```

#### 表清单

| 表 | 作用 | 关键字段 |
| --- | --- | --- |
| `schema_version` | 迁移版本记录 | `version`(PK)、`description`、`checksum`(SHA-256)、`applied_at` |
| `projects` | project → tenant 映射 | `project_id`(PK)、`tenant_id`、`name`、`deleted_at` |
| `branches` | branch → timeline 映射与父子关系 | `branch_id`(PK)、`project_id`(FK)、`tenant_id`、`timeline_id`、`parent_id`(FK, `ON DELETE RESTRICT`)、`is_default`、`deleted_at` |
| `endpoints` | endpoint 身份、类型与运行状态 | `endpoint_id`(PK)、`project_id`/`branch_id`(FK)、`tenant_id`、`timeline_id`、`compute_image`、`type`、`status`、`route_fingerprint`、`safekeeper_generation`、`pending_reconfigure`、`deleted_at` |
| `endpoint_credentials` | 凭据引用（**不存验证器本体**） | `endpoint_id`(PK/FK `ON DELETE CASCADE`)、`role_name`、`database_name`、`owner`、`credential_secret_name`、`credential_secret_key` |
| `operations` | 预留：异步操作/审计，当前版本未写入 | `operation_id`(PK)、`project_id`、`branch_id`、`endpoint_id`、`action`、`status`、`error_message` |

#### 约束与索引要点

- 唯一索引均带软删条件，删除后可用同名重建：
  - `uq_projects_tenant_id`：`(tenant_id) WHERE deleted_at IS NULL`
  - `uq_branches_tenant_timeline`：`(tenant_id, timeline_id) WHERE deleted_at IS NULL`
  - `uq_branches_default_per_project`：`(project_id) WHERE is_default AND deleted_at IS NULL`
- `branches.parent_id` 是 `ON DELETE RESTRICT`：有子分支时数据库层也会拒绝删除（API 侧同时返回 409）。
- `endpoints.status` 有 CHECK 约束：`provisioning` / `running` / `stopped` / `failed` / `unknown`。
- `endpoints.type` 有 CHECK 约束（迁移 0002 引入）：`read_write` / `read_only`；用于保证“每个分支最多一个主节点”。
- `endpoint_credentials` 随 endpoint 级联删除；Secret 本体由控制面单独回收。

#### 迁移机制

- SQL 内嵌在二进制里（`src/migrations/*.sql`，`go:embed`），启动时按版本号升序执行；
- 整个迁移在一个事务内完成，并用 `pg_advisory_xact_lock` 串行，多实例同时启动也只有一个会改表；
- 已应用脚本的 SHA-256 记入 `schema_version`，**脚本被改动会导致启动失败**（checksum 不匹配）；
- 只做 additive 变更（加表/加列/加索引），保证 N-1 版本镜像仍可读写同一库。

当前版本：`0001_init.sql`（初始化 6 张表）→ `0002_endpoint_type.sql`（新增 `endpoints.type`）。

> **升级注意**：0002 会把存量 endpoint 回填为 `read_write`。旧版本创建的 `read_only` endpoint
> 需要删除重建；否则它会被当作主节点参与 spec 重建（可能被提升为 Primary，造成同一 timeline 双写）。

#### 常用排障 SQL

```sql
-- 1) 迁移版本与校验和
select version, description, applied_at from schema_version order by version;

-- 2) 项目 / 分支 / 端点全景
select p.project_id, p.tenant_id, b.branch_id, b.timeline_id,
       e.endpoint_id, e.type, e.status
from projects p
join branches b using (project_id)
left join endpoints e using (branch_id)
where p.deleted_at is null
order by p.project_id, b.branch_id, e.created_at;

-- 3) 某分支的端点与连接身份（role/database 属于分支，不属于 endpoint）
select e.endpoint_id, e.type, e.status,
       c.role_name, c.database_name, c.credential_secret_name
from endpoints e
left join endpoint_credentials c using (endpoint_id)
where e.branch_id = '<branch_id>' and e.deleted_at is null;

-- 4) 存储路由指纹（与 SC locate 结果比对，判断本地 spec 是否落后）
select endpoint_id, route_fingerprint, pending_reconfigure
from endpoints where deleted_at is null;
```

#### 运维约束

- **单写者**：控制面固定 `replicas: 1` + `Recreate`（未实现 Lease 选主），不要扩副本；
- **不要手工改表结构或 `schema_version`**：checksum 校验会拒绝启动；需要变更请走新的 additive 迁移；
- `deleted_at` 由 API 维护，手工清理会与 storage controller / K8s 状态漂移；
- 备份直接 `pg_dump neon_control_plane` 即可；恢复后重启控制面，会按库中记录重新对账 SC 与 K8s；
- 库中不存明文密码：`endpoint_credentials` 只是 Secret 引用，排障时请到对应 `neon-cp-cred-<endpoint_id>` Secret 取验证器。