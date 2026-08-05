## 用户需求

在 Kubernetes 中部署生产级可用的完整 Neon 平台，基于现有 helm-charts 仓库进行扩展，补齐缺失组件并形成开箱即用的整体部署方案。

## 产品概述

本方案以现有 helm-charts 仓库为基础，补齐 Neon 最核心、也是最缺失的有状态存储组件（pageserver、safekeeper），新增一个**最小可运行的 control plane 服务**（创建 project / branch / endpoint，并完成与 storage-controller、pageserver、safekeeper、compute 的关键交互），并提供一个 umbrella 总 chart 将上述组件及已有组件（storage-broker、storage-controller）统一编排，实现一条命令完成生产级 Neon 集群部署。**proxy 列为 phase-2 可选组件，默认不启用**（见「分阶段实施」），首版以「client 直连 compute」验证端到端链路。计算节点（compute）由 control plane 在创建 endpoint 时动态拉起，并提供静态演示回退。

MinIO对象存储后端由外部提供。storage controller依赖的PostgreSQL数据库也由外部提供。
```yaml
---
# 创建namespace neon
apiVersion: v1
kind: Namespace
metadata:
  name: neon

---
# 外部 MinIO S3 凭证（运行在 :9000，非 K8s 内部）
apiVersion: v1
kind: Secret
metadata:
  name: bucket-credentials
  namespace: neon
stringData:
  AWS_ACCESS_KEY_ID: "minioadmin"
  AWS_ENDPOINT_URL: "http://192.168.232.128:9000"
  AWS_REGION: "us-east-1"
  AWS_SECRET_ACCESS_KEY: "minioadmin"
  BUCKET_NAME: "neondata"

---
apiVersion: v1
kind: Secret
metadata:
  name: storage-controller-pg-cluster
  namespace: neon
type: Opaque
stringData:
  uri: "postgres://storage_controller:storage_controller@192.168.232.128:5432/storage_controller"
```

## 核心特性

- 补齐 pageserver / safekeeper 有状态服务 chart（StatefulSet + PVC + ConfigMap TOML 配置 + 探针 + PDB）
- 新增最小 **control plane 服务**（`neon-control-plane`）：一个常驻运行的轻量服务（自定义镜像 + Deployment），对外提供**精简的用户 API**（创建 project / 创建 branch / 创建 endpoint，对齐 v2.json 风格），对内完成与 storage-controller、pageserver、safekeeper、compute 的关键交互（proxy 兼容接口留作 phase-2 启用 proxy 时实现）；并在启动时执行**一次性 bootstrap**（注册节点、创建默认租户/时间线）
- 提供示例 compute 节点（静态 PostgreSQL Deployment）作为 Neon compute 演示
- umbrella 总 chart 统一编排所有子 chart，集中管理 values、依赖顺序与全局配置
- 生产级能力：Guaranteed QoS（requests=limits）、拓扑分布/反亲和、ServiceMonitor、TLS、RBAC、安全上下文、滚动更新与 PDB
- 暂不考虑监控

## 技术栈

- 包管理：Helm 3（chart apiVersion v2），复用现有仓库约定（Chart.yaml + values.yaml + templates/）
- 编排：Kubernetes StatefulSet（有状态：pageserver/safekeeper）、Deployment（无状态：control-plane/broker/controller/compute）；**proxy 为 phase-2 可选（Deployment，默认不部署）**
- 存储：PVC + StorageClass（pageserver/safekeeper 本地盘；WAL/层数据持久化在 PVC）
- 对象存储：外部 MinIO（S3 兼容，运行在 K8s 之外，凭证由 `bucket-credentials` Secret 提供），不内建对象存储 chart
- 监控：Prometheus Operator ServiceMonitor（复用现有 values 中的 metrics 开关）
- 模板语言：Go template（与现有 chart 一致），_helpers.tpl 复用命名约定

## 实现方案

### 总体策略

在现有 charts 基础上，采用「补齐缺口 + umbrella 聚合」策略：新增 pageserver、safekeeper、control-plane、compute 四个子 chart，再用一个 umbrella 总 chart（charts/neon）通过 dependencies 统一编排，复用已有 storage-broker、storage-controller 两个成熟 chart（**proxy 复用的 `neon-proxy` chart 列为 phase-2，umbrella 中以 `condition` 默认 `false` 不启用**）。

- 有状态组件（pageserver/safekeeper）用 StatefulSet + PVC，保证 Pod 重建后数据不丢失、网络标识稳定（稳定的 DNS）。
- 控制面（`neon-control-plane`）是一个**常驻最小服务**：服务内部聚合了 bootstrap 逻辑与精简用户 API。它在启动时幂等地调用 storage-controller 的 `/control/v1/node`、`/control/v1/safekeeper`、`/control/v1/tenant` 完成节点注册与租户/时间线创建；运行时对外提供 `POST /projects`、`POST /projects/{id}/branches`、`POST /projects/{id}/endpoints` 等核心 API，并经 storage-controller 完成 pageserver/safekeeper 调度。参考 neon 源码 `control_plane`（即 `neon_local`，本地开发编排工具，**非生产控制面**）的实际调用路径与 `storage_controller/src/http.rs` 的 `/control/v1/*` REST 接口。
- 存储后端为外部 MinIO（S3 兼容），凭证由 `bucket-credentials` Secret 提供，chart 通过 values 透传 endpoint/bucket/region/密钥，不内建对象存储。
- compute（计算节点）由 control plane 在创建 endpoint 时**动态拉起**：control plane 生成 `ComputeSpec` 并通过 in-cluster K8s API 为 endpoint 创建一个 compute Deployment（`compute_ctl` 从 control plane 拉取 spec）。`neon-compute` chart 作为静态/演示回退（预部署一个默认 compute），二者并存。

### 关键技术决策

1. **StatefulSet vs Deployment**：pageserver/safekeeper 有本地持久状态（tenant 层数据、safekeeper.control 控制文件），必须用 StatefulSet + volumeClaimTemplates，保证 stable network ID 与持久卷绑定；控制面无状态用 Deployment，便于水平扩容。
2. **配置方式**：pageserver/safekeeper 主要依赖 TOML 配置文件而非 CLI 参数（已确认 pageserver 启动仅 `-D/--workdir` + TOML；safekeeper 用 clap 参数 + data-dir）。因此用 ConfigMap 渲染 TOML/toml 片段，通过 volume 挂载到 workdir/data-dir。
3. **JWT 与密钥（已逐项核对 `storage_controller/src/main.rs` 启动参数）**：全局用**一套 Ed25519 私钥/公钥**。SC 启动必填 `--listen`（HTTP 端口，如 8080）与 `--database-url`（外部 PostgreSQL），并注入以下 token（同一私钥签发，scope 不同）：
   - `--jwt-token`：**PageServerApi** scope，SC → pageserver 调用用。
   - `--safekeeper-jwt-token`：**SafekeeperData** scope，SC → safekeeper 调用用。
   - `--peer-jwt-token`：**Admin** scope，SC → 其他 SC 实例（peer）用。
   - `--control-plane-jwt-token`：**ControlPlane** scope，SC → control plane 的 compute-notification upcall 鉴权用。
   - `--public-key`：公钥（PEM），SC 用于**校验**入站请求（control plane / pageserver 的 JWT）。
   - `--control-plane-url`：**严格模式（默认，非 `--dev`）下为必填**——SC 启动即校验，缺失会 `anyhow::bail!("`--control-plane-url` is not set: this is only permitted in `--dev` mode")`（见 `storage_controller/src/main.rs:388`）。它用于故障转移 upcall（远程模式，需 control plane 实现 `/notify-attach`、`/notify-safekeepers` 接收端点，见 1.5）。K8s 下 `--use-local-compute-notifications` **被禁止**（同样 `bail`，见 `main.rs:395`），不可用作回退。因此即便 phase-1 未实现 notify 端点，umbrella 也必须始终透传 `--control-plane-url=http://neon-control-plane:<port>`，否则 SC 根本起不来；未实现端点时运行时通知请求失败但 SC 已启动、非致命。
   - **SC 不连接 storage_broker**（无 `--broker-endpoint` 参数）；safekeeper/pageserver 各自连接 broker 做节点发现。
   - pageserver/safekeeper 侧挂载**同一公钥**（pageserver 用 `auth_validation_public_key_path` + `http_auth_type/pg_auth_type=NeonJWT`；safekeeper 用 `--pg-auth-public-key-path`）校验 SC 与 compute 的 token。
   - `neon-control-plane` 持有**私钥**，按目标路径 scope 现签 token（见 Bootstrap 段），并为 compute 签发 `storage_auth_token`。
   用独立 Secret（`global.jwtSecretName`）统一管理私钥 + 公钥，各 chart 挂载引用。
