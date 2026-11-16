## 用户需求

在 Kubernetes 中部署生产级可用的完整 Neon 平台，基于现有 helm-charts 仓库进行扩展，补齐缺失组件并形成开箱即用的整体部署方案。当前实现已跑通核心链路，鉴权、密钥轮换、控制面 HA、监控与 proxy 接入等生产化能力列为后续扩展点（见「当前实现状态速览」与根目录 README「限制」）。

## 产品概述

本方案以现有 helm-charts 仓库为基础，补齐 Neon 最核心、也是最缺失的有状态存储组件（pageserver、safekeeper），新增一个**最小可运行的 control plane 服务**（创建 project / branch / endpoint，并完成与 storage-controller、pageserver、safekeeper、compute 的关键交互），并提供一个 umbrella 总 chart 将上述组件及已有组件（storage-broker、storage-controller）统一编排，实现一条命令完成 Neon 集群部署。**proxy 不纳入 umbrella 依赖**：`neon-proxy` chart 仍独立存在于仓库中，但 `charts/neon/Chart.yaml` 没有该依赖、values 也没有对应覆盖，默认部署中不存在 proxy，客户端直连 compute。计算节点（compute）**仅**由 control plane 在创建 endpoint 时经 in-cluster K8s API 动态拉起，仓库中已无静态 `neon-compute` chart，也无静态演示回退。

MinIO对象存储后端由外部提供。storage controller依赖的PostgreSQL数据库也由外部提供。

> 本文已按**当前仓库代码**（`charts/`、`charts/neon-control-plane/src/`、`jwt.py`）校正。历史上文档同时保留了早期设计（bootstrap、默认租户、静态 compute、notify 待实现）与后续实现，现已统一为下述口径。

## 当前实现状态速览

| 项目 | 现状 |
| --- | --- |
| umbrella 依赖 | `neon-storage-broker`、`neon-storage-controller`、`neon-pageserver`、`neon-safekeeper`、`neon-control-plane`（默认启用）+ `local-path-provisioner`（默认关闭）；**无 `neon-proxy` 依赖** |
| 节点注册 | 无 bootstrap。pageserver 经 `/upcall/v1/re-attach` + `metadata.json` 自注册；safekeeper 由 `sk-register` sidecar upsert 并显式激活 |
| 租户/时间线 | 无默认租户；每个 `POST /projects` 独占一个 tenant + 初始 timeline（main 分支） |
| compute | 仅动态拉起：Deployment + 主 Service(ClusterIP) + 可选对外 Service(NodePort)；compute_ctl 从控制面拉 spec |
| 故障转移 | `PUT /notify-attach`、`PUT /notify-safekeepers` 已实现，`/configure` 主动推送 + 30s 路由对账 + 15s 状态对账 |
| proxy 兼容接口 | `wake_compute` / `get_endpoint_access_control` / `endpoints/{id}/jwks` **已实现**，但 proxy 未部署、未联调 |
| 状态持久化 | 进程内 `state{RWMutex, ...}` + ConfigMap `neon-cp-state`（`state.json`，1s 去抖 write-through）；单副本 + `Recreate`，无 Lease 选主 |
| 密钥 | 外部预建 `neon-jwt` Secret（8 个键，由根目录 `jwt.py` 生成）；chart 不创建、values 不含明文 |
| 监控 / TLS | `metrics.*` 与 `serviceMonitor.*` 全部默认关闭；控制面无 `/metrics` 路由；本部署未启用 TLS |
| 已知限制 | JWT 无过期、用户 API 无鉴权、SC 单副本、local-path 本地盘不具跨节点 HA（见根目录 README「限制」） |

```yaml
---
# 创建namespace neon
apiVersion: v1
kind: Namespace
metadata:
  name: neon

---
# 外部 MinIO S3 —— 对象存储的**唯一事实来源**（凭证 + 连接坐标，运行在 :9000，非 K8s 内部）
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

- 补齐 pageserver / safekeeper 有状态服务 chart（StatefulSet + PVC + init 容器渲染 TOML/运行参数 + 探针 + PDB）
- 新增最小 **control plane 服务**（`neon-control-plane`）：常驻 Go 服务（自定义镜像 + Deployment），提供精简用户 API（project/branch/endpoint 的 create/list/delete，project 另有 get；对齐 v2.json 风格）、compute spec 拉取接口、SC 回调接口与 proxy 兼容接口；**不做 bootstrap**：节点由各组件自注册，控制面运行时只读查询 SC 并按需创建 tenant/timeline
- compute 由控制面动态拉起（Deployment + Service），另有 pre-delete 钩子 Job 在卸载时清理运行期创建的资源
- umbrella 总 chart 统一编排子 chart，集中管理 values 与全局配置；`local-path-provisioner` 作为可选依赖提供节点本地盘 StorageClass
- 可靠性：核心数据面组件 requests=limits、节点 AZ 动态探测、探针、PDB、控制面单副本 + `Recreate`、卸载清理
- 监控暂不启用：所有 chart 的 `metrics.enabled` / `serviceMonitor.enabled` 默认 `false`（模板保留，后续可开启）

## 技术栈

- 包管理：Helm 3（chart apiVersion v2），复用现有仓库约定（Chart.yaml + values.yaml + templates/）
- 编排：Kubernetes StatefulSet（pageserver/safekeeper）、Deployment（control-plane/broker/controller/动态 compute）；`neon-proxy` 为仓库内独立 chart，**未纳入 umbrella**
- 存储：PVC + StorageClass（默认 `local-path`，可用集群已有 SC 或启用 `local-path-provisioner`；RWO 本地盘，不具跨节点 HA）
- 对象存储：外部 MinIO（S3 兼容，运行在 K8s 之外），**连接坐标与凭证统一由 `bucket-credentials` Secret 提供（唯一事实来源，values 中不保留副本）**，不内建对象存储 chart
- 控制面：Go 1.21（`net/http`，无第三方依赖）+ 进程内状态 + ConfigMap 快照 + in-cluster K8s API
- 监控：Prometheus Operator ServiceMonitor 模板保留但默认关闭；控制面当前不暴露 `/metrics`
- 模板语言：Go template（与现有 chart 一致），_helpers.tpl 复用命名约定

## 实现方案

### 总体策略

在现有 charts 基础上，采用「补齐缺口 + umbrella 聚合」策略：新增 `neon-pageserver`、`neon-safekeeper`、`neon-control-plane` 三个子 chart 与配套 `local-path-provisioner` chart，再用 umbrella 总 chart（`charts/neon`）通过 dependencies 统一编排，复用已有 `neon-storage-broker`、`neon-storage-controller`。umbrella 共 6 个 dependencies（均为 `file://../<name>` + `condition`），其中 5 个默认启用、`local-path-provisioner` 默认关闭；`neon-proxy`、`neon-pg-sni-router`、`neon-endpoint-storage`、`neon-storage-scrubber` 仍在仓库中，但**均不在依赖列表内**。

- 有状态组件（pageserver/safekeeper）用 StatefulSet + PVC，保证 Pod 重建后数据不丢失、网络标识稳定（稳定的 DNS）。
- 控制面（`neon-control-plane`）是一个**常驻最小服务，无 bootstrap**。启动时只做只读诊断（打印 SC 中已注册节点快照）、从 ConfigMap 恢复状态并与 K8s 运行态对账，然后启动 SC 路由对账 / 状态对账循环；节点由 pageserver 的 `/upcall/v1/re-attach` 与 safekeeper 的 `sk-register` sidecar 自注册，控制面绝不写注册接口。运行时对外提供 project/branch/endpoint CRUD、`compute spec` 拉取、SC notify 接收与 proxy 兼容接口；tenant/timeline 由 `POST /projects` 按需创建。参考 neon 源码 `control_plane`（即 `neon_local`，本地开发编排工具，**非生产控制面**）的实际调用路径与 `storage_controller/src/http.rs` 的 REST 接口。
- 存储后端为外部 MinIO（S3 兼容），存储后端的连接坐标（bucket / region / endpoint）与凭证**统一由 `bucket-credentials` Secret 提供**，chart 只引用该 Secret、不内建对象存储。pageserver 的 `remote_storage` 无环境变量覆盖机制，坐标只能由 init 容器在 Pod 启动时从该 Secret 读出后写入 `pageserver.toml`；因此在 values 中保留 endpoint/bucket/region 副本会造成两处漂移（且漂移不会被任何校验捕获），已彻底移除。
- compute（计算节点）**仅**由 control plane 在创建 endpoint 时**动态拉起**：control plane 生成 `ComputeSpec` 并通过 in-cluster K8s API 创建 Deployment + 主 Service（`ClusterIP`，5432 + 3080），`computeServiceType=NodePort` 时再创建只暴露 5432 的对外 Service（`neon-compute-<ep>-ext`）；`compute_ctl` 从 control plane 拉取 spec。umbrella 不再内置静态 `neon-compute` chart（该 chart 已移除），集群内不存在预部署的默认 compute。

### 关键技术决策

