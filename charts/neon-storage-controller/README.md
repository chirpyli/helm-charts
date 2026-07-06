# neon-storage-controller

![Version: 1.19.0](https://img.shields.io/badge/Version-1.19.0-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) [![Lint and Test Charts](https://github.com/neondatabase/helm-charts/actions/workflows/lint-test.yaml/badge.svg)](https://github.com/neondatabase/helm-charts/actions/workflows/lint-test.yaml)

Neon 存储控制器（Storage Controller）

**主页：** https://neon.tech

## 源代码

* <https://github.com/neondatabase/neon>

## 安装 Chart

使用发布名称 `neon-storage-controller` 安装 Chart：

```console
$ helm repo add neondatabase https://neondatabase.github.io/helm-charts
$ helm install neon-storage-controller neondatabase/neon-storage-controller
```

## 环境要求

Kubernetes: `^1.18.x-x`

## 配置参数

| 参数 | 类型 | 默认值 | 描述 |
|-----|------|---------|-------------|
| affinity | object | `{}` | Pod 亲和性调度配置 |
| extraManifests | list | `[]` | 随 Chart 一同创建的额外 Kubernetes 资源清单 |
| fullnameOverride | string | `""` | 完全覆盖 neon-storage-controller.fullname 模板的字符串 |
| image.pullPolicy | string | `"Always"` | 镜像拉取策略 |
| image.repository | string | `"neondatabase/neon"` | Neondatabase 镜像仓库 |
| image.tag | string | `"latest"` | 覆盖镜像标签，默认为 Chart 的 appVersion |
| imagePullSecrets | list | `[]` | 指定 docker-registry 的 Secret 名称数组 |
| ingress.annotations | object | `{}` | Ingress 资源的附加注解 |
| ingress.className | string | `"nginx-int"` | 控制器的 Ingress 类 |
| ingress.enabled | bool | `true` | 启用 Ingress 控制器资源 |
| ingress.hosts[0].host | string | `"chart-example.local"` |  |
| ingress.hosts[0].paths[0].path | string | `"/"` |  |
| ingress.hosts[0].paths[0].pathType | string | `"Prefix"` |  |
| ingress.hosts[0].paths[0].protocol | string | `"TCP"` |  |
| metrics.enabled | bool | `false` | 启用 Prometheus 指标自动发现 |
| metrics.serviceMonitor.enabled | bool | `false` | 创建 ServiceMonitor 资源 |
| metrics.serviceMonitor.interval | string | `"10s"` | Prometheus 抓取间隔 |
| metrics.serviceMonitor.namespace | string | `""` | 创建 ServiceMonitor 的命名空间，为空则使用 Release.Namespace |
| metrics.serviceMonitor.scrapeTimeout | string | `"10s"` | Prometheus 抓取超时时间 |
| metrics.serviceMonitor.selector | object | `{}` | 附加标签（供 Prometheus operator 使用） |
| nameOverride | string | `""` | 部分覆盖 neon-storage-controller.fullname 模板的字符串（保留发布名称） |
| nodeSelector | object | `{}` | Pod 节点选择器标签 |
| podAnnotations | object | `{}` | neon-storage-controller Pod 的注解 |
| podLabels | object | `{}` | neon-storage-controller Pod 的附加标签 |
| podSecurityContext | object | `{}` | neon-storage-controller Pod 安全上下文 |
| priorityClassName | string | `""` | Pod 优先级类 |
| registerControlPlane.controlPlaneJwtToken | string | `""` |  |
| registerControlPlane.enable | bool | `false` |  |
| registerControlPlane.resources.limits.cpu | string | `"100m"` |  |
| registerControlPlane.resources.limits.memory | string | `"128M"` |  |
| registerControlPlane.resources.requests.cpu | string | `"100m"` |  |
| registerControlPlane.resources.requests.memory | string | `"128M"` |  |
| resources.limits.cpu | string | `"2"` |  |
| resources.limits.memory | string | `"4Gi"` |  |
| resources.requests.cpu | string | `"2"` |  |
| resources.requests.memory | string | `"4Gi"` |  |
| securityContext | object | `{}` | neon-storage-controller 容器安全上下文 |
| service.annotations | object | `{}` | 添加到 Service 的注解 |
| service.port | int | `50051` | 控制器监听端口 |
| service.type | string | `"ClusterIP"` |  |
| serviceAccount.annotations | object | `{}` | 添加到 ServiceAccount 的注解 |
| serviceAccount.create | bool | `true` |  |
| serviceAccount.name | string | `""` |  |
| settings.antiEntropyJobDelay | string | `""` | 两个独立 tenant 之间的延迟时间，避免 API 请求耗尽 LBM |
| settings.antiEntropyJobEnact | bool | `false` | 如果为 true，则实际对不匹配项执行操作，修正 storcon 条目（必要时 detach，必要时调整 tenant 配置） |
| settings.antiEntropyJobInterval | string | `""` | 运行反熵后台任务的间隔（例如，每天一次、每小时一次、每周一次） |
| settings.chaosExitCrontab | string | `""` | 混沌测试：立即退出的 crontab 配置 |
| settings.chaosInterval | string | `""` | 混沌测试：tenant 迁移间隔 |
| settings.chaosSafekeeperInterval | string | `""` | 混沌测试：timeline safekeeper 迁移间隔 |
| settings.checkTimelineDigestAcrossSmallShardSplits | bool | `false` | 启用后，在小型（<20GB）shard 拆分中触发并等待参考 timeline digest，拆分后触发拆分后 digest 并检查其是否与参考值一致 |
| settings.consistencyCheckInterval | string | `""` | 后台一致性检查的间隔 |
| settings.controlPlaneJwtToken | string | `""` |  |
| settings.controlPlaneUrl | string | `""` | Control Plane API 的基础 URL（例如 https://control-plane.example.com/storage/api/v1/） |
| settings.databaseUrl | string | `""` |  |
| settings.enableLocationUpdates | bool | `false` | 启用后，开启 location_updates 子系统 |
| settings.heartbeatInterval | string | `""` | 向已注册节点发送心跳的周期 |
| settings.initialSplitShards | string | `""` | 初始 tenant 拆分时使用的 shard 数量 |
| settings.initialSplitThreshold | string | `""` | 初始 tenant 拆分的字节大小阈值 |
| settings.jwtToken | string | `""` |  |
| settings.lazyDrainsFills | string | `""` | 如果为 true，对节点 drain 和 fill 使用延迟挂载（lazy attach） |
| settings.lbmManagementUrl | string | `""` | Control Plane Management API 的基础 URL（例如 https://control-plane.example.com:1000/） |
| settings.lbmStorageUrl | string | `""` | Control Plane Storage API 的基础 URL（例如 https://control-plane.example.com:1002/storage/api/v1/） |
| settings.longReconcileThreshold | string | `"30min"` | 如果一次 reconcile 耗时超过此值，则触发告警指标 |
| settings.maxOfflineInterval | string | `""` | 将无响应的 pageserver 标记为 offline 之前的宽限期 |
| settings.maxSplitShards | string | `""` | 自动拆分的最大 shard 数量 |
| settings.maxWarmingUpInterval | string | `""` | 扩展宽限期，在此时间内 pageserver 可能不对心跳做出响应。在节点被 drain 以进行重启后和/或处理来自节点的 re-attach 请求时生效 |
| settings.neonCloud | string | `""` | neon_cloud 标签，用于 neon.com 可观测性栈 |
| settings.neonRegion | string | `""` | neon_region 标签，用于 neon.com 可观测性栈 |
| settings.pageserverAutoMigrationDiskActualUsageSourceMin | string | `""` | 触发迁移的源节点最小实际磁盘使用百分比 |
| settings.pageserverAutoMigrationDiskUtilizationDestinationMax | string | `""` | 目标节点最大磁盘使用百分比 |
| settings.pageserverAutoMigrationDiskUtilizationSourceMin | string | `""` | 触发迁移的源节点最小磁盘使用百分比 |
| settings.pageserverAutoMigrationDiskUtilizationSourceTarget | string | `""` | 迁移后源节点目标磁盘使用百分比 |
| settings.pageserverAutoMigrationEnabled | bool | `false` | 是否启用 pageserver tenant shard 自动迁移 |
| settings.pageserverAutoMigrationMaxMigrationsPerDestination | string | `""` | 每个目标节点最大并发迁移数 |
| settings.pageserverAutoMigrationMaxShardSizeLimit | string | `""` | 迁移的最大 shard 大小限制 |
| settings.peerJwtToken | string | `""` | 与其他 storage controller 实例认证的 JWT token |
| settings.posthogConfig | object | `{}` | Posthog 配置 |
| settings.publicKey | string | `""` |  |
| settings.safekeeperJwtToken | string | `""` | 与 safekeeper 认证的 JWT token |
| settings.sentryEnvironment | string | `"development"` | "development" 或 "production"，在 Sentry 中用于过滤问题 |
| settings.sentryUrl | string | `""` | URL（将转换为 `SENTRY_DSN` 环境变量），Sentry 用于收集 storage-controller 中的错误/panic 事件 |
| settings.splitThreshold | string | `""` | 自动拆分 shard 的字节大小阈值。不设置则禁用自动分片（默认） |
| settings.startAsCandidate | bool | `false` | 设置为 True 时，优雅重启服务 |
| settings.timelinesOntoSafekeepers | bool | `false` | 是否同样在 safekeeper 上创建 timelines |
| tolerations | list | `[]` | Pod 容忍调度配置 |

----------------------------------------------
由 Chart 元数据通过 [helm-docs v1.9.1](https://github.com/norwoodj/helm-docs/releases/v1.9.1) 自动生成