4. **umbrella 编排**：总 chart 用 dependencies 声明子 chart 与条件（condition），通过全局 values 透传（如 global.objectStorage、global.jwtSecretName、global.region），解决组件间配置耦合。

### Control Plane 详细设计（基于 neon 源码深度调研）

#### 重要澄清：Neon 真实控制面构成

- 仓库内 `control_plane/` crate 实为 **`neon_local`**——本地开发/测试**编排工具（CLI）**，通过 `.neon/config` 管理本地进程生命周期，**不适合直接上 K8s**，也不是生产控制面。
- 生产控制面由三层组成：
  1. **Console（云端控制面 API）**：即用户提供的 `v2.json`（`https://console.neon.tech/api/v2`），资源为 `projects` / `branches` / `endpoints`，对外暴露租户/分支/计算端点的 CRUD。该 Console **不在本仓库、也不开源**，自建需自研或仅做兼容壳。
  2. **storage_controller**：真正的**存储调度大脑**，提供 `/control/v1/*` REST API（节点注册、租户创建与调度、safekeeper 管理、upcall 回调）。本仓库已包含并可独立部署（`neon-storage-controller` chart）。
  3. **K8s Operator / compute_ctl**：计算节点由 `compute_ctl` 拉起，它从 `CONTROL_PLANE_API_BASE_URL` 拉取 `ComputeSpec`，或读取本地 `spec.json`。

#### 本方案 `neon-control-plane` 的定位：最小可运行的 control plane 服务

**明确要构建一个最小但真实运行的 control plane 服务**（不是仅 bootstrap Job）。它是一个常驻 Deployment，对外有两张"脸"：

1. **用户 API（精简，对齐 v2.json）**：`create project` / `create branch` / `create endpoint`（含 list/get/delete）。
2. **Proxy 兼容 API（console 子集，proxy 路由必需，phase-2 启用 proxy 时实现）**：`wake_compute` / `get_endpoint_access_control` / `get_endpoint_jwks`。注意：角色 SCRAM 密钥**内嵌**在 `get_endpoint_access_control.role_secret` 中（proxy 的 `get_role_access_control` 即调用该接口并取 `role_secret`），无需独立 `get_role_secret` 端点。phase-1 可不实现此面，仅保留占位。
3. **Bootstrap（服务内常驻化）**：启动时幂等地把 pageserver/safekeeper 注册进 storage_controller，并创建默认租户/时间线。

它与其他组件的关键交互：
- → **storage_controller**：注册节点、创建/查询租户、创建时间线、定位租户（取 pageserver 地址）、列出 safekeeper。
- → **pageserver**：经 SC 转发创建时间线（`/v1/tenant/{tid}/timeline`）。
- → **compute**：生成 `ComputeSpec`，用 in-cluster K8s API 为每个 endpoint 创建一个 compute Deployment，`compute_ctl` 从本服务拉取 spec。
- ↔ **proxy（phase-2）**：proxy 每次连接时调用本服务的 console 兼容接口做鉴权与 compute 寻址（phase-1 不部署 proxy，此项不触发）。
- → **对象存储（neon-endpoint-storage）**：可选，存放 LFC 状态（`endpoint_storage_addr/token`）。

#### 服务内部状态（minimal，in-memory + ConfigMap，无需外部 DB）

运行时真相源为进程内 `RwLock<State>`（**所有读零 API 开销**）；持久化层为 K8s `ConfigMap`（`neon-cp-state`，JSON 快照，KB 级，存于 etcd，随集群高可用），采用 **write-through + 去抖** 落盘。该范式与 Neon 上游 `control_plane` crate 的「内存 HashMap + `persist()` 落盘」完全一致（`control_plane/src/branch_mappings.rs:12`："Keep human-readable aliases in memory (and persist them to config XXX)"）。重启时先 load 快照，再与 K8s（`List` compute Deployment）和 SC API（tenant/timeline 真相）**双向对账**恢复：

- `project_id → tenant_id`
- `branch_id → (tenant_id, timeline_id)`
- `endpoint_id → (tenant_id, timeline_id, compute_pod_name, status)`

> **数据边界**：`tenant↔timeline` 的真相源是 storage-controller 的 PostgreSQL，启动时向 SC API 重新拉取，**不重复持久化**；ConfigMap 仅存上述人类可读别名 + endpoint 运行态，数据量稳定在 KB 级，远低于 ConfigMap ~1MiB 建议上限（etcd 单对象硬上限 ~1.5MiB）。

#### 用户 API（v2.json 风格，仅核心字段）

- `POST /projects` → 在 SC 创建租户 + 初始时间线；返回 `{project_id, tenant_id, ...}`
- `GET /projects`、`GET /projects/{project_id}`
- `POST /projects/{project_id}/branches`（body `{name, parent_branch_id?}`）→ 在对应 tenant 创建 timeline（branch）；返回 `{branch_id, timeline_id, ...}`
- `GET /projects/{project_id}/branches`
- `POST /projects/{project_id}/endpoints`（body `{branch_id, type:"read_write"|"read_only"}`）→ 见下"Endpoint 创建"
- `GET /projects/{project_id}/endpoints`
- `DELETE /projects/{project_id}/endpoints/{endpoint_id}` → 删除 endpoint（缩容 / 停 compute Pod）

#### Endpoint 创建的关键交互（核心难点）

创建 endpoint 时，control plane 必须构造 `ComputeSpec`（结构见 `libs/compute_api/src/spec.rs`）并让 compute 跑起来，步骤（对应 `control_plane/src/endpoint.rs::start()` 的真实逻辑）：

1. **定位 pageserver**：`GET <sc>/debug/v1/tenant/{tenant_id}/locate` → `TenantLocateResponse`（各 shard 的 pageserver pg/http/grpc 地址）→ 组装 `pageserver_connection_info`（`shard_count` + `stripe_size` + `shards` map，每个 shard 含 `pageservers:[{ id?, libpq_url, grpc_url }]`；**注意**：字段是 `libpq_url`（libpq 连接串）+ `grpc_url`（gRPC 管理地址），**没有** `host`/`port`/`http_host`/`http_port`，见 `libs/compute_api/src/spec.rs::PageserverShardConnectionInfo`）。
   > 已核对 `control_plane/src/storage_controller.rs::tenant_locate()`：真实路径是 **`debug/v1/tenant/{id}/locate`**（不是 `control/v1/tenant/{id}`）。该路径属 `debug` 前缀 → 需 **Admin** scope JWT。
2. **取 safekeeper 列表**：`GET <sc>/control/v1/safekeeper`（Active 节点）→ 连接串 `<host>:5454` 列表 → 填入 `safekeeper_connstrings`（仅 Primary 需要）。
3. **签发 storage JWT**：用 Ed25519 私钥签一个 scope 含 `PageServerApi`+`SafekeeperData`、且 `compute_id`=本 endpoint 的 token → 填入 `storage_auth_token`（compute 用它向 pageserver/safekeeper 认证；公钥已由各 chart 挂载到 pageserver/safekeeper）。

   > **已核对 `libs/utils/src/auth.rs:25` 的 `Scope` 枚举**：合法值为 `PageServerApi`(36)、`SafekeeperData`(40)、`Tenant`(246) 等，**没有 `SafekeeperApi`**。SC 的 `--safekeeper-jwt-token` 也是 `SafekeeperData` scope，故 compute→safekeeper 的 token 必须用 `SafekeeperData`，写错 scope 会被 safekeeper 拒收（401）。