1. **StatefulSet vs Deployment**：pageserver/safekeeper 有本地持久状态（tenant 层数据、safekeeper.control 控制文件），必须用 StatefulSet + volumeClaimTemplates，保证 stable network ID 与持久卷绑定；控制面无状态用 Deployment，便于水平扩容。
2. **配置方式**：pageserver/safekeeper 主要依赖 TOML 配置文件而非 CLI 参数（已确认 pageserver 启动仅 `-D/--workdir` + TOML；safekeeper 用 clap 参数 + data-dir）。因此用 ConfigMap 渲染 TOML/toml 片段，通过 volume 挂载到 workdir/data-dir。
3. **JWT 与密钥（已核对 `storage_controller/src/main.rs::Secrets::load`、`libs/utils/src/auth.rs` 与根目录 `jwt.py`）**：全局用**一套 Ed25519 私钥/公钥**，统一放在**单个外部预建的 Secret**（`global.jwt.existingSecret`，默认 `neon-jwt`）。chart **不创建**该 Secret、也不接收任何密钥明文，由根目录 `jwt.py` 生成后 `kubectl apply`（或交给 SOPS / Sealed Secrets / External Secrets 管理）。Secret 共 8 个键，键名与 chart 模板一一对应：

   | 键 | scope（JWT payload 字面值） | 消费方 |
   | --- | --- | --- |
   | `publicKey.pem` | — | pageserver / safekeeper / SC / control-plane（校验签名） |
   | `privateKey.pem` | — | **仅** control-plane（签发 token） |
   | `pageserverJwtToken` | `pageserverapi` | SC → pageserver |
   | `safekeeperJwtToken` | `safekeeperdata` | pageserver → safekeeper（`NEON_AUTH_TOKEN`）/ SC → safekeeper |
   | `controlPlaneJwtToken` | `controlplane` | SC → control-plane 的 notify 回调 |
   | `peerJwtToken` | `admin` | safekeeper 注册 sidecar / SC 副本间通信 |
   | `generationsApiJwtToken` | `generations_api` | pageserver → SC 的 upcall（re-attach / validate） |
   | `computeJwtToken` | `tenant` | 带 `tenant_id`，`jwt.py` 标注「预留，当前无消费方」；控制面持有私钥自行现签同名 scope token |

   上游 `libs/utils/src/auth.rs::Scope` 是字符串枚举（`rename_all = "lowercase"`），合法值含 `tenant` / `tenantendpoint` / `pageserverapi` / `safekeeperdata` / `generations_api` / `admin` / `infra` / `scrubber` / `controller_peer`；**没有 `SafekeeperApi`**。`controlplane` 是本方案自研控制面使用的自定义 scope，由 Go 侧 `validateNotifyAuth` 校验签名后接受 `controlplane` 或 `admin`。token 由 `jwt.py` 一次性签发且**不含 `exp`**（上游 `JwtAuth` 不校验过期），泄漏即长期有效，只能换密钥 + 重签 + 重启。
   - SC 启动命令为 `storage_controller -l 0.0.0.0:50051 --control-plane-url http://neon-control-plane-svc:8080 --address-for-peers ...`；密钥类参数**全部走 `env.secretKeyRef`**（`PUBLIC_KEY`、`PAGESERVER_JWT_TOKEN`、`SAFEKEEPER_JWT_TOKEN`、`CONTROL_PLANE_JWT_TOKEN`、`PEER_JWT_TOKEN`），连接串走 `DATABASE_URL`。上游 `Secrets::load_secret()` 对同一配置项是「CLI 参数优先、其次环境变量」；多行 PEM 只有经环境变量注入才能保留真实换行（内联 `toJson` 会把 `\n` 变成字面量导致解析失败，见 SC `deployment.yaml` 注释）。
   - 严格模式（默认，未传 `--dev`）下 SC 启动即校验：缺少上述密钥或 `--control-plane-url` 会 `bail`（`storage_controller/src/main.rs:374-402`）；`--use-local-compute-notifications` 被禁止；`--timeline-safekeeper-count` 默认 3（不传即满足，故 safekeeper 至少 3 副本）。因此 umbrella 必须始终透传 `settings.controlPlaneUrl`，且控制面已实现 notify 接收端点（见 1.5.1），不存在「通知失败仅重试」的降级路径。
   - **SC 不连接 storage_broker**（无 `--broker-endpoint` 参数）；broker 仅服务 pageserver↔safekeeper 的 WAL 流发现。
   - pageserver / safekeeper 侧挂载**同一公钥**（pageserver 用 `auth_validation_public_key_path=/etc/neon/jwt/publicKey.pem` + `http_auth_type/pg_auth_type=NeonJWT`；safekeeper 用 `--pg-auth-public-key-path /etc/neon/jwt/publicKey.pem`）校验 SC 与 compute 的 token。
   - `neon-control-plane` 是**唯一持有私钥**者，按目标路径现签 scope token（`v1/*`→`pageserverapi`、`control|debug/*`→`admin`），为 compute 签发 `storage_auth_token`（**`tenant` scope + `tenant_id`**，不是 `PageServerApi`+`SafekeeperData`），并为 `/configure` 调用签发只带 `compute_id` + `exp` 的 compute JWT。
     消费方式遵循最小权限：
   - control-plane：只投影 `privateKey.pem` + `publicKey.pem`；
   - pageserver：只投影 `publicKey.pem` + `generationsApiJwtToken`（后者由 `init-identity` 在 Pod 启动时追加到 `pageserver.toml` 的 `control_plane_api_token`，因为该字段仅支持 TOML 内联，不能进 ConfigMap）；
   - safekeeper：主容器只投影 `publicKey.pem`，`sk-register` sidecar 另挂一个只投影 `peerJwtToken` 的卷；
   - storage-controller：通过 `env.secretKeyRef` 逐键注入 `PUBLIC_KEY` 与各 `*_JWT_TOKEN`。
     绝不能把整个 Secret 挂进容器——那会把 `privateKey.pem` 暴露给 pageserver / safekeeper。
4. **umbrella 编排**：`charts/neon/Chart.yaml` 用 dependencies + condition 声明 6 个子 chart；`charts/neon/values.yaml` 通过 `global.jwt.existingSecret`、`global.storage.bucket.existingSecret` / `keys`、`global.storageController.databaseUrl.existingSecret` / `secretKey`、`global.region` 与各子 chart 同名段（如 `neon-storage-controller.settings.controlPlaneUrl`、`neon-pageserver.settings.brokerEndpoint`、`neon-control-plane.settings.computeImage`）解决配置耦合。外部 Secret 缺失时在渲染期 `fail`（`neon/templates/jwt-check.yaml`、SC `deployment.yaml`），不会静默起半认证集群。

### Control Plane 详细设计（基于 neon 源码深度调研）

#### 重要澄清：Neon 真实控制面构成

- 仓库内 `control_plane/` crate 实为 **`neon_local`**——本地开发/测试**编排工具（CLI）**，通过 `.neon/config` 管理本地进程生命周期，**不适合直接上 K8s**，也不是生产控制面。
- 生产控制面由三层组成：
  1. **Console（云端控制面 API）**：即用户提供的 `v2.json`（`https://console.neon.tech/api/v2`），资源为 `projects` / `branches` / `endpoints`，对外暴露租户/分支/计算端点的 CRUD。该 Console **不在本仓库、也不开源**，自建需自研或仅做兼容壳。
  2. **storage_controller**：真正的**存储调度大脑**，提供 `/control/v1/*` REST API（节点注册、租户创建与调度、safekeeper 管理、upcall 回调）。本仓库已包含并可独立部署（`neon-storage-controller` chart）。
  3. **K8s Operator / compute_ctl**：计算节点由 `compute_ctl` 拉起，它从 `CONTROL_PLANE_API_BASE_URL` 拉取 `ComputeSpec`，或读取本地 `spec.json`。

#### 本方案 `neon-control-plane` 的定位：最小可运行的 control plane 服务

**最小但真实运行的 control plane 服务**（不是 bootstrap Job），常驻 Deployment（`replicas: 1` + `Recreate`），对外有四张"脸"：

1. **用户 API（精简，对齐 v2.json）**：project / branch / endpoint 的 create / list / get / delete。
2. **Compute spec API**：`GET /compute/api/v2/computes/{compute_id}/spec` → `{spec, status:"attached", compute_ctl_config:{jwks}}`，由 compute_ctl 拉取。
3. **Proxy 兼容 API（已实现，但 proxy 未部署）**：`wake_compute` / `get_endpoint_access_control` / `endpoints/{id}/jwks`。角色 SCRAM 密钥**内嵌**在 `get_endpoint_access_control.role_secret` 中（proxy 的 `get_role_access_control` 也调该接口取 `role_secret`），无需独立 `get_role_secret` 端点。
4. **SC 回调接收（已实现）**：`PUT|POST /notify-attach`、`PUT|POST /notify-safekeepers`（见 1.5.1），配合后台对账把新路由推给运行中的 compute。

**没有 Bootstrap**：不做节点注册、不创建默认租户/时间线；启动时只做只读诊断、状态恢复与对账循环启动。

它与其他组件的关键交互：

- → **storage_controller**：创建/删除 tenant 与 timeline（`v1/*`）、定位租户分片（`debug/v1/.../locate`）、列出 Active safekeeper 与已注册节点（`control/v1/*`，只读）；**无任何注册类写操作**。
- ↔ **compute**：生成 `ComputeSpec`，用 in-cluster K8s API 为每个 endpoint 创建 Deployment + Service；compute_ctl 从本服务拉取 spec；故障转移时控制面主动 `POST /configure` 推送新 spec，并周期探测 `/status`。
- ← **storage_controller（notify）**：SC 在调度变更后回调本服务，触发 spec 重建与推送。
- ↔ **proxy（未部署）**：proxy 兼容接口已就绪；当前默认部署中无 proxy，接口不被触发。
- → **对象存储（neon-endpoint-storage）**：未部署；`endpoint_storage_addr/token` 仅在类型定义中保留，当前 spec 不下发该字段。

#### 服务内部状态（minimal，in-memory + ConfigMap，无需外部 DB）

运行时真相源为进程内 `state{RWMutex, projects, branches, endpoints}`；持久化层为 K8s `ConfigMap`（`neon-cp-state` 的 `state.json`，KB 级，存于 etcd），采用 **write-through + 1s 去抖**落盘（`src/persist.go`：`triggerPersist` 不重置已有 timer）。该范式与 Neon 上游 `control_plane` crate 的「内存 HashMap + `persist()` 落盘」一致。重启时 `reconcileOnStartup()` 先 load 快照，再用 K8s 运行态修正 endpoint 状态（`reconcileWithK8s` → `reconcileEndpointStatuses`：Deployment 就绪度 / Pod 失败态 / compute_ctl `/status`），并启动时同步跑一次 SC 路由指纹对账；**不向 SC 重新拉取 tenant/timeline 映射**（见下数据边界）。

- `project_id → tenant_id`
- `branch_id → (tenant_id, timeline_id)`
- `endpoint_id → (tenant_id, timeline_id, compute 资源名, status, route_fingerprint)`

> **数据边界**：`tenant↔timeline` 真相源是 SC 的 PostgreSQL，控制面只在 `POST /projects` 时创建、`DELETE /projects` 时删除，**不做持久化对账**；ConfigMap 仅存人类可读别名 + endpoint 运行态。`ScramPassword`（明文）与 `ScramVerifier` 字段标记为 `json:"-"`，不单独落盘，但同一个验证器会以 `spec.cluster.roles[].encrypted_password` 随 spec 持久化；重启后 `get_endpoint_access_control` 从该字段回退读取。单写者由 `replicas: 1` + `Recreate` 保证，**未实现 Lease 选主**。

#### 用户 API（v2.json 风格，仅核心字段）

