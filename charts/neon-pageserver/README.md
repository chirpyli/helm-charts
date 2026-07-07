# neon-pageserver

![Version: 0.1.0](https://img.shields.io/badge/Version-0.1.0-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square)

Neon Pageserver — 存储计算分离架构中的页面存储层，负责从远程存储（S3/MinIO）拉取数据页并响应 Compute 节点的页面请求。

## 简介

`neon-pageserver` 是 Neon 存储架构的核心组件，以 StatefulSet 方式部署。它从远程对象存储（S3 / MinIO）
读取页面数据，并通过 PostgreSQL 协议向 Compute 节点提供页面服务。

**核心特性**

- **StatefulSet + PVC**：每个 Pod 独立的持久化存储卷，保障数据可靠性
- **远程存储集成**：通过 S3 兼容接口连接 MinIO 或 AWS S3，存储所有页面数据
- **三层网络**：PostgreSQL 协议（6400）、HTTP API（9898）、gRPC（50051）
- **双 Service 模式**：Headless Service（SC 点对点路由） + 可选 ClusterIP Service（外部访问）
- **自动注册**：通过 Storage Controller API 自动注册节点信息
- **磁盘驱逐策略**：本地磁盘使用率超阈值自动清理冷数据
- **零 JWT 认证**：默认 `Trust` 模式，适配无 Control Plane 的私有化部署

**前置依赖**

| 组件 | 用途 |
|------|------|
| Storage Broker | 服务发现，pageserver 启动时向其注册 |
| Storage Controller | 节点管理 API，自动注册节点元数据 |
| S3 / MinIO | 远程持久化存储层 |

## 架构

```
┌────────────────────────────────────────────────────────┐
│  neon-pageserver StatefulSet                           │
│                                                        │
│  ┌──────────────┐                                      │
│  │ initContainer │  → 写入 /data/.neon/metadata.json    │
│  │ (busybox)     │     (postgres_host, http_host,       │
│  │              │      grpc_host = headless DNS)        │
│  └──────┬───────┘                                      │
│         │                                              │
│  ┌──────▼───────────────────────────────────────────┐  │
│  │  pageserver binary                               │  │
│  │  ├─ pg  port  :6400  (PostgreSQL 协议)            │  │
│  │  ├─ http port  :9898  (管理 API, /status)         │  │
│  │  ├─ grpc port  :50051 (内部 RPC)                  │  │
│  │  └─ /config/pageserver.toml ← ConfigMap          │  │
│  └──────────────────────────────────────────────────┘  │
│                                                        │
│  Volumes:                                              │
│  ├─ /config  ← ConfigMap (pageserver.toml)             │
│  └─ /data    ← PVC (持久化存储)                         │
│                                                        │
│  Storage:                                              │
│  └─ S3 / MinIO  ← 通过 envFrom Secret 注入凭证         │
└────────────────────────────────────────────────────────┘
         │
         ▼
   ┌─────────────────┐
   │  Headless SVC    │  ← Storage Controller 点对点路由
   │  (publishNotReady│     每个 Pod 独立 DNS
   │   Addresses=true)│
   └─────────────────┘
   ┌─────────────────┐
   │  ClusterIP SVC  │  ← 可选：单副本外部访问
   │  (可选)          │
   └─────────────────┘
```

## 源代码

* <https://github.com/chirpyli/helm-charts>

## 环境要求

Kubernetes: `^1.18.x-x`

## 前置准备

部署前需创建包含 S3 凭证的 Secret：

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: bucket-credentials
type: Opaque
stringData:
  AWS_ACCESS_KEY_ID: "minioadmin"
  AWS_SECRET_ACCESS_KEY: "minioadmin"
  AWS_ENDPOINT_URL: "http://minio.minio.svc.cluster.local:9000"
  BUCKET_NAME: "neon-pageserver"
  AWS_REGION: "us-east-1"
```

## 安装

```console
$ git clone https://github.com/chirpyli/helm-charts.git
$ cd helm-charts/charts/neon-pageserver
$ helm dependency update
$ helm install neon-pageserver . -f values.yaml
```

## 配置参数

### 核心配置

| 参数 | 类型 | 默认值 | 描述 |
|-----|------|---------|-------------|
| baseId | int | `1` | Pageserver 节点 ID 基数（多副本时用于分配唯一 ID） |
| replicas | int | `1` | 副本数（MVP 阶段为 1） |

### 镜像

| 参数 | 类型 | 默认值 | 描述 |
|-----|------|---------|-------------|
| image.pullPolicy | string | `"Always"` | 镜像拉取策略 |
| image.repository | string | `"neondatabase/neon"` | Neondatabase 镜像仓库 |
| image.tag | string | `"latest"` | 覆盖镜像标签 |
| imagePullSecrets | list | `[]` | 指定 docker-registry 的 Secret 名称数组 |

### 服务配置

| 参数 | 类型 | 默认值 | 描述 |
|-----|------|---------|-------------|
| settings.brokerEndpoint | string | `"http://neon-storage-broker:50051"` | Storage Broker 地址 |
| settings.controlPlaneApi | string | `"http://neon-storage-controller:50051"` | Storage Controller API 地址 |
| settings.httpAuth | string | `"Trust"` | HTTP 认证模式（Trust 表示无需 JWT） |
| settings.listenHttpAddr | string | `"0.0.0.0:9898"` | HTTP API 监听地址 |
| settings.listenPgAddr | string | `"0.0.0.0:6400"` | PostgreSQL 协议监听地址 |
| settings.pgDistribDir | string | `"/usr/local/"` | PostgreSQL 发行版目录 |
| settings.virtualFileIoMode | string | `"buffered"` | 虚拟文件 IO 模式 |

### 远程存储

| 参数 | 类型 | 默认值 | 描述 |
|-----|------|---------|-------------|
| remoteStorage.prefixInBucket | string | `"/pageserver"` | 远程存储路径前缀 |
| s3Credentials.existingSecret | string | `"bucket-credentials"` | 引用的已有 Secret 名称 |

### 本地盘驱逐

| 参数 | 类型 | 默认值 | 描述 |
|-----|------|---------|-------------|
| diskUsageEviction.enabled | bool | `true` | 启用本地磁盘使用驱逐 |
| diskUsageEviction.maxUsagePct | int | `80` | 磁盘使用百分比上限 |
| diskUsageEviction.minAvailBytes | int | `0` | 最小可用字节数 |

### 持久化存储

| 参数 | 类型 | 默认值 | 描述 |
|-----|------|---------|-------------|
| persistence.enabled | bool | `true` | 启用持久化存储 |
| persistence.size | string | `"100Gi"` | 持久卷大小 |

### 网络

| 参数 | 类型 | 默认值 | 描述 |
|-----|------|---------|-------------|
| service.headless.name | string | `"neon-pageserver-headless"` | Headless Service 名称 |
| service.clusterIP.enabled | bool | `true` | 启用 ClusterIP Service |
| service.clusterIP.name | string | `"neon-pageserver"` | ClusterIP Service 名称 |

### 其他

| 参数 | 类型 | 默认值 | 描述 |
|-----|------|---------|-------------|
| affinity | object | `{}` | Pod 亲和性调度配置 |
| extraManifests | list | `[]` | 随 Chart 一同创建的额外 Kubernetes 资源清单 |
| fullnameOverride | string | `""` | 完全覆盖 neon-pageserver.fullname 模板的字符串 |
| metrics.enabled | bool | `false` | 启用 Prometheus 指标自动发现 |
| metrics.serviceMonitor.enabled | bool | `false` | 创建 ServiceMonitor 资源 |
| metrics.serviceMonitor.interval | string | `"10s"` | Prometheus 抓取间隔 |
| metrics.serviceMonitor.namespace | string | `""` | 创建 ServiceMonitor 的命名空间 |
| metrics.serviceMonitor.scrapeTimeout | string | `"10s"` | Prometheus 抓取超时时间 |
| metrics.serviceMonitor.selector | object | `{}` | 附加标签（供 Prometheus operator 使用） |
| nameOverride | string | `""` | 部分覆盖 neon-pageserver.fullname 模板的字符串 |
| nodeSelector | object | `{}` | Pod 节点选择器标签 |
| podAnnotations | object | `{}` | neon-pageserver Pod 的注解 |
| podLabels | object | `{}` | neon-pageserver Pod 的附加标签 |
| podSecurityContext | object | `{}` | neon-pageserver Pod 安全上下文 |
| priorityClassName | string | `""` | Pod 优先级类 |
| resources.limits.cpu | string | `"2"` | CPU 上限 |
| resources.limits.memory | string | `"4Gi"` | 内存上限 |
| resources.requests.cpu | string | `"1"` | CPU 请求 |
| resources.requests.memory | string | `"2Gi"` | 内存请求 |
| securityContext | object | `{}` | neon-pageserver 容器安全上下文 |
| serviceAccount.annotations | object | `{}` | 添加到 ServiceAccount 的注解 |
| serviceAccount.create | bool | `true` | 指定是否创建 ServiceAccount |
| serviceAccount.name | string | `""` | 要使用的 ServiceAccount 名称 |
| tolerations | list | `[]` | Pod 容忍调度配置 |

## 端口

| 端口 | 协议 | 用途 | Service |
|------|------|------|---------|
| `6400` | TCP | PostgreSQL 页面协议 | Headless + ClusterIP |
| `9898` | TCP | HTTP 管理 API (`/status`) | Headless + ClusterIP |
| `50051` | TCP | gRPC 内部 RPC | Headless |

## 健康检查

| 探针 | 方式 | 路径 | 配置 |
|------|------|------|------|
| startupProbe | `httpGet` | `/status` | 最长等待 5 分钟（30 × 10s） |
| livenessProbe | `httpGet` | `/status` | 每 15s 检查，超时 10s |
| readinessProbe | `httpGet` | `/status` | 每 15s 检查，超时 10s |

## 存储架构

```
请求流程:
  Compute Node               Pageserver              S3 / MinIO
      │                          │                       │
      │ ── page request ──→     │                       │
      │                          │ ── 检查本地缓存 ──    │
      │                          │    (PVC /data)        │
      │                          │                       │
      │                     [缓存命中]                   │
      │ ←─── 返回页面 ────      │                       │
      │                          │                       │
      │                     [缓存未命中]                  │
      │                          │ ── GET /page ────→   │
      │                          │ ←─── 返回数据 ────    │
      │                          │ 写入本地缓存           │
      │ ←─── 返回页面 ────      │                       │
```

## 其他

### 查看日志

```console
$ kubectl logs -f statefulset/neon-pageserver
```

### 检查状态

```console
# HTTP API 健康检查
$ kubectl exec neon-pageserver-0 -- curl -s http://localhost:9898/status

# 通过 Service 访问
$ kubectl run -it --rm debug --image=curlimages/curl -- \
    curl -s http://neon-pageserver-headless:9898/status
```

### 扩容

```console
# 修改 replicas 并升级（当前 MVP 阶段建议保持为 1）
$ helm upgrade neon-pageserver ./charts/neon-pageserver --set replicas=3
```

----------------------------------------------
由 Chart 元数据通过 [helm-docs](https://github.com/norwoodj/helm-docs) 自动生成