4. **生成角色 SCRAM 密钥（关键一致性点）**：control plane 生成**一个** `SCRAM-SHA-256$<salt>:<iters>$<StoredKey>:<ServerKey>` 验证器字符串，把它**同时**写入：
   - ComputeSpec 的 `cluster.roles[].encrypted_password`（compute_ctl 在 `pg_helpers.rs` 中检测到 `SCRAM-SHA-256` 前缀即按 SCRAM 验证器写入 `PASSWORD`，见 `compute_tools/src/pg_helpers.rs:151`）；
   - 后续 `get_endpoint_access_control` 返回的 `role_secret`（proxy 用 `scram::ServerSecret::parse` 解析，二者必须完全一致，否则 proxy 鉴权失败）。
   - 默认角色 `cloud_admin` + 库 `postgres`。
5. **组装 ComputeSpec**：`format_version:1.0`、`tenant_id`、`timeline_id`、`mode`(Primary/Replica)、`pageserver_connection_info`、`safekeeper_connstrings`、`storage_auth_token`、`project_id`/`branch_id`/`endpoint_id`、可选 `endpoint_storage_addr/token`（`suspend_timeout_seconds` 仅本地用，置 -1 或省略）。
6. **拉起 compute（K8s Deployment，主路径）**：control plane 用 in-cluster K8s client 为 endpoint 创建一个 Deployment + Service：
   - Service `neon-compute-<endpoint_id>` 暴露 `5432`（postgres）+ 可选 `compute_ctl` 外部 http 端口（如 8081）。
   - Deployment 容器镜像 `computeImage`（含 postgres + compute_ctl），启动命令：
     `compute_ctl -C postgresql://cloud_admin@localhost/postgres --control-plane-uri http://neon-control-plane:<port> --compute-id <endpoint_id> --pgdata /var/db/postgres/data --pgbin /usr/lib/postgresql/<ver>/bin/postgres --external-http-port 3080`
     （`--connstr`/`-C` 必填；`-p/--control-plane-uri` 与 `-c/--config` 互斥，选前者时必带 `-i/--compute-id`；`--external-http-port` 默认 3080）
   - `compute_ctl` 周期性向 `GET <cp>/compute/api/v2/computes/<compute_id>/spec` 拉取第 5 步的 spec（路径源自 `compute_tools/src/spec.rs:78`：`{base_uri}/compute/api/v2/computes/{compute_id}/spec`，故 `--control-plane-uri` 传控制面**根地址**即可）。
   - **备选（静态回退）**：把 spec 写入 `neon-endpoint-storage`(S3) 或 ConfigMap，`neon-compute` chart 以 `--config /spec.json` 文件方式加载。v1 主路径采用 K8s Deployment 动态管理（最贴近真实控制面），`neon-compute` 作为无 K8s 写权限环境下的回退。

#### Proxy 兼容接口（proxy 路由必需，严格对齐 `proxy/src/control_plane/client/cplane_proxy_v1.rs` 与 `messages.rs`）

proxy 的 `NeonControlPlaneClient` 在每次连接时调用以下接口（proxy 配置的 `control_plane_api` 基址指向本服务的 Service；proxy 会在基址后追加下列路径，并带 `Authorization: Bearer <jwt>` 与 `session_id` 等查询参数）：

- `GET <base>/get_endpoint_access_control?endpointish=<endpoint>&role=<role>&session_id=...` → 响应 `GetEndpointAccessControl { role_secret, project_id, account_id, allowed_ips, allowed_vpc_endpoint_ids, block_public_connections, block_vpc_connections, rate_limits }`；`role_secret` 即第 4 步生成的 SCRAM 验证器。
- `GET <base>/wake_compute?endpointish=<endpoint>&session_id=...&application_name=...` → 响应 `WakeCompute { address:"<compute-svc>.<ns>.svc.cluster.local:5432", server_name:null, aux:{endpoint_id, project_id, branch_id, compute_id, cold_start_info} }`；proxy 据此把连接路由到对应 compute Service。（注意路径是 `wake_compute`，**不是** `proxy_wake_compute`；查询参数是 `endpointish`，**不是** `endpoint`。）
- `GET <base>/endpoints/<endpoint>/jwks?session_id=...` → 响应 `EndpointJwksResponse { jwks:[] }`（v1 返回空列表即可）。
- `get_role_access_control`：proxy 内部同样调用 `get_endpoint_access_control` 并取 `role_secret`，**无独立端点**。

> 实现时必须严格对齐 `messages.rs` 的 `WakeCompute` / `GetEndpointAccessControl` / `EndpointJwksResponse` 字段名与类型（如 `cold_start_info` 用 `snake_case`：`unknown`/`warm`/`warm_cached`；`address` 为 `host:port` 字符串）。proxy 对这些接口的路径/字段有强约定，错一个字段即鉴权或路由失败。

#### Bootstrap 流程（服务内一次性 init，幂等，源自 `neon_local` 实际调用）

按依赖顺序（与 `control_plane/src/storage_controller.rs` 一致）：

1. **注册每个 pageserver 节点**：`POST <sc>/control/v1/node`（body `NodeRegisterRequest { node_id, listen_pg_addr, listen_pg_port(64000), listen_http_addr, listen_http_port(9898), listen_grpc_addr/port?, availability_zone_id }`）
2. **注册每个 safekeeper 节点并设为 Active**：`POST <sc>/control/v1/safekeeper/{id}`（SafekeeperUpsert，body 见下）+ `POST <sc>/control/v1/safekeeper/{id}/scheduling_policy`（`SafekeeperSchedulingPolicyRequest { scheduling_policy:"Active" }`）
3. **创建默认租户**：`POST <sc>/v1/tenant`（**注意：是 `v1/tenant`，不是 `control/v1/tenant`**；body `TenantCreateRequest { new_tenant_id, shard_parameters(默认单分片 unsharded), config }`）
4. **创建初始时间线（branch）**：`POST <sc>/v1/tenant/{tid}/timeline`（由 SC 转发到对应 pageserver shard 0，body `TimelineCreateRequest { new_timeline_id }`）

> **已核对 `control_plane/src/storage_controller.rs`（neon_local 真实调用）**：
> - `node_register` → `POST control/v1/node`；`node_configure` → `PUT control/v1/node/{id}/config`；`node_list` → `GET control/v1/node`
> - `tenant_create` → **`POST v1/tenant`**（不是 control/v1）；`tenant_locate` → **`GET debug/v1/tenant/{id}/locate`**
> - `tenant_timeline_create` → `POST v1/tenant/{tid}/timeline`；`set_tenant_config` → `PUT v1/tenant/config`
> - `upsert_safekeeper` → `POST control/v1/safekeeper/{id}`；`safekeeper_scheduling_policy` → `POST control/v1/safekeeper/{id}/scheduling_policy`
> - SafekeeperUpsert body 字段（源自 `libs/pageserver_api/src/controller_api.rs::SafekeeperDescribeResponse`，即 upsert 体）：`{ id, region_id, host, port(pg_port), http_port, https_port, version, availability_zone_id }`（`scheduling_policy` 经独立端点 `POST control/v1/safekeeper/{id}/scheduling_policy` 设置，不在 upsert 体内）；**注意：没有 `created_at`/`updated_at`**（时间戳由 SC 内部维护，不在此请求体内）。

**JWT scope（关键，源自 `get_claims_for_path()`）**：control plane → SC 的调用需按**路径前缀**签发不同 scope 的 JWT（用同一 Ed25519 私钥）：
- `status` / `ready` → **无需** JWT
- `control/*` / `debug/*` → **Admin** scope（如所有 `control/v1/node`、`control/v1/safekeeper`、`debug/v1/tenant/.../locate`）
- `v1/*` → **PageServerApi** scope（如 `v1/tenant`、`v1/tenant/{tid}/timeline`、`v1/tenant/config`）
> 即：创建租户/时间线用 PageServerApi token，节点管理与 locate 用 Admin token。错用 scope 会被 SC 拒绝（403）。

**幂等性**：所有步骤先 GET 判断"已存在则跳过"，支持服务重启重跑。

#### 技术实现要点