- `POST /projects` → 在 SC 创建独立 tenant + 初始 timeline，自动建 `main` 分支与默认 `read_write` endpoint，并动态拉起 compute；响应含 `project`、`connection_uris`、`roles`（含明文密码）、`databases`、`default_branch`、`operations`。默认角色 `<database_name>_owner`（官方语义，如 `neondb_owner`）、默认库 **`neondb`**（均可用请求体覆盖）。
- `GET /projects`、`GET /projects/{project_id}`、`DELETE /projects/{project_id}`（级联删除 endpoint / branch / timeline / tenant 与 compute 资源）。
- `POST /projects/{project_id}/branches`、`GET /projects/{project_id}/branches`、`DELETE /projects/{project_id}/branches/{branch_id}`（default 分支或存在子分支时返回 409）。
- `POST /projects/{project_id}/endpoints`（body `{"endpoint":{"branch_id":"...","type":"read_write"|"read_only"}}`，兼容旧扁平形态；`type` 必填；每分支仅一个 `read_write`，重复创建 409）→ 见下「Endpoint 创建」；`GET /projects/{project_id}/endpoints`；`DELETE /projects/{project_id}/endpoints/{endpoint_id}`；`GET /projects/{project_id}/connection_uri?database_name=&role_name=`（连接串，role/db 必填）。
- `GET /compute/api/v2/computes/{compute_id}/spec` → compute_ctl 的 spec 拉取端点（与用户 API 同端口）；`GET /healthz` → 健康检查。
- 未提供单个 branch / endpoint 的 GET；**用户 API 无鉴权、控制面无 `/metrics` 路由**（已知限制，见根目录 README）。

#### Endpoint 创建的关键交互（核心难点）

创建 endpoint 时，control plane 必须构造 `ComputeSpec`（结构见 `libs/compute_api/src/spec.rs`）并让 compute 跑起来。实现入口为 `src/main.go::prepareEndpointForBranch()` / `createEndpoint()`，步骤：

1. **定位 pageserver**：`GET <sc>/debug/v1/tenant/{tenant_id}/locate` → `TenantLocateResponse` → 组装 `pageserver_connection_info`（`shard_count` + `stripe_size` + `shards` map，每个 shard 含 `pageservers:[{ id?, libpq_url, grpc_url? }]`；**没有** `host`/`port`/`http_host`/`http_port`）。唯一构造入口是 `src/notify.go::buildPageserverConnInfo`，创建 endpoint / getComputeSpec 自愈 / notify-attach / 周期对账四条路径同源：
   - locate 响应的分片数字段是 **`shard_params.count`**（`shard_count` 仅作旧版兜底），`shard_count=0` 时 `stripe_size` 必须为 `null`；
   - `shards` 的 key 是 `TenantShardId` 的 4 位 hex 后缀（`"0000"` / `"0002"` 等），由 `parseShardIndex` 解析，不能用数组下标；
   - `grpc_url` 仅在 pageserver 上报了 gRPC 地址时下发，不能用 http 地址冒充。

   > 已核对 `control_plane/src/storage_controller.rs::tenant_locate()`：真实路径是 **`debug/v1/tenant/{id}/locate`**（不是 `control/v1/tenant/{id}`）。该路径属 `debug` 前缀 → 需 `admin` scope JWT。
   >
2. **取 safekeeper 列表**：`GET <sc>/control/v1/safekeeper`（SC 只返回 Active）→ `<host>:5454` 列表 → 填入 `safekeeper_connstrings`。无 Active safekeeper 时**返回 503**，绝不下发坏 spec。
3. **签发 storage JWT**：用 Ed25519 私钥现签 `{"scope":"tenant","tenant_id":"<tid>"}` → `storage_auth_token`（`src/jwt.go::signWithTenant`）。`tenant` scope 同时满足 pageserver 与 safekeeper 的数据面鉴权；**不再**使用早期设计里的 `PageServerApi` + `SafekeeperData` + `compute_id` 组合（`computeJwtToken` 键在 `jwt.py` 中仅作预留）。
4. **生成角色 SCRAM 密钥（关键一致性点）**：生成**一个** `SCRAM-SHA-256$<iterations>:<salt>$<StoredKey>:<ServerKey>` 验证器（`src/spec.go::generateScramVerifier`，4096 次 PBKDF2），把它**同时**写入：

   - ComputeSpec 的 `cluster.roles[].encrypted_password`（compute_ctl 在 `pg_helpers.rs` 中检测到 `SCRAM-SHA-256` 前缀即按 SCRAM 验证器写入 `PASSWORD`）；
   - `get_endpoint_access_control` 返回的 `role_secret`（proxy 用 `scram::ServerSecret::parse` 解析，二者必须逐字节一致，否则 proxy 鉴权失败）。

   > **字段顺序不可写错**：必须是 `<iterations>:<salt>`（冒号分隔）。早期文档写成 `salt:iters` 会让 Postgres 把整串当作明文口令重新哈希，表现为 `password authentication failed`（见 `src/spec.go` 注释）。明文密码只在创建响应中返回一次，不落 ConfigMap。

  > **仅 `POST /projects` 走到这一步**。`POST /endpoints` 对齐 Neon 语义：不创建 role/database、不生成新密码，
  > 而是解析分支（或父分支）已有 endpoint 的 role/db，并复用其 SCRAM 凭据引用（`credStore.AttachReference`）；
  > 响应也不再返回 `connection_uri`/`password`，连接串改由 `GET /projects/{id}/connection_uri` 提供。
5. **组装 ComputeSpec**：`format_version:1.0`、`tenant_id`、`timeline_id`、`mode`(Primary/Replica，按 endpoint `type` 决定)、`pageserver_connection_info`、`safekeeper_connstrings`、`storage_auth_token`、`project_id`/`branch_id`/`endpoint_id`、**`suspend_timeout_seconds: 0`（必填，0 = 不自动挂起）**、`local_proxy_config:{jwks:[]}`、`cluster`（`settings` 为 `GenericOption[]`，强制含 `shared_preload_libraries=neon` 与 `listen_addresses=*`；`roles/databases` 仅在项目创建时下发）；默认角色 `<database_name>_owner`、默认库 `neondb`。创建时记录 `route_fingerprint` 作为「spec 是否落后于 SC 真相」的基线。
6. **拉起 compute（唯一路径）**：用 in-cluster K8s client 创建：

   - Deployment + 主 Service `neon-compute-<endpoint_id>`（恒为 `ClusterIP`：5432 + compute_ctl 3080）；`computeServiceType=NodePort` 时额外创建 `neon-compute-<ep>-ext`（仅 5432），对外地址返回 `<nodePortExternalHost>:<nodePort>`。3080 是管理端口，不以 NodePort 暴露。
   - 镜像 `settings.computeImage`（compute-node 专用镜像，含 postgres + compute_ctl），启动命令：
     `bash -c exec /usr/local/bin/compute_ctl -C postgresql://cloud_admin@localhost/postgres --control-plane-uri http://neon-control-plane-svc:8080 --compute-id <ep> --pgdata /var/db/postgres/data --pgbin /usr/local/bin/postgres --external-http-port 3080`
     （`--connstr`/`-C` 必填；`-p/--control-plane-uri` 与 `-c/--config` 互斥，选前者时必带 `-i/--compute-id`。）
   - compute_ctl 周期性向 `GET <cp>/compute/api/v2/computes/<compute_id>/spec` 拉取 spec（路径源自 `compute_tools/src/spec.rs:78`，故 `--control-plane-uri` 传控制面**根地址**即可）；响应里的 `compute_ctl_config.jwks` 下发控制面 Ed25519 公钥（OKP/Ed25519），否则 `/configure`、`/status` 会全部 401。
   - **备选（已废弃，不再提供）**：把 spec 写入 S3/ConfigMap 并由静态 `neon-compute` chart 以 `--config /spec.json` 加载的方案已移除，仓库中不存在该 chart。
7. **故障转移后的热更新**：SC 回调或 30s 周期对账发现路由指纹变化时，控制面主动 `POST http://neon-compute-<ep>.<ns>.svc.cluster.local:3080/configure` 推送整份新 spec，无需重建 Pod（见 1.5.1）。

#### Proxy 兼容接口（已实现，严格对齐 `proxy/src/control_plane/client/cplane_proxy_v1.rs` 与 `messages.rs`）

以下接口已在 `src/main.go` 注册（**proxy 未部署，当前无调用方**；接口不校验 method 与 `Authorization`）：

- `GET <base>/get_endpoint_access_control?endpointish=<endpoint>&role=<role>&session_id=...` → 响应 `GetEndpointAccessControl { role_secret, project_id, account_id:null, allowed_ips:[], allowed_vpc_endpoint_ids:[], block_public_connections:false, block_vpc_connections:false, rate_limits:{} }`；`role_secret` 取内存中的 SCRAM 验证器，控制面重启后回退到 spec 中持久化的 `encrypted_password`。endpoint 不存在返回 404。
- `GET <base>/wake_compute?endpointish=<endpoint>&session_id=...&application_name=...` → 响应 `WakeCompute { address, server_name:null, aux:{endpoint_id, project_id, branch_id, compute_id, cold_start_info:"unknown"} }`。`address` 在 `NodePort` 模式下是 `<nodePortExternalHost>:<nodePort>`，否则是 `<compute-svc>.<ns>.svc.cluster.local:5432`；endpoint 不存在时仍返回 200（project/branch 为空串）。（路径是 `wake_compute`，**不是** `proxy_wake_compute`；查询参数是 `endpointish`，**不是** `endpoint`。）
- `GET <base>/endpoints/<endpoint>/jwks?session_id=...` → 固定返回 `{"jwks":[]}`（按契约 v1 返回空列表，proxy 会 fallback）。
- `get_role_access_control`：proxy 内部同样调用 `get_endpoint_access_control` 并取 `role_secret`，**无独立端点**。

> 字段名与类型必须严格对齐 `messages.rs` 的 `WakeCompute` / `GetEndpointAccessControl` / `EndpointJwksResponse`（如 `cold_start_info` 用 `snake_case`：`unknown`/`warm`/`warm_cached`；`address` 为 `host:port` 字符串）。proxy 对路径/字段有强约定，错一个字段即鉴权或路由失败。

#### 节点注册：无 Bootstrap，各组件自注册（当前实现）

控制面**不做任何节点注册、也不创建默认租户**（原 bootstrap 已移除）。原因：

- pageserver 启动时 `POST <sc>/upcall/v1/re-attach` 会携带 `NodeRegisterRequest` 自注册
  （`pageserver/src/controller_upcall_client.rs` 读取 `metadata.json` 构造，SC 侧 `re_attach()` 内部调 `node_register`）；
  `metadata.json` 由 `neon-pageserver` 的 init 容器生成，含 `node_id = nodeIdBase + ordinal`、完整 FQDN 与 pg/http/https/grpc 端口。
- safekeeper 由 `neon-safekeeper` 的 `sk-register` sidecar 注册：`POST <sc>/control/v1/safekeeper/{id}`（SafekeeperUpsert）
  + `POST <sc>/control/v1/safekeeper/{id}/scheduling_policy`（`{"scheduling_policy":"Active"}`，**注意是首字母大写的变体名**，
    上游 `SkSchedulingPolicy` 的 serde 未做 rename，小写会被 400 拒收），并周期对账、终止时置 `Decomissioned`。
- 控制面若再注册一遍会出现双写者：同 id 不同地址会被 SC 判 409（`service.rs` 的 `RegistrationStatus::Mismatched`），
  且静态清单无法感知副本数变化、node id 规则还与子 chart 的 `nodeIdBase + ordinal` 重复定义。

