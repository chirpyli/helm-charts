# neon-storage-broker

![Version: 1.4.0](https://img.shields.io/badge/Version-1.4.0-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) [![Lint and Test Charts](https://github.com/neondatabase/helm-charts/actions/workflows/lint-test.yaml/badge.svg)](https://github.com/neondatabase/helm-charts/actions/workflows/lint-test.yaml)

Neon 存储代理（Storage Broker）

**主页：** https://neon.tech

## 源代码

* <https://github.com/neondatabase/neon>

## 安装 Chart

使用发布名称 `neon-storage-broker` 安装 Chart：

```console
$ helm repo add neondatabase https://neondatabase.github.io/helm-charts
$ helm install neon-storage-broker neondatabase/neon-storage-broker
```

## 环境要求

Kubernetes: `^1.18.x-x`

## 配置参数

| 参数 | 类型 | 默认值 | 描述 |
|-----|------|---------|-------------|
| affinity | object | `{}` | Pod 亲和性调度配置 |
| extraManifests | list | `[]` | 随 Chart 一同创建的额外 Kubernetes 资源清单 |
| fullnameOverride | string | `""` | 完全覆盖 neon-storage-broker.fullname 模板的字符串 |
| image.pullPolicy | string | `"Always"` | 镜像拉取策略 |
| image.repository | string | `"neondatabase/neon"` | Neondatabase 镜像仓库 |
| image.tag | string | `"latest"` | 覆盖镜像标签，默认为 Chart 的 appVersion |
| imagePullSecrets | list | `[]` | 指定 docker-registry 的 Secret 名称数组 |
| ingress.annotations | object | `{}` |  |
| ingress.className | string | `""` |  |
| ingress.enabled | bool | `false` |  |
| ingress.hosts[0].host | string | `"chart-example.local"` |  |
| ingress.hosts[0].paths[0].path | string | `"/"` |  |
| ingress.hosts[0].paths[0].pathType | string | `"ImplementationSpecific"` |  |
| ingress.tls | list | `[]` |  |
| metrics.enabled | bool | `false` | 启用 Prometheus 指标自动发现 |
| metrics.serviceMonitor.enabled | bool | `false` | 创建 ServiceMonitor 资源 |
| metrics.serviceMonitor.interval | string | `"10s"` | Prometheus 抓取间隔 |
| metrics.serviceMonitor.namespace | string | `""` | 创建 ServiceMonitor 的命名空间，为空则使用 Release.Namespace |
| metrics.serviceMonitor.scrapeTimeout | string | `"10s"` | Prometheus 抓取超时时间 |
| metrics.serviceMonitor.selector | object | `{}` | 附加标签（供 Prometheus operator 使用） |
| nameOverride | string | `""` | 部分覆盖 neon-storage-broker.fullname 模板的字符串（保留发布名称） |
| nodeSelector | object | `{}` | Pod 节点选择器标签 |
| podAnnotations | object | `{}` | neon-storage-broker Pod 的注解 |
| podLabels | object | `{}` | neon-storage-broker Pod 的附加标签 |
| podSecurityContext | object | `{}` | neon-storage-broker Pod 安全上下文 |
| priorityClassName | string | `""` | Pod 优先级类 |
| resources.limits.memory | string | `"8Gi"` |  |
| resources.requests.cpu | string | `"1"` |  |
| resources.requests.memory | string | `"2Gi"` |  |
| securityContext | object | `{}` | neon-storage-broker 容器安全上下文 |
| service.annotations | object | `{}` | 添加到 Service 的注解 |
| service.port | int | `50051` | Broker 监听端口 |
| service.type | string | `"ClusterIP"` |  |
| serviceAccount.annotations | object | `{}` | 添加到 ServiceAccount 的注解 |
| serviceAccount.create | bool | `true` |  |
| serviceAccount.name | string | `""` |  |
| settings.sentryEnvironment | string | `"development"` | "development" 或 "production"，在 Sentry 中用于过滤问题 |
| settings.sentryUrl | string | `""` | URL（将转换为 `SENTRY_DSN` 环境变量），Sentry 用于收集 storage-broker 中的错误/panic 事件 |
| tolerations | list | `[]` | Pod 容忍调度配置 |

----------------------------------------------
由 Chart 元数据通过 [helm-docs v1.9.1](https://github.com/norwoodj/helm-docs/releases/v1.9.1) 自动生成