- **语言/镜像（已确认）**：用 **Go** 实现一个轻量 HTTP 服务（`net/http` + 轻量路由即可，无需框架），自带镜像构建流水线（`Dockerfile` + `src/main.go`），chart 仅通过 `image` 引用镜像。需访问 K8s API（为每个 endpoint 创建 compute Deployment/Service）→ 配 RBAC ServiceAccount + ClusterRole（`pods`/`deployments`/`services` 的 `create`/`delete`/`get`/`list`/`watch`，限定在自身 namespace）。
- **JWT 复用**：control plane 持有与 pageserver/safekeeper 同一套 Ed25519 私钥（`global.jwtSecretName`，Secret 挂载到 Pod），用于签发 compute 的 `storage_auth_token`；公钥已由各 chart 挂载到 pageserver/safekeeper。proxy→control-plane 的调用可复用同一 JWT 或由 proxy 配置专用 JWT，control plane 侧做最小校验（v1 可宽松放行，后续用 JWT 鉴权）。
- **SCRAM 角色密钥自管**：control plane 在创建 endpoint 时生成 SCRAM-SHA-256 验证器（见上"Endpoint 创建"第 4 步），既写入 ComputeSpec 又作为 `role_secret` 返回，保证 proxy 与 compute 密码一致。角色名默认 `cloud_admin`、库 `postgres`。
- **不实现完整 Console**：不做用户/组织/计费/鉴权体系；用户 API 仅保留 project/branch/endpoint 几个核心，字段做最小集（对齐 v2.json 的 `projects`/`branches`/`endpoints` 资源形状，但只实现 create/list/get/delete）。
- **state 持久化（最优方案 A+B：in-memory + ConfigMap）**：运行时真相源为内存 `RwLock<State>`（承接所有读/复杂查询，零 etcd 开销），持久化层为 K8s `ConfigMap`（`neon-cp-state`）JSON 快照，采用 **write-through + 去抖**（如 500ms~2s 合并刷新，规避 ConfigMap 整对象重写放大）落盘。要点：
  - **单写者约束**：ConfigMap 无事务，默认单副本（`replicas: 1` + `Recreate` 策略）；需 HA 时加 **Lease 选主**，仅 leader 持有写权与内存真相，follower 待命（不建议多副本同时读写）。
  - **防重复拉起 compute 的核心不靠持久化，而靠**：(1) compute 用确定性命名 `neon-compute-<endpoint_id>` + server-side apply 幂等创建（已存在即 no-op）；(2) **启动双向对账**——load 快照后 `List` 现有 compute Deployment（以 K8s 实际状态为准修正 `endpoint.status`/`compute_pod_name`）并向 SC API 拉取 `tenant/timeline` 真相，即使 ConfigMap 丢失也可全量重建。
  - **消除单方案缺点**：相对「单独 A（in-memory + PVC，需额外 PVC、默认 RWO 与无状态扩容冲突、崩溃一致性弱）」省去 PVC；相对「单独 B（每次读写打 etcd、无事务）」所有读走内存、ConfigMap 仅作低频快照。该方案与 Neon 上游范式一致且**不引入 PostgreSQL 依赖**。

### 1.5 SC → control plane 反向回调（upcall / compute_hook，可选但重要）

**机制（已核对 `storage_controller/src/compute_hook.rs`、`controller_upcall_client.rs`、`pageserver_api/src/upcall_api.rs`）**：需区分**两类 upcall**，结论完全不同：

**(A) pageserver → SC 的 upcall（SC 已原生实现，配置即可，零开发）**
- SC 在 `http.rs` 暴露 `/upcall/v1/re-attach`、`/upcall/v1/validate`、`/upcall/v1/timeline_import_status`。
- pageserver 启动时 `POST /upcall/v1/re-attach` 向 SC 上报节点信息并接受租户 attach 指令；路径由 `controller_upcall_client.rs:163` 的 `base_url().join("re-attach")` 拼接，故 pageserver.toml 的 `control_plane_api` **必须配成 `http://<sc>:<port>/upcall/v1`**（带前缀）。
- 结论：本方案直接配置即可启用，SC 侧无需写代码。

**(B) SC → control plane 的 compute_hook（需 control plane 实现端点，当前未做）**
- SC 在租户 shard 调度变更后，通过 `ComputeHook` 向 `control_plane_url` 发送两类通知（`do_notify_iteration` 使用 **PUT**）：
  - `PUT {control_plane_url}/notify-attach`：让运行中 compute **热更新其 pageserver 映射**（shard→pageserver 路由）。
  - `PUT {control_plane_url}/notify-safekeepers`：让 compute **热更新其 safekeeper 集合**（body：`NotifySafekeepersRequest { tenant_id, timeline_id, generation, safekeepers: Vec<SafekeeperInfo> }`，见 `compute_hook.rs:353`）。
  - 鉴权：`Authorization: Bearer <control_plane_jwt_token>`（ControlPlane scope）。
  - 用途：让运行中 compute **不重建 Pod** 即热更新存储路由，使存储层故障转移对客户端透明（HA 关键机制）。
- SC 两种模式（关键坑点）：
  - **远程模式** `--control-plane-url`：发往 control plane（console）端点，由其转发给 compute。✅ **这是 K8s 唯一可行的路径**。
  - **本地模式** `--use-local-compute-notifications`：依赖 neon_local 的 `LocalEnv`/本地 repo 配置（`neon_local_repo_dir`），仅适用于 neon_local 单机开发；且**严格模式下该参数被直接禁止**（SC 启动即 `anyhow::bail!("`--use-local-compute-notifications` is only permitted in `--dev` mode")`，见 `storage_controller/src/main.rs:395`），`--dev` 才允许。本方案 K8s 场景既不沿用本地模式、也不能省略远程模式——远程模式 (`--control-plane-url`) 在严格模式下为**必填**。

**当前能否实现？**
- (A) pageserver→SC upcall：**能**，SC 原生，配好 `control_plane_api=/upcall/v1` 即可。
- (B) SC→control plane compute_hook：**接收端点当前未实现**。注意严格模式下 SC **必须**配置 `--control-plane-url`（否则启动即 `bail`），故"SC 不设 `--control_plane_url`"在 K8s 下不成立——umbrella 必须始终透传 `--control-plane-url=http://neon-control-plane:<port>`。compute notification 的语义变为：SC 会向该地址发起 `PUT /notify-attach`、`PUT /notify-safekeepers`，但 phase-1 控制面**未实现这两个端点**（请求失败/404），SC 进入重试，**不影响 SC 自身启动与基本链路**；compute 经重连 + `compute_ctl reconfigure()` 自愈。

**实现 compute_hook（远程模式）需要做哪些工作？**
1. **control plane 新增两个 HTTP 端点（PUT）**：`/notify-attach`、`/notify-safekeepers`；校验 `Authorization: Bearer <token>` 且 scope=ControlPlane（与 SC `--control-plane-jwt-token` 同源私钥签发，control plane 用同一公钥验证）；body 反序列化复用 `pageserver_api::upcall_api` / `compute_api` 的对应结构（如 `NotifyReAttachRequest`、`NotifySafekeepersRequest`）以保持二进制兼容。
2. **维护 running compute 路由表（核心难点）**：control plane 需记录 `tenant_id → running compute 的 compute_ctl 地址（默认 :3080）`。动态拉起时由 endpoint 创建流程记录 Pod IP；静态 `neon-compute` 经 values 配置固定映射；随 compute 生命周期增删。
3. **转发逻辑（迷你 console）**：收到 notify 后，把"pageserver/safekeeper 路由变化"转成 compute 能理解的 `ConfigurationRequest`（ComputeSpec 更新），`POST http://<compute>:3080/configure`（compute_ctl 的 `configure` 端点，已有 `reconfigure()` 自愈）。这要求 control plane 持有并能在通知时重建该 tenant 的 ComputeSpec 模板——逻辑量与 Neon console 一致。
4. **compute 侧可达性**：compute 的 `:3080` 需对 control plane 可达（Service 暴露）；动态 compute 由 control plane 直连 Pod IP。
5. **SC 配置变更**：storage_controller chart 移除 `--use-local-compute-notifications`，改为 `--control-plane-url=http://<neon-control-plane>:<port>` + `--control-plane-jwt-token`。此时 SC 进入远程通知模式（strict 模式允许且强制要求 control_plane_url，同时禁止 local）。
6. **失败语义对齐**：端点需正确处理 SC 的 `NotifyError` 映射（如返回 423 LOCKED 让 SC 视为 Busy 继续 reconcile；429 触发 SlowDown 退避）。

**工作量评估**：相当于在轻量控制面内实现"迷你 Neon console 的 compute 通知转发器"，中等偏大量，且要求 control plane 持久化/维护 compute 路由与 ComputeSpec 模板，超出"轻量控制面 + Helm 部署"原始定位。