控制面对 SC 的**只读查询**（`src/scclient.go`）：

- `GET <sc>/control/v1/node`：启动诊断，以及 locate 失败时增强错误信息（统计 Active pageserver 数）
- `GET <sc>/control/v1/safekeeper`：取 Active safekeeper 列表（SC 的 `get_safekeepers()` 只返回 `Active`）
- `GET <sc>/debug/v1/tenant/{id}/locate`：定位该 tenant 的 pageserver 分片（创建 endpoint / notify / 周期对账共用）

写操作仅用于资源生命周期：`POST|DELETE /v1/tenant`、`POST|DELETE /v1/tenant/{tid}/timeline`（由 project / branch API 触发）。**绝不**调用 `POST /control/v1/node`、`POST /control/v1/safekeeper/{id}` 与 scheduling_policy 端点——那属于各组件自注册的职责。

tenant / timeline 一律由 `POST /projects` 按需创建（每个 project 独占 tenant），**不再有默认租户**；
创建 endpoint 时若 SC 尚未完成调度或无 Active safekeeper，控制面返回 **503**（不下发坏 spec），由 compute_ctl 周期性拉取 spec 重试；路由落后则由后台 30s 对账 + `POST /configure` 主动推送修正。

> **已核对 `control_plane/src/storage_controller.rs`（neon_local 真实调用）**：
>
> - `node_register` → `POST control/v1/node`；`node_configure` → `PUT control/v1/node/{id}/config`；`node_list` → `GET control/v1/node`
> - `tenant_create` → **`POST v1/tenant`**（不是 control/v1）；`tenant_locate` → **`GET debug/v1/tenant/{id}/locate`**
> - `tenant_timeline_create` → `POST v1/tenant/{tid}/timeline`；`set_tenant_config` → `PUT v1/tenant/config`
> - `upsert_safekeeper` → `POST control/v1/safekeeper/{id}`；`safekeeper_scheduling_policy` → `POST control/v1/safekeeper/{id}/scheduling_policy`
> - SafekeeperUpsert body 字段（源自 `libs/pageserver_api/src/controller_api.rs::SafekeeperDescribeResponse`，即 upsert 体）：`{ id, region_id, host, port(pg_port), http_port, https_port, version, availability_zone_id }`（`scheduling_policy` 经独立端点 `POST control/v1/safekeeper/{id}/scheduling_policy` 设置，不在 upsert 体内）；**注意：没有 `created_at`/`updated_at`**（时间戳由 SC 内部维护，不在此请求体内）。

**JWT scope（关键；已核对 `storage_controller/src/http.rs` 各 handler 的 `check_permissions`）**：control plane → SC 的调用按**路径前缀**现签不同 scope 的 JWT（同一 Ed25519 私钥，payload 为小写字符串）：

- `status` / `ready` → **无需** JWT
- `control/*` / `debug/*` → `admin`（`GET control/v1/node`、`control/v1/safekeeper`、`debug/v1/tenant/.../locate`）
- `v1/*` → `pageserverapi`（`POST v1/tenant`、`POST|DELETE v1/tenant/{tid}/timeline`、`DELETE v1/tenant/{tid}`）

> 即：创建/删除租户与时间线用 `pageserverapi` token，节点查询与 locate 用 `admin` token。错用 scope 会被 SC 拒绝（403）。`src/scclient.go::do()` 按调用点传入 scope 字符串现签（`sign` / `signWithTenant`），不依赖 Secret 中的静态 token。

**幂等性**：SC 返回 409（已存在）视为成功；compute 资源用确定性命名 + K8s 对象已存在即 no-op；状态由 ConfigMap 快照 + 启动对账恢复。不再有 bootstrap 的「先 GET 再跳过」流程。

#### 技术实现要点

- **语言/镜像（已确认）**：用 **Go 1.21** 实现轻量 HTTP 服务（`net/http` + `ServeMux` 手写路由，无第三方依赖）；`src/` 下 13 个 `.go` 文件（main / notify / reconciler / status_reconciler / compute / persist / scclient / spec / types / jwks / jwt / k8s / shard_test），`Dockerfile` 为 alpine 运行时 + 预编译二进制，`build.sh` 构建并推送私有仓库；chart 仅通过 `image` 引用镜像，启动参数 `--config /etc/neon/config.json`。需访问 K8s API → `templates/serviceaccount.yaml` 创建 **ClusterRole + ClusterRoleBinding**（`deployments` / `pods` / `services` / `configmaps` 的 `create/get/list/watch/update/patch/delete`，集群范围、**非**限本 namespace；代码实际只用 create/get/list/delete）。
- **JWT 复用**：控制面持有同一套 Ed25519 私钥（只投影 `privateKey.pem` + `publicKey.pem`），现签 `pageserverapi` / `admin` / `tenant` token 及只带 `compute_id` + `exp` 的 compute token；`validateNotifyAuth` 用同一公钥校验 SC notify 的 `controlplane`（或 `admin`）token。proxy 兼容接口当前**不校验** `Authorization`。
- **SCRAM 角色密钥自管**：仅在创建 project（首次建分支）时生成 `SCRAM-SHA-256$<iterations>:<salt>$<StoredKey>:<ServerKey>` 验证器（见上「Endpoint 创建」第 4 步），既写入 ComputeSpec 又作为 `role_secret` 返回，保证 proxy 与 compute 密码一致。后续创建 read_only 副本复用同一角色的验证器，不生成新密码。角色名默认 `<database_name>_owner`、库默认 `neondb`。
- **不实现完整 Console**：不做用户/组织/计费/鉴权体系；用户 API 仅保留 project/branch/endpoint 核心字段（对齐 v2.json 资源形状，只实现 create/list/get/delete）。
- **state 持久化（in-memory + ConfigMap）**：运行时真相源为内存 `state{RWMutex,...}`，持久化层为 `ConfigMap/neon-cp-state` 的 `state.json` 快照，采用 **write-through + 1s 去抖**（`src/persist.go`）。要点：
  - **单写者约束**：ConfigMap 无事务，实际部署 `replicas: 1` + `Recreate`；**未实现 Lease 选主**，多副本并发写会互相覆盖状态（后续 HA 扩展点）。
  - **防重复拉起 compute**：(1) 确定性命名 `neon-compute-<endpoint_id>` + 已存在即 no-op；(2) 启动与 15s 周期状态对账以 K8s 实际状态（Deployment 就绪度 / Pod 失败态 / compute_ctl `/status`）修正 `endpoint.status`；(3) 30s SC 路由指纹对账修正 spec。`tenant/timeline` **不做持久化对账**，ConfigMap 丢失时无法全量重建这部分映射。
  - **消除单方案缺点**：相对「单独 A（in-memory + PVC）」省去 PVC；相对「单独 B（每次读写打 etcd）」所有读走内存、ConfigMap 仅作低频快照。不引入 PostgreSQL 依赖。

### 1.5 SC → control plane 反向回调（upcall / compute_hook，已实现并启用）

**机制（已核对 `storage_controller/src/compute_hook.rs`、`controller_upcall_client.rs`、`pageserver_api/src/upcall_api.rs`）**：需区分**两类 upcall**，结论完全不同：

**(A) pageserver → SC 的 upcall（SC 已原生实现，配置即可，零开发）**

- SC 在 `http.rs` 暴露 `/upcall/v1/re-attach`、`/upcall/v1/validate`、`/upcall/v1/timeline_import_status`。
- pageserver 启动时 `POST /upcall/v1/re-attach` 向 SC 上报节点信息并接受租户 attach 指令；路径由 `controller_upcall_client.rs:163` 的 `base_url().join("re-attach")` 拼接，故 pageserver.toml 的 `control_plane_api` **必须配成 `http://<sc>:<port>/upcall/v1`**（带前缀）。
- 结论：本方案直接配置即可启用，SC 侧无需写代码。

**(B) SC → control plane 的 compute_hook（✅ 已实现，见 1.5.1）**

- SC 在租户 shard 调度变更后，通过 `ComputeHook` 向 `control_plane_url` 发送两类通知（`do_notify_iteration` 使用 **PUT**）：
  - `PUT {control_plane_url}/notify-attach`：让运行中 compute **热更新其 pageserver 映射**（shard→pageserver 路由）。
  - `PUT {control_plane_url}/notify-safekeepers`：让 compute **热更新其 safekeeper 集合**（body：`NotifySafekeepersRequest { tenant_id, timeline_id, generation, safekeepers: Vec<SafekeeperInfo> }`，见 `compute_hook.rs:353`）。
  - 鉴权：`Authorization: Bearer <CONTROL_PLANE_JWT_TOKEN>`（Secret 键 `controlPlaneJwtToken`，scope 字面值 `controlplane`；控制面侧也接受 `admin`）。
  - 用途：让运行中 compute **不重建 Pod** 即热更新存储路由，使存储层故障转移对客户端透明（HA 关键机制）。
- SC 两种模式（关键坑点）：
  - **远程模式** `--control-plane-url`：发往 control plane（console）端点，由其转发给 compute。✅ **这是 K8s 唯一可行的路径**。
  - **本地模式** `--use-local-compute-notifications`：依赖 neon_local 的 `LocalEnv`/本地 repo 配置（`neon_local_repo_dir`），仅适用于 neon_local 单机开发；且**严格模式下该参数被直接禁止**（SC 启动即 `anyhow::bail!("`--use-local-compute-notifications`is only permitted in`--dev` mode")`，见 `storage_controller/src/main.rs:395`），`--dev` 才允许。本方案 K8s 场景既不沿用本地模式、也不能省略远程模式——远程模式 (`--control-plane-url`) 在严格模式下为**必填**。

**实现现状**

- (A) pageserver→SC upcall：**已启用**，SC 原生；`pageserver.toml` 的 `control_plane_api` 配为 `<sc-url>/upcall/v1`，`control_plane_api_token` 由 `init-identity` 从 Secret 追加。
- (B) SC→control plane compute_hook：**已实现并启用**（接收端点 + `/configure` 主动推送 + 周期对账，见 1.5.1）；不再依赖「compute 重连 + compute_ctl 自愈」。

### 1.5.1 compute_hook 实现现状（已落地）

链路：`SC PUT /notify-attach` → 控制面按 SC `tenant_locate` 重建 spec → 后台 `POST /configure` 推送给 compute。

