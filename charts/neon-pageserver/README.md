# neon-pageserver

![Version: 0.1.0](https://img.shields.io/badge/Version-0.1.0-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) [![Lint and Test Charts](https://github.com/neondatabase/helm-charts/actions/workflows/lint-test.yaml/badge.svg)](https://github.com/neondatabase/helm-charts/actions/workflows/lint-test.yaml)

Neon pageserver（有状态存储节点，物化 tenant 层数据到本地存储并持久化到对象存储）。

**Homepage:** https://neon.tech

## Source Code

* <https://github.com/neondatabase/neon>

## 简介

pageserver 是 Neon 存储层的核心组件，负责：
- 从 safekeeper 接收 WAL 流并物化为 tenant 层数据（页层面）
- 对 compute 节点暴露 PostgreSQL 协议（端口 64000），支持只读/读写连接
- 定期将数据发布到远程对象存储（MinIO / S3）
- 接收 storage_controller 的调度指令（attach/detach 租户）

本 chart 以 **StatefulSet + PVC** 方式部署 pageserver，保证 Pod 重建后数据不丢失、网络标识稳定。

## 架构要点

| 资源 | 说明 |
|------|------|
| StatefulSet | 有状态编排，每个 Pod 拥有独立持久卷 |
| volumeClaimTemplates | 自动为每个 Pod 创建 PVC |
| Headless Service | 稳定的 DNS 地址 `{pod}.{headless-svc}` |
| ClusterIP Service | 集群内统一访问 pageserver 端点 |
| ConfigMap | 渲染 `pageserver.toml` 配置文件 |
| ServiceAccount | 运行时的身份标识 |
| PodDisruptionBudget | 保证滚动维护时最小可用数 |

## 配置说明（pageserver.toml）

pageserver **仅通过 TOML 配置文件**启动（`-D workdir` 指定工作目录，其余配置全部在 `pageserver.toml` 中），关键配置项：

- `listen_pg_addr / listen_http_addr`：监听地址与端口
- `broker_endpoint`：storage_broker 地址（WAL 流节点发现）
- `remote_storage`：外部 MinIO / S3 对象存储配置
- `control_plane_api`：storage_controller upcall 基址（需带 `/upcall/v1` 前缀）
- `auth_validation_public_key_path`：JWT 公钥路径（校验 compute / SC 的 token）

## 安装

```console
$ helm repo add neondatabase https://neondatabase.github.io/helm-charts
$ helm install neon-pageserver neondatabase/neon-pageserver
```

通过 umbrella chart 一键部署（推荐）：

```console
$ helm install neon ./charts/neon
```

## 前置依赖

- **外部 MinIO / S3**：对象存储后端，凭证通过 `bucket-credentials` Secret 提供
- **storage_broker**：WAL 流节点发现（需先部署 `neon-storage-broker`）
- **storage_controller**：租户调度与心跳管理（需先部署 `neon-storage-controller`）
- **JWT 密钥对**：Ed25519 公钥用于校验入站请求

## Requirements

Kubernetes: `^1.18.x-x`

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| affinity | object | `{}` | 亲和性（建议 podAntiAffinity 跨节点分布） |
| extraManifests | list | `[]` | 额外创建的 K8s 清单 |
| fullnameOverride | string | `""` | 完全覆盖 fullname 模板 |
| global | object | `{}` | 全局配置（umbrella 化时由父 chart global 值覆盖） |
| image.pullPolicy | string | `"IfNotPresent"` | 镜像拉取策略 |
| image.repository | string | `"neondatabase/neon"` | Neondatabase 镜像仓库（含 pageserver / safekeeper / compute_ctl） |
| image.tag | string | `"latest"` | 覆盖镜像 tag（默认取 chart appVersion） |
| imagePullSecrets | list | `[]` | docker-registry secret 列表 |
| metrics.enabled | bool | `false` | 启用 Prometheus 指标自动发现 |
| metrics.serviceMonitor.enabled | bool | `false` | 创建 ServiceMonitor 资源 |
| metrics.serviceMonitor.interval | string | `"10s"` | Prometheus 抓取间隔 |
| metrics.serviceMonitor.namespace | string | `""` | ServiceMonitor 命名空间（空则用 Release.Namespace） |
| metrics.serviceMonitor.scrapeTimeout | string | `"10s"` | 抓取超时 |
| metrics.serviceMonitor.selector | object | `{}` | 附加标签（供 Prometheus operator 使用） |
| nameOverride | string | `""` | 部分覆盖 fullname 模板 |
| nodeSelector | object | `{}` | 节点选择 |
| podAnnotations | object | `{}` | Pod 注解 |
| podDisruptionBudget.maxUnavailable | int | `1` | 最大不可用副本数 |
| podDisruptionBudget.minAvailable | int | `0` | 最小可用副本数（单副本设 0 表示允许中断） |
| podLabels | object | `{}` | Pod 额外标签 |
| podSecurityContext | object | `{}` | Pod 安全上下文 |
| priorityClassName | string | `""` | Pod 优先级类 |
| securityContext | object | `{}` | 容器安全上下文 |
| service.type | string | `"ClusterIP"` | Service 类型 |
| service.grpcPort | int | `51051` | gRPC 实验端口 |
| service.httpPort | int | `9898` | HTTP 管理端口（SC 调用 / 探针 /metrics） |
| service.httpsPort | int | `9899` | HTTPS 管理端口 |
| service.pgPort | int | `64000` | pg/libpq 端口（compute 连接） |
| serviceAccount.annotations | object | `{}` | SA 注解 |
| serviceAccount.create | bool | `true` | 是否创建 ServiceAccount |
| serviceAccount.name | string | `""` | 显式指定 SA 名称 |
| settings.availabilityZone | string | `"az1"` | 可用区标识 |
| settings.brokerEndpoint | string | `"http://neon-broker-svc:50051"` | storage_broker 地址（WAL 流节点发现） |
| settings.jwtSecretName | string | `"neon-jwt"` | 共享 JWT 公钥 Secret 名称 |
| settings.remoteStorage.bucketName | string | `"neondata"` | 对象存储 bucket 名称 |
| settings.remoteStorage.bucketRegion | string | `"us-east-1"` | 对象存储 region |
| settings.remoteStorage.endpoint | string | `"http://192.168.232.128:9000"` | 对象存储 endpoint |
| settings.remoteStorage.prefixInBucket | string | `"pageserver"` | 对象存储路径前缀 |
| settings.storageControllerApiToken | string | `""` | SC→PS 的 PageServerApi JWT（来自全局 jwt Secret） |
| settings.storageControllerUrl | string | `"http://neon-storage-controller-svc:50051"` | storage_controller 基址（模板自动追加 /upcall/v1 前缀） |
| statefulSet.nodeIdBase | int | `1000` | node id 基址（每 pod id = base + ordinal） |
| statefulSet.replicas | int | `1` | 副本数（生产可分片调度增大） |
| statefulSet.resources.limits.cpu | string | `"500m"` | CPU 上限（测试环境；生产建议 >= 4） |
| statefulSet.resources.limits.memory | string | `"512Mi"` | 内存上限（测试环境；生产建议 >= 8Gi） |
| statefulSet.resources.requests.cpu | string | `"500m"` | CPU 请求（Guaranteed QoS） |
| statefulSet.resources.requests.memory | string | `"512Mi"` | 内存请求（Guaranteed QoS） |
| statefulSet.storage.mountPath | string | `"/home/postgres/pageserver"` | 数据卷挂载路径 |
| statefulSet.storage.size | string | `"5Gi"` | PVC 大小（测试环境；生产建议 >= 50Gi） |
| statefulSet.storage.storageClassName | string | `""` | StorageClass（留空使用集群默认；生产建议本地 SSD） |
| tolerations | list | `[]` | 容忍 |

----------------------------------------------
Autogenerated from chart metadata using [helm-docs v1.9.1](https://github.com/norwoodj/helm-docs/releases/v1.9.1)
