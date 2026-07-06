# neon-proxy

![Version: 1.15.0](https://img.shields.io/badge/Version-1.15.0-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) [![Lint and Test Charts](https://github.com/neondatabase/helm-charts/actions/workflows/lint-test.yaml/badge.svg)](https://github.com/neondatabase/helm-charts/actions/workflows/lint-test.yaml)

Neon 代理服务

**主页:** https://neon.tech

## 源码

* <https://github.com/neondatabase/neon>

## 安装 Chart

使用 release 名称 `neon-proxy` 安装 chart：

```console
$ helm repo add neondatabase https://neondatabase.github.io/helm-charts
$ helm install neon-proxy neondatabase/neon-proxy
```

## 环境要求

Kubernetes: `^1.18.x-x`

## 配置项

| 配置项 | 类型 | 默认值 | 说明 |
|-----|------|---------|-------------|
| affinity | object | `{}` | Pod 调度的亲和性策略 |
| containerLifecycle | object | `{}` | neon-proxy 容器的生命周期钩子配置 |
| deploymentStrategy | object | `{"type":"Recreate"}` | Deployment 的更新策略覆盖 |
| exposedService.annotations | object | `{}` | 外部暴露 Service 的注解 |
| exposedService.externalTrafficPolicy | string | `"Cluster"` | 外部流量策略（Cluster, Internal） |
| exposedService.httpsPort | int | `nil` | 外部暴露的 HTTPS 端口。为 null 时不暴露 HTTPS 服务 |
| exposedService.name | string | `""` | 此 Service 的名称 |
| exposedService.port | int | `5432` | 外部暴露的代理端口。为 null 时不暴露代理端口，适用于 auth-broker 场景 |
| exposedService.type | string | `"LoadBalancer"` | 外部暴露的 Service 类型 |
| extraManifests | list | `[]` | 随 chart 创建的额外 K8s 清单 |
| fullnameOverride | string | `""` | 完全覆盖 neon-proxy.fullname 模板名称 |
| image.pullPolicy | string | `"Always"` | 镜像拉取策略 |
| image.repository | string | `"neondatabase/neon"` | Neondatabase 镜像仓库 |
| image.tag | string | `"latest"` | 覆盖镜像 tag，默认为 chart 的 appVersion |
| imagePullSecrets | list | `[]` | 以数组形式指定 docker-registry 的 secret 名称 |
| internalCa | string | `nil` | neon-proxy 用于验证计算节点 TLS 证书的根证书 |
| metrics.enabled | bool | `false` | 启用 Prometheus 指标自动发现 |
| metrics.serviceMonitor.enabled | bool | `false` | 创建 ServiceMonitor 资源 |
| metrics.serviceMonitor.interval | string | `"10s"` | Prometheus 抓取指标的时间间隔 |
| metrics.serviceMonitor.namespace | string | `""` | 创建 ServiceMonitor 的命名空间，为空则使用 Release.Namespace |
| metrics.serviceMonitor.scrapeTimeout | string | `"10s"` | Prometheus 抓取超时时间 |
| metrics.serviceMonitor.selector | object | `{}` | 附加标签（供 Prometheus operator 使用） |
| nameOverride | string | `""` | 部分覆盖 neon-proxy.fullname 模板名称（保留 release 名称） |
| nodeSelector | object | `{}` | Pod 调度的节点标签选择器 |
| pgSniRouter.destination | string | `"svc.cluster.local"` | 将此域名后缀追加到转换后的 SNI 主机名以获取目标地址，例如 "svc.cluster.local" |
| pgSniRouter.domain | string | `""` | 客户端 PostgreSQL 连接的 TLS 证书中使用的域名 |
| pgSniRouter.exposedService.annotations | object | `{}` | 外部暴露 Service 的注解 |
| pgSniRouter.exposedService.name | string | `""` | 此 Service 的名称 |
| pgSniRouter.exposedService.port | int | `5432` | 外部暴露的代理端口 |
| pgSniRouter.exposedService.portTls | int | `5433` | 使用 TLS 连接计算节点的外部暴露代理端口 |
| pgSniRouter.exposedService.type | string | `"LoadBalancer"` | 外部暴露的 Service 类型 |
| podAnnotations | object | `{}` | neon-proxy Pod 的注解 |
| podLabels | object | `{}` | neon-proxy Pod 的附加标签 |
| podSecurityContext | object | `{}` | neon-proxy Pod 的安全上下文 |
| replicaCount | int | `1` | 副本数 |
| resources.limits.memory | string | `"32Gi"` | 内存上限 |
| resources.requests.cpu | string | `"400m"` | CPU 请求量 |
| resources.requests.memory | string | `"2Gi"` | 内存请求量 |
| securityContext | object | `{}` | neon-proxy 容器的安全上下文 |
| service.annotations | object | `{}` | Service 的注解 |
| service.httpPort | int | `9090` | HTTP 管理端口 |
| service.port | int | `7000` | Service 管理端口 |
| service.type | string | `"ClusterIP"` | Service 类型 |
| serviceAccount.annotations | object | `{}` | ServiceAccount 的注解 |
| serviceAccount.create | bool | `true` | 是否创建 ServiceAccount |
| serviceAccount.name | string | `""` | ServiceAccount 名称 |
| settings.authBackend | string | `"link"` | 认证方式（console\|link\|postgres） |
| settings.authEndpoint | string | `""` | 认证端点，例如 "http://console.neon/authenticate_proxy_request/" |
| settings.authRateLimits | string | `nil` | 认证速率限制配置 |
| settings.authRateLimitsEnabled | bool | `nil` | 是否启用认证速率限制 |
| settings.awsAccessKeyId | string | `""` | AWS Access Key ID |
| settings.awsRegion | string | `""` | 获取凭证的 AWS 区域 |
| settings.awsSecretAccessKey | string | `""` | AWS Secret Access Key |
| settings.connectComputeLock | string | `""` | 按 compute 配置 connect_compute 的锁定策略 |
| settings.consoleJwtPublicKey | string | `""` | Console JWT 验证的公钥 |
| settings.controlplane_token | string | `""` | 传递给 Control Plane 管理 API 的 JWT token |
| settings.domain | string | `""` | 客户端 PostgreSQL 连接的 TLS 证书中使用的域名 |
| settings.enable_neonMotd | bool | `false` | 是否启用 neon 欢迎信息 |
| settings.endpointCacheConfig | string | `""` | 所有有效 endpoint 的缓存配置 |
| settings.endpointRpsLimits | list | `[]` | 不同时间间隔下的连接尝试速率限制器列表 |
| settings.extraCmdFlags | list | `[]` | proxy 二进制的额外命令行参数 |
| settings.extraDomains | list | `[]` | 客户端 PostgreSQL 连接额外 TLS 证书中使用的域名 |
| settings.extraEnvVars | list | `[]` | proxy 二进制的额外环境变量 |
| settings.httpPoolOptIn | bool | `true` | 为 true 时 SQL over HTTP 连接池为 opt-in 模式；false 时始终启用 |
| settings.logfmt | string | `nil` | 日志格式："text"（默认）或 "json" |
| settings.metricBackupCollectionChunkSize | string | `"4194304"` | 指标备份文件中每个 chunk 的大小（字节） |
| settings.metricBackupCollectionInterval | string | `"10m"` | 指标备份采集间隔 |
| settings.metricBackupCollectionRemoteStorage | string | `""` | 指标备份文件的上传目标存储位置 |
| settings.metricCollectionEndpoint | string | `""` | 指标上报端点。为 null 时不发送指标 |
| settings.metricCollectionInterval | string | `""` | 指标发送频率 |
| settings.otelExporterDisabled | bool | `false` | 禁用 OpenTelemetry（转换为 `OTEL_SDK_DISABLED` 环境变量） |
| settings.otelExporterOtlpEndpoint | string | `""` | OpenTelemetry Collector URL（转换为 `OTEL_EXPORTER_OTLP_ENDPOINT` 环境变量） |
| settings.parquetUploadCompression | string | `"uncompressed"` | Parquet 文件压缩级别 |
| settings.parquetUploadDisconnectEventsRemoteStorage | string | `""` | 包含断连事件的 Parquet 文件上传目标存储位置 |
| settings.parquetUploadMaximumDuration | string | `"20m"` | 强制上传文件前的最大等待时间 |
| settings.parquetUploadPageSize | string | `"1048576"` | 每个列页的大小（字节） |
| settings.parquetUploadRemoteStorage | string | `""` | Parquet 文件的上传目标存储位置 |
| settings.parquetUploadRowGroupSize | string | `"8192"` | 每个 row group 包含的行数 |
| settings.parquetUploadSize | string | `"100000000"` | Parquet 文件总大小的上限（字节） |
| settings.proxyProtocolV2 | string | `""` | 是否启用 PROXY protocol V2 解析。可选值："rejected"、"supported"、"required" |
| settings.redisAuthType | string | `"irsa"` | 区域 Redis 客户端的认证方式。支持 "irsa" 和 "plain"。"plain" 使用 settings.redisNotifications 中的 URI；"irsa" 使用 AWS IRSA |
| settings.redisClusterName | string | `"regional-control-plane-redis"` | Redis 集群名称，用于 AWS Elasticache |
| settings.redisHost | string | `""` | 流式连接的 Redis 主机（可能与通知主机不同） |
| settings.redisNotifications | string | `""` | Redis 客户端配置 URL |
| settings.redisPort | string | `""` | 流式连接的 Redis 端口 |
| settings.redisUserId | string | `"neon"` | Redis user_id，用于 AWS Elasticache |
| settings.region | string | `""` | 此 proxy 服务部署的区域 |
| settings.rustLog | string | `"INFO"` | Proxy 日志级别 |
| settings.sentryEnvironment | string | `"development"` | "development" 或 "production"，在 Sentry 中可见用于筛选问题 |
| settings.sentryUrl | string | `""` | Sentry 用于收集 neon-proxy 错误/panic 事件的 URL（转换为 `SENTRY_DSN` 环境变量） |
| settings.sqlOverHttpMaxRequestSizeBytes | string | `"10485760"` | SQL over HTTP 请求体的最大大小（字节） |
| settings.sqlOverHttpMaxResponseSizeBytes | string | `"10485760"` | SQL over HTTP 响应体的最大大小（字节） |
| settings.sqlOverHttpTimeout | string | `"15s"` | HTTP 连接请求的超时时间 |
| settings.uri | string | `""` | URI 配置 |
| settings.useCertManager | bool | `true` | 是否使用 cert-manager |
| settings.wakeComputeLimits | list | `[]` | 不同时间间隔下 wake_compute 的速率限制器列表 |
| settings.wakeComputeLock | string | `"permits=0"` | 按 endpoint 配置 wake_compute 的锁定策略 |
| settings.wssPort | int | `nil` | WSS/HTTPS 端口。为 null 时不启动 WSS 服务 |
| terminationGracePeriodSeconds | int | `30` | Deployment 的优雅终止宽限期（秒） |
| tolerations | list | `[]` | Pod 调度的容忍策略 |
| topologySpreadConstraints | object | `{}` | Pod 调度的拓扑分布约束 |
| zones | object | `{}` | 部署分区。为空时 1 个 Deployment 覆盖所有分区；非空时每个分区有 1 个独立 Deployment |

----------------------------------------------
由 [helm-docs v1.9.1](https://github.com/norwoodj/helm-docs/releases/v1.9.1) 从 chart 元数据自动生成