| 环节         | 实现位置                                           | 要点                                                                                                                                      |
| ------------ | -------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------- |
| 接收端点     | `src/notify.go`                                  | 同步只做 locate + 内存更新、推送异步，以赶上 SC 的 `NOTIFY_REQUEST_TIMEOUT=10s`；locate 失败返回 **503**（旧 423 只重试 3 次就放弃） |
| spec 重建    | `src/notify.go::buildPageserverConnInfo`         | 唯一构造入口，主字段取 `shard_params.count`（`shard_count` 仅旧版兜底），shard key 取 `TenantShardId` 的 4hex 后缀          |
| 路由指纹     | `src/reconciler.go::routeFingerprint`            | 由"已写入 spec 的路由"计算，用于判断 spec 是否落后于 SC 真相                                                                              |
| 周期对账     | `src/reconciler.go`（默认 30s）                  | 兜底：通知丢失 / 控制面重启 / SC 去重跳过                                                                                                 |
| 状态对账     | `src/status_reconciler.go`（默认 15s）           | 探测 Deployment/Pod 就绪度 + compute_ctl `/status`，修正 `endpoint.Status`                                                              |
| 推送         | `src/compute.go::configureCompute`               | `POST http://neon-compute-<ep>.<ns>.svc.cluster.local:3080/configure`，`ConfigurationRequest{spec, compute_ctl_config}`，120s 超时    |
| compute 鉴权 | `src/jwks.go` + `src/jwt.go::signComputeToken` | spec 下发 OKP/Ed25519 JWKS；调用携带 `compute_id` 声明的 JWT（对齐 neon_local `create_jwks_from_pem` / `generate_jwt(None)`）       |
| compute 地址 | 主 Service DNS（ClusterIP）                        | 不需要维护 endpoint→PodIP 映射；3080 **不再**以 NodePort 暴露                                                                      |

> 升级注意：`compute_ctl_config.jwks` 只在 compute 启动时读取，**存量 compute 必须重建一次**才能生效。

**当前实现要点（替代早期「待办清单」）**

1. **接收端点已落地**：`src/notify.go` 实现 `PUT|POST /notify-attach`、`PUT|POST /notify-safekeepers`；校验 `Authorization: Bearer <controlPlaneJwtToken>`，签名有效且 scope 为 `controlplane` / `admin` 才放行（400 body 错 / 401 缺或错 token / 403 scope 错）。body 用 Go 结构体对应上游 `NotifyReAttachRequest` / `NotifySafekeepersRequest` 的字段。
2. **无需 Pod IP 路由表**：compute 地址由确定性 Service DNS 推导（`http://neon-compute-<ep>.<ns>.svc.cluster.local:3080`），控制面按 `tenant_id` 扫描内存中的 endpoints，不维护 `endpoint → PodIP` 映射。
3. **转发逻辑**：notify 到达后在 SC 的 10s 超时内完成 locate + 内存 spec 更新，成功返回 200（失败见第 4 点）；推送由后台 worker 异步 `POST /configure` 完成，指数退避 5s→60s，2xx 成功、401/403/412/5xx 可重试、400 放弃。
4. **失败语义**：locate 失败或无 shard 返回 **503**（SC 映射为 `NotifyError::Unavailable`，持续退避重试）；**不再使用 423**（旧实现被 SC 视为 Busy/fatal，只重试 3 次），也未实现 429 路径。
5. **SC 配置**：`charts/neon/values.yaml` 透传 `neon-storage-controller.settings.controlPlaneUrl=http://neon-control-plane-svc:8080`，JWT 经 `CONTROL_PLANE_JWT_TOKEN` env 注入；SC 始终以严格模式运行（无 `--dev`、无 `--use-local-compute-notifications`）。
6. **兜底**：`src/reconciler.go` 30s 周期用路由指纹对比 SC 真相，覆盖「通知丢失 / 控制面重启 / SC 去重跳过」；`src/status_reconciler.go` 15s 修正 endpoint 运行态。

**遗留限制**

- 控制面单副本、状态存 ConfigMap，无 Lease 选主；重启窗口内丢失的通知依赖周期对账补齐。
- `compute_ctl_config.jwks` 只在 compute 启动时读取，升级后存量 compute 需重建一次。
- 用户 API 无鉴权、JWT 无过期、SC 单主节点、本地盘不具跨节点 HA（见根目录 README「限制」）；启用多 pageserver + 自动迁移前需评估对账压力。

### 1.6 Proxy 运行条件与正确性校验（必读，决定 proxy 能否真正可用）

**适用范围**：proxy 目前**不在 umbrella 依赖中**，`charts/neon/values.yaml` 也没有 `neon-proxy` 段。要使用 proxy 必须单独部署 `charts/neon-proxy` 并按下述三项配置；本节是「未来启用 proxy」的检查清单，**不是当前已落实的配置**。

**结论先行**：控制面的 proxy 兼容接口**已实现**，路径与字段与 `cplane_proxy_v1` 对齐（已逐行核对 `proxy/src/control_plane/client/cplane_proxy_v1.rs`），接口本身可正常工作。但按仓库当前默认值 proxy 跑不起来：`neon-proxy/values.yaml` 中 `settings.authBackend: "link"`、`settings.authEndpoint: ""`、`settings.controlplane_token: ""`、`settings.domain: ""`、`internalCa: null`，且 umbrella 没有任何覆盖；TLS、内部 CA、SNI/DNS 也都未配置。

**1) proxy 的 backend 选择机制（已核对 `proxy/src/binary/proxy.rs:779` `build_auth_backend`）**

- `--auth-backend ControlPlane`（别名 `cplane-v1`）→ 代码内 `AuthBackendType::ControlPlane` → 构造 `ControlPlaneClient::ProxyV1(NeonControlPlaneClient)`。
  > **已核对 `proxy/src/binary/proxy.rs:63-77`**：`AuthBackendType` 的合法值仅 `ControlPlane`(别名 `cplane-v1`)、`ConsoleRedirect`(别名 `link`，**默认**)，以及仅 `testing` 特性可用的 `Postgres`/`Local`。**`console` 不是合法枚举值**——若 chart 传 `--auth-backend console`，proxy 会在 clap 解析阶段直接启动失败（报 unknown variant）。故对接我们的 control plane 必须写 `ControlPlane`（或 `cplane-v1`）。
  >
- 该 client 的 **base URL 取自 `--auth-endpoint`（`-a`）参数**，即 `args.auth_endpoint`；JWT 取自 `args.control_plane_token`（环境变量 `NEON_PROXY_TO_CONTROLPLANE_TOKEN`）。
- **关键**：`neon-proxy` chart 的 deployment 模板把 `settings.authEndpoint` 渲染成 `-a` 参数——**即 `settings.authEndpoint` 就是 control-plane 基址**（与 chart 注释里「legacy console authenticate_proxy_request」的旧含义不同，当前代码已把它用作 cplane_proxy_v1 基址）。
- **必要条件 A**：部署 `neon-proxy` 时必须设置 `settings.authBackend: "ControlPlane"`（**切勿写 `console`，那是非法值，proxy 启动即崩**）+ `settings.authEndpoint: "http://neon-control-plane-svc:8080"` + `settings.controlplane_token`（chart 经 Secret `...-controlplane-token` 注入为 `NEON_PROXY_TO_CONTROLPLANE_TOKEN`）。三者缺一，proxy 不会去查本控制面。umbrella 当前**没有**这些覆盖，启用时需自行在独立 values 中指定（或先把 proxy 加入依赖）。

**2) TLS 是最大实践障碍（两处）**

- **客户端 → proxy**：Neon proxy 默认要求 TLS。仅当 `settings.domain` 非空时才挂 `--tls-key/--tls-cert`；没有证书则客户端必须 `sslmode=disable` 且 proxy 需允许非 TLS（标准镜像不允许，除非构建时带 `testing` 特性 + `--disable-pg-session-jwt`）。自托管最小部署二选一：
  - 用 `settings.useCertManager: true` + `settings.domain: "neon.local"` 由 cert-manager 签发 `*.neon.local` 证书，客户端 `sslmode=require`；
  - 或构建带 `testing` 特性的 proxy 镜像，加 `--disable-pg-session-jwt`，客户端 `sslmode=disable`。
- **proxy → compute**：proxy 以 TLS 连接 compute（postgres 5432），用 `NEON_INTERNAL_CA_FILE`（`internalCa`）校验 compute 证书。compute_ctl 默认生成自签证书，proxy 必须信任其 CA；自托管需共享内部 CA（把同一 CA 注入 proxy 的 `internalCa` 与 compute 的证书链），或在最小部署中放宽。
- **必要条件 B**：在 proxy 的部署 values 中明确 TLS 方案（cert-manager 或 testing 镜像 + 内部 CA），否则 proxy 与 compute 之间握手失败。

**3) SNI / 主机名路由（决定 proxy 能否解析出 endpoint_id）**

- proxy 从客户端连接的服务名（SNI 首标签）提取 `endpoint_id`，再拼成 `endpointish` 去查 `wake_compute`。即客户端必须以 `<endpoint_id>.<domain>` 连接（如 `ep-abc123.neon.local`）。
- 需要 DNS 或 `/etc/hosts` 把 `*.neon.local` 解析到 proxy Service；`settings.domain` 与连接串必须一致。`pgSniRouter` 是另一套 TLS SNI 直连 compute 特性，**非必需**，基础链路用普通 proxy Service 即可。
- **必要条件 C**：文档化连接串（`psql "postgresql://cloud_admin:<pw>@<endpoint_id>.neon.local:5432/neondb?sslmode=require"`，默认库为 `neondb`），并打通 DNS/hosts。

**4) 字段兼容性（已对齐，列此防回归）**

- `wake_compute` 响应 `WakeCompute.address` 必须是 `host:port`（集群内为 `neon-compute-ep-xxx.neon.svc.cluster.local:5432`，NodePort 模式为 `<nodePortExternalHost>:<nodePort>`），`server_name` 置 `null`，`aux.cold_start_info` 用合法枚举值（`unknown`/`warm`/`warm_cached`）。
- `get_endpoint_access_control.role_secret` 必须与该 endpoint 的 compute 角色 SCRAM 验证器**逐字节一致**（compute 写入 `pg_auth`、proxy 用 `scram::ServerSecret::parse` 校验）。
- 三个接口均带 `Authorization: Bearer <controlplane_token>`；控制面当前**不校验 token 与 method**（v1 宽松放行，未来启用 proxy 时建议补鉴权）。

**综合判定**：proxy 能否「正常使用」= 必要条件 A（backend+endpoint）+ B（TLS 两处）+ C（SNI/DNS）三者同时成立。当前 A/B/C **均未落实**（proxy 未部署、无覆盖值、无 TLS/DNS），默认链路仍是 client 直连 compute；接口侧已就绪，启用 proxy 时需按本节逐项补齐，尤其 `authBackend` 必须用 `ControlPlane` 而非 `console`。

### 性能与可靠性

