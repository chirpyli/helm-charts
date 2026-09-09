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
| ClusterRole/ClusterRoleBinding | 供 init 容器读取节点标签（仅 `nodes: get`） |

## 可用区（AZ）动态获取

可用区**不通过 values 静态指定**，而是在 Pod 启动时由 `init-identity` 容器通过 kube-apiserver 读取
**所在节点**的 `topology.kubernetes.io/zone` 标签得到，随后：

- 追加进运行时生成的 `pageserver.toml`（`availability_zone`，用于同区 safekeeper 优选）；
- 写入 `metadata.json`（`availability_zone_id`，SC re-attach 注册时优先使用）。

前置条件：

```console
# 节点必须带 zone 标签，否则 Pod 会停在 Init 状态并输出中文告警日志
kubectl label node <node-name> topology.kubernetes.io/zone=<az> --overwrite
```

权限由 `rbac.nodeReader.enabled`（默认 `true`）创建的 ClusterRole 提供，仅授予 `nodes: get`；
若由平台方统一授权，可将其置为 `false`。

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

- **外部 MinIO / S3**：对象存储后端，**连接坐标（bucket / region / endpoint）与凭证统一由 `bucket-credentials` Secret 提供**（唯一事实来源，values 中不保留副本）
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
| podAntiAffinity.topologyKey | string | `"kubernetes.io/hostname"` | 节点级反亲和拓扑域（强制开启的硬约束，不可关闭）：kubernetes.io/hostname=按 k8s 节点打散（默认，一个节点一个副本）；topology.kubernetes.io/zone=按可用区打散 |
| podDisruptionBudget.maxUnavailable | int | `1` | 最大不可用副本数 |
| podDisruptionBudget.minAvailable | int | `0` | 最小可用副本数（单副本设 0 表示允许中断） |
| podLabels | object | `{}` | Pod 额外标签 |
| podSecurityContext | object | `{}` | Pod 安全上下文 |
| priorityClassName | string | `""` | Pod 优先级类 |
| rbac.nodeReader.enabled | bool | `true` | 是否创建读取节点 zone 标签的 RBAC（nodes: get，用于动态获取可用区） |
| securityContext | object | `{}` | 容器安全上下文 |
| service.type | string | `"ClusterIP"` | Service 类型 |
| service.grpcPort | int | `51051` | gRPC 实验端口 |
| service.httpPort | int | `9898` | HTTP 管理端口（SC 调用 / 探针 /metrics） |
| service.httpsPort | int | `9899` | HTTPS 管理端口 |
| service.pgPort | int | `64000` | pg/libpq 端口（compute 连接） |
| serviceAccount.annotations | object | `{}` | SA 注解 |
| serviceAccount.create | bool | `true` | 是否创建 ServiceAccount |
| serviceAccount.name | string | `""` | 显式指定 SA 名称 |
| settings.brokerEndpoint | string | `"http://neon-broker-svc:50051"` | storage_broker 地址（WAL 流节点发现） |
| settings.jwtSecretName | string | `""` | 共享 JWT Secret 名称（外部预建；留空时回退 `global.jwt.existingSecret` > `global.jwt.secretName`，三者皆空则不挂载 jwt 卷）。默认必须为空串，否则父级的 `existingSecret` 无法生效 |
| settings.remoteStorage.existingSecret | string | `""` | 对象存储 Secret 名称（**连接坐标与凭证的唯一事实来源**）；留空时回退 `global.storage.bucket.existingSecret` > `"bucket-credentials"`。默认必须为空串，否则父级传入的值无法生效 |
| settings.remoteStorage.keys.bucketName | string | `"BUCKET_NAME"` | Secret 中桶名的键（必填非空） |
| settings.remoteStorage.keys.region | string | `"AWS_REGION"` | Secret 中地域的键（必填非空） |
| settings.remoteStorage.keys.endpoint | string | `"AWS_ENDPOINT_URL"` | Secret 中 endpoint 的键（可空：留空走 AWS S3 官方 endpoint；MinIO 必填） |
| settings.remoteStorage.keys.accessKeyId | string | `"AWS_ACCESS_KEY_ID"` | Secret 中 AccessKeyId 的键（可选：IRSA / WebIdentity 场景可不存在） |
| settings.remoteStorage.keys.secretAccessKey | string | `"AWS_SECRET_ACCESS_KEY"` | Secret 中 SecretAccessKey 的键（可选，同上） |
| settings.remoteStorage.prefixInBucket | string | `"pageserver"` | 对象存储路径前缀（布局参数，非连接坐标，故保留在 values） |
| settings.storageControllerUrl | string | `"http://neon-storage-controller-svc:50051"` | storage_controller 基址（模板自动追加 /upcall/v1 前缀） |

说明：PS→SC upcall 的 JWT（generations_api scope）不再由 values 提供 ——
它只支持 TOML 内联，放在 values 里会把 token 明文渲染进 ConfigMap。
改由 init 容器从共享 JWT Secret 的 `generationsApiJwtToken` 键读出后写入 `pageserver.toml`。
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
