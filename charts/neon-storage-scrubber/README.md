# neon-storage-scrubber

![Version: 1.4.0](https://img.shields.io/badge/Version-1.4.0-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) [![Lint and Test Charts](https://github.com/neondatabase/helm-charts/actions/workflows/lint-test.yaml/badge.svg)](https://github.com/neondatabase/helm-charts/actions/workflows/lint-test.yaml)

neon-storage-scrubber（存储清理器）

**主页：**

## 源代码

* <https://github.com/neondatabase/neon/tree/main/storage_scrubber>

## 安装 Chart

使用发布名称 `neon-storage-scrubber` 安装 Chart：

```console
$ helm repo add neondatabase https://neondatabase.github.io/helm-charts
$ helm install neon-storage-scrubber neondatabase/neon-storage-scrubber
```

## 配置参数

| 参数 | 类型 | 默认值 | 描述 |
|-----|------|---------|-------------|
| affinity | object | `{}` | Pod 亲和性调度配置 |
| fullnameOverride | string | `""` | 完全覆盖 neon-storage-scrubber.fullname 模板的字符串 |
| image.pullPolicy | string | `"IfNotPresent"` | 镜像拉取策略 |
| image.repository | string | `"neondatabase/neon"` | 镜像仓库 |
| image.tag | string | `""` | 覆盖镜像标签，默认为 Chart 的 appVersion |
| imagePullSecrets | list | `[]` | 指定 docker-registry 的 Secret 名称数组 |
| nameOverride | string | `""` | 部分覆盖 neon-storage-scrubber.fullname 模板的字符串（保留发布名称） |
| nodeSelector | object | `{}` | Pod 节点选择器标签 |
| podAnnotations | object | `{}` | neon-storage-scrubber Pod 的注解 |
| podLabels | object | `{}` | neon-storage-scrubber Pod 的标签 |
| podSecurityContext | object | `{}` | neon-storage-scrubber Pod 安全上下文 |
| resources.limits.cpu | string | `"200m"` |  |
| resources.limits.memory | string | `"2Gi"` |  |
| securityContext | object | `{}` | neon-storage-scrubber 容器安全上下文 |
| serviceAccount.annotations | object | `{}` | 添加到 ServiceAccount 的注解 |
| serviceAccount.create | bool | `true` | 指定是否创建 ServiceAccount |
| serviceAccount.name | string | `""` | 要使用的 ServiceAccount 名称。如果未设置且 create 为 true，则使用 fullname 模板生成名称 |
| settings.extraEnvs | list | `[{"name":"RUST_BACKTRACE","value":"1"},{"name":"PAGESERVER_DISABLE_FILE_LOGGING","value":"1"}]` | 运行 Job 时的额外环境变量 |
| settings.sentryEnvironment | string | `"development"` | "development" 或 "production"，在 Sentry 中用于过滤问题 |
| settings.sentryUrl | string | `""` | URL（将转换为 `SENTRY_DSN` 环境变量），Sentry 用于收集 neon-pg-sni-router 中的错误/panic 事件 |
| storageScrubber.activeDeadlineSeconds | int | `86400` | CronJob 运行的超时时间 |
| storageScrubber.awsBucket | string | `""` | Pageserver 存储的 AWS Bucket |
| storageScrubber.awsRegion | string | `""` | 运行 scrubber 的 AWS 区域 |
| storageScrubber.command | list | `["pageserver-physical-gc","--min-age=1week"]` | 要执行的命令 |
| storageScrubber.enableStorageControllerConnection | bool | `false` | 启用 storage controller 相关功能 |
| storageScrubber.remoteStorageConfig | object | `{}` | 连接到远程存储的配置对象（可替代前两个变量） |
| storageScrubber.schedule | string | `"0 18 * * *"` |  |
| storageScrubber.storageControllerJwtToken | string | `""` | 连接到 storage controller 的 Control Plane / storage controller JWT Token |
| storageScrubber.storageControllerUrl | string | `""` | Storage Controller 的 URL |
| storageScrubber.timeZone | string | `"Etc/UTC"` | CronJob 的时区 |
| tolerations | list | `[]` | Pod 容忍调度配置 |

----------------------------------------------
由 Chart 元数据通过 [helm-docs v1.9.1](https://github.com/norwoodj/helm-docs/releases/v1.9.1) 自动生成