- 有状态组件使用 `local-path` StorageClass（`local-path-provisioner` 可选安装）或集群已有 SC；RWO 本地盘不具跨节点 HA，仅适用于开发/测试（见 `docs/image.md`）。umbrella 为 controller / pageserver / safekeeper / control-plane 配置 requests=limits（Guaranteed QoS）；**broker 的覆盖值 requests≠limits**，不属于 Guaranteed。
- 默认副本数：pageserver 2、safekeeper 3（奇数，满足 WAL 多数派，也满足 SC 严格模式 `--timeline-safekeeper-count>=3`）、storage-controller 1、broker 1、control-plane 1。可用区由 init 容器读取所在节点 `topology.kubernetes.io/zone` 动态探测（需 `nodes: get` RBAC），未打标签的节点上 Pod 会停在 Init。
- 探针：control-plane `/healthz`、SC `/status` `/live` `/ready`、pageserver/safekeeper 各自的 HTTP 管理端口；`metrics.*` 开关全部关闭。
- PDB：pageserver `maxUnavailable: 1`、safekeeper `minAvailable: 2`、broker `minAvailable: 1`、SC `maxUnavailable: 1`；control-plane 的 values 中有 `podDisruptionBudget` 项但**无对应模板**，其余 chart 无 PDB。
- 卸载清理：control-plane 的 pre-delete 钩子 Job 按标签删除动态 compute Deployment/Service/Pod 与 `neon-cp-state` ConfigMap。

## 实现要点（防回归）

- 严格复用现有模板范式：`templates/_helpers.tpl` 的 fullname/label/selectorLabels 命名约定、values 结构（image/service/resources/metrics/ingress/securityContext 等），避免引入新范式。
- 子 chart 的 values 字段以 `settings.*` 为主：`storageControllerUrl`、`brokerEndpoint`、`controlPlaneUrl`、`jwtSecretName`、`computeImage` / `computeServiceType` / `computeCtlPort` / `reconcileIntervalSeconds` 等；umbrella 通过同名段透传，不要再引入 `scUrl`、`global.objectStorage`、`defaultTenant`、静态节点清单等已废弃字段。
- 密钥不落 values 明文，统一走外部 Secret（`global.jwt.existingSecret` / `global.storage.bucket.existingSecret` / `global.storageController.databaseUrl.existingSecret`）；新增代码注释使用中文（遵循 AGENTS.md）。
- 保持对现有独立 chart 的向后兼容：umbrella 只通过 values 覆盖增强；`neon-storage-controller` 的明文 `settings.databaseUrl` 入口已移除，旧命令会静默失效并被渲染期校验拦下（见 README「破坏性变更」）。
- 监控：`metrics.enabled` / `metrics.serviceMonitor.enabled` 全部默认 `false`，umbrella **没有**统一开启；ServiceMonitor 模板仍抓 `/metrics`，但 control-plane 未实现该路由，开启前需先补端点。
- **proxy 可用性三件套（见 1.6，当前均未落实）**：如未来单独部署 `neon-proxy`，必须设置 `settings.authBackend="ControlPlane"`（**不是 `console`**）、`settings.authEndpoint="http://neon-control-plane-svc:8080"`、`settings.controlplane_token`（经 Secret 注入），明确 TLS 方案（`useCertManager:true` + `domain:"neon.local"`，或 `testing` 镜像 + `--disable-pg-session-jwt`），对齐 proxy→compute 的 `internalCa` 与 compute 证书链，并文档化 `<endpoint_id>.<domain>:5432` + DNS/hosts。`settings.authEndpoint` 即 cplane_proxy_v1 基址，与 chart 注释中「legacy console」的旧含义不同；umbrella 当前既无 proxy 依赖也无这些覆盖值。

## 架构设计

```mermaid
graph TD
  U[umbrella: charts/neon] -->|依赖编排| SB[neon-storage-broker]
  U -->|依赖编排| SC[neon-storage-controller]
  U -->|依赖编排| PS[neon-pageserver 有状态]
  U -->|依赖编排| SK[neon-safekeeper 有状态]
  U -->|依赖编排| CP[neon-control-plane 服务]
  U -.->|可选, 默认关闭| LPP[local-path-provisioner]
  PX[neon-proxy 独立 chart 当前未部署] -.->|未来: console 兼容接口| CP
  CL[client] -->|直连 compute Service / NodePort| CM[compute Pod: compute_ctl]
  PS -->|re-attach 自注册| SC
  SK -->|sk-register sidecar 注册并激活| SC
  SC -->|PUT notify-attach / notify-safekeepers| CP
  CP -->|创建删除 tenant 与 timeline, locate, 列节点| SC
  CP -->|创建删除 compute Deployment 与 Service| K8S[(K8s API)]
  K8S -->|拉起| CM
  CM -->|GET compute spec| CP
  CP -->|POST configure 热更新 spec| CM
  SC -->|心跳/调度| PS
  SC -->|心跳/调度| SK
  SC -->|节点/租户/分片元数据持久化| PG[(外部 PostgreSQL 元数据)]
  PS -->|WAL 上传| EXT[(外部 MinIO 对象存储)]
  SK -->|归档| EXT
  CM -->|WAL 流| SK
  SK -->|WAL 流| PS
```

数据流（**当前，无 proxy**）：client → compute(postgres)（直连主 Service `neon-compute-<endpoint_id>.<ns>.svc.cluster.local:5432`，或 `NodePort` 模式下的 `<nodePortExternalHost>:<nodePort>`；用创建响应返回的明文密码）→ safekeeper(WAL 多数派) → pageserver(物化层) → 外部 MinIO(对象存储)。控制面**无 bootstrap**：pageserver / safekeeper 自注册进 SC，`POST /projects` 才创建 tenant/timeline 并动态拉起 compute；创建 endpoint 时经 SC 定位 pageserver、取 Active safekeeper 列表并生成 `ComputeSpec`；故障转移时由 SC notify + 控制面 `/configure` 推送 + 周期对账热更新路由。

### 分阶段实施（当前已落地 / 后续可选）

- **当前（默认部署，umbrella 6 个依赖）**：`neon-storage-broker` + `neon-storage-controller` + `neon-pageserver` + `neon-safekeeper` + `neon-control-plane`（5 个默认启用）+ `local-path-provisioner`（默认关闭，按需启用）；**无 `neon-minio` chart**（外部 MinIO）、**无 `neon-compute` chart**（动态拉起）、**无 `neon-proxy` 依赖**。client 直连 compute 验证端到端读写。
- **已一并落地的增强项**：SC notify 接收端点 + `/configure` 主动推送 + 30s 路由对账 + 15s 状态对账；proxy 兼容接口（已实现但未联调）；三个外部 Secret（`neon-jwt` / `bucket-credentials` / `storage-controller-pg-cluster`）；动态 compute 的 NodePort 对外 Service 与 pre-delete 卸载清理。
- **后续可选（phase-2，非默认）**：单独部署 `neon-proxy` 或把它加入 umbrella 依赖，并按 1.6 节落实 A（`authBackend=ControlPlane` + `authEndpoint` + `controlplane_token`）、B（TLS 两处 + `internalCa`）、C（SNI/DNS）；再补齐控制面接口鉴权、Lease 选主、`/metrics` 端点与自动密钥轮换等生产化能力。

**phase-2 启用 proxy 后**的数据流：client → proxy → compute，proxy 通过控制面已实现的 console 兼容接口完成鉴权与 compute 寻址。

**外部 PostgreSQL（仅 storage-controller 元数据后端，控制面不依赖）**：storage-controller 是控制面调度核心，但其全部元数据（节点注册、租户/分片位置、调度状态、attachment 服务状态）**不自带存储**，必须持久化到**外部 PostgreSQL**（`DATABASE_URL` 环境变量或 `--database-url` 启动参数，chart 用前者）。因此部署前需先准备一个可用的 PostgreSQL 实例/集群，并满足：

- 数据库由 `storage-controller-pg-cluster` Secret 提供连接串（`postgres://<user>:<pass>@<host>:5432/storage_controller` 形式），storage-controller 启动时自动建表/迁移（内置 migration，无 neon 定制扩展依赖，见 `storage_controller/src/persistence.rs`）。示例见 `docs/neon.yaml`，开发/测试可用根目录 `postgres-dev.yaml` 在集群内起一个最小 PG。
- 落地口径（最终实现，替代早期 `externalPostgres.existingSecret` 与明文 `settings.databaseUrl` 提法）：chart **不接收明文连接串**，只引用外部 Secret，取值链为 `neon-storage-controller.settings.databaseUrlSecretName` > `global.storageController.databaseUrl.existingSecret`，键名由 `...databaseUrlSecretKey` 指定、默认 `uri`；两者皆空时渲染阶段 `fail`。注入方式为 `env.secretKeyRef → DATABASE_URL`（`storage_controller` 只认 `--database-url` 或 `DATABASE_URL`，不支持文件）。
- 该 PostgreSQL **必须先于** storage-controller 就绪；umbrella **不部署**数据库，也不声明前置依赖，只引用 Secret（可由 External Secrets Operator / SOPS / Sealed Secrets 供给）。
- 连接串轮换与 Helm 解耦：只更新 Secret 再 `kubectl rollout restart deploy/<release>-storage-controller` 即可，无需 `helm upgrade`（env 注入不会热更新，故必须重启）。
- **控制面（`neon-control-plane`）不使用 PostgreSQL**：其状态采用「in-memory + ConfigMap」持久化（见「Control Plane 详细设计 → 服务内部状态」），无需任何外部 DB 依赖。若未来演进为功能完整的 console 需要关系型存储，再考虑在**同一 PostgreSQL 实例上建独立 `control_plane` 库（同实例异库，绝不混用 `storage_controller` 库/表）**——以隔离 SC 的 migration 主权、避免 `__diesel_schema_migrations` 冲突，并隔离故障域（重置控制面可 `DROP DATABASE control_plane` 而不误伤 SC）。
- 职责区分：外部 PostgreSQL **仅服务 storage-controller**；实际页面层/WAL 数据落在外部 MinIO（高吞吐对象存储）。两者不可混淆，MinIO 不能替代 PostgreSQL。
- 生产环境建议 PostgreSQL 启用主从/HA 与定期备份；若集群已有 PostgreSQL Operator（如 CloudNativePG），可直接复用其 Service 地址。

## 目录结构

以下为**当前仓库已落地**的相关文件（不再标注 `[NEW]`；仓库中不存在 `neon-minio` 与静态 `neon-compute` chart）：

