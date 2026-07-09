# neon-pg-sni-router

![Version: 0.0.6](https://img.shields.io/badge/Version-0.0.6-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) [![Lint and Test Charts](https://github.com/neondatabase/helm-charts/actions/workflows/lint-test.yaml/badge.svg)](https://github.com/neondatabase/helm-charts/actions/workflows/lint-test.yaml)

Neon PostgreSQL SNI 路由器

**主页:** https://neon.tech

## 源码

* [https://github.com/neondatabase/neon](https://github.com/neondatabase/neon)
* [https://github.com/neondatabase/helm-charts](https://github.com/neondatabase/helm-charts)

## 安装 Chart

使用 release 名称 `neon-pg-sni-router` 安装 chart：

```console
$ helm repo add neondatabase https://neondatabase.github.io/helm-charts
$ helm install neon-pg-sni-router neondatabase/neon-pg-sni-router
```

## 环境要求

Kubernetes: `^1.18.x-x`

## 配置项

| 配置项                               | 类型   | 默认值                                                                                  | 说明                                                                                      |
| ------------------------------------ | ------ | --------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------- |
| affinity                             | object | `{}`                                                                                  | Pod 调度的亲和性策略                                                                      |
| containerLifecycle                   | object | `{}`                                                                                  | neon-pg-sni-router 容器的生命周期钩子配置                                                 |
| deploymentStrategy                   | object | `{"rollingUpdate":{"maxSurge":"100%","maxUnavailable":"50%"},"type":"RollingUpdate"}` | Deployment 的更新策略覆盖                                                                 |
| exposedService.annotations           | object | `{}`                                                                                  | 外部暴露 Service 的注解                                                                   |
| exposedService.httpsPort             | int    | `nil`                                                                                 | 外部暴露的 HTTPS 端口。为 null 时不暴露 HTTPS 服务                                        |
| exposedService.port                  | int    | `5432`                                                                                | 外部暴露的代理端口                                                                        |
| exposedService.portTls               | int    | `5433`                                                                                | 使用 TLS 连接计算节点的外部暴露代理端口                                                   |
| exposedService.type                  | string | `"LoadBalancer"`                                                                      | 外部暴露的 Service 类型                                                                   |
| extraManifests                       | list   | `[]`                                                                                  | 随 chart 创建的额外 K8s 清单                                                              |
| fullnameOverride                     | string | `""`                                                                                  | 完全覆盖 neon-pg-sni-router.fullname 模板名称                                             |
| image.pullPolicy                     | string | `"IfNotPresent"`                                                                      | 镜像拉取策略                                                                              |
| image.repository                     | string | `"neondatabase/neon"`                                                                 | Neondatabase 镜像仓库地址                                                                 |
| image.tag                            | string | `"latest"`                                                                            | 覆盖镜像 tag，默认为 chart 的 appVersion                                                  |
| imagePullSecrets                     | list   | `[]`                                                                                  | 以数组形式指定 docker-registry 的 secret 名称                                             |
| internalCa                           | string | `nil`                                                                                 | neon-proxy 用于验证计算节点 TLS 证书的根证书                                              |
| metrics.enabled                      | bool   | `false`                                                                               | 启用 Prometheus 指标自动发现                                                              |
| metrics.serviceMonitor.enabled       | bool   | `false`                                                                               | 创建 ServiceMonitor 资源                                                                  |
| metrics.serviceMonitor.interval      | string | `"10s"`                                                                               | Prometheus 抓取指标的时间间隔                                                             |
| metrics.serviceMonitor.namespace     | string | `""`                                                                                  | 创建 ServiceMonitor 的命名空间，为空则使用 Release.Namespace                              |
| metrics.serviceMonitor.scrapeTimeout | string | `"10s"`                                                                               | Prometheus 抓取超时时间                                                                   |
| metrics.serviceMonitor.selector      | object | `{}`                                                                                  | 附加标签（供 Prometheus operator 使用）                                                   |
| nameOverride                         | string | `""`                                                                                  | 部分覆盖 neon-pg-sni-router.fullname 模板名称（保留 release 名称）                        |
| nodeSelector                         | object | `{}`                                                                                  | Pod 调度的节点标签选择器                                                                  |
| podAnnotations                       | object | `{}`                                                                                  | neon-pg-sni-router Pod 的注解                                                             |
| podLabels                            | object | `{}`                                                                                  | neon-pg-sni-router Pod 的附加标签                                                         |
| podSecurityContext                   | object | `{}`                                                                                  | neon-pg-sni-router Pod 的安全上下文                                                       |
| replicaCount                         | int    | `1`                                                                                   | 副本数                                                                                    |
| resources.limits.memory              | string | `"1Gi"`                                                                               | 内存上限                                                                                  |
| resources.requests.cpu               | string | `"200m"`                                                                              | CPU 请求量                                                                                |
| resources.requests.memory            | string | `"512Mi"`                                                                             | 内存请求量                                                                                |
| securityContext                      | object | `{}`                                                                                  | neon-pg-sni-router 容器的安全上下文                                                       |
| service.annotations                  | object | `{}`                                                                                  | Service 的注解                                                                            |
| service.httpPort                     | int    | `9090`                                                                                | HTTP 管理端口                                                                             |
| service.port                         | int    | `7000`                                                                                | Service 管理端口                                                                          |
| service.type                         | string | `"ClusterIP"`                                                                         | Service 类型                                                                              |
| serviceAccount.annotations           | object | `{}`                                                                                  | ServiceAccount 的注解                                                                     |
| serviceAccount.create                | bool   | `true`                                                                                | 是否创建 ServiceAccount                                                                   |
| serviceAccount.name                  | string | `""`                                                                                  | ServiceAccount 名称                                                                       |
| settings.authBackend                 | string | `"link"`                                                                              | 认证方式（console\|link\|postgres）                                                       |
| settings.authEndpoint                | string | `""`                                                                                  | 认证端点，例如 "http://console.neon/authenticate_proxy_request/"                          |
| settings.destination                 | string | `"svc.cluster.local"`                                                                 | 将此域名后缀追加到转换后的 SNI 主机名以获取目标地址，例如 "svc.cluster.local"             |
| settings.domain                      | string | `"dummy"`                                                                             | 客户端 PostgreSQL 连接的 TLS 证书中使用的域名                                             |
| settings.metricCollectionEndpoint    | string | `""`                                                                                  | (url) 指标上报端点。为 null 时不发送指标                                                  |
| settings.metricCollectionInterval    | string | `""`                                                                                  | (string) 指标发送频率                                                                     |
| settings.sentryEnvironment           | string | `"development"`                                                                       | "development" 或 "production"，在 Sentry 中可见用于筛选问题                               |
| settings.sentryUrl                   | string | `""`                                                                                  | Sentry 用于收集 neon-pg-sni-router 错误/panic 事件的 URL（转换为`SENTRY_DSN` 环境变量） |
| settings.uri                         | string | `""`                                                                                  | URI 配置                                                                                  |
| settings.wssPort                     | int    | `nil`                                                                                 | WSS/HTTPS 端口。为 null 时不启动 WSS 服务                                                 |
| terminationGracePeriodSeconds        | int    | `30`                                                                                  | Deployment 的优雅终止宽限期（秒）                                                         |
| tolerations                          | list   | `[]`                                                                                  | Pod 调度的容忍策略                                                                        |
| useCertManager                       | bool   | `false`                                                                               | 是否使用 cert-manager                                                                     |

---

由 [helm-docs v1.9.1](https://github.com/norwoodj/helm-docs/releases/v1.9.1) 从 chart 元数据自动生成
