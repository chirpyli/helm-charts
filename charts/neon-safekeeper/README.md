# neon-safekeeper

![Version: 0.1.0](https://img.shields.io/badge/Version-0.1.0-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square)

Neon Safekeeper — WAL 持久化层，负责接收 Compute 节点的 WAL 流并将其持久化到本地磁盘和远程存储。

## 简介

`neon-safekeeper` 是 Neon 存储架构中的 WAL（Write-Ahead Log）持久化层，以 StatefulSet 方式部署，推荐 3 副本以实现 WAL 法定人数（quorum）。每个 Pod 拥有独立 PVC，WAL 数据同时写入本地磁盘和 S3/MinIO 远程存储。

**核心特性**

- **WAL 法定人数**：3 副本（默认），Compute 节点需收到多数派确认才算写入成功
- **双重持久化**：本地 PVC + 远程 S3/MinIO，数据可靠性极高
- **动态 ID**：`node_id = baseId + pod_ordinal`，例如 baseId=1 时 3 副本 ID 为 1、2、3
- **Headless Service**：每个 Pod 独立 DNS 记录，Compute 节点直连各个 Safekeeper 实例
- **磁盘控制**：支持全局磁盘使用比例限制和单 timeline 磁盘上限

**前置依赖**

| 组件 | 用途 |
|------|------|
| Storage Broker | 服务注册与发现 |
| S3 / MinIO | WAL 远程持久化存储 |

## 架构

```
┌────────────────────────────────────────────────────────┐
│  neon-safekeeper StatefulSet (replicas=3)              │
│                                                        │
│  ┌──────────────────┐  ┌──────────────────┐           │
│  │ safekeeper-0     │  │ safekeeper-1     │  ...      │
│  │ id=1 (baseId+0)  │  │ id=2 (baseId+1)  │           │
│  │                  │  │                  │           │
│  │ pg  :5454        │  │ pg  :5454        │           │
│  │ http:7676        │  │ http:7676        │           │
│  │ /data ← PVC      │  │ /data ← PVC      │           │
│  └────────┬─────────┘  └────────┬─────────┘           │
│           │                     │                      │
│           └──────────┬──────────┘                      │
│                      │                                 │
│              ┌───────▼────────┐                        │
│              │ Headless SVC   │  publishNotReady=true  │
│              │ DNS 独立解析   │                        │
│              └────────────────┘                        │
└────────────────────────────────────────────────────────┘
         │                              │
         ▼                              ▼
   Compute Node                   S3 / MinIO
   (WAL 写入)                     (远程持久化)
```

## 源代码

* <https://github.com/chirpyli/helm-charts>

## 环境要求

Kubernetes: `^1.18.x-x`

## 前置准备

部署前需创建包含 S3 凭证的 Secret（与 pageserver 共用同一 Secret）：

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
  BUCKET_NAME: "neon-safekeeper"
  AWS_REGION: "us-east-1"
```

## 安装

```console
$ git clone https://github.com/chirpyli/helm-charts.git
$ cd helm-charts/charts/neon-safekeeper
$ helm dependency update
$ helm install neon-safekeeper . -f values.yaml
```

## 配置参数

### 核心配置

| 参数 | 类型 | 默认值 | 描述 |
|-----|------|---------|-------------|
| baseId | int | `1` | 基础 ID（node_id = baseId + pod_ordinal） |
| replicas | int | `3` | 副本数（推荐 3 实现 WAL 法定人数） |

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
| settings.brokerEndpoint | string | `"http://neon-storage-broker:50051"` | Storage Broker 端点地址 |
| settings.listenHttp | string | `"0.0.0.0:7676"` | HTTP API 监听地址 |
| settings.listenPg | string | `"0.0.0.0:5454"` | PostgreSQL 协议监听地址 |

### 远程存储

| 参数 | 类型 | 默认值 | 描述 |
|-----|------|---------|-------------|
| remoteStorage.prefixInBucket | string | `"/safekeeper/"` | S3 Bucket 中的前缀路径 |
| s3Credentials.existingSecret | string | `"bucket-credentials"` | 包含 S3 凭证的已有 Secret 名称 |

### 磁盘控制

| 参数 | 类型 | 默认值 | 描述 |
|-----|------|---------|-------------|
| diskUsage.globalDiskCheckInterval | string | `"60s"` | 全局磁盘检查间隔 |
| diskUsage.maxGlobalDiskUsageRatio | float | `0.0` | 全局磁盘最大使用比例（0=不限制，推荐 0.6~0.8） |
| diskUsage.maxTimelineDiskUsageBytes | int | `0` | 单个 timeline 的磁盘使用上限（0=不限制） |

### 持久化存储

| 参数 | 类型 | 默认值 | 描述 |
|-----|------|---------|-------------|
| persistence.enabled | bool | `true` | 是否启用持久化存储 |
| persistence.size | string | `"50Gi"` | 持久卷大小 |

### 网络

| 参数 | 类型 | 默认值 | 描述 |
|-----|------|---------|-------------|
| service.headless.name | string | `"neon-safekeeper-headless"` | Headless Service 名称 |

### 其他

| 参数 | 类型 | 默认值 | 描述 |
|-----|------|---------|-------------|
| affinity | object | `{}` | Pod 亲和性调度配置 |
| extraManifests | list | `[]` | 随 Chart 一同创建的额外 Kubernetes 资源清单 |
| fullnameOverride | string | `""` | 完全覆盖 neon-safekeeper.fullname 模板的字符串 |
| nameOverride | string | `""` | 部分覆盖 neon-safekeeper.fullname 模板的字符串 |
| nodeSelector | object | `{}` | Pod 节点选择器标签 |
| podAnnotations | object | `{}` | neon-safekeeper Pod 的注解 |
| podLabels | object | `{}` | neon-safekeeper Pod 的附加标签 |
| podSecurityContext | object | `{}` | neon-safekeeper Pod 安全上下文 |
| priorityClassName | string | `""` | Pod 优先级类 |
| resources.limits.cpu | string | `"1"` | CPU 上限 |
| resources.limits.memory | string | `"2Gi"` | 内存上限 |
| resources.requests.cpu | string | `"500m"` | CPU 请求 |
| resources.requests.memory | string | `"1Gi"` | 内存请求 |
| securityContext | object | `{}` | neon-safekeeper 容器安全上下文 |
| serviceAccount.annotations | object | `{}` | 添加到 ServiceAccount 的注解 |
| serviceAccount.create | bool | `true` | 指定是否创建 ServiceAccount |
| serviceAccount.name | string | `""` | 要使用的 ServiceAccount 名称 |
| tolerations | list | `[]` | Pod 容忍调度配置 |

## 端口

| 端口 | 协议 | 用途 | Service |
|------|------|------|---------|
| `5454` | TCP | PostgreSQL WAL 协议 | Headless |
| `7676` | TCP | HTTP 管理 API (`/status`) | Headless |

## 节点 ID 分配

```
baseId=1, replicas=3:

  Pod (ordinal 0) → ID = 1 + 0 = 1
  Pod (ordinal 1) → ID = 1 + 1 = 2
  Pod (ordinal 2) → ID = 1 + 2 = 3
```

各 Pod 通过以下 DNS 可被独立访问：

```
neon-safekeeper-0.neon-safekeeper-headless.<namespace>.svc.cluster.local
neon-safekeeper-1.neon-safekeeper-headless.<namespace>.svc.cluster.local
neon-safekeeper-2.neon-safekeeper-headless.<namespace>.svc.cluster.local
```

## WAL 法定人数

Neon 的 Safekeeper 采用法定人数（quorum）提交协议：

- 默认 3 副本，`W`（写入法定人数）= 2
- Compute 写入 WAL 时需至少 2 个 Safekeeper 确认
- 读取时需从最新写入的副本获取
- 允许容忍 1 个副本故障而不丢失数据

## 健康检查

| 探针 | 方式 | 路径 | 配置 |
|------|------|------|------|
| startupProbe | `httpGet` | `/status` | 最长等待 5 分钟（30 × 10s） |
| livenessProbe | `httpGet` | `/status` | 每 15s 检查，超时 10s |
| readinessProbe | `httpGet` | `/status` | 每 15s 检查，超时 10s |

## 其他

### 查看日志

```console
$ kubectl logs -f safekeeper-0
```

### 检查状态

```console
# 检查所有副本
$ for i in 0 1 2; do
    echo "=== safekeeper-$i ==="
    kubectl exec neon-safekeeper-$i -- curl -s http://localhost:7676/status
  done
```

### 扩容

```console
# 扩容到 5 副本（注意：需确保 baseId 正确，新 Pod 获得新 ID）
$ helm upgrade neon-safekeeper ./charts/neon-safekeeper --set replicas=5
```

----------------------------------------------
由 Chart 元数据通过 [helm-docs](https://github.com/norwoodj/helm-docs) 自动生成