```
charts/
├── neon/                                   # umbrella 总 chart
│   ├── Chart.yaml                          # 6 个 dependencies（file://../<name> + condition），无 neon-proxy
│   ├── Chart.lock                          # 解析出的子 chart 版本
│   ├── values.yaml                         # global.jwt / storage / storageController + 6 个子 chart 同名段
│   ├── charts/*.tgz                        # helm dependency build 产物
│   ├── templates/jwt-check.yaml            # 缺 global.jwt.existingSecret 时渲染期 fail
│   └── templates/NOTES.txt                 # 外部 Secret 键契约与轮换提示
├── neon-pageserver/
│   ├── values.yaml                         # settings(brokerEndpoint / storageControllerUrl / remoteStorage.prefixInBucket / pgDistribDir / nodeIdBase)、statefulSet(replicas=2 / 5Gi / local-path)
│   └── templates/
│       ├── statefulset.yaml                # StatefulSet + PVC + init-identity（AZ / remote_storage / control_plane_api_token / identity.toml / metadata.json）+ 探针
│       ├── configmap.yaml                  # pageserver.toml 模板（不含 remote_storage / control_plane_api_token）
│       ├── service.yaml / serviceaccount.yaml / node-reader-rbac.yaml
│       └── pod-disruption-budget.yaml / servicemonitor.yaml / _helpers.tpl
├── neon-safekeeper/
│   ├── values.yaml                         # replicas=3、nodeIdBase=2000、brokerEndpoint / storageControllerUrl、sk-register 开关与周期
│   └── templates/
│       ├── statefulset.yaml                # init-node-id（AZ + node-id）+ 主容器 + sk-register sidecar（upsert / Active / 周期对账 / 终止 Decomissioned）
│       ├── service.yaml / serviceaccount.yaml / node-reader-rbac.yaml
│       └── pod-disruption-budget.yaml / servicemonitor.yaml / _helpers.tpl   # 无 configmap.yaml
├── neon-control-plane/
│   ├── values.yaml                         # image、settings(storageControllerUrl / computeImage / computeServiceType / nodePortExternalHost / computeCtlPort / reconcileIntervalSeconds / reconfigureTimeoutSeconds / statusReconcileIntervalSeconds / domain / jwtSecretName)、cleanup
│   ├── build.sh / src/Dockerfile           # 镜像构建（alpine 运行时 + 预编译二进制）
│   ├── src/                                # Go 1.21：main / notify / reconciler / status_reconciler / compute / persist / scclient / spec / types / jwks / jwt / k8s / shard_test
│   └── templates/
│       ├── deployment.yaml                 # 单副本 + Recreate，--config /etc/neon/config.json，只投影 privateKey.pem + publicKey.pem（无 init 容器）
│       ├── configmap.yaml                  # config.json（SC 地址 / compute 参数 / 对账周期；无节点清单、无 default_tenant_id）
│       ├── service.yaml                    # ClusterIP :8080（用户 API / spec / notify / proxy 兼容接口同端口）
│       ├── serviceaccount.yaml             # ClusterRole + ClusterRoleBinding（deployments/pods/services/configmaps）
│       ├── pre-delete-cleanup-job.yaml     # 卸载前清理动态 compute 资源与 neon-cp-state
│       └── servicemonitor.yaml / _helpers.tpl
├── local-path-provisioner/                 # 可选 StorageClass（umbrella 默认关闭）
├── neon-storage-broker/                    # 既有 chart，umbrella 复用（仅 values 覆盖）
├── neon-storage-controller/                # 既有 chart，已定制：DATABASE_URL / JWT env 注入、--control-plane-url、渲染期 fail 校验
├── neon-proxy/                             # 独立 chart，**未纳入 umbrella**（phase-2 可选）
└── neon-pg-sni-router / neon-endpoint-storage / neon-storage-scrubber   # 独立 chart，未纳入 umbrella

根目录：jwt.py（生成 neon-jwt 的 8 键 Secret）、docs/neon.yaml（外部 Secret 示例）、postgres-dev.yaml（开发用 PG）、docs/image.md（本地镜像仓库 / StorageClass）
```

## 关键代码结构（配置契约）

以下为各组件配置与端口契约（**已逐项核对 neon 源码 `libs/pageserver_api/src/config.rs`、`pageserver/src/config.rs`、`safekeeper/src/bin/safekeeper.rs`、`storage_controller/src/main.rs`**）。

### pageserver（仅 `-D/--workdir` + TOML，无 CLI 覆盖参数）

> 关键：pageserver.toml **所有字段平铺在根级**，没有 `[broker_endpoint]`/`[storage]`/`[http]`/`[auth]` 嵌套 section 表（未知字段被静默忽略）；node id 来自 `identity.toml` 而非命令行。

```toml
# pageserver.toml：ConfigMap 渲染的静态部分（字段全部平铺在根级）
listen_pg_addr   = "0.0.0.0:64000"                  # pg/libpq 端口（compute 连接）
listen_http_addr = "0.0.0.0:9898"                   # HTTP 管理 API（SC 调用 / 探针）
broker_endpoint  = "http://neon-broker-svc:50051"   # storage_broker（WAL 流节点发现）
# 运行时强制必填；pageserver 在此基址后 .join("re-attach")，故必须带 /upcall/v1 前缀
control_plane_api       = "http://neon-storage-controller-svc:50051/upcall/v1"
auth_validation_public_key_path = "/etc/neon/jwt/publicKey.pem"
http_auth_type = "NeonJWT"
pg_auth_type   = "NeonJWT"
pg_distrib_dir = "/usr/local"                       # 镜像内 v{14..17} PostgreSQL 二进制根目录

# ↓ 以下 3 类字段**不在 ConfigMap 模板中**，由 init-identity 在 Pod 启动时追加：
# 1) 从 bucket-credentials Secret 读出坐标（唯一事实来源，values 中无副本；字段名 bucket_region 非 region）
remote_storage = { bucket_name = "<neon-bucket>", bucket_region = "<region>",
                   endpoint = "<s3-or-minio-endpoint>", prefix_in_bucket = "pageserver" }
# 2) 从 neon-jwt Secret 的 generationsApiJwtToken 键读出（scope=generations_api，PS→SC upcall；
#    control_plane_api_token 仅支持 TOML 内联，写进 ConfigMap 会明文泄密）
control_plane_api_token = "<generations_api JWT>"
# 3) 从所在节点 topology.kubernetes.io/zone 标签动态探测
availability_zone = "<运行时探测的节点 zone>"
```

- **`identity.toml`**（同 workdir，每 pod 唯一）：`id = nodeIdBase(1000) + ordinal`，是 node id 唯一真实来源。同一 init 容器还生成 **`metadata.json`**（`node_id`、FQDN、pg 64000 / http 9898 / https 9899 / grpc 51051、`availability_zone_id`），pageserver 启动时用它 `POST /upcall/v1/re-attach` 自注册进 SC。
- **环境变量**：`HADRON_NODE_IP_ADDRESS=<PodIP>`（Downward API `status.podIP`，re-attach 时向 SC 上报可达地址）；`NEON_AUTH_TOKEN` ← `safekeeperJwtToken`（`safekeeperdata` scope，pageserver→safekeeper）；AWS 凭证由 `bucket-credentials` 逐键 `env.secretKeyRef` 注入。JWT Secret 只投影 `publicKey.pem` + `generationsApiJwtToken`，bucket Secret 只投影 3 个坐标键。

### safekeeper（clap 参数，无 TOML 配置文件）

> 关键：safekeeper **没有** `--listen-grpc`（gRPC 已废弃）、**没有** `--control-plane-api`；SC 由 `sk-register` sidecar 注册获知 host/port，broker 仅用于 WAL 流节点发现。本 chart 的 templates 下**没有** `configmap.yaml`。

```text
safekeeper -D /var/lib/safekeeper \
  --id "$(cat /etc/neon/node-id)" \
  --listen-pg 0.0.0.0:5454 \
  --advertise-pg "<pod-fqdn>:5454" \
  --listen-http 0.0.0.0:7676 \
  --broker-endpoint http://neon-broker-svc:50051 \
  --pg-auth-public-key-path /etc/neon/jwt/publicKey.pem \
  --availability-zone "$(cat /etc/neon/availability-zone)"
# init-node-id 写 /etc/neon/node-id（nodeIdBase 2000 + ordinal）与 AZ（读节点 topology.kubernetes.io/zone 标签）
# --advertise-pg 必须显式给 FQDN：否则 broker 广播 0.0.0.0，pageserver 无法连接
# --pg-auth-public-key-path：WAL 服务（pageserver/compute 连 safekeeper）JWT 公钥
```

**`sk-register` sidecar**（默认启用）：`POST <sc>/control/v1/safekeeper/{id}`（body 含 `id`、`region_id:"default"`、`version:1`、`host`、`port`、`http_port`、`https_port:null`、`active:null`、`availability_zone_id`；409 视为成功）+ `POST <sc>/control/v1/safekeeper/{id}/scheduling_policy`（`{"scheduling_policy":"Active"}`，**首字母大写的变体名**，小写会被 400 拒收）；默认 60s 周期对账，Pod 终止时置 `Decomissioned`。sidecar 只投影 `peerJwtToken`（`admin` scope）作为 `ADMIN_TOKEN`。

### storage_controller（严格模式，密钥走 env，**不连接 storage_broker**）

```text
# charts/neon-storage-controller/templates/deployment.yaml 实际生成的启动命令
storage_controller -l 0.0.0.0:50051 \
  --control-plane-url http://neon-control-plane-svc:8080 \
  --address-for-peers http://$(POD_IP):50051/

# 其余参数（心跳 / 分片分裂 / 自动迁移等）按 settings.* 条件追加，默认不传。

# 同一 Pod 的 env（均来自外部 Secret，非明文 values）：
DATABASE_URL            ← storage-controller-pg-cluster.uri
PUBLIC_KEY              ← neon-jwt.publicKey.pem
PAGESERVER_JWT_TOKEN    ← neon-jwt.pageserverJwtToken    (pageserverapi)
SAFEKEEPER_JWT_TOKEN    ← neon-jwt.safekeeperJwtToken    (safekeeperdata)
CONTROL_PLANE_JWT_TOKEN ← neon-jwt.controlPlaneJwtToken  (controlplane)
PEER_JWT_TOKEN          ← neon-jwt.peerJwtToken          (admin)
```

- 上游 `Secrets::load_secret()` 支持「CLI 参数优先、其次环境变量」：`--database-url` / `--public-key` / `--jwt-token` / `--safekeeper-jwt-token` / `--control-plane-jwt-token` / `--peer-jwt-token` 与上表 env 一一对应。chart 选择 env 注入：多行 PEM 的换行不会被转义破坏，且 Helm release / manifest 中不含任何密钥。
- 严格模式（未传 `--dev`）启动即校验：缺少上述密钥或 `--control-plane-url` 会 `bail`（`storage_controller/src/main.rs:374-402`）；`--use-local-compute-notifications` 被禁止；`--timeline-safekeeper-count` 默认 3（不传即满足，故 safekeeper 至少 3 副本）。
- SC **不订阅 broker**（无 `--broker-endpoint` 参数）：safekeeper 由 `sk-register` sidecar 注册，pageserver 由 re-attach 自注册；broker 仅服务 safekeeper↔pageserver 的 WAL 流发现。