**本次部署决策**：
- SC **始终**以严格模式运行并配置 `--control-plane-url=http://neon-control-plane:<port>`（必填，否则启动即 `bail`）。compute notification 的**接收端点**（`/notify-attach`、`/notify-safekeepers`）列为 **phase-2**：phase-1 未实现，SC 通知请求失败时仅重试、非致命，pageserver→SC upcall 正常启用。
- 单 pageserver（replicas=1、未启用 `pageserverAutoMigration`）+ 静态 `neon-compute` 下，存储故障转移极少触发；即便触发，compute 经重连 + `compute_ctl reconfigure()` 自愈通常可恢复，不影响基本可用。
- **后续扩展点**：上生产多 pageserver + 自动迁移时，再实现 1.5 节上述 1–6 启用远程通知端点。本次不实现端点，但始终保持 `--control-plane-url` 已配置（避免回头改启动参数引发 SC 重启）。

### 1.6 Proxy 运行条件与正确性校验（必读，决定 proxy 能否真正可用）

**phase-2 说明**：proxy 在 phase-1 默认不启用；本节所列条件**仅在使用 proxy 时才需落实**（对应「分阶段实施」的 phase-2）。

**结论先行**：本方案 `neon-control-plane` 的 proxy 兼容接口设计（wake_compute / get_endpoint_access_control / endpoints/{id}/jwks）**路径与字段与 `cplane_proxy_v1` 完全对得上**（已逐行核对 `proxy/src/control_plane/client/cplane_proxy_v1.rs`），**设计上可以让 proxy 正常工作**。但「当前（按现有 chart 默认值）」proxy **还跑不起来**，必须在 umbrella 层补齐以下前置条件，否则 proxy 要么选错 backend、要么 TLS/SNI 不通。

**1) proxy 的 backend 选择机制（已核对 `proxy/src/binary/proxy.rs:779` `build_auth_backend`）**
- `--auth-backend ControlPlane`（别名 `cplane-v1`）→ 代码内 `AuthBackendType::ControlPlane` → 构造 `ControlPlaneClient::ProxyV1(NeonControlPlaneClient)`。
  > **已核对 `proxy/src/binary/proxy.rs:63-77`**：`AuthBackendType` 的合法值仅 `ControlPlane`(别名 `cplane-v1`)、`ConsoleRedirect`(别名 `link`，**默认**)，以及仅 `testing` 特性可用的 `Postgres`/`Local`。**`console` 不是合法枚举值**——若 chart 传 `--auth-backend console`，proxy 会在 clap 解析阶段直接启动失败（报 unknown variant）。故对接我们的 control plane 必须写 `ControlPlane`（或 `cplane-v1`）。
- 该 client 的 **base URL 取自 `--auth-endpoint`（`-a`）参数**，即 `args.auth_endpoint`；JWT 取自 `args.control_plane_token`（环境变量 `NEON_PROXY_TO_CONTROLPLANE_TOKEN`）。
- **关键**：现有 `neon-proxy` chart 默认 `settings.authBackend: "link"`（发登录链接，仅适配浏览器/HTTP，**不适配 psql**），且 `settings.authEndpoint` 为空。chart 的 deployment 模板把 `authEndpoint` 渲染成 `-a` 参数——**也就是说 `settings.authEndpoint` 就是 control-plane 基址**（与 chart 注释里「legacy console authenticate_proxy_request」的旧含义不同，当前代码已把它用作 cplane_proxy_v1 基址）。
- **必要条件 A**：umbrella 必须覆盖 `neon-proxy.settings.authBackend: "ControlPlane"`（**切勿写 `console`，那是非法值，proxy 启动即崩**）+ `neon-proxy.settings.authEndpoint: "http://neon-control-plane:<port>"` + `neon-proxy.settings.controlplane_token`（chart 已把 `controlplane_token` 经 Secret `...-controlplane-token` 注入为 `NEON_PROXY_TO_CONTROLPLANE_TOKEN`）。三者缺一，proxy 不会去查我们的 control plane。

**2) TLS 是最大实践障碍（两处）**
- **客户端 → proxy**：Neon proxy 默认要求 TLS。仅当 `settings.domain` 非空时才挂 `--tls-key/--tls-cert`；没有证书则客户端必须 `sslmode=disable` 且 proxy 需允许非 TLS（标准镜像不允许，除非构建时带 `testing` 特性 + `--disable-pg-session-jwt`）。自托管最小部署二选一：
  - 用 `settings.useCertManager: true` + `settings.domain: "neon.local"` 由 cert-manager 签发 `*.neon.local` 证书，客户端 `sslmode=require`；
  - 或构建带 `testing` 特性的 proxy 镜像，加 `--disable-pg-session-jwt`，客户端 `sslmode=disable`。
- **proxy → compute**：proxy 以 TLS 连接 compute（postgres 5432），用 `NEON_INTERNAL_CA_FILE`（`settings.internalCa` / `internalCa`）校验 compute 证书。compute_ctl 默认生成自签证书，proxy 必须信任其 CA；自托管需共享内部 CA（把同一 CA 注入 proxy 的 `internalCa` 与 compute 的证书链），或在最小部署中放宽。
- **必要条件 B**：在 umbrella/values 中明确 TLS 方案（cert-manager 或 testing 镜像 + 内部 CA），否则 proxy 与 compute 之间握手失败。

**3) SNI / 主机名路由（决定 proxy 能否解析出 endpoint_id）**
- proxy 从客户端连接的服务名（SNI 首标签）提取 `endpoint_id`，再拼成 `endpointish` 去查 `wake_compute`。即客户端必须以 `<endpoint_id>.<domain>` 连接（如 `ep-abc123.neon.local`）。
- 需要 DNS 或 `/etc/hosts` 把 `*.neon.local` 解析到 proxy Service；`settings.domain` 与连接串必须一致。`pgSniRouter` 是另一套 TLS SNI 直连 compute 特性，**非必需**，基础链路用普通 proxy Service 即可。
- **必要条件 C**：文档化连接串（`psql "postgresql://cloud_admin:<pw>@<endpoint_id>.neon.local:5432/postgres?sslmode=require"`），并打通 DNS/hosts。

**4) 字段兼容性（已对齐，列此防回归）**
- `wake_compute` 响应 `WakeCompute.address` 必须是 `host:port`（如 `neon-compute-ep-xxx.neon.svc.cluster.local:5432`），`server_name` 置 `null`，`aux.cold_start_info` 用合法枚举值（`unknown`/`warm`/`warm_cached`）。
- `get_endpoint_access_control.role_secret` 必须与该 endpoint 的 compute 角色 SCRAM 验证器**逐字节一致**（compute 写入 `pg_auth`、proxy 用 `scram::ServerSecret::parse` 校验）。
- 三个接口均带 `Authorization: Bearer <controlplane_token>`，control plane 侧按 v1 宽松放行或校验该 JWT。

**综合判定**：proxy 能否「正常使用」= 必要条件 A（backend+endpoint）+ B（TLS 两处）+ C（SNI/DNS）三者同时成立。当前 plan 已正确设计 control plane 接口，且上述覆盖项与 TLS 处理已在本节与「实现要点」中明确，**实现阶段必须严格照此设置**（尤其 `authBackend` 必须用 `ControlPlane` 而非 `console`）。

### 性能与可靠性

- 有状态组件使用本地 SSD StorageClass + 合理的 resources（requests=limits 保证 Guaranteed QoS，避免被驱逐影响数据面）。
- safekeeper 至少 3 副本（奇数，满足 WAL 多数派），pageserver 副本数按租户分片需求（默认 1~3）；使用 podAntiAffinity 跨节点/跨区分布。
- 探针：startupProbe（容忍慢启动）+ livenessProbe + readinessProbe，端口复用各自 /metrics 或管理端口。
- PDB 保证滚动维护时不少于 (replicas-1) 可用。

## 实现要点（防回归）

