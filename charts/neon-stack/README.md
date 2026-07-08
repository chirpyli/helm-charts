# neon-stack

![Version: 0.1.0](https://img.shields.io/badge/Version-0.1.0-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square)

Neon Serverless Postgres 私有化一站式部署 Umbrella Chart（无 Control Plane 方案）。

## 简介

`neon-stack` 将 Neon 的全部核心组件打包为一个 Helm Umbrella Chart，一次部署即可运行一个完整的 Neon 存储计算分离架构集群。

**核心特性**

- **零 Control Plane 依赖**：使用 Dummy CP 吸收 compute hook 通知，无需部署完整的 Neon Control Plane
- **Storage Controller 驱动**：由 Storage Controller 管理 tenant、timeline 和 node 生命周期
- **静态 ComputeSpec**：Compute 节点通过 ConfigMap 挂载预生成的 `config.json`，无需 HTTP API 动态下发
- **Trust 认证模式**：关闭 JWT 认证，所有组件间通信使用 Trust 模式
- **S3/MinIO 远程存储**：Pageserver 和 Safekeeper 均连接外部 S3 兼容对象存储
- **自动初始化**：通过 post-install Helm Hook Job 自动创建 tenant、timeline 并注册 safekeeper
- **固定拓扑**：3 个 Safekeeper + 1 个 Pageserver + 1 个 Compute，不支持动态扩缩

**限制说明**

| 限制项 | 说明 |
|-------|------|
| 无 Serverless | 不支持冷启动/自动休眠/按需唤醒 |
| 无 Proxy | 直连 Compute 节点，无路由/认证代理层 |
| 无 JWT | 所有组件 Trust 模式，适用于内网/私有环境 |
| 固定拓扑 | Safekeeper/Pagserver 数量不可动态变更 |
| 无预热 | 缺少 endpoint_storage，无计算节点预热功能 |

## 架构

```
┌──────────────────────────────────────────────────────────────────────┐
│                        neon-stack (Umbrella Chart)                    │
│                                                                       │
│  ┌──────────────────┐   ┌───────────────────────────────┐            │
│  │ neon-init-job    │   │ neon-storage-controller        │            │
│  │ (post-install    │──▶│ (HTTP :50051, --dev mode)      │            │
│  │  Helm Hook)      │   │   ↓                            │            │
│  │                  │   │ PostgreSQL (外部)               │            │
│  └───────┬──────────┘   └───────────────┬───────────────┘            │
│          │ 创建 tenant/timeline          │ upcall (/upcall/v1)         │
│          ▼              注册 safekeeper  ▼                            │
│  ┌──────────────────┐   ┌───────────────┐   ┌──────────────────────┐ │
│  │ neon-pageserver  │   │ neon-safekeeper│   │ neon-storage-broker  │ │
│  │ (StatefulSet x1) │   │ (StatefulSet   │   │ (Deployment x1)      │ │
│  │ pg   :6400       │   │  x3)           │   │ gRPC :50051          │ │
│  │ http :9898       │   │ pg   :5454     │   │ 服务发现 & 心跳       │ │
│  │      ↓           │   │ http :7676     │   └──────────────────────┘ │
│  │ PVC + S3         │   │      ↓         │                            │
│  └────────┬─────────┘   │ PVC + S3       │   ┌──────────────────────┐ │
│           │             └───────┬─────────┘   │ neon-dummy-cp        │ │
│           │ page request        │ WAL          │ (Deployment x1)      │ │
│           ▼                     ▼              │ :8080 ← notify hook  │ │
│  ┌──────────────────┐   ┌──────────────────┐  └──────────────────────┘ │
│  │ neon-compute     │   │ neon-storage-    │                            │
│  │ (Deployment x1)  │   │ scrubber         │   ┌────────────────────┐ │
│  │ pg :55433        │   │ (CronJob, 每日)   │   │ 外部依赖           │ │
│  │ config.json 静态 │   │ physical-gc  S3   │   │ • MinIO / S3       │ │
│  │ LFC (emptydir)   │   └──────────────────┘   │ • PostgreSQL       │ │
│  └──────────────────┘                           └────────────────────┘ │
└──────────────────────────────────────────────────────────────────────┘
```

### 组件间调用关系

```
Compute ──page request──▶ Pageserver ──cache miss──▶ S3/MinIO
    │                          │
    │                          ▼
    │                    Storage Broker (服务发现)
    │
    ▼
Safekeeper ◀──WAL stream── Compute
    │
    ▼
S3/MinIO (WAL offload)

Storage Controller ◀──re_attach─── Pageserver (启动时自注册)
Storage Controller ◀──register──── Init Job (注册 Safekeeper)
Storage Controller ──notify──────▶ Dummy CP (compute hook 吸收)
```

## 前置依赖

### 外部服务

| 服务 | 用途 | 必需 |
|------|------|:----:|
| S3 / MinIO | 远程持久化存储层（Pageserver 数据 + Safekeeper WAL offload） | ✅ |
| PostgreSQL | Storage Controller 元数据存储 | ✅ |

### Kubernetes Secret

部署前需在 `neon` 命名空间创建两个 Secret：

**1. S3 凭证**（`bucket-credentials`）

```bash
kubectl create secret generic bucket-credentials -n neon \
  --from-literal=AWS_ACCESS_KEY_ID="minioadmin" \
  --from-literal=AWS_SECRET_ACCESS_KEY="minioadmin" \
  --from-literal=AWS_ENDPOINT_URL="http://<minio-host>:9000" \
  --from-literal=AWS_REGION="us-east-1" \
  --from-literal=BUCKET_NAME="neondata"
```

**2. PostgreSQL 连接串**（`storage-controller-pg-cluster`）

```bash
kubectl create secret generic storage-controller-pg-cluster -n neon \
  --from-literal=uri="postgres://storage_controller:storage_controller@<pg-host>:5432/storage_controller"
```

## 子 Chart 一览

| Chart | 来源 | 工作负载 | 副本数 | 描述 |
|-------|------|----------|:------:|------|
| `neon-storage-broker` | 上游 Helm 仓库 | Deployment | 1 | 服务发现与心跳，组件启动时注册 |
| `neon-storage-controller` | 上游 Helm 仓库 | Deployment | 1 | 核心调度器，管理 tenant/timeline/node 生命周期 |
| `neon-dummy-cp` | 本地 Chart | Deployment | 1 | 伪 Control Plane HTTP Server，吸收 compute hook 通知并返回 200 |
| `neon-pageserver` | 本地 Chart | StatefulSet | 1 | 页面存储层，从 S3 拉取数据页并响应 Compute 页面请求 |
| `neon-safekeeper` | 本地 Chart | StatefulSet | 3 | WAL 持久化层，接收 Compute 的 WAL 流并上传到 S3 |
| `neon-init-job` | 本地 Chart | Helm Hook Job | 1 | post-install 钩子，自动创建 tenant/timeline 并注册 safekeeper |
| `neon-storage-scrubber` | 上游 Helm 仓库 | CronJob | 1 | 每日物理 GC，清理 S3 中未被引用的垃圾 layer 对象 |
| `neon-compute` | 本地 Chart | Deployment | 1 | PostgreSQL 计算节点，通过静态 config.json 获取集群拓扑 |

## 安装

```bash
# 1. 克隆仓库
git clone https://github.com/chirpyli/helm-charts.git
cd helm-charts

# 2. 构建 Umbrella Chart 依赖（首次或 Chart.yaml 变更后）
helm dep build charts/neon-stack

# 3. 部署到 neon 命名空间
helm install neon-stack ./charts/neon-stack -n neon --create-namespace

# 4. 检查各组件就绪状态
kubectl get pods -n neon -w
```

## 升级

```bash
# 修改 values.yaml 后
helm upgrade neon-stack ./charts/neon-stack -n neon

# 如 ConfigMap 有变更，需手动重启相关 Pod
kubectl rollout restart statefulset neon-stack-neon-pageserver -n neon
```

## 卸载

```bash
helm uninstall neon-stack -n neon

# PVC 不会被自动删除，如需清理：
kubectl delete pvc -n neon -l app.kubernetes.io/instance=neon-stack
```

## 配置参数

### 全局配置 `global`

全局配置为所有子 Chart 提供跨组件引用。

| 参数 | 类型 | 默认值 | 描述 |
|------|------|--------|------|
| `global.s3.existingSecret` | string | `"bucket-credentials"` | 包含 S3 凭证的 Secret 名称 |
| `global.storageController.endpoint` | string | `"http://neon-stack-neon-storage-controller-svc:50051"` | Storage Controller HTTP API 地址 |
| `global.storageController.existingPgUriSecret` | string | `"storage-controller-pg-cluster"` | PostgreSQL 连接串 Secret 名称 |
| `global.storageBroker.endpoint` | string | `"http://neon-stack-neon-storage-broker:50051"` | Storage Broker gRPC 地址 |
| `global.pageserver.host` | string | `"neon-stack-neon-pageserver"` | Pageserver Service 地址 |
| `global.pageserver.pgPort` | int | `6400` | Pageserver PostgreSQL 协议端口 |
| `global.pageserver.httpPort` | int | `9898` | Pageserver HTTP API 端口 |
| `global.safekeeper.replicas` | int | `3` | Safekeeper 副本数 |
| `global.safekeeper.headlessService` | string | `"neon-stack-neon-safekeeper-headless"` | Safekeeper Headless Service 名称 |
| `global.safekeeper.pgPort` | int | `5454` | Safekeeper PostgreSQL 协议端口 |
| `global.safekeeper.httpPort` | int | `7676` | Safekeeper HTTP API 端口 |
| `global.dummyCp.endpoint` | string | `"http://neon-stack-neon-dummy-cp:8080"` | Dummy CP 地址 |

### 子 Chart 覆盖配置

每个子 Chart 均可通过 `neon-<name>:` 前缀覆盖其 `values.yaml` 中的配置。

### Storage Broker

| 参数 | 类型 | 默认值 | 描述 |
|------|------|--------|------|
| `neon-storage-broker.enabled` | bool | `true` | 启用 Storage Broker |
| `neon-storage-broker.image.repository` | string | `"ghcr.io/neondatabase/neon"` | 镜像仓库 |

### Storage Controller

| 参数 | 类型 | 默认值 | 描述 |
|------|------|--------|------|
| `neon-storage-controller.enabled` | bool | `true` | 启用 Storage Controller |
| `neon-storage-controller.settings.devMode` | bool | `true` | 开发模式：跳过 JWT、允许 < 3 SK |
| `neon-storage-controller.settings.controlPlaneUrl` | string | `"http://neon-stack-neon-dummy-cp:8080"` | Control Plane URL（指向 Dummy CP） |
| `neon-storage-controller.settings.databaseUrl` | string | — | PostgreSQL 连接串 |
| `neon-storage-controller.settings.timelineSafekeeperCount` | int | `3` | Timeline 所需 Safekeeper 数量 |

### Dummy CP

| 参数 | 类型 | 默认值 | 描述 |
|------|------|--------|------|
| `neon-dummy-cp.enabled` | bool | `true` | 启用 Dummy Control Plane |
| `neon-dummy-cp.image.repository` | string | `"docker.m.daocloud.io/library/python"` | 镜像（使用 DaoCloud 代理避免 Docker Hub 不可达） |
| `neon-dummy-cp.image.tag` | string | `"3.11-slim"` | 镜像标签 |

### Pageserver

| 参数 | 类型 | 默认值 | 描述 |
|------|------|--------|------|
| `neon-pageserver.enabled` | bool | `true` | 启用 Pageserver |
| `neon-pageserver.replicas` | int | `1` | 副本数 |
| `neon-pageserver.baseId` | int | `1` | 节点 ID 基数 |
| `neon-pageserver.settings.brokerEndpoint` | string | `"http://neon-stack-neon-storage-broker:50051"` | Broker 地址 |
| `neon-pageserver.settings.controlPlaneApi` | string | `"http://neon-stack-neon-storage-controller-svc:50051/upcall/v1"` | SC upcall API 地址（**注意 `/upcall/v1` 后缀**） |
| `neon-pageserver.s3Credentials.existingSecret` | string | `"bucket-credentials"` | S3 凭证 Secret |
| `neon-pageserver.persistence.size` | string | `"10Gi"` | PVC 大小 |

### Safekeeper

| 参数 | 类型 | 默认值 | 描述 |
|------|------|--------|------|
| `neon-safekeeper.enabled` | bool | `true` | 启用 Safekeeper |
| `neon-safekeeper.replicas` | int | `3` | 副本数 |
| `neon-safekeeper.baseId` | int | `1` | 节点 ID 基数 |
| `neon-safekeeper.settings.brokerEndpoint` | string | `"http://neon-stack-neon-storage-broker:50051"` | Broker 地址 |
| `neon-safekeeper.s3Credentials.existingSecret` | string | `"bucket-credentials"` | S3 凭证 Secret |
| `neon-safekeeper.persistence.size` | string | `"10Gi"` | PVC 大小 |

### Init Job

| 参数 | 类型 | 默认值 | 描述 |
|------|------|--------|------|
| `neon-init-job.enabled` | bool | `true` | 启用初始化 Job |
| `neon-init-job.settings.storageControllerEndpoint` | string | `"http://neon-stack-neon-storage-controller-svc:50051"` | SC API 地址 |
| `neon-init-job.settings.pageserverEndpoint` | string | `"http://neon-stack-neon-pageserver:9898"` | Pageserver HTTP 地址 |
| `neon-init-job.settings.safekeeper.replicas` | int | `3` | Safekeeper 副本数 |
| `neon-init-job.settings.safekeeper.podNamePrefix` | string | `"neon-stack-neon-safekeeper"` | Safekeeper Pod 名称前缀 |
| `neon-init-job.settings.safekeeper.headlessService` | string | `"neon-stack-neon-safekeeper-headless"` | Headless Service 名称 |

### Storage Scrubber

| 参数 | 类型 | 默认值 | 描述 |
|------|------|--------|------|
| `neon-storage-scrubber.enabled` | bool | `true` | 启用 Storage Scrubber |
| `neon-storage-scrubber.s3Credentials.existingSecret` | string | `"bucket-credentials"` | S3 凭证 Secret |
| `neon-storage-scrubber.storageScrubber.schedule` | string | `"0 3 * * *"` | CronJob 调度表达式 |
| `neon-storage-scrubber.storageScrubber.command` | list | `["pageserver-physical-gc", "--min-age=1week"]` | GC 命令 |

### Compute

| 参数 | 类型 | 默认值 | 描述 |
|------|------|--------|------|
| `neon-compute.enabled` | bool | `true` | 启用 Compute |
| `neon-compute.replicas` | int | `1` | 副本数 |
| `neon-compute.pgVersion` | int | `16` | PostgreSQL 主版本号 |
| `neon-compute.port` | int | `55433` | Compute 监听端口 |
| `neon-compute.pageserver.host` | string | `"neon-stack-neon-pageserver"` | Pageserver 地址 |
| `neon-compute.pageserver.port` | int | `6400` | Pageserver 端口 |
| `neon-compute.safekeepers` | list | (见 values.yaml) | Safekeeper 连接信息列表 |

## 端口

| 组件 | 端口 | 协议 | 用途 |
|------|:----:|------|------|
| Storage Controller | `50051` | HTTP | REST API（`/v1/tenant`、`/upcall/v1/re-attach` 等） |
| Storage Broker | `50051` | gRPC | 服务发现与心跳 |
| Pageserver | `6400` | TCP | PostgreSQL 页面协议 |
| Pageserver | `9898` | HTTP | 管理 API（`/status`） |
| Safekeeper | `5454` | TCP | WAL 协议 |
| Safekeeper | `7676` | HTTP | 管理 API |
| Dummy CP | `8080` | HTTP | 伪 Control Plane API |
| Compute | `55433` | TCP | PostgreSQL 客户端协议 |

## 连接数据库

```bash
# 端口转发
kubectl port-forward -n neon svc/neon-stack-neon-compute 55433:55433

# 连接（默认用户 cloud_admin，无密码）
psql "postgresql://cloud_admin@localhost:55433/postgres"
```

## 验证部署

```bash
# 所有 Pod 应为 Running，Pageserver 和 Compute 可能需 1-3 分钟完成初始化
kubectl get pods -n neon

# 期望输出（就绪后）：
# NAME                                       READY   STATUS
# neon-stack-neon-storage-broker-*           1/1     Running
# neon-stack-neon-storage-controller-*       1/1     Running
# neon-stack-neon-dummy-cp-*                 1/1     Running
# neon-stack-neon-pageserver-0               1/1     Running
# neon-stack-neon-safekeeper-0               1/1     Running
# neon-stack-neon-safekeeper-1               1/1     Running
# neon-stack-neon-safekeeper-2               1/1     Running
# neon-stack-neon-compute-*                  1/1     Running
```

## 故障排查

### Pageserver 频繁重启（startup probe 超时）

检查 `controlPlaneApi` 是否包含 `/upcall/v1` 后缀：

```bash
kubectl logs -n neon neon-stack-neon-pageserver-0 | grep "re.attach\|404"
```

如果出现 `404` 或 `re-attach` 失败，说明 URL 前缀缺失，需确认 values.yaml 中配置为：

```yaml
controlPlaneApi: "http://neon-stack-neon-storage-controller-svc:50051/upcall/v1"
```

### Init Job 卡在等待 SC 就绪

检查健康检查端点是否正确：

```bash
kubectl logs -n neon neon-stack-neon-init-job-* | grep "SC not ready"
```

如果持续等待，确认 Job 中使用的是 `/ready`（而非不存在的 `/health`）。

### Pageserver 无法连接 S3

```bash
kubectl exec -n neon neon-stack-neon-pageserver-0 -- env | grep AWS
kubectl exec -n neon neon-stack-neon-pageserver-0 -- env | grep BUCKET
```

确认 `AWS_ENDPOINT_URL`、`AWS_ACCESS_KEY_ID`、`AWS_SECRET_ACCESS_KEY`、`BUCKET_NAME` 均正确注入。

## 源代码

* [https://github.com/chirpyli/helm-charts](https://github.com/chirpyli/helm-charts)
* [https://github.com/neondatabase/neon](https://github.com/neondatabase/neon)

---

由 Chart 元数据通过 [helm-docs](https://github.com/norwoodj/helm-docs) 自动生成