### 组件端口与心跳汇总

| 组件               | 端口                                                    | 用途                                          |
| ------------------ | ------------------------------------------------------- | --------------------------------------------- |
| storage-broker     | 50051 (gRPC/HTTP2)                                      | 节点发现 pub-sub（无状态，无需 PVC）          |
| pageserver         | 64000(pg) / 9898(http) / 9899(https) / 51051(grpc 实验) | compute 连接 / SC 管理 / 探针                 |
| safekeeper         | 5454(pg WAL) / 7676(http)                               | WAL 流 / 管理                                 |
| storage-controller | 50051(http, `-l`)                                       | 存储控制面 REST / 节点注册 / compute_hook     |
| control-plane      | 8080(http, ClusterIP `neon-control-plane-svc`)          | 用户 API / spec / notify / proxy 兼容接口     |
| compute            | 5432(postgres) / 3080(compute_ctl external http)        | 主 Service 恒 ClusterIP；NodePort 仅暴露 5432 |

**心跳 / 重连机制（已核对 `storage_controller/src/heartbeater.rs` 与 `pageserver/src/controller_upcall_client.rs`）**：

- **SC → pageserver**：周期 `GET /v1/utilization`（9898）判定 `Available`/`WarmingUp`/`Offline`；调度用 `PUT /v1/tenant/:id/location_config`（attach）。
- **SC → safekeeper**：类似周期健康检查（7676 端口）。
- **pageserver → SC**：启动 `POST /upcall/v1/re-attach`（上报 node 信息 + `empty_local_disk`），并周期 `POST /upcall/v1/validate`；失败无限重试。
- **safekeeper → SC**：主进程不主动调用；由 `sk-register` sidecar upsert + 显式激活 + 60s 对账维护注册状态（broker 只广播 `safekeeper_info` 给 WAL 流对端）。
- **SC → control-plane → compute**：调度变更后 `PUT /notify-attach` / `PUT /notify-safekeepers`；控制面重建 spec 并 `POST <compute-svc>:3080/configure` 热更新，另有 30s 路由指纹对账兜底。

以下为 `neon-control-plane` 服务（常驻 Deployment，自带镜像）的关键契约：

**1) 控制面 → storage-controller 的 REST 调用**（**无 bootstrap**；字段见 `libs/pageserver_api/src/controller_api.rs`，实现见 `src/scclient.go`）：

```text
# 启动（只读诊断）
GET  <sc>/control/v1/node                       # 打印已注册节点快照                          [admin]

# 用户 API / 对账内部调用
POST <sc>/v1/tenant                             # {new_tenant_id, shard_parameters:{count:1,stripe_size:1024}} [pageserverapi]
POST <sc>/v1/tenant/{tid}/timeline              # {new_timeline_id}（branch 时带 ancestor_timeline_id） [pageserverapi]
GET  <sc>/debug/v1/tenant/{tid}/locate          # tenant_locate：shard_params.count + shards[]  [admin]
GET  <sc>/control/v1/safekeeper                  # 只返回 Active                             [admin]
GET  <sc>/control/v1/node                       # locate 失败时的诊断                       [admin]
DELETE <sc>/v1/tenant/{tid}/timeline/{tlid}     # 删除 branch                               [pageserverapi]
DELETE <sc>/v1/tenant/{tid}                     # 删除 project                              [pageserverapi]
```

> 路径与 JWT scope 依据 `control_plane/src/storage_controller.rs` 及 SC `http.rs` 的 `check_permissions`：`v1/*`=`pageserverapi`、`control|debug/*`=`admin`、`status|ready`=无需 token。**易错点**：`tenant_create` 是 `v1/tenant`、`tenant_locate` 是 `debug/v1/tenant/{id}/locate`。**控制面从不调用** `POST control/v1/node`、`POST control/v1/safekeeper/{id}`、`.../scheduling_policy`——这些由各组件自注册负责。

**1.5) SC → control plane 的反向回调（upcall / compute_hook，已启用）**

storage_controller 启动**必须**配置 `--control-plane-url <cp>`（严格模式默认，缺失即 `bail`，见 `storage_controller/src/main.rs:388`；对应 neon_local 的 `control_plane_hooks_api`）。当 SC 因故障/调度把某租户 shard 迁移到新 pageserver 时，通过 `compute_hook` **主动回调**控制面，通知 compute 重新配置 `pageserver_connection_info`（否则 compute 仍连旧 pageserver）。

- **已实现**（见 1.5.1）：控制面接收 `PUT|POST /notify-attach`、`PUT|POST /notify-safekeepers`（body 对应 `pageserver_api::upcall_api` 的 `NotifyReAttachRequest` / `NotifySafekeepersRequest`），按 SC locate 重建 spec，并主动 `POST /configure` 推送给运行中的 compute；另有 30s 周期对账兜底通知丢失场景。不再依赖「compute 重连 + 自愈」，故障转移后无需人工删 Pod。
- 鉴权：`Authorization: Bearer <controlPlaneJwtToken>`（`controlplane` scope，也接受 `admin`）；locate 失败返回 **503** 让 SC 退避重试（不再是 423）。

**2) Endpoint 创建的 ComputeSpec 关键字段**（实际结构见 `src/spec.go`，上游定义见 `libs/compute_api/src/spec.rs`）：

```json
{
  "format_version": 1.0,
  "tenant_id": "<tid>",
  "timeline_id": "<timeline_id>",
  "mode": "Primary",
  "pageserver_connection_info": {
    "shard_count": 1,
    "stripe_size": 1024,
    "shards": { "0001": { "pageservers": [
      { "id": 1000, "libpq_url": "postgresql://<ps-fqdn>:64000", "grpc_url": "http://<ps-fqdn>:51051" }
    ]}}
  },
  "safekeeper_connstrings": ["<sk0>:5454", "<sk1>:5454", "<sk2>:5454"],
  "storage_auth_token": "<Ed25519-JWT: scope=tenant, tenant_id=<tid>>",
  "project_id": "<project_id>", "branch_id": "<branch_id>", "endpoint_id": "<endpoint_id>",
  "cluster": {
    "roles": [{"name":"cloud_admin","encrypted_password":"SCRAM-SHA-256$4096:<salt>$<StoredKey>:<ServerKey>"}],
    "databases": [{"name":"neondb","owner":"cloud_admin"}],
    "settings": [
      {"name":"shared_preload_libraries","value":"neon","vartype":"string"},
      {"name":"listen_addresses","value":"*","vartype":"string"}
    ]
  },
  "local_proxy_config": { "jwks": [] },
  "suspend_timeout_seconds": 0
}
```

> 关键契约（均为踩坑点）：
> - `shards` 的 key 是 `TenantShardId` 的 **4 位 hex 后缀**（示例 `0001`；unsharded 为 `0000`），不是数组下标；`shard_count=0` 时 `stripe_size` **必须为 `null`**。
> - `grpc_url` 仅当 pageserver 上报 gRPC 地址时下发（`omitempty`）；`libpq_url` 必须是 `postgresql://host:port`。
> - `cluster.roles[].encrypted_password` 必须是 `SCRAM-SHA-256$<iterations>:<salt>$<StoredKey>:<ServerKey>` 验证器（compute_ctl 在 `pg_helpers.rs:151` 据此判断 SCRAM）；**同一个字符串**必须作为 `get_endpoint_access_control.role_secret` 返回给 proxy。
> - `cluster.settings` 是 `GenericOption[]` 数组而非 map，且必须含 `shared_preload_libraries=neon` 与 `listen_addresses=*`；缺省角色 `cloud_admin`、缺省库 `neondb`。
> - `suspend_timeout_seconds` **必填**，当前置 0（不自动挂起）；`local_proxy_config.jwks` 下发空数组。`endpoint_storage_addr/token` 只在类型定义中保留，`neon-endpoint-storage` 未部署故不下发。

compute_ctl 拉取 spec 的真实路径（源自 `compute_tools/src/spec.rs:78`）：
`GET <control-plane-uri 根地址>/compute/api/v2/computes/<compute_id>/spec` → 返回 `{ spec: <上述 ComputeSpec>, status: "attached", compute_ctl_config: { jwks: [<OKP/Ed25519 公钥>] } }`。因此 compute Deployment 的启动参数应为 `--control-plane-uri http://neon-control-plane-svc:8080`（根地址，不带 `/compute/api/v2` 后缀）；`compute_ctl_config.jwks` 是 `/configure`、`/status` 等鉴权路由能工作的前提，且只在 compute 启动时读取。

**3) Proxy 兼容接口契约**（路径/字段严格对齐 `proxy/src/control_plane/client/cplane_proxy_v1.rs` 与 `messages.rs`；`<base>` 为 proxy 配置的 `control_plane_api` 基址 = 本服务 Service）：

```text
GET <base>/get_endpoint_access_control?endpointish=<ep>&role=cloud_admin&session_id=...
   → { "role_secret":"SCRAM-SHA-256$4096:<salt>$<StoredKey>:<ServerKey>",
       "allowed_ips":[], "allowed_vpc_endpoint_ids":[],
       "block_public_connections":false, "block_vpc_connections":false,
       "project_id":"<pid>", "account_id":null, "rate_limits":{} }

GET <base>/wake_compute?endpointish=<ep>&application_name=&session_id=...
   → { "address":"<compute-svc>.<ns>.svc.cluster.local:5432 或 <nodePortExternalHost>:<nodePort>",
       "server_name":null,
       "aux": { "endpoint_id":"<ep>", "project_id":"<pid>", "branch_id":"<bid>",
                "compute_id":"<ep>", "cold_start_info":"unknown" } }

GET <base>/endpoints/<ep>/jwks?session_id=...
   → { "jwks": [] }
```

`neon-proxy` 未纳入 umbrella；如未来启用，其 `settings.authEndpoint` 需指向本服务（`http://neon-control-plane-svc:8080`）。接口可带 `Authorization: Bearer <jwt>`，但控制面当前**不校验**、也不限制 method。注意路径是 `wake_compute`（非 `proxy_wake_compute`），查询参数为 `endpointish`（非 `endpoint`）；`role_secret` 在控制面重启后从 spec 的 `encrypted_password` 回退读取。

---

本任务为 Kubernetes Helm 部署方案（基础设施即代码），不涉及前端 UI 页面开发，因此不输出可视化界面设计。交付物为 Helm chart 模板、values.yaml、ConfigMap、StatefulSet/Deployment 清单与 Go 控制面源码（含中文注释），目标是在 k8s 集群中一条命令部署可端到端读写验证的 Neon 集群。当前仍属学习/开发测试定位，JWT 轮换、接口鉴权、控制面 HA、proxy 接入与监控等生产化能力见「当前实现状态速览」与根目录 README「限制」，列为后续扩展点。