- 严格复用现有模板范式：`templates/_helpers.tpl` 的 fullname/label/selectorLabels 命名约定、values 结构（image/service/resources/metrics/ingress/securityContext 等），避免引入新范式。
- 新增 chart 的 values.yaml 字段命名与 storage-controller 的 settings 保持一致（如 jwtToken、brokerEndpoint、controlPlaneUrl），减少 umbrella 透传复杂度。
- 密钥不落 values 明文，统一走 Secret（可通过 `.Values.existingSecret` 引用）；新增代码注释使用中文（遵循 AGENTS.md）。
- 保持对现有独立 chart 的向后兼容：新增 umbrella 不改动子 chart 原有默认行为，仅通过 values 覆盖增强。
- 监控复用现有 `metrics.serviceMonitor.enabled` 开关，新增 umbrella 层统一开启，避免重复定义 ServiceMonitor。监控暂时先关闭，下一期开发再考虑监控。
- **proxy 可用性三件套（见 1.6，缺失则 proxy 跑不起来）**：umbrella 必须覆盖 `neon-proxy` 的 `settings.authBackend="ControlPlane"`（**注意：不是 `console`；`console` 是非法枚举值，合法值为 `ControlPlane`/`cplane-v1`，见 1.6 枚举核对**）、`settings.authEndpoint="http://neon-control-plane:<port>"`、`settings.controlplane_token`（经 Secret 注入）；并在 values 中明确 TLS 方案（`useCertManager:true`+`domain: "neon.local"` 由 cert-manager 签发 `*.neon.local`，或构建 `testing` 特性镜像 + `--disable-pg-session-jwt`），同时把 proxy→compute 的内部 CA（`internalCa`）与 compute 证书链对齐；文档化连接串 `<endpoint_id>.<domain>:5432` 并打通 DNS/hosts。注意 `settings.authEndpoint` 即 proxy 的 control-plane 基址（当前代码已将其用作 `cplane_proxy_v1` URL），与 chart 注释中「legacy console」的旧含义不同。

## 架构设计

```mermaid
graph TD
  U[umbrella: charts/neon] -->|依赖编排| SB[neon-storage-broker]
  U -->|依赖编排| SC[neon-storage-controller]
  U -.->|phase-2 可选: 依赖编排| PX[neon-proxy ⚡phase-2]
  U -->|依赖编排| PS[neon-pageserver 有状态]
  U -->|依赖编排| SK[neon-safekeeper 有状态]
  U -->|依赖编排| CP[neon-control-plane 服务]
  U -->|依赖编排| CM[neon-compute 静态回退]
  CP -->|bootstrap: 注册节点/建租户| SC
  CP -->|创建tenant/时间线/定位| SC
  CP -->|创建compute Deployment| K8S[(K8s API)]
  K8S -->|拉起| CM2[compute Pod: compute_ctl]
  CP -.->|phase-2: console兼容接口 鉴权/寻址| PX
  PX -.->|phase-2 路由连接| CM2
  PX -.->|phase-2 路由连接| CM
  SC -->|心跳/调度| PS
  SC -->|心跳/调度| SK
  SC -->|节点/租户/分片元数据持久化| PG[(外部 PostgreSQL 元数据)]
  PS -->|WAL 上传| EXT[(外部 MinIO 对象存储)]
  SK -->|归档| EXT
  CM2 -->|WAL 流| SK
  SK -->|WAL 流| PS
  CM -->|WAL 流| SK
```

数据流（**phase-1，无 proxy**）：client → compute(postgres)（**直连 compute Service** `neon-compute-<endpoint_id>.<ns>.svc.cluster.local:5432`，用 control plane 生成的 SCRAM 密码）→ safekeeper(WAL 多数派) → pageserver(物化层) → 外部 MinIO(对象存储)。`neon-control-plane` 在启动时经 storage-controller 完成节点注册与默认租户/时间线创建；创建 endpoint 时经 storage-controller 定位 pageserver、取 safekeeper 列表并生成 `ComputeSpec`，动态拉起 compute Pod。

### 分阶段实施（phase-1 先跑通，phase-2 再叠加 proxy）

- **phase-1（本期实现，默认部署）**：`neon-storage-broker` + `neon-storage-controller` + `neon-pageserver` + `neon-safekeeper` + `neon-minio` + `neon-control-plane` + `neon-compute` + umbrella。client **直连 compute Service** 验证端到端读写，proxy 不部署。
- **phase-2（后续分步实现，默认不启用）**：启用 `neon-proxy`（umbrella `neon-proxy.enabled=true`）+ 控制面实现 proxy 兼容接口（`wake_compute` / `get_endpoint_access_control` / `endpoints/{id}/jwks`）+ 补齐 TLS/SNI/内部 CA（见 1.6 节）。启用后 client 改为 `<endpoint_id>.<domain>:5432` 经 proxy 路由。

> 控制面 chart 可预先把 proxy 兼容接口的占位/桩留在 `main.go`，但 phase-1 不要求联调；proxy 的三大前置条件（backend=`ControlPlane`、TLS 两处、SNI/DNS）仅在 phase-2 才需落实（详见 1.6 节）。

**phase-2 启用 proxy 后**的数据流：client → proxy → compute，proxy 通过 control plane 的 console 兼容接口完成鉴权与 compute 寻址。

**外部 PostgreSQL（仅 storage-controller 元数据后端，控制面不依赖）**：storage-controller 是控制面调度核心，但其全部元数据（节点注册、租户/分片位置、调度状态、attachment 服务状态）**不自带存储**，必须持久化到**外部 PostgreSQL**（即其必填启动参数 `--database-url`）。因此部署前需先准备一个可用的 PostgreSQL 实例/集群，并满足：
- 数据库由 `storage-controller-pg-cluster` Secret 提供连接串（`postgres://<user>:<pass>@<host>:5432/storage_controller` 形式），storage-controller 启动时自动建表/迁移（内置 migration，无 neon 定制扩展依赖，见 `storage_controller/src/persistence.rs`）。
- 该 PostgreSQL **必须先于** storage-controller 就绪（umbrella 中将其列为前置依赖，或在 values 中指定 `externalPostgres.existingSecret` 指向既有实例）。
- **控制面（`neon-control-plane`）不使用 PostgreSQL**：其状态采用「in-memory + ConfigMap」持久化（见 1.4 节「服务内部状态」），无需任何外部 DB 依赖。若未来演进为功能完整的 console 需要关系型存储，再考虑在**同一 PostgreSQL 实例上建独立 `control_plane` 库（同实例异库，绝不混用 `storage_controller` 库/表）**——以隔离 SC 的 migration 主权、避免 `__diesel_schema_migrations` 冲突，并隔离故障域（重置控制面可 `DROP DATABASE control_plane` 而不误伤 SC）。
- 职责区分：外部 PostgreSQL **仅服务 storage-controller**；实际页面层/WAL 数据落在外部 MinIO（高吞吐对象存储）。两者不可混淆，MinIO 不能替代 PostgreSQL。
- 生产环境建议 PostgreSQL 启用主从/HA 与定期备份；若集群已有 PostgreSQL Operator（如 CloudNativePG），可直接复用其 Service 地址。

## 目录结构

本次为现有仓库扩展，仅列出新增/修改文件：

