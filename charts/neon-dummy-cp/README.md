# neon-dummy-cp

![Version: 0.1.0](https://img.shields.io/badge/Version-0.1.0-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square)

Neon Dummy Control Plane — 用于吸收 Neon Compute Hook 通知的占位 HTTP 服务。


## 简介

`neon-dummy-cp` 是一个最小化的 HTTP 服务，基于 `python:3.11-slim` 镜像内置的 `http.server` 模块实现。它只是一个临时占位组件，用于在真正的 Control Plane 尚未部署时，吞掉来自 Storage Controller 的 Compute Hook 通知（如 `/notify-attach`、`/notify-safekeepers`），确保 Neon 存储集群可以正常运行。

**当前实现**
- ~20 行内联 Python 代码，零外部依赖
- 所有 GET / POST / PUT 请求均返回 `200 {"status":"ok"}`
- 镜像体积 ~50MB，资源占用极低（默认 50m CPU / 64Mi memory）

**未来方向**
- 当前阶段（dummy）：保持 Python，不改动
- 中期（简单 CP）：可切换为 Go（K8s 生态最佳、Neon 已有 storage-broker 先例）
- 长期（完整 CP）：可切换为 Rust（与 Neon 上游核心组件栈对齐）

## 源代码

* <https://github.com/chirpyli/helm-charts>

## 安装 Chart

使用发布名称 `neon-dummy-cp` 安装 Chart：

```console
$ helm dependency update
$ helm install neon-dummy-cp ./charts/neon-dummy-cp
```

## 环境要求

Kubernetes: `^1.18.x-x`

## 配置参数

| 参数 | 类型 | 默认值 | 描述 |
|-----|------|---------|-------------|
| affinity | object | `{}` | Pod 亲和性调度配置 |
| extraManifests | list | `[]` | 随 Chart 一同创建的额外 Kubernetes 资源清单 |
| fullnameOverride | string | `""` | 完全覆盖 neon-dummy-cp.fullname 模板的字符串 |
| image.pullPolicy | string | `"IfNotPresent"` | 镜像拉取策略 |
| image.repository | string | `"python"` | Python 镜像仓库 |
| image.tag | string | `"3.11-slim"` | 覆盖镜像标签，默认为 Chart 的 appVersion |
| imagePullSecrets | list | `[]` | 指定 docker-registry 的 Secret 名称数组 |
| nameOverride | string | `""` | 部分覆盖 neon-dummy-cp.fullname 模板的字符串（保留发布名称） |
| nodeSelector | object | `{}` | Pod 节点选择器标签 |
| podAnnotations | object | `{}` | neon-dummy-cp Pod 的注解 |
| podLabels | object | `{}` | neon-dummy-cp Pod 的附加标签 |
| podSecurityContext | object | `{}` | neon-dummy-cp Pod 安全上下文 |
| priorityClassName | string | `""` | Pod 优先级类 |
| resources.limits.cpu | string | `"100m"` | CPU 上限 |
| resources.limits.memory | string | `"128Mi"` | 内存上限 |
| resources.requests.cpu | string | `"50m"` | CPU 请求 |
| resources.requests.memory | string | `"64Mi"` | 内存请求 |
| securityContext | object | `{}` | neon-dummy-cp 容器安全上下文 |
| service.annotations | object | `{}` | 添加到 Service 的注解 |
| service.port | int | `8080` | dummy-cp 监听端口 |
| service.type | string | `"ClusterIP"` | Service 类型 |
| serviceAccount.annotations | object | `{}` | 添加到 ServiceAccount 的注解 |
| serviceAccount.create | bool | `true` | 指定是否创建 ServiceAccount |
| serviceAccount.name | string | `""` | 要使用的 ServiceAccount 名称 |
| tolerations | list | `[]` | Pod 容忍调度配置 |

## API 端点

本服务对所有 HTTP 方法（GET / POST / PUT）的任何路径均返回：

```json
{"status": "ok"}
```

当前被 Storage Controller 调用的实际路径：

| 端点 | 方法 | 用途 |
|------|------|------|
| `/notify-attach` | PUT | 通知 attach 状态变更 |
| `/notify-safekeepers` | PUT | 通知 safekeeper 信息更新 |

----------------------------------------------
由 Chart 元数据通过 [helm-docs](https://github.com/norwoodj/helm-docs) 自动生成
