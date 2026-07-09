# Neon 私有化部署

> **分析时间**: 2026-07-06（初始分析），2026-07-09（根据代码更新）
> **上游仓库**: [neondatabase/neon](https://github.com/neondatabase/neon)
> **本地源码**: `/home/postgres/works/opensource/neon`
> **Helm Charts 仓库**: [chirpyli/helm-charts](https://github.com/chirpyli/helm-charts)
> **方案定位**: Kubernetes 部署 Neon，使用 Storage Controller（不部署 Control Plane），非生产级可用

---

## 一、需求定义

核心需求：

- 在 Kubernetes 中部署 Neon
- **不部署 Control Plane**，但部署 Storage Controller
- 不要求生产级可用，能运行 Neon 即可

### 1.1 依赖准备

Neon 部署依赖两个外部服务：S3 兼容对象存储 和 PostgreSQL 元数据库。**均由集群外部提供**，通过 Kubernetes Secret 引用连接信息。

| 外部服务             | 提供方式          | 说明                             |
| -------------------- | ----------------- | -------------------------------- |
| S3 兼容存储（MinIO） | 集群外 MinIO 实例 | Pageserver/Safekeeper 的远程存储 |
| PostgreSQL           | 集群外 PostgreSQL | Storage Controller 的元数据库    |

> 使用外部依赖意味着**不需要在集群内部署 MinIO 和 PostgreSQL**，只需创建对应的 Kubernetes Secret（`bucket-credentials` 和 `storage-controller-pg-cluster`）供组件引用。

#### 预备 S3（MinIO）

启动 MinIO 服务并创建存储桶：

```sh
# 启动 MinIO
minio server miniodata/ --console-address :9001

# 使用 mc 创建存储桶
mc alias set localminio http://192.168.232.128:9000 minioadmin minioadmin
mc mb localminio/neondata
```

| 项目     | 值                              |
| -------- | ------------------------------- |
| Console  | `http://192.168.232.128:9001` |
| S3 API   | `http://192.168.232.128:9000` |
| RootUser | `minioadmin`                  |
| RootPass | `minioadmin`                  |
| 存储桶   | `neondata`                    |

创建 Kubernetes Secret 供各 Neon 组件引用：

```yaml
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
```

> **注意**：Pageserver 和 Safekeeper 都通过 `envFrom` 引用此 Secret 来获取 S3 凭证。各组件使用独立的 key prefix 隔离不同 bucket 引用（见 [4.4 neonpageserver](#44-neon-pageserver--正常模式) 和 [4.5 neonsafekeeper](#45-neon-safekeeper)）。

#### 准备 PostgreSQL 数据库

创建 Storage Controller 专用数据库和用户：

```sql
CREATE USER storage_controller WITH PASSWORD 'storage_controller';
CREATE DATABASE storage_controller OWNER storage_controller;
```

| 项目   | 值                       |
| ------ | ------------------------ |
| Host   | `192.168.232.128:5432` |
| 数据库 | `storage_controller`   |
| 用户   | `storage_controller`   |
| 密码   | `storage_controller`   |

创建 Kubernetes Secret 供 Storage Controller 引用：

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: storage-controller-pg-cluster
  namespace: neon
type: Opaque
stringData:
  uri: "postgres://storage_controller:storage_controller@192.168.232.128:5432/storage_controller"
```

> **注意**：Storage Controller 启动时会自动执行数据库迁移（创建表结构），无需手动初始化 Schema。

---

## 二、方案 B 架构总览

### 2.1 组件拓扑

```
                         psql / Client
                              │
                              │ port 55433
                              ▼
                    ┌─────────────────┐
                    │  neon-compute    │  ◄── ✅ 已创建 Chart
                    │  (Deployment xN) │  MVP 单副本；
                    │  compute_ctl     │  读扩展可增副本
                    │  --config /      │  (同一 ConfigMap)
                    │    config.json   │  ← 静态 ComputeSpec (ConfigMap)
                    │  --dev           │
                    └───┬─────────┬───┘
                        │ WAL     │ GetPage@LSN
           ┌────────────┘         └─────────────┐
           ▼                                     ▼
  ┌─────────────────┐                  ┌─────────────────┐
  │ neon-safekeeper  │                  │ neon-pageserver  │  ◄── ✅ 已创建 Chart
  │ (StatefulSet x3) │  ◄── ✅ 已创建     │ (StatefulSet x1) │  MVP 单副本；
  │ → init-job 注册  │                  │ 正常模式         │  后续可扩至 xN
  │ → broker 连      │                  │ (非 emergency)   │
  │   (gRPC pub/sub) │                  │ → SC re_attach   │
  └────────┬────────┘                  │   (自注册)       │
           │                            │ → broker 连      │
           │                            │   (gRPC sub)     │
           │                            └───┬─────────┬────┘
           │  gRPC pub/sub                  │         │ HTTP
           ▼                                │         │ (SC 拉取
  ┌─────────────────┐                       │         │  /utilization)
  │ storage-broker   │◄── gRPC sub ─────────┘         │
  │ ✅ 已有 Chart    │                                 │
  │ (内存 pub-sub)   │                                 │
  └─────────────────┘                                 │
                                                      │
                      ┌───────────────────────────────┘
                      ▼
  ┌──────────────────────┐
  │ storage-controller    │  ✅ 已有 Chart
  │ (Deployment)          │
  │ + ext PostgreSQL      │  ◄── 外部提供
  │ + dummy-cp (HTTP 200) │  ◄── ✅ 已创建
  └──────────┬───────────┘
             │  S3 上传/下载 (外部 MinIO)
    ┌────────┼────────┐
    ▼        ▼        ▼
 ┌──────┐ ┌──────┐ ┌──────┐
 │ MinIO │ │ext PG│ │ ...  │
 │(S3)  │ │(SC元DB)│ │     │
 └──────┘ └──────┘ └──────┘

  ┌───────────────────┐
  │ dummy-cp           │  ◄── ✅ 已创建 (极简 HTTP Server)
  │ (dummy HTTP 200)   │      吸收 storage controller 的
  │ PUT /notify-*      │      compute hook notify 通知
  └───────────────────┘

  ┌───────────────────┐
  │ neon-init-job      │  ◄── ✅ 已创建 (Helm post-install Hook)
  │ (注册 safekeeper  │      通过 SC API 创建 tenant
  │  创建 tenant/      │      和 timeline，输出
  │  timeline)         │      tenant_id/timeline_id
  └───────────────────┘

  ┌───────────────────────────────────┐
  │ neon-storage-scrubber              │  ◄── ✅ 已创建 (CronJob)
  │ (pageserver-physical-gc)           │      S3 存储物理垃圾回收
  │ 每日凌晨 3 点运行                  │      清理过期 layer 文件
  └───────────────────────────────────┘

说明：
- storage-controller 不连接 broker（SC 通过直接 HTTP 与 pageserver/safekeeper 通信）
- safekeeper 不主动连接 SC（SC 通过 HTTP 拉取 safekeeper 的 /utilization 信息）
- pageserver 连接 broker（gRPC subscribe）和 SC（HTTP re_attach + API 调用）
- pageserver 使用 StatefulSet + Headless Service：SC 需要点对点路由到指定 pageserver 节点（`base_url()` 用 headless DNS），`registration_match()` 要求地址在节点生命周期内不变；单副本时等同 Deployment 行为
- compute 使用 Deployment：compute 是**客户端**（主动向外连接 pageserver/safekeeper），没有外部系统需要回调到特定 compute Pod；SC 的 compute hook 通知发往 dummy-cp 而非 compute；同租户多副本（同一 ConfigMap 共享）时 Deployment 完美满足
- storage-scrubber 是独立的 S3 GC CronJob，直接扫描 S3 bucket，不需要连接 SC 或 CP
```

### 2.2 与 docker-compose / emergency mode 方案的本质区别

| 维度               | emergency mode 方案                   | 本方案（方案 B）                                                          |
| ------------------ | ------------------------------------- | ------------------------------------------------------------------------- |
| Storage Controller | ❌ 不部署                             | ✅ 部署                                                                   |
| Pageserver 模式    | `control_plane_emergency_mode=true` | 正常模式，通过 SC re_attach                                               |
| Generation 安全    | ❌ 无（有脑裂风险）                   | ✅ 有 generation 保护                                                     |
| Tenant 创建        | pageserver API 直接创建               | Storage Controller API 调度创建                                           |
| Safekeeper 注册    | 无（手动配置）                        | Storage Controller 自动管理                                               |
| 多 Pageserver      | ❌ 不支持                             | ✅ 支持（StatefulSet + Headless Service 提供稳定网络标识，SC 点对点路由） |
| Control Plane      | ❌                                    | ❌（dummy HTTP server 替代）                                              |
| Compute Spec       | 静态 config.json                      | 静态 config.json（相同）                                                  |
| initJob            | 调 pageserver API                     | 调 SC API                                                                 |

---

## 三、组件清单

### 3.1 组件依赖与 Chart 状态

| 组件                                | 运行必需 | Chart 状态 | 说明                                                           |
| ----------------------------------- | :------: | :--------: | -------------------------------------------------------------- |
| **外部 MinIO（S3 兼容存储）** | 🔴 必需 |  外部提供  | S3 兼容存储，通过`bucket-credentials` Secret 引用            |
| **外部 PostgreSQL**           | 🔴 必需 |  外部提供  | SC 元数据库，通过`storage-controller-pg-cluster` Secret 引用 |
| **neon-storage-broker**       | 🟡 必需 |  ✅ 已创建  | 事件消息代理，已适配私有化部署 |
| **neon-storage-controller**   | 🔴 必需 |  ✅ 已创建  | 存储编排，已添加 `--dev` 支持并配置 compute hook 到 dummy server |
| **neon-pageserver**           | 🔴 必需 |  ✅ 已创建 | **正常模式**（非 emergency），连接 SC；StatefulSet + Headless Service + PVC |
| **neon-safekeeper**           | 🔴 必需 |  ✅ 已创建 | WAL 持久化，3 副本 StatefulSet + Headless Service + PVC |
| **neon-compute**              | 🔴 必需 |  ✅ 已创建 | PostgreSQL 计算节点，Deployment + 静态 config.json ConfigMap |
| **neon-dummy-cp**             | 🟡 必需 |  ✅ 已创建 | 极简 Python HTTP Server，吸收 compute hook 通知 |
| **neon-init-job**             | 🔴 必需 |  ✅ 已创建 | Helm post-install Hook Job：等待依赖就绪 → 注册 safekeeper → 创建 tenant/timeline |
| **neon-storage-scrubber**     | 🟡 可选 |  ✅ 已创建 | S3 存储物理垃圾回收 CronJob（`pageserver-physical-gc`），每日定时运行 |
| **neon-stack**                | 🔴 必需 |  ✅ 已创建 | Umbrella Chart，统一编排 8 个子 Chart |

> **外部依赖模式**：MinIO 和 PostgreSQL 均由集群外部提供，只需创建对应的 Kubernetes Secret（`bucket-credentials` 和 `storage-controller-pg-cluster`），无需在集群内部署这些组件。Pageserver/Safekeeper 通过 Secret 引用 S3，Storage Controller 通过 Secret 引用 PostgreSQL。

### 3.2 关键设计决策

#### dummy-cp：解决 compute hook 通知没有去处的问题

Storage Controller 的 Reconciler 在完成 tenant attach 后会调用 `ComputeHook::notify_attach()`。没有 Control Plane 时：

- `StrictMode::Strict` + `control_plane_url=None` → **启动即报错退出**
- `StrictMode::Strict` + `control_plane_url=Some(dummy)` → notify 失败 → 日志 ERROR → reconciler 无限重试

**解决方案**：部署一个已返回 `200 OK` 的极简 HTTP Server，配置为 `control_plane_url`。Notify 请求得到 200 响应，不触发重试，无 ERROR 日志。

#### postgresql：Storage Controller 元数据库

Storage Controller 的调度状态（节点信息、tenant→pageserver 映射、generation 等）持久化在 PostgreSQL 中。**由集群外部提供**，通过 `storage-controller-pg-cluster` Secret 引用连接串。

#### 静态 ComputeSpec：Compute 如何知道 pageserver/safekeeper 地址

compute_ctl 通过 `--config /path/to/config.json` 获取 spec。initJob 在创建 tenant/timeline 后从 SC API 查询调度结果，组装成 ComputeSpec 写入 ConfigMap，compute Pod 挂载使用。

---

## 四、缺失组件详细设计

### 4.1 外部 MinIO / S3 兼容存储

> MinIO 由集群外部提供，不在集群内部署。通过 `bucket-credentials` Secret 供 Pageserver 和 Safekeeper 引用。

**前置条件**：已在外部启动 MinIO 服务并创建存储桶 `neondata`（见 [1.1 依赖准备](#11-依赖准备)）。

**需要的 Kubernetes Secret**：

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: bucket-credentials
  namespace: neon
stringData:
  AWS_ACCESS_KEY_ID: "minioadmin"
  AWS_ENDPOINT_URL: "http://<minio-host>:9000"
  AWS_REGION: "us-east-1"
  AWS_SECRET_ACCESS_KEY: "minioadmin"
  BUCKET_NAME: "neondata"
```

Pageserver 和 Safekeeper 通过 `envFrom` 引用此 Secret 来获取 S3 凭证，各组件使用独立的 `prefix_in_bucket` 隔离数据。

---

### 4.2 外部 PostgreSQL — SC 元数据库

> PostgreSQL 由集群外部提供，不在集群内部署。通过 `storage-controller-pg-cluster` Secret 供 Storage Controller 引用。

**前置条件**：已在外部 PostgreSQL 中创建专用数据库和用户（见 [1.1 依赖准备](#11-依赖准备)）：

```sql
CREATE USER storage_controller WITH PASSWORD 'storage_controller';
CREATE DATABASE storage_controller OWNER storage_controller;
```

**需要的 Kubernetes Secret**：

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: storage-controller-pg-cluster
  namespace: neon
type: Opaque
stringData:
  uri: "postgres://storage_controller:storage_controller@<pg-host>:5432/storage_controller"
```

Storage Controller 启动时会自动执行数据库迁移（创建表结构），无需手动初始化 Schema。

---

### 4.3 `neon-storage-controller` — 已有 Chart 的配置适配

已有 Chart 需要关注以下配置：

#### `--dev` 模式：启动必需

Storage Controller 的 `--dev` 模式是本方案的关键依赖。**必须启用**，否则 SC 在 Strict 模式下会因为缺少 secrets 和 `--control-plane-url` 而拒绝启动。

`--dev` 模式放松了以下 4 项启动检查（`storage_controller/src/main.rs:366-409`）：

| 检查项                                                                                             | Strict 模式（生产）    | `--dev` 模式                                                                              |
| -------------------------------------------------------------------------------------------------- | ---------------------- | ------------------------------------------------------------------------------------------- |
| `public_key` / `pageserver_jwt_token` / `safekeeper_jwt_token` / `control_plane_jwt_token` | 缺一即拒绝启动         | 可选，缺失则隐式禁用对应 auth                                                               |
| `--control-plane-url`                                                                            | 必须设置，否则拒绝启动 | 可选，但不设置 + 不设`--use-local-compute-notifications` 则在 compute hook 被调用时 panic |
| `--use-local-compute-notifications`                                                              | 禁止使用               | 允许使用（测试专用）                                                                        |
| `--timeline-safekeeper-count`                                                                    | 必须 ≥ 3              | 可 < 3（如 1 或 2）                                                                         |

> **注意**：`--dev` 模式放过了启动检查，但如果 `--control-plane-url` 和 `--use-local-compute-notifications` 都未设置，reconciler 调用 `ComputeHook::notify_attach()` 时仍会 panic（`compute_hook.rs:892` 行 `unwrap()`）。因此**仍需设置 `--control-plane-url` 指向 dummy-cp**。

> **部署现状**：当前 `neon-storage-controller` Chart 的 `deployment.yaml` 模板已支持 `--dev` 命令行参数（通过 `{{ .Values.settings.devMode }}` 条件启用），无需手动修改模板。`neon-stack` Umbrella Chart 的 `values.yaml` 中已默认设置 `devMode: true`。

#### 必须配置项

```yaml
neon-storage-controller:
  settings:
    # 启用 dev 模式（deployment.yaml 已通过 settings.devMode 支持 --dev 参数）
    devMode: true
    # 数据库连接串（直接填入，SC 通过 envFrom secretRef 从 Secret 注入环境变量）
    databaseUrl: "postgres://storage_controller:storage_controller@<pg-host>:5432/storage_controller"
    # 关键：指向 dummy HTTP server
    controlPlaneUrl: "http://neon-dummy-cp:8080"
    controlPlaneJwtToken: ""  # dev 模式下 jwt token 可选，dummy server 不做认证
    # dev 模式下 safekeeper 数量可以少于 3
    timelineSafekeeperCount: 3  # 通过 --timeline-safekeeper-count 参数传入 SC
    # 注意：SC 不需要 broker 连接！SC 通过直接 HTTP 与 pageserver/safekeeper 通信
```

> **重要**：SC 使用扁平结构的 values（如 `controlPlaneUrl`），而非嵌套结构（如 `controlPlane.endpoint`）。
> SC **不使用 broker**（源码中无任何 broker/message-queue 依赖），与 pageserver/safekeeper 通过 HTTP 直接通信。

#### compute hook 通知流程

```
SC Reconciler → notify_attach()
             → HTTP PUT http://neon-dummy-cp:8080/notify-attach      (通知 pageserver 分片位置变更)
             → HTTP PUT http://neon-dummy-cp:8080/notify-safekeepers (通知 safekeeper 成员变更)
             → 200 OK → 正常继续
```

dummy server 返回 200，reconciler 不会设置 `pending_compute_notification=true`，不产生 ERROR 日志。

#### `--dev` 模式对各组件的连锁影响

由于 dev 模式下 SC 不携带 JWT token 访问下游组件，所有被 SC 调用的组件必须配置为 Trust 认证：

| 组件       | 配置                              | 说明                                                 |
| ---------- | --------------------------------- | ---------------------------------------------------- |
| Pageserver | `http_auth = "Trust"`           | SC 调用其`/location_config` 等 API 时无 JWT        |
| Safekeeper | 不传`--pg-auth-public-key-path` | SC 调用其 mgmt API 时无 JWT                          |
| Dummy CP   | 不校验 Authorization header       | SC 调用 compute hook 时无`control_plane_jwt_token` |

---

### 4.4 `neon-pageserver` — 正常模式（非 emergency）

**这是与 docker-compose 方案最大的区别：pageserver 运行在正常模式，通过 Storage Controller 进行 tenant attach。**

#### 启动命令

```bash
pageserver -D /data/.neon -c /config/pageserver.toml
```

#### pageserver.toml

```toml
# 服务发现
broker_endpoint = "http://neon-storage-broker:50051"

# 监听地址
listen_pg_addr = "0.0.0.0:6400"
listen_http_addr = "0.0.0.0:9898"

# PostgreSQL 发行版目录
pg_distrib_dir = "/usr/local/"

# 远程存储（外部 MinIO）
remote_storage = {
  endpoint = "http://<minio-host>:9000",
  bucket_name = "neondata",
  bucket_region = "us-east-1",
  prefix_in_bucket = "/pageserver"
}

# Storage Controller 连接（非 emergency mode）
# 注意：路径末尾必须是 /upcall/v1（upload coords upcall 端点），否则 re_attach 会 404
control_plane_api = "http://neon-storage-controller:50051/upcall/v1"

# IO 模式
virtual_file_io_mode = "buffered"

# 认证：默认 Trust（无需 JWT）
http_auth = "Trust"

# 本地盘驱逐策略：磁盘使用率 80% 开始驱逐冷数据
[disk_usage_based_eviction]
enabled = true
max_usage_pct = 80
min_avail_bytes = 0
period = "60s"
```

> **关键**：`control_plane_emergency_mode` 不设置（默认为 `false`），pageserver 启动时向 SC 发起 `re_attach` upcall，其中**内嵌节点注册信息（从 `metadata.json` 读取）**。SC 的 `re_attach` 处理函数在内部自动调用 `node_register()`，无需外部显式调用 `POST /control/v1/node`。

#### Pageserver 自注册机制（与 Safekeeper 的关键区别）

Pageserver 和 Safekeeper 的注册机制有**本质不同**：

|               | Pageserver                                                            | Safekeeper                                                               |
| ------------- | --------------------------------------------------------------------- | ------------------------------------------------------------------------ |
| 注册方式      | **自注册**：启动时 re-attach 请求中内嵌 `NodeRegisterRequest` | **外部注册**：需要外部脚本调用 `POST /control/v1/safekeeper/:id` |
| 前提条件      | 数据目录下需存在`metadata.json`                                     | 无前提条件（但必须外部调用）                                             |
| 注册时机      | 每次启动                                                              | 仅首次部署时（稳定 DNS 无需重注）                                        |
| init-job 角色 | **无需注册** pageserver                                         | **必须注册** safekeeper                                            |

**`metadata.json` 是 pageserver 自注册的关键**。源码路径：`pageserver/src/controller_upcall_client.rs:156-251`：

```rust
// re_attach() 方法：
let metadata_path = conf.metadata_path();  // = /data/.neon/metadata.json
let register = match tokio::fs::read_to_string(&metadata_path).await {
    Ok(metadata_str) => {
        let m: NodeMetadata = serde_json::from_str(&metadata_str)?;
        Some(NodeRegisterRequest {
            node_id: conf.id,
            listen_pg_addr: m.postgres_host,
            listen_pg_port: m.postgres_port,
            listen_http_addr: m.http_host,
            listen_http_port: m.http_port,
            listen_grpc_addr: m.grpc_host,
            listen_grpc_port: m.grpc_port,
            availability_zone_id: az_id,
        })
    }
    Err(NotFound) => {
        // metadata.json 不存在 → register = None
        // re-attach 仍可继续，但节点必须已被外部预先注册
        None
    }
};

// POST /upcall/v1/re-attach，请求体中包含 register 字段
// SC 端 service.rs:re_attach() 内部调用 node_register()
```

#### metadata.json

```json
{
  "host": "<POD_FQDN>",
  "port": 6400,
  "http_host": "<POD_FQDN>",
  "http_port": 9898,
  "grpc_host": "<POD_FQDN>",
  "grpc_port": 50051,
  "https_port": 0,
  "availability_zone_id": "az1"
}
```

> **关键**：`http_host` 和 `host` 必须使用 StatefulSet 的 **Headless Service DNS**（如 `neon-pageserver-0.neon-pageserver-headless.neon.svc.cluster.local`），而非 `0.0.0.0`。
> 原因：SC 通过 `base_url()` 构造 `http://<http_host>:9898` 向指定 pageserver 发送 HTTP 请求，必须使用可路由的单 Pod DNS 地址；`0.0.0.0` 是从外部无法连接的本地地址。
>
> **字段名说明**：Neon 源码中 `NodeMetadata` 结构体使用 `#[serde(rename = "host")]` 和 `#[serde(rename = "port")]`（源码：`libs/pageserver_api/src/config.rs:34-49`），因此 JSON 中的字段名是 `host`/`port` 而非 `postgres_host`/`postgres_port`。

由 initContainer 在首次启动时动态生成 `/data/.neon/metadata.json` 和 `/data/.neon/identity.toml`：

```yaml
initContainers:
  - name: init-metadata
    image: "{{ .Values.initContainer.repository }}:{{ .Values.initContainer.tag }}"
    env:
      - name: POD_NAME
        valueFrom:
          fieldRef:
            fieldPath: metadata.name
      - name: POD_NAMESPACE
        valueFrom:
          fieldRef:
            fieldPath: metadata.namespace
    command:
      - sh
      - -c
      - |
        HEADLESS_DNS="${POD_NAME}.neon-pageserver-headless.${POD_NAMESPACE}.svc.cluster.local"
        if [ ! -f /data/.neon/metadata.json ]; then
          mkdir -p /data/.neon
          cat > /data/.neon/metadata.json <<EOF
        {
          "host": "${HEADLESS_DNS}",
          "port": 6400,
          "http_host": "${HEADLESS_DNS}",
          "http_port": 9898,
          "grpc_host": "${HEADLESS_DNS}",
          "grpc_port": 50051,
          "https_port": 0,
          "availability_zone_id": "az1"
        }
        EOF

          # identity.toml — pageserver 节点 ID，启动时必需
          # baseId + pod 序号 = 节点唯一 ID
          ORDINAL="${POD_NAME##*-}"
          NODE_ID=$(({{ .Values.baseId }} + ORDINAL))
          cat > /data/.neon/identity.toml << IDEOF
        id = ${NODE_ID}
        IDEOF

          # 让 nonroot 主容器也能写入 /data/.neon/ 目录
          chmod 777 /data/.neon
        else
          chmod 777 /data/.neon
        fi
    volumeMounts:
      - name: data
        mountPath: /data
```

> **注意**：与文档早期设计不同，`identity.toml` 由 initContainer 动态计算（`baseId + ordinal`），而非 Helm 模板渲染。这确保了 StatefulSet 的每个 Pod 获得正确的唯一 ID，即使在 Umbrella Chart 的 release 名称发生变化时也无需调整。

#### identity.toml

```toml
id = {{ baseId + ordinal }}  # 由 initContainer 动态计算，baseId + pod 序号
```

> **说明**：`identity.toml` 由 initContainer 动态生成而非 Helm 模板渲染。`baseId` 通过 values.yaml 配置（MVP 时 `baseId=1, replicas=1`），initContainer 通过 `$(POD_NAME##*-)` 提取 ordinal 后计算 `NODE_ID = baseId + ordinal`。多 Pageserver 场景下，如 `baseId=100, replicas=3`，则 `pageserver-0` id=100，`pageserver-1` id=101，`pageserver-2` id=102。

#### 本地盘存储结构

Pageserver 的本地盘（`workdir`，即 `-D` 指定的目录）存储以下关键数据：

| 路径                                         | 内容                                      | 说明                                 |
| -------------------------------------------- | ----------------------------------------- | ------------------------------------ |
| `tenants/<tenant_id>/timelines/`           | **Layer 文件**（delta/image layer） | 核心数据，从 S3 拉取后缓存到本地 SSD |
| `tenants/<tenant_id>/location_config.json` | tenant 位置配置                           | SC 分配的位置信息                    |
| `tenants/<tenant_id>/heatmap`              | 访问热度图                                | 用于 eviction 决策                   |
| `metadata.json`                            | 节点元数据（自注册用）                    | initContainer 创建                   |
| `deletion/`                                | 删除队列                                  | 待清理的 remote layer 列表           |
| `basebackup_cache/`                        | Basebackup 缓存                           | 加速 basebackup 请求                 |

> **关键**：本地盘是 pageserver 的**缓存层**，数据以 S3 为真实来源。本地 SSD 存放热数据（最近访问的 layer），冷数据根据 `disk_usage_based_eviction` 策略自动驱逐。默认配置：`max_usage_pct = 80`，即磁盘使用率达 80% 开始驱逐。

#### StatefulSet 选型理由

**为什么必须使用 StatefulSet 而非 Deployment**？

SC 与 Pageserver 之间的通信是**点对点**的，而非负载均衡：

```
SC 调用 node.base_url() → http://<http_host>:9898 → 必须到达指定的 pageserver Pod
```

源码中的 `registration_match()`（`storage_controller/src/node.rs:134-143`）要求 `http_addr`、`pg_addr` 等核心地址在节点生命周期内**不变**：

```rust
pub(crate) fn registration_match(&self, register_req: &NodeRegisterRequest) -> bool {
    self.id == register_req.node_id
        && self.listen_http_addr == register_req.listen_http_addr  // 必须一致！
        && self.listen_http_port == register_req.listen_http_port
        && self.listen_pg_addr == register_req.listen_pg_addr       // 必须一致！
        && self.listen_pg_port == register_req.listen_pg_port
        && self.availability_zone_id == register_req.availability_zone_id
}
```

地址不匹配会被拒绝（`ApiError::Conflict`）。

| 场景                               |                     Deployment                     |                   StatefulSet                   |
| ---------------------------------- | :------------------------------------------------: | :----------------------------------------------: |
| **Pod 地址稳定性**           | 重启后 IP 变，Service DNS 指向所有 Pod（负载均衡） |   Headless DNS 永不变（`ps-0.ps-hl.ns.svc`）   |
| **SC 点对点路由**            | ❌ Service DNS 是 load-balance，无法定向到指定 Pod |           ✅ headless DNS 直达目标 Pod           |
| **`registration_match()`** |  ❌ 重启后新 IP →`http_addr` 变化 → Conflict  | ✅ Headless DNS →`http_addr` 不变 → 始终匹配 |
| **多副本**                   |   ❌ 所有 Pod 共享 Service DNS，SC 无法区分节点   |       ✅ 每 Pod 有唯一 DNS，SC 可精确路由       |
| **PVC 管理**                 |               ❌ 需手动创建 N 个 PVC               |       ✅`volumeClaimTemplates` 自动创建       |

**结论**：MVP 阶段 `replicas=1`，StatefulSet 行为与 Deployment 等价（可用 ClusterIP Service 简化访问）；`replicas≥2` 时 StatefulSet 是唯一选择。

#### Chart 设计要点

| 要素                        | 设计                                                                                                                                                                                                                          |
| --------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **工作负载**          | **StatefulSet**（replicas=1，可扩至 N）                                                                                                                                                                                 |
| **镜像**              | `neondatabase/neon:latest`（`neon-stack` 中覆盖为 `ghcr.io/neondatabase/neon:latest`）                                                                                                                                    |
| **命令**              | 启动脚本通过 `awk` 将 S3 环境变量占位符（`$(AWS_ENDPOINT_URL)` 等）替换到 `pageserver.toml` 中，然后 `exec pageserver -D /data/.neon`                                                                                 |
| **Headless Service**  | `neon-pageserver-headless`（无 ClusterIP），提供稳定 Pod DNS 用于 SC 点对点路由                                                                                                                                             |
| **ClusterIP Service** | 可选：`neon-pageserver`，单副本模式下简单访问（通过 `service.clusterIP.enabled` 开关控制）                                                                                                                                                                          |
| **配置**              | ConfigMap 挂载 `pageserver.toml`（含 `$(AWS_ENDPOINT_URL)` 等占位符）；identity.toml 和 metadata.json 由 initContainer 动态生成                                                                                                         |
| **持久化**            | `volumeClaimTemplates` 自动创建 PVC 挂载 `/data`（**本地 SSD**，存放 layer 缓存数据），默认 `10Gi`                                                                                                                   |
| **initContainer**     | 使用 busybox 镜像，通过 `POD_NAME`/`POD_NAMESPACE` 动态生成 `/data/.neon/metadata.json` 和 `/data/.neon/identity.toml`（仅首次），并 `chmod 777 /data/.neon` 确保 nonroot 主容器可写                                       |
| **注册 SC**           | **自动**：启动时 re-attach 内嵌注册（headless DNS 为 advertise 地址），无需 init-job 显式调用                                                                                                                           |
| **环境变量**          | **必须设置 `NODE_IP_ADDR`**（`podIP`）用于 re-attach 请求中的节点地址字段；通过 `envFrom` 从 `bucket-credentials` Secret 注入 S3 凭证                                                                              |
| **健康检查**          | HTTP GET `/v1/status` 端口 9898；startupProbe（10s 间隔，30 次失败阈值）、livenessProbe（15s 间隔）、readinessProbe（15s 间隔）                                                                                              |
| **暴露端口**          | 6400（pg 协议）、9898（HTTP API）、50051（gRPC）                                                                                                                                                                             |
| **多副本扩展**        | replicas > 1 时，SC 通过 headless DNS 精确路由到每个 Pod，`baseId + ordinal` 分配合一 node_id                                                                                                                               |
| **S3 凭证注入**       | `pageserver.toml` 中 S3 配置使用 `$(AWS_ENDPOINT_URL)` 等 shell 变量占位符，由主容器启动脚本通过 awk 在运行时替换为环境变量实际值                                                                                          |

#### values.yaml

```yaml
# === StatefulSet 配置 ===
replicas: 1       # MVP 单副本；后续可扩至 N（需手动调整）
baseId: 1         # node_id = baseId + ordinal；

image:
  repository: neondatabase/neon
  tag: latest
  pullPolicy: Always

# init 容器镜像（metadata 初始化）
initContainer:
  repository: busybox
  tag: latest
  pullPolicy: IfNotPresent

settings:
  brokerEndpoint: "http://neon-storage-broker:50051"
  listenPgAddr: "0.0.0.0:6400"
  listenHttpAddr: "0.0.0.0:9898"
  pgDistribDir: "/usr/local/"
  controlPlaneApi: "http://neon-storage-controller:50051/upcall/v1"  # 注意 /upcall/v1 路径
  virtualFileIoMode: "buffered"
  httpAuth: "Trust"

remoteStorage:
  prefixInBucket: "/pageserver"

s3Credentials:
  existingSecret: "bucket-credentials"

# 本地盘驱逐策略
diskUsageEviction:
  enabled: true
  maxUsagePct: 80
  minAvailBytes: 0

persistence:
  enabled: true
  size: 10Gi      # 默认 10Gi，生产环境建议 100Gi+

# 网络
service:
  headless:
    name: ""      # 空值则自动使用 {release}-neon-pageserver-headless
  clusterIP:
    enabled: true # 单副本时启用 ClusterIP Service
    name: ""      # 空值则自动使用 {release}-neon-pageserver

resources:
  limits:
    cpu: "4"
    memory: 4Gi
  requests:
    cpu: "1"
    memory: 512Mi
```

---

### 4.5 `neon-safekeeper` — WAL 安全守护者

Safekeeper 通过 Storage Controller API 注册，SC 自动管理 safekeeper 的调度状态。

> **注意**：Safekeeper **不主动连接 SC**。SC 通过 safekeeper 暴露的 HTTP 端口（7676）主动拉取 `/utilization` 信息来感知 safekeeper 的在线状态。

#### 启动命令

```bash
SAFEKEEPER_ID=$(({{ baseId }} + $(echo ${POD_NAME##*-})))
SAFEKEEPER_PG_PORT=$(echo {{ listenPg }} | cut -d: -f2)
exec safekeeper \
  --listen-pg=0.0.0.0:5454 \
  --listen-http=0.0.0.0:7676 \
  --advertise-pg=${POD_NAME}.${HEADLESS_SVC}:${SAFEKEEPER_PG_PORT} \
  --id=${SAFEKEEPER_ID} \
  --broker-endpoint=http://neon-storage-broker:50051 \
  -D /data \
  --remote-storage="{endpoint = \"$(AWS_ENDPOINT_URL)\", bucket_name = \"$(BUCKET_NAME)\", bucket_region = \"$(AWS_REGION)\", prefix_in_bucket = \"/safekeeper/\"}"
```

> **关键变化**：添加了 `--advertise-pg` 参数使用 Pod Headless DNS 而非 `0.0.0.0`，使 Pageserver 能正确连接到 Safekeeper（`0.0.0.0` 会被解析为 localhost）。S3 配置通过 shell 变量展开从 `bucket-credentials` Secret 注入。

#### SC 注册（由 neon-init-job 统一注册）

Safekeeper 本身**不会自我注册**到 SC。注册由外部发起。本方案由 `neon-init-job` 统一执行：

```bash
# neon-init-job 中注册所有 safekeeper（使用稳定 DNS 名，Pod 重启后自动生效）
for i in 0 1 2; do
  curl -sf -X POST "http://neon-storage-controller:50051/control/v1/safekeeper/${i}" \
    -H "Content-Type: application/json" \
    -d "{
      \"id\": ${i},
      \"region_id\": \"default\",
      \"version\": 1,
      \"host\": \"neon-safekeeper-${i}.neon-safekeeper-headless.neon.svc.cluster.local\",
      \"port\": 5454,
      \"http_port\": 7676,
      \"availability_zone_id\": \"az1\",
      \"active\": true
    }"
done
```

**为什么 init-job 是最优方案**（详见 [ADR-8](#adr-8-safekeeper-注册采用-neon-init-job-统一注册)）：

| 方案                       |    每次 Pod 重启    |      SC 未就绪时      |   额外容器   | 关键问题               |
| -------------------------- | :-----------------: | :--------------------: | :----------: | ---------------------- |
| **init-job**（推荐） |     无需重注册     |      Job 重试即可      |      0      | DNS 不变，心跳自动恢复 |
| postStart hook             | 重新 curl（无意义） | **Pod 启动失败** |      0      | 耦合容器生命周期       |
| sidecar                    | 重新 curl（无意义） |         可重试         | 3 个额外容器 | 过度设计               |

核心原因：**StatefulSet + Headless Service 提供永不变更的稳定 DNS**，SC 心跳需要的 `host` 字段指向 DNS 名而非 IP，
Pod 重启后 DNS 自动解析到新 IP，SC 的心跳检测（每 5s 调用 `/utilization`）自动恢复在线状态，**无需重注册**。

#### 本地盘存储结构

Safekeeper 的本地盘（`workdir`，即 `-D` 指定的目录）存储以下关键数据：

| 路径                           | 内容                   | 说明                                  |
| ------------------------------ | ---------------------- | ------------------------------------- |
| `<tenant_id>/<timeline_id>/` | WAL segment 文件       | 接收自 compute 的 WAL，上传 S3 前暂存 |
| `control_file`               | 持久化状态（term/LSN） | WAL 共识协议的持久化状态              |
| `<id>`                       | 节点 ID 文件           | 持久化 safekeeper 节点 ID             |

> **关键**：Safekeeper 本地盘是 WAL 的**暂存区**。WAL 通过 `wal_backup` 模块持续上传到 S3，上传完成后可被 eviction 驱逐。Safekeeper 通过 `max_global_disk_usage_ratio`（磁盘使用率上限，默认 0=不限制）和 `max_timeline_disk_usage_bytes`（每个 timeline 的 WAL 磁盘上限）控制本地盘使用。

#### Chart 设计要点

| 要素                    | 设计                                                         |
| ----------------------- | ------------------------------------------------------------ |
| **工作负载**      | StatefulSet（3 副本，Quorum 要求），`podManagementPolicy: Parallel` |
| **镜像**          | `neondatabase/neon:latest`（`neon-stack` 中覆盖为 `ghcr.io/neondatabase/neon:latest`） |
| **节点 ID**       | `${baseId} + ordinal`，由主容器启动脚本动态计算（`echo ${POD_NAME##*-}`） |
| **持久化**        | PVC 挂载`/data`（**本地 SSD**，存放 WAL segment），默认 `10Gi` |
| **S3 凭证**       | 通过 `envFrom` 从 `bucket-credentials` Secret 注入 |
| **SC 注册**       | 由`neon-init-job` 统一注册（一次性，DNS 名稳定无需重注册） |
| **健康检查**      | HTTP GET `/v1/status` 端口 7676；startupProbe（10s 间隔，30 次失败阈值）、livenessProbe + readinessProbe（15s 间隔） |
| **暴露端口**      | 5454（pg 协议）、7676（HTTP）                                |
| **Advertise PG**  | `${POD_NAME}.${HEADLESS_SVC}:${PG_PORT}`，通过环境变量传入 Pod DNS 名 |

#### values.yaml

```yaml
image:
  repository: neondatabase/neon
  tag: latest
  pullPolicy: Always

replicas: 3
baseId: 1

settings:
  brokerEndpoint: "http://neon-storage-broker:50051"
  listenPg: "0.0.0.0:5454"
  listenHttp: "0.0.0.0:7676"

remoteStorage:
  prefixInBucket: "/safekeeper/"

s3Credentials:
  existingSecret: "bucket-credentials"

persistence:
  enabled: true
  size: 10Gi      # 默认 10Gi，生产环境建议 50Gi+

# 磁盘使用控制
diskUsage:
  maxGlobalDiskUsageRatio: 0.0    # 0=不限制，推荐 0.6~0.8
  maxTimelineDiskUsageBytes: 0    # 0=不限制
  globalDiskCheckInterval: "60s"

resources:
  limits:
    cpu: "4"
    memory: 4Gi
  requests:
    cpu: "1"
    memory: 512Mi
```

---

### 4.6 `neon-compute` — 计算节点（静态 ComputeSpec + LFC 本地盘）

Compute 节点通过 `shared_preload_libraries = 'neon'` 加载 neon 扩展，neon 扩展内置 **LFC（Local File Cache）**——在本地文件系统中缓存从 pageserver 读取的数据页面，显著减少网络 I/O。

#### LFC 本地盘说明

LFC 是 neon 计算节点中**可选的本地缓存层**，原理如下：

```mermaid
graph LR
    A[PostgreSQL 请求页面] --> B{shared_buffers}
    B -->|命中| C[返回内存中的页面]
    B -->|未命中| D{LFC 本地盘}
    D -->|命中| E[返回本地缓存的页面]
    D -->|未命中| F[从 Pageserver 拉取]
    F --> G[写入 LFC 本地盘]
    G --> C
```

**LFC 关键特性**（源码：`pgxn/neon/file_cache.c`）：

| 特性                 | 说明                                                                                           |
| -------------------- | ---------------------------------------------------------------------------------------------- |
| **存储形式**   | 所有关系的所有块存储在**单个文件**中，通过共享哈希表寻址                                 |
| **Chunk 粒度** | 默认 128 个页面（1MB）为一个 chunk，减少哈希表内存开销                                         |
| **淘汰策略**   | 基于双向链表的 LRU 算法                                                                        |
| **生命周期**   | **每次 PostgreSQL 启动时重建**（`BasicOpenFile + O_TRUNC`），因此 LFC 不需要持久化 PVC |
| **动态调整**   | 支持运行时通过`neon.file_cache_size_limit` 调整大小（`fallocate(FALLOC_FL_PUNCH_HOLE)`）   |
| **优雅降级**   | 磁盘故障时自动禁用 LFC 并打印 WARNING，不影响系统正常运行                                      |

**LFC 路径设计**：

LFC 文件默认在 PostgreSQL 的 `pgdata` 目录下创建（`neon.file_cache_path` GUC 默认值为 `"file.cache"`）。
使用独立的 `emptydir` 或 `hostPath` 挂载点可以：

- 将 LFC 文件放在**本地 NVMe/SSD** 上，与容器根文件系统隔离
- 避免 LFC 的 I/O 影响容器根文件系统性能
- 如果使用 `emptydir`，Pod 重启后 LFC 自动重建（原生行为，不需要持久化 PVC）

**LFC 相关 PostgreSQL GUC**：

| GUC                            | 默认值         | 说明                                           |
| ------------------------------ | -------------- | ---------------------------------------------- |
| `neon.file_cache_path`       | `file.cache` | LFC 文件路径（可指向 raw device）              |
| `neon.max_file_cache_size`   | 取决于内存     | 最大缓存大小（MB），postmaster 启动时确定      |
| `neon.file_cache_size_limit` | 动态           | 软限制（MB），运行时可通过 vm_monitor 动态调整 |
| `neon.file_cache_chunk_size` | 128 页(1MB)    | 每个 chunk 包含的页面数                        |

#### config.json（ComputeSpec，由 ConfigMap 模板生成）

```json
{
  "spec": {
    "format_version": 1.0,
    "cluster": {
      "cluster_id": "k8s-deploy",
      "name": "default",
      "state": "active",
      "roles": {{ .Values.roles | toJson }},
      "databases": [{
        "name": "postgres",
        "owner": "cloud_admin"
      }],
      "settings": [
        {"name": "fsync", "value": "off", "vartype": "bool"},
        {"name": "wal_level", "value": "logical", "vartype": "enum"},
        {"name": "port", "value": "55433", "vartype": "integer"},
        {"name": "shared_buffers", "value": "1MB", "vartype": "string"},
        {"name": "listen_addresses", "value": "0.0.0.0", "vartype": "string"},
        {"name": "shared_preload_libraries", "value": "neon,pg_stat_statements", "vartype": "string"},
        {"name": "neon.file_cache_path", "value": "/lfc/file.cache", "vartype": "string"},
        {"name": "neon.max_file_cache_size", "value": "4096", "vartype": "integer"}
      ]
    },
    "tenant_id": "{{ .Values.tenantId }}",
    "timeline_id": "{{ .Values.timelineId }}",
    "pageserver_connstring": "host=<pod-0.headless-svc> port=6400",
    "safekeeper_connstrings": [
      "neon-safekeeper-0.neon-safekeeper-headless:5454",
      "neon-safekeeper-1.neon-safekeeper-headless:5454",
      "neon-safekeeper-2.neon-safekeeper-headless:5454"
    ],
    "mode": "Primary",
    "storage_auth_token": null,
    "suspend_timeout_seconds": 3600
  },
  "compute_ctl_config": {
    "jwks": {
      "keys": []
    }
  }
}
```

> **关键差异**：与早期设计不同，实际 ComputeSpec 使用 `{"spec": {...}}` 嵌套结构（顶层有 `spec` 和 `compute_ctl_config` 两个字段），而非扁平结构。
> - `safekeeper_connstrings` 格式为 `"host:port"`（如 `"sk-0.headless:5454"`），匹配 WalProposer 的解析器
> - `pageserver_connstring` 支持逗号分隔的多 pageserver：`"host=ps-0.headless port=6400,host=ps-1.headless port=6400"`
> - `roles` 由 Helm `toJson` 从 values 动态生成，支持多个角色配置
> - `compute_ctl_config.jwks.keys` 为空数组（dev 模式无需密钥）

#### 启动命令

```bash
compute_ctl \
  --pgdata /var/db/postgres/compute \
  -C "postgresql://cloud_admin@0.0.0.0:55433/postgres" \
  -b /usr/local/bin/postgres \
  --compute-id "compute-1" \
  --config /config/config.json \
  --dev
```

#### Deployment 选型理由

**Compute 使用 Deployment 而非 StatefulSet，即使多副本场景下也满足需求。**

核心原因是 **Compute 是客户端而非服务端**——与 Pageserver 的通信模式根本不同：

```
Compute（客户端）                               Pageserver（服务端）
─────────────────                               ────────────────────
pg_neon ─── PQconnectStartParams() ──→ pageserver     (主动连接)
WalProposer ─── PQconnectStartParams() ──→ safekeepers  (主动连接)
compute_ctl ─── 不接收外部回调            ←── SC 回调 HTTP (被动接受)
```

**为什么 Deployment 满足要求**：

| 维度                   | Pageserver（必须 StatefulSet）                   | Compute（Deployment 满足）                                     |
| ---------------------- | :----------------------------------------------- | :------------------------------------------------------------- |
| **连接发起方**   | SC 主动回调 Pageserver                           | Compute**主动**向外连接 PS/SK                            |
| **地址解析方式** | SC 用`base_url()` 路由 → 必须直达指定 Pod     | `PQconnectStartParams()` 使用 host:port → 可走 Service 转发 |
| **身份稳定性**   | `registration_match()` 校验 `http_addr` 不变 | `compute_id` 仅用于日志/指标，不做路由                       |
| **持久存储**     | 必须 PVC（S3 layer 缓存）                        | 不需要 PVC（LFC 每次`O_TRUNC` 重建）                         |
| **SC 回调**      | —                                               | SC →**dummy-cp**，不直接回调 compute                    |
| **同配置多副本** | —                                               | 共用同一 ConfigMap → 同一 tenant/timeline → 天然适合         |

**多副本场景分析**：

| 场景                                               |   Deployment 可行性   | 说明                                                                                                            |
| -------------------------------------------------- | :--------------------: | --------------------------------------------------------------------------------------------------------------- |
| **同租户读扩展**（同一 config.json，多副本） |        ✅ 完美        | ClusterIP Service 负载均衡到任意 Pod；pg_neon 各自独立连接 pageserver；仅一个 Primary 能写 WAL（neon 层面保证） |
| **多租户**（不同 config.json per Pod）       | ⚠️ 需独立 Deployment | 不同 Pod 需要不同 tenant_id/timeline_id；应创建独立 Deployment + Service 对（一个租户一对）                     |
| **Pod 重启**                                 |           ✅           | LFC 自动重建（`O_TRUNC`），pgdata 为临时目录，无持久状态依赖                                                  |

**结论**：MVP 单副本使用 Deployment，后续扩展到多副本（同一租户的读扩展）时 Deployment **无需任何架构变更**，直接调大 `replicas` 即可。多租户场景下应创建独立的 Deployment 实例而非混部。

#### Chart 设计要点

| 要素                    | 设计                                                                                                |
| ----------------------- | --------------------------------------------------------------------------------------------------- |
| **工作负载**      | Deployment（replicas=1，可扩至 N），`RollingUpdate` 策略                                             |
| **镜像**          | `ghcr.io/neondatabase/compute-node-v16:latest`                                                      |
| **命令**          | `compute_ctl --pgdata /var/db/postgres/compute --config /config/config.json --compute-id "compute-1" --dev` |
| **config.json**   | ConfigMap 挂载 `/config/config.json`，由 Helm 模板按 values 动态生成（含 `spec` + `compute_ctl_config` 嵌套结构） |
| **LFC 本地盘**    | `emptyDir` 挂载 `/lfc`（本地 NVMe/SSD），LFC 文件路径 `neon.file_cache_path = /lfc/file.cache`        |
| **pgdata 目录**   | `emptyDir` 挂载 `/var/db/postgres`（父目录），由 compute_ctl 自行管理 `compute` 子目录               |
| **持久化**        | 无需 PVC（LFC 每次启动自动 `O_TRUNC` 重建，pgdata 为临时目录）                                       |
| **initContainer** | busybox 等待 pageserver 和 safekeeper 就绪；创建 `/lfc` 目录；`OTEL_SDK_DISABLED=true` 禁用 OpenTelemetry |
| **健康检查**      | `pg_isready` 检查 55433 端口；startupProbe（10s 间隔，30 次失败阈值）                                |
| **暴露端口**      | 55433（PostgreSQL 协议）、3080（compute_ctl HTTP）                                                   |
| **多副本扩展**    | replicas > 1 时，同一 ConfigMap 共享，ClusterIP Service 负载均衡；适合同租户读扩展                   |

#### values.yaml

```yaml
# === Deployment 配置 ===
replicas: 1

image:
  repository: ghcr.io/neondatabase/compute-node-v16
  tag: latest
  pullPolicy: Always

pgVersion: 16
computeId: "compute-1"
port: 55433
devMode: true

# 由 initJob 填充（tenant 创建后写入）
tenantId: ""
timelineId: ""

# Pageserver 连接（支持多副本）
pageservers:
  replicas: 1
  podNamePrefix: "neon-stack-neon-pageserver"
  headlessService: "neon-stack-neon-pageserver-headless"
  port: 6400

# 兼容单副本配置
pageserver:
  host: "neon-pageserver"
  port: 6400

# Safekeeper 连接
safekeepers:
  - host: "neon-safekeeper-0.neon-safekeeper-headless"
    port: 5454
  - host: "neon-safekeeper-1.neon-safekeeper-headless"
    port: 5454
  - host: "neon-safekeeper-2.neon-safekeeper-headless"
    port: 5454

# PostgreSQL 角色配置
roles:
  - name: "postgres"
    encrypted_password: "SCRAM-SHA-256$4096:..."
    options:
      - name: "SUPERUSER"
        value: null
        vartype: "bool"

# LFC（Local File Cache）本地盘配置
lfc:
  maxFileCacheSizeMB: 4096
  mountPath: "/lfc"

resources:
  limits:
    cpu: "4"
    memory: 4Gi
  requests:
    cpu: "1"
    memory: 512Mi
```

---

### 4.7 `neon-dummy-cp` — 极简 HTTP Server

**作用**：吸收 Storage Controller 的 compute hook 通知，返回 200 OK。

**SC 会调用的具体端点**（源码 `compute_hook.rs`）：

| 端点                                   |   HTTP 方法   | 用途                                                                | 处理要求           |
| -------------------------------------- | :-----------: | ------------------------------------------------------------------- | ------------------ |
| `/notify-attach`                     | **PUT** | 通知 pageserver 分片位置变更                                        | 返回 200 OK        |
| `/notify-safekeepers`                | **PUT** | 通知 safekeeper 成员变更                                            | 返回 200 OK        |
| `/compute/api/v2/computes/{id}/spec` | **GET** | compute 拉取配置（仅当 compute 使用`--control-plane-uri` 模式时） | 本方案不经过此路径 |

**方案选择**（最简单）：

- 使用极小 Python HTTP Server
- 部署为 Deployment（1 副本），Service ClusterIP
- dummy-cp 需处理 **GET**、**POST**、**PUT** 三种方法（实际 SC 只调用 PUT，但完整处理更健壮）

#### 部署方式

```yaml
# neon-dummy-cp 使用 python http server
apiVersion: apps/v1
kind: Deployment
metadata:
  name: neon-dummy-cp
spec:
  replicas: 1
  selector:
    matchLabels:
      app: neon-dummy-cp
  template:
    metadata:
      labels:
        app: neon-dummy-cp
    spec:
      containers:
        - name: server
          image: python:3.11-slim
          command:
            - python3
            - -c
            - |
              from http.server import HTTPServer, BaseHTTPRequestHandler
              class Handler(BaseHTTPRequestHandler):
                  def do_GET(self):
                      self.send_response(200)
                      self.end_headers()
                      self.wfile.write(b'{"status":"ok"}')
                  def do_POST(self):
                      self.send_response(200)
                      self.end_headers()
                      self.wfile.write(b'{"status":"ok"}')
                  def do_PUT(self):
                      self.send_response(200)
                      self.end_headers()
                      self.wfile.write(b'{"status":"ok"}')
              HTTPServer(('0.0.0.0', 8080), Handler).serve_forever()
          ports:
            - containerPort: 8080
```

也可以用更轻量的 nginx 或直接使用 K8s 的 ExternalName Service 指向 localhost，但独立的 dummy server 最可靠。

---

### 4.8 `neon-init-job` — 初始化引导（Helm post-install Hook）

这是整个方案的核心编排 Job，作为 **Helm post-install Hook** 自动执行，负责：

1. 等待所有依赖组件就绪
2. 向 Storage Controller 注册 safekeeper 节点（pageserver 已通过 re-attach 自注册）
3. 通过 Storage Controller API 创建 tenant
4. 创建 timeline
5. 输出 tenant_id 和 timeline_id 供参考

#### Job 实现特点

- **Helm Hook 机制**：`"helm.sh/hook": post-install`，在 `helm install` 完成后自动触发
- **一次性执行**：`helm.sh/hook-delete-policy: before-hook-creation,hook-succeeded`，成功即清理
- **自动清理**：`ttlSecondsAfterFinished: 300`，完成后 5 分钟自动删除
- **可重试**：`restartPolicy: OnFailure`，依赖未就绪时自动重试
- **灵活 ID 生成**：如果 `values` 中预设了 `tenantId`/`timelineId` 则使用，否则通过 `/proc/sys/kernel/random/uuid` 自动生成

#### Job 逻辑

```bash
#!/bin/bash
set -e

SC="{{ .Values.settings.storageControllerEndpoint }}"
PS="{{ .Values.settings.pageserverEndpoint }}"
SK_REPLICAS={{ .Values.settings.safekeeper.replicas }}
SK_BASE_ID={{ .Values.settings.safekeeper.baseId }}
SK_POD_PREFIX="{{ .Values.settings.safekeeper.podNamePrefix }}"
SK_HEADLESS="{{ .Values.settings.safekeeper.headlessService }}"
SK_HTTP_PORT={{ .Values.settings.safekeeper.httpPort }}
SK_PG_PORT={{ .Values.settings.safekeeper.pgPort }}
TENANT_ID="{{ .Values.settings.tenantId }}"
TIMELINE_ID="{{ .Values.settings.timelineId }}"
PG_VERSION={{ .Values.settings.pgVersion }}
NAMESPACE="$(cat /var/run/secrets/kubernetes.io/serviceaccount/namespace)"

echo "[1/6] Waiting for Storage Controller..."
until curl -sf "${SC}/ready"; do sleep 2; done

echo "[2/6] Waiting for Pageserver..."
until curl -sf "${PS}/v1/status"; do sleep 2; done

echo "[3/6] Waiting for Safekeepers..."
i=0
while [ "$i" -lt "$SK_REPLICAS" ]; do
  SK_HOST="${SK_POD_PREFIX}-${i}.${SK_HEADLESS}.${NAMESPACE}.svc.cluster.local"
  until curl -sf "http://${SK_HOST}:${SK_HTTP_PORT}/v1/status"; do sleep 2; done
  i=$((i + 1))
done

echo "[4/6] Registering Safekeepers with Storage Controller..."
i=0
while [ "$i" -lt "$SK_REPLICAS" ]; do
  SK_ID=$((SK_BASE_ID + i))
  SK_HOST="${SK_POD_PREFIX}-${i}.${SK_HEADLESS}.${NAMESPACE}.svc.cluster.local"
  curl -sf -X POST "${SC}/control/v1/safekeeper/${SK_ID}" \
    -H "Content-Type: application/json" \
    -d "{
      \"id\": ${SK_ID},
      \"region_id\": \"default\",
      \"version\": 1,
      \"host\": \"${SK_HOST}\",
      \"port\": ${SK_PG_PORT},
      \"http_port\": ${SK_HTTP_PORT},
      \"availability_zone_id\": \"az1\",
      \"active\": true
    }"
  i=$((i + 1))
done

echo "[5/6] Creating Tenant..."
# 如果未预设 tenant_id，则自动生成
if [ -z "${TENANT_ID}" ]; then
  TENANT_ID=$(cat /proc/sys/kernel/random/uuid | tr -d '-')
fi
curl -sf -X POST "${SC}/v1/tenant" \
  -H "Content-Type: application/json" \
  -d "{\"new_tenant_id\": \"${TENANT_ID}\", \"placement_policy\": {\"Attached\": 0}}"

echo "[6/6] Creating Timeline..."
if [ -z "${TIMELINE_ID}" ]; then
  TIMELINE_ID=$(cat /proc/sys/kernel/random/uuid | tr -d '-')
fi
curl -sf -X POST "${SC}/v1/tenant/${TENANT_ID}/timeline" \
  -H "Content-Type: application/json" \
  -d "{\"new_timeline_id\": \"${TIMELINE_ID}\", \"mode\": \"Bootstrap\", \"pg_version\": ${PG_VERSION}}"

echo "Done! TENANT_ID=${TENANT_ID} TIMELINE_ID=${TIMELINE_ID}"
```

> **重要**：如果使用预设的 `tenantId`/`timelineId`，需同时确保 `neon-compute` 的 `tenantId`/`timelineId` 与其一致。当前 `neon-stack` values 中已预设了一对固定的 UUID。

---

### 4.9 `neon-stack` — Umbrella Chart

统一编排所有子 Chart。

#### Chart.yaml

```yaml
apiVersion: v2
name: neon-stack
description: Neon Serverless Postgres - Private Deployment (Storage Controller + No Control Plane)
type: application
version: 0.1.0
appVersion: "latest"

dependencies:
  - name: neon-storage-broker
    version: ">=1.4.0"
    repository: "file://../neon-storage-broker"
    condition: neon-storage-broker.enabled
  - name: neon-storage-controller
    version: ">=1.19.0"
    repository: "file://../neon-storage-controller"
    condition: neon-storage-controller.enabled
  - name: neon-dummy-cp
    version: "0.1.0"
    repository: "file://../neon-dummy-cp"
    condition: neon-dummy-cp.enabled
  - name: neon-pageserver
    version: "0.1.0"
    repository: "file://../neon-pageserver"
    condition: neon-pageserver.enabled
  - name: neon-safekeeper
    version: "0.1.0"
    repository: "file://../neon-safekeeper"
    condition: neon-safekeeper.enabled
  - name: neon-init-job
    version: "0.1.0"
    repository: "file://../neon-init-job"
    condition: neon-init-job.enabled
  - name: neon-storage-scrubber
    version: ">=1.4.0"
    repository: "file://../neon-storage-scrubber"
    condition: neon-storage-scrubber.enabled
  - name: neon-compute
    version: "0.1.0"
    repository: "file://../neon-compute"
    condition: neon-compute.enabled
```

#### 全局 values.yaml（核心配置）

```yaml
global:
  # === 外部 S3（MinIO）— 通过 Secret 引用 ===
  s3:
    existingSecret: "bucket-credentials"

  # === 外部 PostgreSQL — 通过 Secret 引用 ===
  storageController:
    endpoint: "http://neon-stack-neon-storage-controller-svc:50051"
    existingPgUriSecret: "storage-controller-pg-cluster"

  storageBroker:
    endpoint: "http://neon-stack-neon-storage-broker:50051"

  pageserver:
    host: "neon-stack-neon-pageserver"
    pgPort: 6400
    httpPort: 9898

  safekeeper:
    replicas: 3
    headlessService: "neon-stack-neon-safekeeper-headless"
    pgPort: 5454
    httpPort: 7676

  dummyCp:
    endpoint: "http://neon-stack-neon-dummy-cp:8080"

# Storage Controller
neon-storage-controller:
  enabled: true
  settings:
    devMode: true
    controlPlaneUrl: "http://neon-stack-neon-dummy-cp:8080"
    controlPlaneJwtToken: ""
    databaseUrl: "postgres://storage_controller:storage_controller@192.168.232.128:5432/storage_controller"
    timelineSafekeeperCount: 3

# Pageserver
neon-pageserver:
  enabled: true
  replicas: 1
  baseId: 1
  settings:
    brokerEndpoint: "http://neon-stack-neon-storage-broker:50051"
    controlPlaneApi: "http://neon-stack-neon-storage-controller-svc:50051/upcall/v1"
  persistence:
    size: 10Gi
  s3Credentials:
    existingSecret: "bucket-credentials"

# Safekeeper
neon-safekeeper:
  enabled: true
  replicas: 3
  baseId: 1
  settings:
    brokerEndpoint: "http://neon-stack-neon-storage-broker:50051"
  persistence:
    size: 10Gi
  s3Credentials:
    existingSecret: "bucket-credentials"

# Init Job（预设 tenant_id/timeline_id，与 compute 保持一致）
neon-init-job:
  enabled: true
  settings:
    storageControllerEndpoint: "http://neon-stack-neon-storage-controller-svc:50051"
    pageserverEndpoint: "http://neon-stack-neon-pageserver:9898"
    tenantId: "dc38beec6b7645139518229fcf4c0235"
    timelineId: "bb278410ea8046a39e3fa33be0aa82dd"
    pgVersion: 16
    safekeeper:
      replicas: 3
      baseId: 1
      podNamePrefix: "neon-stack-neon-safekeeper"
      headlessService: "neon-stack-neon-safekeeper-headless"
      pgPort: 5454
      httpPort: 7676

# Storage Scrubber（S3 GC，每天凌晨 3 点）
neon-storage-scrubber:
  enabled: true
  s3Credentials:
    existingSecret: "bucket-credentials"
  storageScrubber:
    schedule: "0 3 * * *"
    timeZone: "Asia/Shanghai"
    command:
      - pageserver-physical-gc
      - --min-age=1week

# Compute（预设 tenant/timeline，与 init-job 一致）
neon-compute:
  enabled: true
  replicas: 1
  pgVersion: 16
  port: 55433
  devMode: true
  tenantId: "dc38beec6b7645139518229fcf4c0235"
  timelineId: "bb278410ea8046a39e3fa33be0aa82dd"
  pageserver:
    host: ""  # 清空，让模板 fallback 到 headless service DNS
  pageservers:
    replicas: 1
    podNamePrefix: "neon-stack-neon-pageserver"
    headlessService: "neon-stack-neon-pageserver-headless"
    port: 6400
  safekeepers:
    - host: "neon-stack-neon-safekeeper-0.neon-stack-neon-safekeeper-headless"
      port: 5454
    - host: "neon-stack-neon-safekeeper-1.neon-stack-neon-safekeeper-headless"
      port: 5454
    - host: "neon-stack-neon-safekeeper-2.neon-stack-neon-safekeeper-headless"
      port: 5454
```

---

## 五、部署流程

### 5.1 部署顺序

```
Phase 1: 基础设施（由外部提供，部署前已就绪）
  ├── MinIO / S3 兼容存储    (集群外)
  └── PostgreSQL              (集群外 SC 元数据库)

Phase 2: 控制平面
  ├── neon-storage-broker (消息代理)
  ├── neon-dummy-cp       (HTTP 吸收器)
  └── neon-storage-controller (编排引擎)

Phase 3: 存储平面
  ├── neon-pageserver     (页面存储，向 SC re_attach)
  └── neon-safekeeper     (WAL 持久化，向 SC 注册)

Phase 4: 初始化
  └── neon-init-job       (注册 safekeeper → 创建 tenant/timeline → 生成 ComputeSpec)

Phase 5: 计算平面
  └── neon-compute        (PostgreSQL 计算节点)
```

> **注意**：Pageserver 在 Phase 3 启动时已通过 re-attach 自注册到 SC（依赖 `metadata.json`），init-job 无需再注册 pageserver。

### 5.2 一键部署

```bash
# 部署完整 Neon 栈
helm install neon-stack ./charts/neon-stack -n neon --create-namespace

# 等待所有 Pod 就绪
kubectl wait --for=condition=ready pod -l app.kubernetes.io/part-of=neon-stack -n neon --timeout=300s

# 连接数据库
kubectl port-forward -n neon svc/neon-compute 55433:55433 &
psql "postgresql://cloud_admin@localhost:55433/postgres"
```

### 5.3 部署后验证

```bash
# 1. 检查所有 Pod
kubectl get pods -n neon

# 2. 检查 Storage Controller 就绪
kubectl exec -n neon deploy/neon-stack-neon-storage-controller -- curl -sf http://localhost:50051/ready

# 3. 检查 Pageserver 状态（使用 /v1/status 端点）
kubectl exec -n neon sts/neon-stack-neon-pageserver -- curl -sf http://localhost:9898/v1/status

# 4. 检查 Safekeeper 状态
kubectl exec -n neon sts/neon-stack-neon-safekeeper -- curl -sf http://localhost:7676/v1/status

# 5. 检查 Init Job 结果
kubectl logs -n neon job/neon-stack-neon-init-job

# 6. 连接 PostgreSQL（密码: postgres）
kubectl port-forward -n neon svc/neon-stack-neon-compute 55433:55433 &
psql "postgresql://postgres@localhost:55433/postgres"
CREATE TABLE test (id serial PRIMARY KEY, data text);
INSERT INTO test (data) VALUES ('hello neon');
SELECT * FROM test;

# 7. 验证计算存储分离
kubectl delete pod -n neon -l app.kubernetes.io/component=compute
# 等待新 Pod 启动后，重新连接
# 数据应该仍然存在（从 pageserver 恢复）
SELECT * FROM test;

# 8. 检查 Storage Scrubber CronJob 状态
kubectl get cronjob -n neon neon-stack-neon-storage-scrubber
```

---

## 六、实现路线图

### 当前状态

截至 2026-07-09，**所有子 Chart 和 Umbrella Chart 均已创建完成**。以下是各模块的实现详情：

| 序号 | 任务                                         | 状态 | 实现要点                                                                                                                                                                                 |
| :--: | -------------------------------------------- | :--: | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
|  1  | **创建 `neon-dummy-cp` Chart**       | ✅ 完成 | Python `http.server` + JSON，处理 GET/POST/PUT，返回 `{"status":"ok"}`；健康检查 + ServiceAccount                                                                                    |
|  2  | **配置 `neon-storage-controller`**   | ✅ 完成 | deployment.yaml 已添加 `--dev` 开关（`settings.devMode`）；支持 `--control-plane-url`、`--timeline-safekeeper-count` 等参数；dummy-cp 作为 compute hook 目标                         |
|  3  | **创建 `neon-pageserver` Chart**     | ✅ 完成 | StatefulSet + Headless Service + ClusterIP Service + volumeClaimTemplates；initContainer 动态生成 `metadata.json`(host/port 字段) + `identity.toml`(baseId+ordinal)；S3 占位符通过 awk 替换；正常模式 re_attach 自注册 |
|  4  | **创建 `neon-safekeeper` Chart**     | ✅ 完成 | StatefulSet 3 副本 + Headless Service + PVC；`--advertise-pg` 使用 Pod DNS；ID 由 `baseId + ordinal` 动态计算；S3 通过 `--remote-storage` inline TOML 配置；由 init-job Helm Hook 注册 |
|  5  | **创建 `neon-compute` Chart**        | ✅ 完成 | Deployment + ConfigMap（`{"spec": {...}, "compute_ctl_config": {...}}` 嵌套结构）；多 pageserver 逗号分隔 connstring；safekeeper `host:port` 格式；initContainer busybox 等待依赖；emptyDir LFC + pgdata |
|  6  | **创建 `neon-init-job` Chart**       | ✅ 完成 | Helm post-install Hook Job + `hook-delete-policy`；自动等待 SC/PS/SK 就绪 → 注册 safekeeper → 创建 tenant/timeline；支持预设或自动生成 UUID（`/proc/sys/kernel/random/uuid`）      |
|  7  | **创建 `neon-stack` Umbrella Chart** | ✅ 完成 | 8 个子 Chart 依赖声明（含 `condition` 条件启停）；全局 values 统一管理 S3/PG 凭证和服务端点；预设 tenant_id/timeline_id 确保 init-job 与 compute 一致                                |
|  8  | **添加 `neon-storage-scrubber`**    | ✅ 完成 | S3 物理垃圾回收 CronJob（`pageserver-physical-gc --min-age=1week`），每日凌晨 3 点（Asia/Shanghai）；复用相同 `bucket-credentials` Secret                                               |

**当前所有组件状态：已实现，可直接部署。**

### 后续待完成

| 序号 | 任务                                         | 详情                                                                                                                                                                                 |  估时  |
| :--: | -------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | :----: |
|  1  | **端到端验证 + 故障恢复测试**                   | `helm install neon-stack` → 等待就绪 → psql CRUD → Pod 删除重启验证 → 节点故障恢复测试                                                                                        |  1 天  |
|  2  | **安全加固**                         | 替换 `--dev` 模式为 JWT 认证；停止使用 Trust 认证；添加 NetworkPolicy 限制                                                                                                      |  1 天  |
|  3  | **高可用**                             | Pageserver 多副本 + 反亲和；Safekeeper 3 副本 Quorum 验证；Compute 多副本读扩展                                                                                              |  2 天  |
|  4  | **监控与可观测性**                     | Prometheus ServiceMonitor；Loki 日志采集；Grafana Dashboard；metrics.enabled = true                                                                                             |  1 天  |
|  5  | **文档完善**                             | 运维手册、常见问题排查指南、版本升级说明                                                                                                                                       |  1 天  |
|  6  | **ConfigMap 自动化更新**               | init-job 完成后自动 patch neon-compute ConfigMap 填入 tenant_id/timeline_id，消除手动预设值                                                                                   | 0.5 天 |

### MVP 交付物

```bash
# 一键部署
helm install neon-stack ./charts/neon-stack -n neon --create-namespace

# 连接 PostgreSQL
psql "postgresql://cloud_admin@<compute-svc>:55433/postgres"
```

### MVP 不包含

- ❌ Control Plane
- ❌ Proxy
- ❌ Endpoint Storage（无 LFC 预热）
- ❌ JWT 认证（--dev 模式 + Trust）
- ❌ 高可用（单 pageserver、单 compute）
- ❌ 多 tenant 管理（手动操作）
- ❌ 监控告警

---

## 七、技术决策记录

### ADR-1: 使用 Storage Controller（非 emergency mode）

- **决策**：使用 Storage Controller 进行 tenant 调度和 generation 管理
- **原因**：满足需求"部署 Storage Controller"；获得 generation 安全保护，避免脑裂
- **代价**：新增 dummy CP 依赖组件（PostgreSQL 由外部提供，不算新增）
- **优势**：比 emergency mode 更接近生产架构，支持多 pageserver 扩展

### ADR-2: Dummy HTTP Server 吸收 compute hook

- **决策**：部署极简 HTTP Server 作为 SC 的 `control_plane_url` 目标
- **原因**：SC Reconciler 必须调用 `notify_attach()`；不设置 URL 在 StrictMode 下会启动失败；
  `--dev` 模式下虽可跳过启动检查，但 compute hook 调用时仍会 panic（`compute_hook.rs:892` 行 `unwrap()`）；
  设为不可达地址会导致 ERROR 日志和无限重试；返回 200 OK 是最小代价的解决方案
- **风险**：dummy server 挂了会导致 SC 日志噪音增加（但不影响核心功能）
- **缓解**：dummy server 是极简单的 Deployment，故障概率极低

### ADR-7: Storage Controller 使用 `--dev` 模式

- **决策**：Storage Controller 启动时设置 `--dev` 模式
- **原因**：
  - 无 JWT 密钥管理组件，无法提供 `public_key`、`pageserver_jwt_token`、`safekeeper_jwt_token`、`control_plane_jwt_token`
  - Strict 模式要求以上 secret 全部配置，否则拒绝启动
  - `--dev` 模式将缺失的 secret 解释为"隐式禁用对应 auth"，与全 Trust 链路一致
- **影响范围**：
  - SC 对外 API 不做 JWT 鉴权（`public_key` 缺失 → `auth = None`）
  - SC → Pageserver 请求不带 JWT（`pageserver_jwt_token` 缺失）
  - SC → Safekeeper 请求不带 JWT（`safekeeper_jwt_token` 缺失）
  - SC → compute hook 请求不带 JWT（`control_plane_jwt_token` 缺失）
  - `--control-plane-url` 缺失不再导致启动失败（但仍需设置，否则 compute hook 调用 panic）
  - `--timeline-safekeeper-count` 可 < 3
- **风险**：无认证保护，仅适合内网隔离部署
- **缓解**：通过网络策略（NetworkPolicy）限制 SC 端口仅集群内可访问

### ADR-3: Compute 使用静态 config.json

- **决策**：compute_ctl 使用 `--config` 模式，不依赖 `--control-plane-uri`
- **原因**：无 Control Plane 可调用；compute_ctl 的 `--config` 和 `--control-plane-uri` 互斥，
  不能混用
- **代价**：pageserver/safekeeper 地址写死在 config.json 中，变更需手动更新
- **缓解**：单节点部署场景下地址不变；多节点场景需要 ConfigMap 更新 + Pod 重启

### ADR-4: Pageserver 正常模式（非 emergency）+ metadata.json 自注册 + StatefulSet

- **决策**：pageserver 不设置 `control_plane_emergency_mode=true`；使用 **StatefulSet** 部署（MVP replicas=1，后续可扩至 N）；通过 `metadata.json` 实现启动时自动向 SC 注册
- **原因**：
  - 有 Storage Controller 可用，正常模式通过 SC re_attach 获得 generation number，避免脑裂
  - pageserver 启动时自动读取 `metadata.json`，内嵌 `NodeRegisterRequest` 到 re-attach 请求中
  - SC 的 `re_attach()` 处理函数内部调用 `node_register()`，无需 init-job 显式调用 `POST /control/v1/node`
  - 这与 neon_local / 生产环境的 pageserver 注册流程一致
  - **StatefulSet 而非 Deployment**：SC 通过 `base_url()` 点对点路由到指定 pageserver，必须使用 Headless Service 提供的稳定单 Pod DNS（见 [ADR-10](#adr-10-pageserver-使用-statefulset-而非-deployment)）
- **先决条件**：
  - Storage Controller 和 Broker 必须先于 pageserver 部署并就绪
  - `/data/.neon/metadata.json` 和 `/data/.neon/identity.toml` 需在 pageserver 首次启动前由 initContainer 创建
  - `metadata.json` 中的 `http_host`/`host` 必须使用 **Headless Service DNS**（如 `neon-pageserver-0.neon-pageserver-headless.neon.svc.cluster.local`），确保 SC 可点对点路由
  - `identity.toml` 中的 `id` 由 initContainer 动态计算（`baseId + ordinal`），而非 Helm 模板渲染
  - `metadata.json` 的 JSON 字段名使用 `host`/`port`（匹配 Rust `#[serde(rename)]`），而非 `postgres_host`/`postgres_port`
  - `metadata.json` 内容在 pageserver 生命周期内不变（仅首次启动时读取），Pod 重启无需重生成
- **与 Safekeeper 注册的区别**：
  - Pageserver：**自注册**（re-attach 中内嵌注册信息），不需要 init-job 干预
  - Safekeeper：**外部注册**（需显式调用 `POST /control/v1/safekeeper/:id`），由 init-job 执行

### ADR-5: Compute 使用 --dev 模式

- **决策**：MVP 阶段 compute_ctl 使用 `--dev` 模式绕过认证
- **原因**：无 JWT 密钥管理组件；最小化部署复杂度；与 SC `--dev` 模式形成全链路 Trust 认证体系
- **风险**：无认证保护，仅适合测试环境

### ADR-6: Safekeeper StatefulSet + SC 注册

- **决策**：Safekeeper 使用 StatefulSet + Headless Service，由 `neon-init-job` 统一向 SC 注册
- **原因**：SC 需要知道 safekeeper 的地址用于 timeline 调度；StatefulSet + Headless 提供稳定 DNS 名称，
  注册一次后 SC 通过心跳（每 5s `/utilization`）自动感知 safekeeper 在线/离线状态，Pod 重启无需重注册
- **节点 ID**：通过 StatefulSet ordinal index 计算（baseId + index）

### ADR-8: Safekeeper 注册采用 neon-init-job 统一注册

- **决策**：Safekeeper 注册使用 `neon-init-job` 统一执行，而非 postStart hook 或 sidecar
- **原因**：
  - **init-job 最合适**：safekeeper 注册后 SC 通过 5s 心跳（`GET /utilization`）感知在线状态，与注册行为解耦
  - **稳定 DNS 使注册成为一次性操作**：StatefulSet Pod DNS（如 `neon-safekeeper-0.neon-safekeeper-headless.neon.svc.cluster.local`）
    永不改变，Pod 重启后 DNS 自动解析到新 IP，SC 通过旧 host 字段即可恢复心跳
  - **自然融入现有流程**：init-job 已需创建 tenant/timeline，注册 safekeepers 只需多 3 个 curl 调用
  - **无额外开销**：零 sidecar、零 postStart hook
- **否决的方案**：
  - **postStart hook**：阻塞容器启动，SC 未就绪时导致 Pod 启动失败（CrashLoopBackOff）；每次 Pod 重启都重复执行（无意义，host 没变）
  - **sidecar**：每个 safekeeper Pod 额外一个容器（共 3 个），过度设计；注册是一次性 HTTP upsert，不需要独立进程

### ADR-9: 本地盘策略 — Pageserver / Safekeeper 使用 PVC，Compute LFC 使用 emptydir

- **决策**：Pageserver 和 Safekeeper 使用 **PVC 持久卷**（本地 SSD）存储核心数据，Compute 的 LFC 使用 **emptydir**（本地 NVMe/SSD）
- **原因**：
  - **Pageserver**：本地盘是 S3 数据的缓存层，存储 tenant layer 文件（delta/image layer）。虽然数据以 S3 为真实来源，
    但 PVC 持久化可避免 Pod 重启后全量从 S3 重新下载所有 layer（重建代价极大）。使用 `disk_usage_based_eviction`
    策略自动驱逐冷数据
  - **Safekeeper**：本地盘存储 WAL segment（从 compute 接收后、上传 S3 前的暂存区）。PVC 持久化保证 Pod 重启后
    WAL 不丢失，避免数据一致性问题。使用 `max_global_disk_usage_ratio` 控制磁盘水位
  - **Compute LFC**：LFC 是 pageserver 远程数据的本地读缓存，**每次 PostgreSQL 启动时自动重建**
    （`O_TRUNC`），天然不需要持久化。使用 emptydir 挂载本地盘即可，Pod 重启代价仅为冷启动填充 LFC
- **emptydir vs hostPath**：
  - 优先使用 **emptydir**：K8s 原生管理，无需额外权限，Pod 删除时自动清理，适合 LFC 这种可重建缓存
  - 高级场景可考虑 **hostPath** + NVMe：绑核部署，但需要 nodeSelector/nodeName 确保 Pod 调度到有 NVMe 的节点
- **磁盘类型建议**：所有三个组件的本地盘都建议使用 **本地 SSD/NVMe**，不宜使用网络存储（如 NFS/Ceph RBD），
  因为 layer 随机读和 WAL 顺序写对 IOPS 和延迟有较高要求

### ADR-10: Pageserver 使用 StatefulSet 而非 Deployment

- **决策**：Pageserver 使用 **StatefulSet** 部署（MVP replicas=1），而非 Deployment
- **原因**：
  - **SC 点对点路由需求**：SC 通过 `Node.base_url()` → `http://<http_addr>:9898` 向指定 pageserver 发送 HTTP 请求（如 `location_config`、`wait_lsn`、`get_lsns` 等）。Deployment 的 Service 是负载均衡的，SC 无法将请求定向到特定 Pod；StatefulSet 的 Headless Service 提供每 Pod 唯一 DNS（如 `ps-0.ps-hl.ns.svc`），实现精确路由
  - **`registration_match()` 要求地址不变**：SC 源码中 `node_register()` 要求 `http_addr`、`pg_addr` 在节点生命周期内不变，否则返回 `ApiError::Conflict`。Deployment Pod 重启后 IP 变化导致注册失败；StatefulSet 的 headless DNS 是永不变的稳定标识
  - **多副本扩展**：`replicas > 1` 时，每个 Pod 必须有唯一、稳定的网络标识，SC 才能区分各节点。Deployment 的 Pod 名称随机后缀无法满足此要求
  - **PVC 独立管理**：每个 pageserver 需要独立的本地 SSD 存储（layer 缓存）。StatefulSet 的 `volumeClaimTemplates` 自动为每个 Pod 创建独立 PVC，Deployment 需手动创建 N 个 PVC
  - **MVP 兼容性**：`replicas=1` 时 StatefulSet 行为与 Deployment 等价（另提供一个 ClusterIP Service 简化单节点访问），无额外复杂度
- **否决的方案**：
  - **Deployment**：单 Pod 场景可行（唯一的 Service 总指向唯一 Pod），但无法扩展到 2+ 副本；`registration_match()` 在 Pod IP 变化时可能失败
  - **Deployment + HostNetwork**：绕过 Service 但引入端口冲突、调度限制等问题
- **实现要点**：
  - 必须创建 Headless Service（`clusterIP: None`），SC 通过 headless DNS 路由
  - `metadata.json` 的 `host`/`http_host` 使用 headless DNS（如 `neon-pageserver-0.neon-pageserver-headless.neon.svc.cluster.local`），由 initContainer 动态生成。JSON 字段名使用 `host`/`port`（匹配 `NodeMetadata` 结构体的 `#[serde(rename)]`）
  - `identity.toml` 的 `id` 字段由 initContainer 动态计算（`baseId + ordinal`），例如 `baseId=1` + `pageserver-0` → id=1；多副本 `baseId=100` + ordinal → id=100,101,102
  - 可选附带 ClusterIP Service 用于单副本时的简便访问（`neon-pageserver:9898`）

### ADR-11: Compute 使用 Deployment 而非 StatefulSet

- **决策**：Compute 使用 **Deployment** 部署（MVP replicas=1，同租户读扩展可调至 N），而非 StatefulSet
- **原因**：
  - **Compute 是客户端，不是服务端**：`pg_neon` 通过 `PQconnectStartParams()` 主动连接 pageserver（`neon.pageserver_connstring` GUC），WalProposer 主动连接所有 safekeepers（`neon.safekeepers` GUC）。Compute 不接收来自 SC 或其他组件的主动回调——SC 的 compute hook 通知发往 **dummy-cp**，而非 compute 本身
  - **无需稳定网络标识**：没有外部系统需要通过 DNS 精确路由到特定 compute Pod。`compute_id` 仅用于日志/指标标签，不做路由判断。用户通过 ClusterIP Service 连接，负载均衡到任意 Pod 即可
  - **无持久状态依赖**：LFC 使用 `O_TRUNC` 模式每次启动自动重建（`pgxn/neon/file_cache.c`），pgdata 为临时目录，无需 PVC。emptydir 挂载本地盘完全满足
  - **同配置多副本天然适配**：同一 tenant/timeline 的 compute 共享同一 ConfigMap，所有 Pod 连接相同的 pageserver/safekeeper。Deployment 扩缩容后新 Pod 自动挂载同一配置即可工作
  - **与 Pageserver 的对比凸显差异**（见 [ADR-10](#adr-10-pageserver-使用-statefulset-而非-deployment)）：Pageserver 必须 StatefulSet 是因为 SC **需要回调到特定 PS 节点**（`base_url()` 路由）+ `registration_match()` 要求地址不变；Compute 无此需求
- **多租户场景**：不同租户需要不同 `tenant_id`/`timeline_id`，应创建**独立的 Deployment + Service 对**（`neon-compute-tenant-a`、`neon-compute-tenant-b`），而非在单个 StatefulSet 中混合。原因：独立 Deployment 提供独立的 Service、独立的扩缩容策略、独立的故障隔离
- **否决的方案**：
  - **StatefulSet**：引入不必要的复杂度（headless service、volumeClaimTemplates、有序扩缩容），无对应收益（compute 无状态、无稳定标识需求、无外部点对点回调）
  - **Deployment + 多租户混部**：不同 config 需要不同 Pod → 同一 Deployment 无法为不同 Pod 提供不同 ConfigMap → 应拆分为独立 Deployment

---

## 八、方案 B 的已知限制

| 限制                    | 说明                             | 影响                                                                                        | 缓解                              |
| ----------------------- | -------------------------------- | ------------------------------------------------------------------------------------------- | --------------------------------- |
| Pageserver 地址绑定     | config.json 写死 pageserver 地址 | 单 pageserver 场景使用 ClusterIP Service 无影响；多 PS 场景需 init-job 通过 SC API 动态查询 | MVP 阶段不涉及多 PS               |
| Safekeeper 地址静态绑定 | config.json 写死 safekeeper 地址 | StatefulSet 名称固定，足够稳定                                                              | 固定 3 副本 StatefulSet           |
| 无 ComputeSpec 动态刷新 | compute 启动后不会收到 spec 更新 | 修改 PostgreSQL 参数需重启 compute                                                          | 通过 ConfigMap + rollout restart  |
| Dummy CP 是额外组件     | 多一个 Pod 要维护                | 运维成本略增                                                                                | 极简，几乎不需要维护              |
| LFC 无预热机制          | Pod 重启后 LFC 冷启动            | 重启后首次查询需从 pageserver 拉取数据                                                      | LFC 填充快（本地 SSD，1MB chunk） |
| 无自动故障转移          | compute 不会自动切换 pageserver  | pageserver 宕机时 compute 不可用                                                            | 单节点部署不涉及                  |
| Tenant 创建需 initJob   | 每次新建 tenant 需运行 Job       | 单 tenant 够用                                                                              | 接受                              |

---

## 九、风险评估

| 风险                         | 等级 | 影响                | 缓解                     |
| ---------------------------- | :---: | ------------------- | ------------------------ |
| SC PostgreSQL 数据丢失       | 🟡 中 | SC 无法恢复调度状态 | 外部 PG 定期备份         |
| Dummy CP 服务不可用          | 🟢 低 | SC 日志噪音增加     | 极简服务几乎不会挂       |
| Pageserver SC re_attach 失败 | 🟡 中 | pageserver 无法启动 | 确保 SC 和 broker 先就绪 |
| Safekeeper Quorum 不足       | 🟡 中 | WAL 写入失败        | 确保 3 副本全部就绪      |
| 镜像版本不兼容               | 🟡 中 | 组件间协议不兼容    | 统一使用相同 tag         |
| 静态 Spec 与实际拓扑不一致   | 🟢 低 | compute 连接失败    | 单节点部署地址不变       |

---

## 十、与 emergency mode 方案的对比总结

| 维度                   | emergency mode（docker-compose 路线） | 本方案（方案 B）                    |
| ---------------------- | ------------------------------------- | ----------------------------------- |
| Storage Controller     | 不需要                                | 需要（+ 外部 PostgreSQL）           |
| 新增组件数             | 4 个 Chart                            | 6 个 Chart（多 dummy-cp + initJob） |
| Generation 安全        | ❌                                    | ✅                                  |
| 多 Pageserver          | ❌                                    | ✅（基础支持）                      |
| 脑裂风险               | 🟡 有                                 | 🟢 无                               |
| 部署复杂度             | 低                                    | 中                                  |
| 与 Neon 生产架构接近度 | 低                                    | 高                                  |
| 典型场景               | 本地开发、快速测试                    | PoC、准生产验证、单机私有部署       |

---

## 十一、参考资料

- [Neon 源码](https://github.com/neondatabase/neon) — 本地路径 `/home/postgres/works/opensource/neon`
- [Neon Helm Charts 仓库](https://github.com/neondatabase/helm-charts)
- [Neon docker-compose](https://github.com/neondatabase/neon/tree/main/docker-compose)
- [Neon 架构文档](https://github.com/neondatabase/neon/tree/main/docs)
- [方案 B 深度分析](./neon-private-deployment-scheme-b-analysis.md) — 对 Storage Controller + 静态 ComputeSpec 的源码级可行性分析
- [neon-endpoint-storage 分析](./neon-endpoint-storage.md)

---

> **文档状态**: 基于方案 B 深度分析的设计 → 全部 Chart 已实现，文档已同步最新代码
> **分析依据**: Storage Controller 源码分析 + docker-compose + 组件 API 文档 + 实际代码实现
> **上次更新**: 2026-07-09 — 根据最新 Chart 代码同步更新（metadata.json 字段名、identity.toml 动态生成、compute Spec 嵌套结构、init-job Helm Hook、storage-scrubber 新增等）