```
charts/
├── neon/                                  # [NEW] umbrella 总 chart
│   ├── Chart.yaml                         # [NEW] 声明所有子 chart 依赖与全局版本（neon-proxy 以 condition 默认 false 不启用）
│   ├── values.yaml                        # [NEW] 全局 values（global.objectStorage/region/jwtSecretName 及子 chart 透传）
│   └── charts/                            # [NEW] 内联子 chart 依赖（或依赖外部 repo）
├── neon-pageserver/                       # [NEW] pageserver 有状态 chart
│   ├── Chart.yaml                         # [NEW] apiVersion v2，appVersion 对齐 neon 镜像
│   ├── values.yaml                        # [NEW] image/StatefulSet/replicas/StorageClass/PVC/resources/metrics/settings(brokerEndpoint, jwt, storage)
│   ├── templates/
│   │   ├── _helpers.tpl                   # [NEW] 复用命名约定
│   │   ├── statefulset.yaml               # [NEW] StatefulSet+volumeClaimTemplates+probe+PDB+config 挂载
│   │   ├── service.yaml                   # [NEW] Headless + ClusterIP
│   │   ├── configmap.yaml                 # [NEW] 渲染 pageserver.toml（存储后端/端口/JWT/broker）
│   │   ├── pod-disruption-budget.yaml     # [NEW] PDB
│   │   ├── serviceaccount.yaml            # [NEW] RBAC/SA
│   │   └── servicemonitor.yaml            # [NEW] 可选 ServiceMonitor
├── neon-safekeeper/                       # [NEW] safekeeper 有状态 chart（结构同 pageserver）
│   ├── Chart.yaml
│   ├── values.yaml                        # [NEW] 含 replicas>=3、data-dir、brokerEndpoint、JWT
│   └── templates/
│       ├── _helpers.tpl
│       ├── statefulset.yaml               # [NEW] StatefulSet+PVC+clap 参数渲染
│       ├── service.yaml
│       ├── configmap.yaml                 # [NEW] safekeeper 配置片段
│       ├── pod-disruption-budget.yaml
│       ├── serviceaccount.yaml
│       └── servicemonitor.yaml
├── neon-control-plane/                    # [NEW] 最小 control plane 服务 chart
│   ├── Chart.yaml
│   ├── values.yaml                        # [NEW] 含 scUrl/jwtSecretName/tenant/defaultTenant、节点清单、computeImage、proxyApi 基址(phase-2)、RBAC 开关
│   ├── src/                               # [NEW] Go 服务源码（独立构建镜像）
│   │   ├── Dockerfile                     # [NEW] 多阶段构建：golang:alpine 编译 → 轻量运行时
│   │   ├── go.mod
│   │   └── main.go                        # [NEW] 用户 API + proxy 兼容 API + bootstrap + compute K8s 编排
│   └── templates/
│       ├── _helpers.tpl
│       ├── deployment.yaml                # [NEW][核心] 常驻控制面服务（启动幂等 bootstrap init）
│       ├── service.yaml                   # [NEW] 用户 API + proxy 兼容 API（同端口，按路径区分）
│       ├── configmap.yaml                 # [NEW] 服务配置（SC 地址、节点清单、JWT 引用、ComputeSpec 模板）
│       ├── serviceaccount.yaml            # [NEW] RBAC/SA + ClusterRole（pods/deployments/services 的增删查，限本 ns）
│       └── servicemonitor.yaml            # [NEW] 服务指标
├── neon-compute/                          # [NEW] 静态/演示 compute 回退 chart（--config 文件方式加载 spec，无 K8s 写权限环境用）
│   ├── Chart.yaml
│   ├── values.yaml                        # [NEW] 含 postgres 版本、computeImage、连接 safekeeper/proxy 示例、ComputeSpec 来源（S3/ConfigMap）
│   └── templates/
│       ├── _helpers.tpl
│       ├── deployment.yaml                # [NEW] 示例 postgres compute（compute_ctl --config /spec.json）
│       └── service.yaml
└── (复用不改) neon-storage-broker / neon-storage-controller  # 通过 umbrella 依赖引用；neon-proxy 复用但列为 phase-2（umbrella condition 默认 false，不启用）
```

## 关键代码结构（配置契约）

以下为各组件配置与端口契约（**已逐项核对 neon 源码 `libs/pageserver_api/src/config.rs`、`pageserver/src/config.rs`、`safekeeper/src/bin/safekeeper.rs`、`storage_controller/src/main.rs`**）。

### pageserver（仅 `-D/--workdir` + TOML，无 CLI 覆盖参数）

> 关键：pageserver.toml **所有字段平铺在根级**，没有 `[broker_endpoint]`/`[storage]`/`[http]`/`[auth]` 嵌套 section 表（未知字段被静默忽略）；node id 来自 `identity.toml` 而非命令行。

```toml
# pageserver.toml 渲染到 workdir（如 /home/postgres/pageserver/pageserver.toml）
listen_pg_addr   = "0.0.0.0:64000"                 # pg/libpq 端口（compute 连接）
listen_http_addr = "0.0.0.0:9898"                  # HTTP 管理 API（SC 调用 / 探针 /metrics）
broker_endpoint  = "http://<storage-broker>:50051" # storage_broker（WAL 流节点发现）

# 对象存储后端：内联表，字段名已核对 → bucket_region（非 region）
remote_storage = { bucket_name = "<neon-bucket>", bucket_region = "<region>",
                   endpoint = "<s3-or-minio-endpoint>", prefix_in_bucket = "pageserver" }

# storage_controller upcall 基址（**运行时强制必填**，parse_and_validate 校验，缺失即报错）
# 注意：pageserver 在 control_plane_api 后 .join("re-attach") 拼接路径，故必须带 /upcall/v1 前缀，否则路径错误
control_plane_api       = "http://<storage-controller>:<port>/upcall/v1"
control_plane_api_token = "<SC→PS 的 PageServerApi JWT>"   # 建议经 Secret 挂载，勿明文

# JWT 校验公钥（由各 chart 统一挂载；不是 [auth].jwt_token）
auth_validation_public_key_path = "/etc/neon/jwt/public_key.pem"
http_auth_type = "NeonJWT"
pg_auth_type   = "NeonJWT"
availability_zone = "az1"
```

- **`identity.toml`**（同 workdir，每 pod 唯一）：`id = <NodeId>`（整数），是 node id 唯一真实来源，须与 SC `POST control/v1/node` 的 `node_id` 对应；建议 `id = base + statefulsetOrdinal`。
- **环境变量**：`HADRON_NODE_IP_ADDRESS=<PodIP>`（Downward API `status.podIP`）——pageserver 启动 `POST /re-attach` 时据此向 SC 上报本节点可达地址；pageserver→safekeeper 的 JWT 经 `NEON_AUTH_TOKEN` 注入（非 TOML 字段）。

### safekeeper（clap 参数，无 TOML 配置文件）

> 关键：safekeeper **没有** `--listen-grpc`（gRPC 已废弃）、**没有** `--control-plane-api`（safekeeper 通过 broker 广播被 SC/pageserver/compute 发现，SC 经 control plane 注册的 host/port 直接 HTTP 调用它）。配置全部走 CLI 参数 + `safekeeper.id` 文件。

```text
safekeeper -D /var/lib/safekeeper \
  --id <NodeId> \
  --listen-pg 0.0.0.0:5454 \
  --listen-http 0.0.0.0:7676 \
  --broker-endpoint http://<storage-broker>:50051 \
  --pg-auth-public-key-path /etc/neon/jwt/public_key.pem \
  --availability-zone az1
# --id 写入 safekeeper.id；每 pod 唯一（base + ordinal）
# --pg-auth-public-key-path：WAL 服务（pageserver/compute 连 safekeeper）JWT 公钥
```

### storage_controller（必填 `--listen` 与 `--database-url`，**不连接 storage_broker**）

```text
storage_controller \
  --listen 0.0.0.0:8080 \
  --database-url postgres://<user>:<pass>@<pg-host>:5432/storage_controller \
  --public-key <Ed25519 公钥 PEM> \
  --jwt-token <PageServerApi> \            # SC→pageserver
  --safekeeper-jwt-token <SafekeeperData> \# SC→safekeeper
  --peer-jwt-token <Admin> \               # SC→peer SC（多副本）
  --control-plane-jwt-token <ControlPlane> \# SC→control plane（upcall 鉴权）
  --control-plane-url http://<control-plane>:<port> \  # 故障转移 upcall；严格模式（默认）下必填，缺失 SC 启动即 bail（见 storage_controller/src/main.rs:388）
  # --use-local-compute-notifications  # 严格模式下被禁止（SC 启动即 bail，见 storage_controller/src/main.rs:395），切勿使用；K8s 只能走 --control-plane-url 远程模式
  # --timeline-safekeeper-count 3      # 严格模式下取值必须 >=3（见 storage_controller/src/main.rs:398），故 safekeeper 副本数必须 >=3（与本文「safekeeper 至少 3 副本」一致）
```
> SC 通过 control plane 调用 `POST control/v1/safekeeper/{id}` 获知 safekeeper 的 host/port，**自身不订阅 broker**；broker 仅服务 safekeeper↔pageserver 的 WAL 流发现。

### 组件端口与心跳汇总

| 组件 | 端口 | 用途 |
|---|---|---|
| storage-broker | 50051 (gRPC/HTTP2) | 节点发现 pub-sub（无状态，无需 PVC） |
| pageserver | 64000(pg) / 9898(http) / 9899(https) / 51051(grpc 实验) | compute 连接 / SC 管理 / 探针 |
| safekeeper | 5454(pg WAL) / 7676(http) | WAL 流 / 管理 |
| storage-controller | 8080(http, `--listen`) | 控制面 REST / 节点注册 |
| compute | 5432(postgres) / 3080(compute_ctl external http) | 客户端 / 状态 |

