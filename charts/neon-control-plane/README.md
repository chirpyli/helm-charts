# neon-control-plane

![Version: 0.1.0](https://img.shields.io/badge/Version-0.1.0-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) [![Lint and Test Charts](https://github.com/neondatabase/helm-charts/actions/workflows/lint-test.yaml/badge.svg)](https://github.com/neondatabase/helm-charts/actions/workflows/lint-test.yaml)

Neon 最小可运行控制面（轻量 HTTP 服务，提供 project / branch / endpoint 管理 API，对接 storage_controller 完成存储调度）。

**Homepage:** https://neon.tech

## Source Code

* [https://github.com/neondatabase/neon](https://github.com/neondatabase/neon)
* 本 chart 内嵌 Go 实现源码（`src/` 目录）

## 简介

`neon-control-plane` 是一个**常驻运行的轻量 HTTP 服务**（Deployment 部署），对外暴露 Neon v2 风格 REST API，对内完成与 storage_controller、pageserver、safekeeper、compute 的关键交互。

### 核心功能

1. **Bootstrap（启动时幂等初始化）**：将 pageserver / safekeeper 节点注册进 storage_controller，创建系统默认租户（兼容旧 project 回退）
2. **Project API**：完整的 project CRUD（创建时自动创建独立 tenant + timeline + endpoint，删除时级联清理 tenant、分支和端点）
3. **Branch API**：完整的 branch CRUD（创建新 timeline / 列表 / 删除，支持父子分支关系追踪和子分支保护）
4. **Endpoint API**：完整的 endpoint CRUD（创建 / 列表 / 删除），动态定位 pageserver + safekeeper，签发 JWT，生成 SCRAM 密码，可选 K8s 动态拉起 compute
5. **Compute Spec 下发**：`GET /compute/api/v2/computes/{id}/spec`，compute_ctl 启动时拉取
6. **SC 通知处理**：`/notify-attach` (JWT 鉴权 + 重建 ComputeSpec) / `POST /notify-safekeepers` (JWT 鉴权 + 更新 safekeeper 列表)
7. **ConfigMap 持久化**：write-through + 去抖落盘，启动时对账恢复
8. **Proxy 兼容接口**：`wake_compute`、`get_endpoint_access_control`、`jwks` 等 stub

### 技术实现

- **语言**：Go（`net/http` 标准库，无框架依赖）
- **JWT**：持有 Ed25519 私钥，按 scope 现签 token；同时使用公钥校验 SC 回调的 JWT
- **SCRAM**：手动 PBKDF2 生成 `SCRAM-SHA-256` 验证器
- **状态管理**：进程内存（`sync.RWMutex` 保护），project / branch / endpoint 全量追踪
- **持久化**：K8s ConfigMap `neon-cp-state` 存储 JSON 快照，write-through + 1s 去抖合并写入
- **启动对账**：从 ConfigMap 加载快照，与 K8s compute Deployment 双向对账修正状态
- **单写者约束**：`replicas: 1` + `Recreate` 策略保证 ConfigMap 写入安全

---

## API 参考

所有 API 返回 `Content-Type: application/json`。以下示例假设控制面监听 `localhost:8080`。

### 健康检查

#### `GET /healthz`

```console
$ curl http://localhost:8080/healthz
ok
```

始终返回 `200` + `"ok"`，无鉴权。

---

### Project（项目管理）

#### `POST /projects` — 创建 project

**每个 project 拥有独立的 tenant 和 timeline**（数据完全隔离），同时自动创建：

- 一个 main 分支（对应独立 timeline）
- 一个 primary read_write endpoint（含 SCRAM 密码）
- 一个数据库（默认 `neondb`）和角色（默认 `cloud_admin`）

```console
$ curl -X POST http://localhost:8080/projects \
  -H "Content-Type: application/json" \
  -d '{"project":{"name":"myproject","branch":{"name":"main","role_name":"sally","database_name":"mydb"}}}'
```

**请求体** `ProjectCreateRequest`（对齐 Neon Cloud API v2）：

| 字段                             | 类型   | 必填 | 说明                                |
| -------------------------------- | ------ | ---- | ----------------------------------- |
| `project.name`                 | string | 否   | project 名称（默认自动生成）        |
| `project.branch.name`          | string | 否   | 初始分支名称（默认`"main"`）      |
| `project.branch.role_name`     | string | 否   | 初始角色名（默认`"cloud_admin"`） |
| `project.branch.database_name` | string | 否   | 初始数据库名（默认`"neondb"`）    |

```console
# 空请求体也完全支持（全部使用默认值）
$ curl -X POST http://localhost:8080/projects -d '{}'
```

**响应** `201 Created`：

```json
{
  "project": {
    "project_id": "proj-176db416e9091dd9",
    "tenant_id": "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6",
    "name": "myproject",
    "created_at": "2026-07-13T10:30:00Z"
  },
  "connection_uris": [{
    "connection_uri": "postgresql://sally@neon-compute-ep-xxx.default.svc.cluster.local:5432/mydb",
    "connection_parameters": "{\"database\":\"mydb\",\"role\":\"sally\"}"
  }],
  "roles": [{"role_name": "sally", "password": "a1b2c3d4e5f6a7b8..."}],
  "databases": [{"database_name": "mydb", "owner_name": "sally"}],
  "default_branch": {
    "branch_id": "br-a1b2c3d4e5f6a7b8",
    "name": "main",
    "timeline_id": "b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f"
  },
  "operations": [
    {"action": "create_tenant", "status": "finished"},
    {"action": "create_timeline", "status": "finished"}
  ]
}
```

> - `project_id` 格式：`"proj-" + 8 字节随机 hex`
> - `tenant_id` 格式：32 hex 字符（16 字节），**每个 project 独立生成**
> - **请保存 `roles[0].password`**，这是连接数据库所需的 SCRAM 明文密码（后续无法复现）
> - 创建流程：SC 创建 tenant → SC 创建 timeline → 创建本地记录 → 定位 pageserver → 获取 safekeeper → 签发 JWT → 生成 SCRAM → 组装 ComputeSpec

#### `GET /projects` — 列出所有 project

```console
$ curl http://localhost:8080/projects
```

**响应** `200 OK`：

```json
[
  {"project_id": "proj-176db416e9091dd9", "tenant_id": "a1b2c3d4...", "name": "myproject", "created_at": "2026-07-13T10:30:00Z"}
]
```

#### `GET /projects/{project_id}` — 查询单个 project

```console
$ curl http://localhost:8080/projects/proj-176db416e9091dd9
```

**响应** `200 OK`：同上。不存在时返回 `404` `{"error":"project not found"}`。

#### `DELETE /projects/{project_id}` — 删除 project

级联删除 project 下的所有 branch 和 endpoint：

1. 收集该 project 下所有 endpoint，逐个清理 K8s 资源（Deployment + Service）
2. 对该 project 下所有 branch，向 SC 请求删除对应 timeline（best-effort）
3. 向 SC 请求删除该项目独占的 tenant（best-effort）
4. 移除 project、所有 branch、所有 endpoint 的内存记录
5. 持久化到 ConfigMap

```console
$ curl -X DELETE http://localhost:8080/projects/proj-176db416e9091dd9
```

**响应** `200 OK`：

```json
{
  "project": { "project_id": "proj-176db416e9091dd9", "tenant_id": "a1b2c3d4...", "name": "myproject" },
  "branches": [
    {"branch_id": "br-main", "timeline_id": "b2c3d4e5...", "timeline_deleted": true}
  ],
  "endpoints_count": 1,
  "tenant_deleted": true
}
```

---

### Branch（分支管理）

#### `POST /projects/{project_id}/branches` — 创建 branch

在对应 tenant 下创建新 timeline（调用 SC `/v1/tenant/{tenant_id}/timeline`），并追踪 `branch_id → timeline_id` 映射。

```console
$ curl -X POST http://localhost:8080/projects/proj-176db416e9091dd9/branches \
  -d '{"name":"dev", "parent_branch_id":"br-a1b2c3d4e5f6a7b8"}'
```

**请求体**：

| 字段                 | 类型   | 必填 | 说明                                          |
| -------------------- | ------ | ---- | --------------------------------------------- |
| `name`             | string | 否   | 分支名称（默认自动生成`"branch-" + 6 hex`） |
| `parent_branch_id` | string | 否   | 父分支 ID，空则从 main 分支分叉               |

**响应** `201 Created`：

```json
{
  "branch_id": "br-7f1358d3afab2d9c",
  "project_id": "proj-176db416e9091dd9",
  "tenant_id": "3d1f7595b468230304e0b73cecbcb081",
  "timeline_id": "060488c51cd1856877ed0b291c05e385",
  "name": "dev",
  "parent_id": "br-a1b2c3d4e5f6a7b8"
}
```

> - `branch_id` 格式：`"br-" + 8 字节随机 hex
> - `timeline_id` 格式：32 hex 字符（16 字节），对应 SC 的 timeline UUID
> - 父分支未指定时自动使用默认 main 分支的时间线作为 ancestor

#### `GET /projects/{project_id}/branches` — 列出 branch

返回该 project 下所有真实创建的分支（不再返回静态占位数据）。

```console
$ curl http://localhost:8080/projects/proj-176db416e9091dd9/branches
```

**响应** `200 OK`：

```json
[
  {
    "branch_id": "br-a1b2c3d4e5f6a7b8",
    "project_id": "proj-176db416e9091dd9",
    "tenant_id": "3d1f7595b468230304e0b73cecbcb081",
    "timeline_id": "060488c51cd1856877ed0b291c05e385",
    "name": "main",
    "default": true
  },
  {
    "branch_id": "br-7f1358d3afab2d9c",
    "project_id": "proj-176db416e9091dd9",
    "tenant_id": "3d1f7595b468230304e0b73cecbcb081",
    "timeline_id": "7a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e",
    "name": "dev",
    "parent_id": "br-a1b2c3d4e5f6a7b8"
  }
]
```

#### `DELETE /projects/{project_id}/branches/{branch_id}` — 删除 branch

删除分支安全规则：

- **不可删除默认分支**（`default: true`）→ HTTP 409
- **不可删除有子分支的父分支** → HTTP 409，返回 `child_branches` 列表

删除流程：

1. 校验 project 存在、branch 存在且归属正确
2. 检查 default / child 约束
3. 清理该 branch 上所有 endpoint 的 K8s 资源
4. 向 SC 请求删除 timeline（best-effort）
5. 移除 branch 及关联 endpoint 的内存记录
6. 持久化

```console
$ curl -X DELETE http://localhost:8080/projects/proj-176db416e9091dd9/branches/br-7f1358d3afab2d9c
```

**响应** `200 OK`：

```json
{
  "branch": {
    "branch_id": "br-7f1358d3afab2d9c",
    "project_id": "proj-176db416e9091dd9",
    "tenant_id": "3d1f7595b468230304e0b73cecbcb081",
    "timeline_id": "7a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e",
    "name": "dev",
    "parent_id": "br-a1b2c3d4e5f6a7b8"
  },
  "operations": [
    {"action": "delete_timeline", "status": "finished"},
    {"action": "suspend_compute", "status": "finished"}
  ]
}
```

**硬删除**：添加查询参数 `?hard_delete=true` 跳过恢复窗口（当前无恢复机制，仅记录在响应中）：

```console
$ curl -X DELETE "http://localhost:8080/projects/proj-.../branches/br-...?hard_delete=true"
```

**尝试删除有子分支的父分支** → HTTP 409：

```json
{
  "error": "cannot delete a branch that has child branches",
  "child_branches": ["br-child1", "br-child2"],
  "hint": "delete all child branches first before deleting this branch"
}
```

---

### Endpoint（计算端点管理）

#### `POST /projects/{project_id}/endpoints` — 创建 endpoint

创建 endpoint 并生成完整 ComputeSpec，流程：

1. 解析请求体（`branch_id`、`type`）
2. 定位目标 branch → timeline_id
3. **动态定位 pageserver**：调用 SC `GET /debug/v1/tenant/{tid}/locate` 获取最新 shard 拓扑
4. **动态获取 safekeeper**：调用 SC `GET /control/v1/safekeeper` 获取 Active 节点列表
5. 签发 storage JWT（scope: `pageserverapi,safekeeperdata`，含 tenant_id）
6. 生成 SCRAM-SHA-256 验证器（角色 `cloud_admin`）
7. 组装 ComputeSpec（含 pageserver 连接信息、safekeeper 连接串、JWT、角色密码）
8. 可选 `enableK8sCompute=true` 时，通过 K8s API 创建 Deployment + Service

```console
$ curl -X POST http://localhost:8080/projects/proj-176db416e9091dd9/endpoints \
  -d '{"branch_id":"br-7f1358d3afab2d9c","type":"read_write"}'
```

**请求体**：

| 字段          | 类型   | 必填 | 说明                                        |
| ------------- | ------ | ---- | ------------------------------------------- |
| `branch_id` | string | 否   | 目标分支 ID（空则使用默认 main 分支）       |
| `type`      | string | 否   | `"read_write"`（默认） 或 `"read_only"` |

**响应** `201 Created`：

```json
{
  "endpoint_id": "ep-aac4db8fe1aa217a",
  "project_id": "proj-176db416e9091dd9",
  "tenant_id": "3d1f7595b468230304e0b73cecbcb081",
  "timeline_id": "7a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e",
  "branch_id": "br-7f1358d3afab2d9c",
  "compute_image": "neondatabase/neon:latest",
  "spec": {
    "format_version": 1,
    "tenant_id": "3d1f7595b468230304e0b73cecbcb081",
    "timeline_id": "7a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e",
    "mode": "Primary",
    "pageserver_connection_info": {
      "shard_count": 0,
      "stripe_size": 0,
      "shards": {
        "0": {"pageservers": [{"id": 1000, "libpq_url": "postgresql://...", "grpc_url": "http://..."}]}
      }
    },
    "safekeeper_connstrings": [
      "neon-safekeeper-0.neon-safekeeper-hl-svc:5454",
      "neon-safekeeper-1.neon-safekeeper-hl-svc:5454",
      "neon-safekeeper-2.neon-safekeeper-hl-svc:5454"
    ],
    "storage_auth_token": "eyJhbGciOiJFZERTQSIsInR5cCI6IkpXVCJ9...",
    "project_id": "proj-176db416e9091dd9",
    "branch_id": "br-7f1358d3afab2d9c",
    "endpoint_id": "ep-aac4db8fe1aa217a",
    "cluster": {
      "roles": [{"name": "cloud_admin", "encrypted_password": "SCRAM-SHA-256$..."}],
      "databases": [{"name": "postgres", "owner": "cloud_admin"}],
      "settings": {}
    }
  },
  "status": "created",
  "connection_uri": "postgresql://cloud_admin@neon-compute-ep-aac4db8fe1aa217a.default.svc.cluster.local:5432/postgres"
}
```

> - `mode` 根据 `type` 自动设置：`"read_write"` → `"Primary"`，`"read_only"` → `"Replica"`
> - pageserver 和 safekeeper 信息优先从 SC 动态获取，失败时回退到 `values.yaml` 静态配置
> - `status` 为 `"created"`；若 `enableK8sCompute=true` 且 Deployment 创建成功则为 `"running"`

**动态拉起 compute**：当 `settings.enableK8sCompute=true` 时，控制面额外通过 K8s API 创建 Deployment（命名格式 `neon-compute-{endpoint_id}`）和 ClusterIP Service。容器启动命令：

```
compute_ctl --control-plane-uri http://neon-control-plane-svc:8080
            --compute-id ep-aac4db8fe1aa217a
            -C postgresql://cloud_admin@localhost/postgres
            --pgdata /var/db/postgres/data --pgbin ... --external-http-port 3080
```

compute_ctl 启动后通过 `GET /compute/api/v2/computes/{compute_id}/spec` 拉取 spec。

#### `GET /projects/{project_id}/endpoints` — 列出 endpoint

```console
$ curl http://localhost:8080/projects/proj-176db416e9091dd9/endpoints
```

**响应** `200 OK`：endpoint 数组（同创建响应格式）。

#### `DELETE /projects/{project_id}/endpoints/{endpoint_id}` — 删除 endpoint

删除内存记录，同时清理 K8s 资源（若 `enableK8sCompute=true`）。

```console
$ curl -X DELETE http://localhost:8080/projects/proj-176db416e9091dd9/endpoints/ep-aac4db8fe1aa217a
```

**响应** `200 OK`：

```json
{"status": "deleted", "endpoint_id": "ep-aac4db8fe1aa217a"}
```

> K8s 资源清理为 best-effort：删除 Deployment 和 Service，失败不阻塞返回。

---

### Compute Spec 下发（compute_ctl 拉取）

#### `GET /compute/api/v2/computes/{compute_id}/spec`

由 `compute_ctl --control-plane-uri` 模式下的 compute 容器在启动时调用，获取 ComputeSpec。

```console
# compute 容器内部执行（无需手动调用）
$ curl http://neon-control-plane-svc:8080/compute/api/v2/computes/ep-aac4db8fe1aa217a/spec
```

**响应** `200 OK`：

```json
{
  "spec": { /* 完整 ComputeSpec，与创建 endpoint 响应中的 spec 字段一致 */ },
  "compute_ctl_config": {}
}
```

> 若 spec 为 nil（如控制面重启后），调用时按需从 SC 重新 locate 并重建 spec。

---

### Storage Controller 通知接口（SC → CP 回调）

以下为 SC 回调控制面的通知端点，均使用 JWT 鉴权（校验 EdDSA 签名 + ControlPlane scope）。

#### `PUT /notify-attach` — pageserver 重附着通知

SC 在 pageserver shard 迁移后发送。控制面处理流程：

1. JWT 鉴权（校验签名 + scope 含 `ControlPlane`）
2. 解析请求体（`tenant_id`、`timeline_id`、`generation`）
3. 向 SC 重新 locate pageserver（获取最新地址）
4. 更新所有受影响 endpoint 的 `pageserver_connection_info`
5. 触发 ConfigMap 持久化

```console
$ curl -X PUT http://localhost:8080/notify-attach \
  -H "Authorization: Bearer <jwt_token>" \
  -d '{"tenant_id":"3d1f7595b468230304e0b73cecbcb081","timeline_id":"060488c51cd1...","generation":1}'
```

**请求体**：

| 字段            | 类型   | 说明              |
| --------------- | ------ | ----------------- |
| `tenant_id`   | string | 受影响的租户 ID   |
| `timeline_id` | string | 受影响的时间线 ID |
| `generation`  | int    | 配置版本号        |

**响应** `200 OK`：

```json
{"status": "attached", "affected_endpoints": 2}
```

> 同时支持 `POST /notify-attach`（兼容测试场景）。

#### `PUT /notify-safekeepers` — safekeeper 拓扑变更通知

SC 在 safekeeper 集合变更后发送。控制面处理流程：

1. JWT 鉴权（校验签名 + scope 含 `ControlPlane`）
2. 解析请求体（含完整 safekeeper 列表）
3. 直接更新受影响 endpoint 的 `safekeeper_connstrings`
4. 触发 ConfigMap 持久化

```console
$ curl -X PUT http://localhost:8080/notify-safekeepers \
  -H "Authorization: Bearer <jwt_token>" \
  -d '{
    "tenant_id":"3d1f7595b468230304e0b73cecbcb081",
    "timeline_id":"060488c51cd1...",
    "generation":1,
    "safekeepers":[
      {"id":2000,"host":"neon-safekeeper-0","port":5454},
      {"id":2001,"host":"neon-safekeeper-1","port":5454},
      {"id":2002,"host":"neon-safekeeper-2","port":5454}
    ]
  }'
```

**请求体**：

| 字段            | 类型   | 说明                                               |
| --------------- | ------ | -------------------------------------------------- |
| `tenant_id`   | string | 租户 ID                                            |
| `timeline_id` | string | 时间线 ID                                          |
| `generation`  | int    | 配置版本号                                         |
| `safekeepers` | array  | 完整的 Active safekeeper 列表（含 id、host、port） |

**响应** `200 OK`：

```json
{"status": "updated", "affected_endpoints": 3}
```

> 同时支持 `POST /notify-safekeepers`（兼容测试场景）。

---

### Proxy 兼容接口（phase-2 预留）

以下接口为对接 Neon proxy / linkproxy 预留，当前均为 stub 实现。

#### `GET /wake_compute?endpointish={endpoint_id}`

proxy 唤醒 compute 时调用。返回模拟的 PostgreSQL 地址。

```console
$ curl "http://localhost:8080/wake_compute?endpointish=ep-aac4db8fe1aa217a"
```

**响应** `200 OK`：

```json
{
  "address": "neon-compute-ep-aac4db8fe1aa217a.default.svc.cluster.local:5432",
  "server_name": null,
  "aux": {
    "endpoint_id": "ep-aac4db8fe1aa217a",
    "project_id": "proj-176db416e9091dd9",
    "branch_id": "br-7f1358d3afab2d9c",
    "compute_id": "ep-aac4db8fe1aa217a",
    "cold_start_info": "unknown"
  }
}
```

#### `GET /get_endpoint_access_control?endpointish={endpoint_id}`

proxy 获取端点鉴权信息（SCRAM 验证器、IP 白名单等）。

```console
$ curl "http://localhost:8080/get_endpoint_access_control?endpointish=ep-aac4db8fe1aa217a"
```

**响应** `200 OK`：

```json
{
  "role_secret": "SCRAM-SHA-256$...",
  "allowed_ips": [],
  "allowed_vpc_endpoint_ids": [],
  "block_public_connections": false,
  "block_vpc_connections": false,
  "project_id": "proj-176db416e9091dd9",
  "account_id": null,
  "rate_limits": {}
}
```

#### `GET /endpoints/{endpoint_id}/jwks`

返回 JWKS（JSON Web Key Set）。**当前为空**，phase-2 实现 JWT 公钥分发。

```console
$ curl http://localhost:8080/endpoints/ep-aac4db8fe1aa217a/jwks
```

**响应** `200 OK`：

```json
{"jwks": []}
```

---

## 持久化机制

### ConfigMap 快照

运行时状态自动写入 K8s ConfigMap `neon-cp-state`，存储 JSON 格式快照：

```json
{
  "state.json": "{\"projects\":{...},\"branches\":{...},\"endpoints\":{...}}"
}
```

- **写入策略**：write-through + 1s 去抖合并（多次快速变更合并为一次写入）
- **加载恢复**：启动时先从 ConfigMap 加载快照，再与 K8s 实际 compute Deployment 双向对账
- **单写者约束**：`replicas: 1` + `Recreate` 策略，避免并发写冲突

### 启动对账流程

1. 从 ConfigMap 加载持久化快照（project/branch/endpoint 映射）
2. 向 SC 对账 tenant↔timeline 关系（真相源）
3. List K8s compute Deployment，修正 endpoint.status（`running` / `stopped`）

---

## API 速查表

| 方法                        | 路径                                             | 用途                                                      |
| --------------------------- | ------------------------------------------------ | --------------------------------------------------------- |
| `GET`                     | `/healthz`                                     | 健康检查                                                  |
| **Project**           |                                                  |                                                           |
| `POST`                    | `/projects`                                    | 创建 project（自动创建独立 tenant + timeline + endpoint） |
| `GET`                     | `/projects`                                    | 列出所有 project                                          |
| `GET`                     | `/projects/{id}`                               | 查询单个 project                                          |
| `DELETE`                  | `/projects/{id}`                               | 删除 project（级联删除分支和端点）                        |
| **Branch**            |                                                  |                                                           |
| `POST`                    | `/projects/{id}/branches`                      | 创建 branch（新 timeline + 父子追踪）                     |
| `GET`                     | `/projects/{id}/branches`                      | 列出所有真实 branch                                       |
| `DELETE`                  | `/projects/{id}/branches/{bid}`                | 删除分支（默认/子分支保护 + 级联清理）                    |
| **Endpoint**          |                                                  |                                                           |
| `POST`                    | `/projects/{id}/endpoints`                     | 创建 endpoint（动态 locate + 签发 JWT + SCRAM）           |
| `GET`                     | `/projects/{id}/endpoints`                     | 列出 endpoint                                             |
| `DELETE`                  | `/projects/{id}/endpoints/{eid}`               | 删除 endpoint（含 K8s 资源清理）                          |
| **Spec**              |                                                  |                                                           |
| `GET`                     | `/compute/api/v2/computes/{id}/spec`           | compute_ctl 拉取 ComputeSpec                              |
| **Notify（SC 回调）** |                                                  |                                                           |
| `PUT` / `POST`          | `/notify-attach`                               | SC pageserver 重附着通知（JWT 鉴权 + 重建 spec）          |
| `PUT` / `POST`          | `/notify-safekeepers`                          | SC safekeeper 变更通知（JWT 鉴权 + 更新路由）             |
| **Proxy 兼容**        |                                                  |                                                           |
| `GET`                     | `/wake_compute?endpointish=...`                | proxy 唤醒 compute                                        |
| `GET`                     | `/get_endpoint_access_control?endpointish=...` | proxy 鉴权查询                                            |
| `GET`                     | `/endpoints/{id}/jwks`                         | JWKS 公钥分发（空）                                       |

### 当前限制

1. **无外部鉴权**：所有业务 API 均可匿名访问（内网部署假设），仅 SC 回调端点在应用层做 JWT 校验
2. **无恢复窗口**：删除操作即时生效，不支持软删除/回收站恢复
3. **SCRAM 验证器不持久化**：`scram_verifier` 每次重启重新生成会导致 compute 认证问题（phase-1 可接受）
4. **JWKS 为空**：proxy 的 JWT 公钥分发未实现，proxy 会 fallback 处理

## 安装

```console
$ helm repo add neondatabase https://neondatabase.github.io/helm-charts
$ helm install neon-control-plane neondatabase/neon-control-plane
```

通过 umbrella chart 一键部署（推荐）：

```console
$ helm install neon ./charts/neon
```

## 前置依赖

- **storage_controller**：租户/节点调度（需先部署并配置 databaseUrl）
- **pageserver / safekeeper**：节点清单（需先部署，否则 bootstrap 注册失败）
- **JWT 密钥对**：控制面持有私钥签发 token；pageserver/safekeeper 挂载公钥校验

## 本地开发

控制面可以在 K8s 集群外直接运行，适合调试和开发：

### 编译

```bash
cd charts/neon-control-plane/src
CGO_ENABLED=0 go build -o neon-control-plane .
```

> 项目零外部依赖，仅需 Go 1.21+ 标准库，无需 `go mod download`。

### Docker 镜像

```bash
cd charts/neon-control-plane/src
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o neon-control-plane .
docker build -t neon-control-plane:latest .
docker tag neon-control-plane:latest 192.168.232.128:5000/neon-control-plane:latest
docker push 192.168.232.128:5000/neon-control-plane:latest
```

> Dockerfile 基于 `FROM scratch`，产物镜像约 9.5MB。

### 本地运行

1. **准备 JWT 密钥对**（若已有可跳过）：

```bash
openssl genpkey -algorithm ed25519 -out privatekey.pem
openssl pkey -in privatekey.pem -pubout -out publickey.pem
```

2. **准备配置文件** `config.json`：

```json
{
  "storage_controller_url": "http://localhost:50051",
  "listen_port": 8080,
  "default_tenant_id": "3d1f7595b468230304e0b73cecbcb081",
  "pageservers": [
    {"id": 1000, "host": "localhost", "pg_port": 64000, "http_port": 9898}
  ],
  "safekeepers": [
    {"id": 2000, "host": "localhost", "pg_port": 5454, "http_port": 7676},
    {"id": 2001, "host": "localhost", "pg_port": 5455, "http_port": 7677},
    {"id": 2002, "host": "localhost", "pg_port": 5456, "http_port": 7678}
  ],
  "compute_image": "neondatabase/neon:latest",
  "enable_k8s_compute": false
}
```

3. **启动**：

```bash
export JWT_PRIVATE_KEY_PATH="./privatekey.pem"
./neon-control-plane --config ./config.json
```

输出示例：

```
neon-control-plane listening on :8080
  storage-controller: http://localhost:50051
  default tenant: 3d1f7595b468230304e0b73cecbcb081
  pageservers: 1, safekeepers: 3
```

4. **验证**：

```bash
kubectl port-forward -n neon svc/neon-control-plane-svc 8080:8080

curl http://localhost:8080/healthz              # ok
curl http://localhost:8080/projects              # []
# 创建 project（自动创建 tenant + timeline + endpoint）
curl -X POST http://localhost:8080/projects \
  -H "Content-Type: application/json" \
  -d '{"project":{"name":"test-proj","branch":{"database_name":"neondb"}}}'
# 响应包含：project + connection_uris + roles（含密码）+ databases + default_branch + operations
curl -X DELETE http://localhost:8080/projects/proj-xxx
# 级联删除：tenant + timeline + branch + endpoint（全部清理）
```

> **注意**：本地运行时 `enable_k8s_compute=false`（默认），此时不连接 K8s API，也无 ConfigMap 持久化（状态仅存内存，重启丢失）。所有 K8s 相关功能静默跳过。

## Requirements

Kubernetes: `^1.18.x-x`

## Values

| Key                                  | Type   | Default                                                                                            | Description                                                          |
| ------------------------------------ | ------ | -------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------- |
| affinity                             | object | `{}`                                                                                             | 亲和性                                                               |
| extraManifests                       | list   | `[]`                                                                                             | 额外创建的 K8s 清单                                                  |
| fullnameOverride                     | string | `""`                                                                                             | 完全覆盖 fullname 模板                                               |
| global                               | object | `{}`                                                                                             | 全局配置                                                             |
| image.pullPolicy                     | string | `"IfNotPresent"`                                                                                 | 镜像拉取策略                                                         |
| image.repository                     | string | `"neon-control-plane"`                                                                           | 控制面镜像（由 src/ 自行构建）                                       |
| image.tag                            | string | `"latest"`                                                                                       | 覆盖镜像 tag                                                         |
| imagePullSecrets                     | list   | `[]`                                                                                             | docker-registry secret 列表                                          |
| metrics.enabled                      | bool   | `false`                                                                                          | 启用 Prometheus 指标自动发现                                         |
| metrics.serviceMonitor.enabled       | bool   | `false`                                                                                          | 创建 ServiceMonitor 资源                                             |
| metrics.serviceMonitor.interval      | string | `"10s"`                                                                                          | Prometheus 抓取间隔                                                  |
| metrics.serviceMonitor.namespace     | string | `""`                                                                                             | ServiceMonitor 命名空间                                              |
| metrics.serviceMonitor.scrapeTimeout | string | `"10s"`                                                                                          | 抓取超时                                                             |
| metrics.serviceMonitor.selector      | object | `{}`                                                                                             | 附加标签                                                             |
| nameOverride                         | string | `""`                                                                                             | 部分覆盖 fullname 模板                                               |
| nodeSelector                         | object | `{}`                                                                                             | 节点选择                                                             |
| podAnnotations                       | object | `{}`                                                                                             | Pod 注解                                                             |
| podDisruptionBudget.minAvailable     | int    | `1`                                                                                              | 最小可用副本数                                                       |
| podLabels                            | object | `{}`                                                                                             | Pod 额外标签                                                         |
| podSecurityContext                   | object | `{}`                                                                                             | Pod 安全上下文                                                       |
| priorityClassName                    | string | `""`                                                                                             | Pod 优先级类                                                         |
| resources.limits.cpu                 | string | `"100m"`                                                                                         | CPU 上限（测试环境；生产建议 >= 500m）                               |
| resources.limits.memory              | string | `"128Mi"`                                                                                        | 内存上限（测试环境；生产建议 >= 256Mi）                              |
| resources.requests.cpu               | string | `"100m"`                                                                                         | CPU 请求（Guaranteed QoS）                                           |
| resources.requests.memory            | string | `"128Mi"`                                                                                        | 内存请求（Guaranteed QoS）                                           |
| securityContext                      | object | `{}`                                                                                             | 容器安全上下文                                                       |
| service.port                         | int    | `8080`                                                                                           | Service 端口                                                         |
| service.type                         | string | `"ClusterIP"`                                                                                    | Service 类型                                                         |
| serviceAccount.annotations           | object | `{}`                                                                                             | SA 注解                                                              |
| serviceAccount.create                | bool   | `true`                                                                                           | 是否创建 ServiceAccount                                              |
| serviceAccount.name                  | string | `""`                                                                                             | 显式指定 SA 名称                                                     |
| settings.computeImage                | string | `"neondatabase/neon:latest"`                                                                     | 动态拉起 compute 使用的镜像                                          |
| settings.defaultTenantId             | string | `"3d1f7595b468230304e0b73cecbcb081"`                                                             | bootstrap 创建的默认租户 id（32 位 hex）                             |
| settings.domain                      | string | `"neon.local"`                                                                                   | proxy 兼容接口域名（phase-2 启用 proxy 时使用）                      |
| settings.enableK8sCompute            | bool   | `false`                                                                                          | 是否启用 K8s 动态拉起 compute（需 RBAC）                             |
| settings.jwtSecretName               | string | `"neon-jwt"`                                                                                     | 共享 JWT Secret 名称（含 privateKey.pem / publicKey.pem）            |
| settings.listenPort                  | int    | `8080`                                                                                           | 控制面自身监听端口                                                   |
| settings.pageservers                 | list   | `[{"id":1000,"host":"neon-pageserver-0.neon-pageserver-hl-svc","pgPort":64000,"httpPort":9898}]` | pageserver 节点清单（bootstrap 注册进 SC）                           |
| settings.safekeepers                 | list   | `[{"id":2000,"host":"neon-safekeeper-0....","pgPort":5454,"httpPort":7676}, ...]`                | safekeeper 节点清单（bootstrap 注册进 SC 并设为 Active，需 >= 3 个） |
| settings.storageControllerUrl        | string | `"http://neon-storage-controller-svc:50051"`                                                     | storage_controller 基址（含端口）                                    |
| tolerations                          | list   | `[]`                                                                                             | 容忍                                                                 |

-------------------------------------------## Helm Chart 打包

```bash
cd charts
helm package neon-control-plane
# 产出: neon-control-plane-0.1.0.tgz
```

## 环境变量

| 变量                     | 说明                                                               |
| ------------------------ | ------------------------------------------------------------------ |
| `JWT_PRIVATE_KEY_PATH` | Ed25519 私钥文件路径（PKCS8 PEM 格式），用于签发 JWT               |
| `JWT_PUBLIC_KEY_PATH`  | Ed25519 公钥文件路径（用于校验 SC 回调的 JWT，优先级低于私钥反推） |
| `POD_NAMESPACE`        | K8s 命名空间（in-cluster 时自动读取，本地运行不需要）              |

> 容器部署时，Helm 模板（`deployment.yaml`）会自动设置这些环境变量，无需手动配置。

## 命令行参数

| 参数         | 默认值                    | 说明                                   |
| ------------ | ------------------------- | -------------------------------------- |
| `--config` | `/etc/neon/config.json` | 配置文件路径（由 Helm ConfigMap 挂载） |

---

---

Autogenerated from chart metadata using [helm-docs v1.9.1](https://github.com/norwoodj/helm-docs/releases/v1.9.1)