**心跳 / 重连机制（已核对 `storage_controller/src/heartbeater.rs` 与 `pageserver/src/controller_upcall_client.rs`）**：
- **SC → pageserver**：周期 `GET /v1/utilization`（9898）判定 `Available`/`WarmingUp`/`Offline`；调度用 `PUT /v1/tenant/:id/location_config`（attach）。
- **SC → safekeeper**：类似周期健康检查（7676 端口）。
- **pageserver → SC**：启动 `POST /re-attach`（上报 node 信息 + `empty_local_disk`），并周期 `POST /validate`；失败无限重试。
- **safekeeper → SC**：不主动调用；通过 broker 广播 `safekeeper_info`，由 control plane 注册进 SC。

以下为 `neon-control-plane` 服务（常驻 Deployment，自带镜像）的关键契约：

**1) Bootstrap / 用户 API → storage-controller 的 REST 调用**（字段见 `libs/pageserver_api/src/controller_api.rs`）：

```text
# 启动幂等 init（路径与 scope 已按 storage_controller.rs 核对）
POST <sc>/control/v1/node            # NodeRegisterRequest（pageserver）           [Admin]
POST <sc>/control/v1/safekeeper/{id} # SafekeeperUpsert                            [Admin]
POST <sc>/control/v1/safekeeper/{id}/scheduling_policy  # {scheduling_policy:"Active"} [Admin]
POST <sc>/v1/tenant                  # TenantCreateRequest（默认单分片）           [PageServerApi]
POST <sc>/v1/tenant/{tid}/timeline   # TimelineCreateRequest（初始 branch）        [PageServerApi]

# 用户 API 内部调用
POST /projects            → POST <sc>/v1/tenant + POST <sc>/v1/tenant/{tid}/timeline   [PageServerApi]
POST /projects/{id}/branches   → POST <sc>/v1/tenant/{tid}/timeline（parent=祖先 timeline） [PageServerApi]
POST /projects/{id}/endpoints   → GET <sc>/debug/v1/tenant/{tid}/locate [Admin] + GET <sc>/control/v1/safekeeper [Admin] → 生成 ComputeSpec
```
> 路径与 JWT scope 依据 `control_plane/src/storage_controller.rs`：`v1/*`=PageServerApi、`control|debug/*`=Admin、`status|ready`=无。**易错点**：`tenant_create` 是 `v1/tenant`、`tenant_locate` 是 `debug/v1/tenant/{id}/locate`。

**1.5) SC → control plane 的反向回调（upcall / compute_hook，严格模式下必配 `--control-plane-url`）**

storage_controller 启动**必须**配置 `--control-plane-url <cp>`（严格模式默认，缺失即 `bail`，见 `storage_controller/src/main.rs:388`；对应 neon_local 的 `control_plane_hooks_api`）。当 SC 因故障/调度把某租户 shard 迁移到新 pageserver 时，会通过 `compute_hook` **主动回调** control plane，通知 compute 重新配置 `pageserver_connection_info`（否则 compute 仍连旧 pageserver）。
- 若 `neon-control-plane` 要支持 pageserver 故障转移后 compute 自动重连，需实现该 upcall 接收端点（`PUT /notify-attach`、`PUT /notify-safekeepers`，body 复用 `pageserver_api::upcall_api` 的 `NotifyReAttachRequest`/`NotifySafekeepersRequest`）；收到后更新对应 endpoint 的 ComputeSpec 并触发 compute_ctl 重载。
- phase-1 控制面**未实现该端点**，但因 `--control-plane-url` 在严格模式下为必填，SC 仍会向其发起通知请求；请求失败时 SC 进入重试、不影响自身启动与基本链路。故障转移后 compute 经重连 + `compute_ctl reconfigure()` 自愈；多 pageserver/自动迁移等生产场景再补实现（见 1.5 节实现清单 1–6）。

**2) Endpoint 创建的 ComputeSpec 关键字段**（结构见 `libs/compute_api/src/spec.rs`，默认值参考 `control_plane/src/endpoint.rs::start()`）：

```json
{
  "format_version": 1.0,
  "tenant_id": "<tid>",
  "timeline_id": "<timeline_id>",
  "mode": "Primary",
  "pageserver_connection_info": {
    "shard_count": 0,
    "stripe_size": 0,
    "shards": { "0": { "pageservers": [
      { "id": 0, "libpq_url": "postgresql://<ps-svc>:64000", "grpc_url": "http://<ps-svc>:51051" }
    ]}}
  },
  "safekeeper_connstrings": ["<sk0>:5454", "<sk1>:5454", "<sk2>:5454"],
  "storage_auth_token": "<Ed25519-JWT scope=PageServerApi,SafekeeperData compute_id=<ep>>",
  "project_id": "<project_id>", "branch_id": "<branch_id>", "endpoint_id": "<endpoint_id>",
  "cluster": {
    "roles": [{"name":"cloud_admin","encrypted_password":"SCRAM-SHA-256$<salt>:<iters>$<StoredKey>:<ServerKey>"}],
    "databases": [{"name":"postgres","owner":"cloud_admin"}],
    "settings": {},
    "postgresql_conf": "<可选 postgresql 设置>"
  },
  "endpoint_storage_addr": "http://neon-endpoint-storage:50051",
  "endpoint_storage_token": "<jwt>"
}
```
> 关键：`cluster.roles[].encrypted_password` 必须是 `SCRAM-SHA-256$...` 验证器（compute_ctl 在 `pg_helpers.rs:151` 据此判断 SCRAM）；**同一个字符串**必须作为 `get_endpoint_access_control.role_secret` 返回给 proxy（proxy 用 `scram::ServerSecret::parse` 解析），二者不一致会导致 proxy 鉴权失败。

compute_ctl 拉取 spec 的真实路径（源自 `compute_tools/src/spec.rs:78`）：
`GET <control-plane-uri 根地址>/compute/api/v2/computes/<compute_id>/spec` → 返回 `ComputeConfig { spec: <上述 ComputeSpec>, compute_ctl_config }`。因此 compute Deployment 的启动参数应为 `--control-plane-uri http://neon-control-plane:<port>`（根地址，不带 `/compute/api/v2` 后缀）。

**3) Proxy 兼容接口契约**（路径/字段严格对齐 `proxy/src/control_plane/client/cplane_proxy_v1.rs` 与 `messages.rs`；`<base>` 为 proxy 配置的 `control_plane_api` 基址 = 本服务 Service）：

```text
GET <base>/get_endpoint_access_control?endpointish=<ep>&role=cloud_admin&session_id=...
   → { "role_secret":"SCRAM-SHA-256$<salt>:<iters>$<StoredKey>:<ServerKey>",
       "allowed_ips":[], "allowed_vpc_endpoint_ids":[],
       "block_public_connections":false, "block_vpc_connections":false,
       "project_id":"<pid>", "account_id":null, "rate_limits":{} }

GET <base>/wake_compute?endpointish=<ep>&application_name=&session_id=...
   → { "address":"neon-compute-<ep>.<ns>.svc.cluster.local:5432", "server_name":null,
       "aux": { "endpoint_id":"<ep>", "project_id":"<pid>", "branch_id":"<bid>",
                "compute_id":"<ep>", "cold_start_info":"unknown" } }

GET <base>/endpoints/<ep>/jwks?session_id=...
   → { "jwks": [] }
```
`neon-proxy` chart 的 values 需将 `control_plane_api` 基址指向本服务（`http://neon-control-plane:<port>`）；接口均带 `Authorization: Bearer <jwt>`。注意路径是 `wake_compute`（非 `proxy_wake_compute`），查询参数为 `endpointish`（非 `endpoint`）。

本任务为 Kubernetes Helm 部署方案（基础设施即代码），不涉及前端 UI 页面开发，因此不输出可视化界面设计。所有交付物为 Helm chart 模板、values.yaml、ConfigMap、StatefulSet/Deployment 等 YAML 清单及中文注释，目标是在 k8s 集群中可一键部署生产级 Neon。