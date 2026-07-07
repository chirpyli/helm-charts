# neon-init-job

![Version: 0.1.0](https://img.shields.io/badge/Version-0.1.0-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square)

Neon Init Job — 集群初始化的一次性任务，负责创建 Tenant、Timeline 并注册 Safekeeper。

## 简介

`neon-init-job` 是一个 Helm post-install hook Job，在 `neon-stack` 首次部署时自动执行。
它使用 `curlimages/curl` 镜像，通过 HTTP API 调用 Storage Controller 完成以下初始化工作：

1. 等待 Storage Controller、Pageserver、Safekeeper 全部就绪
2. 将 Safekeeper 注册到 Storage Controller
3. 创建 Tenant（租户）
4. 创建 Timeline（时间线）

初始化完成后，生成 `TENANT_ID` 和 `TIMELINE_ID`，后续可供 `neon-compute` 的 ComputeSpec 配置使用。

**前置依赖**

| 组件 | 用途 |
|------|------|
| neon-storage-controller | 提供 tenant/timeline 管理 API |
| neon-pageserver | 页面存储层，初始化时需要其 `/status` 就绪 |
| neon-safekeeper | WAL 持久化层，初始化时需要全部副本就绪 |

## 执行流程

```
┌─────────────────────────────────────────────────┐
│  neon-init-job (Helm post-install hook)         │
│                                                 │
│  [1/6] 等待 Storage Controller 就绪              │
│         curl GET /health                        │
│                    ↓                            │
│  [2/6] 等待 Pageserver 就绪                     │
│         curl GET /status                        │
│                    ↓                            │
│  [3/6] 等待所有 Safekeeper 就绪                  │
│         curl GET /status (每个副本)              │
│                    ↓                            │
│  [4/6] 注册 Safekeeper 到 Storage Controller     │
│         POST /control/v1/safekeeper/{id}        │
│                    ↓                            │
│  [5/6] 创建 Tenant                              │
│         POST /v1/tenant                         │
│                    ↓                            │
│  [6/6] 创建 Timeline（Bootstrap 模式）           │
│         POST /v1/tenant/{id}/timeline           │
│                                                 │
│  → 输出: TENANT_ID TIMELINE_ID                   │
└─────────────────────────────────────────────────┘
```

## Hook 行为

| 配置 | 值 | 说明 |
|------|-----|------|
| `helm.sh/hook` | `post-install` | 在 `helm install` 后执行 |
| `helm.sh/hook-weight` | `-4` | 权重（负数优先执行） |
| `helm.sh/hook-delete-policy` | `before-hook-creation` | 重新安装时先删除旧 Job |
| `ttlSecondsAfterFinished` | `300` | 完成后 5 分钟自动清理 |

> **注意**：当前仅 Hook `post-install`，`helm upgrade` 不会重新执行。
> 如需升级后重新初始化，需要手动删除旧 Job 并触发重新创建。

## 源代码

* <https://github.com/chirpyli/helm-charts>

## 环境要求

Kubernetes: `^1.18.x-x`

## 安装

```console
$ git clone https://github.com/chirpyli/helm-charts.git
$ cd helm-charts/charts/neon-init-job
$ helm dependency update
$ helm install neon-init-job . -f values.yaml
```

## 配置参数

| 参数 | 类型 | 默认值 | 描述 |
|-----|------|---------|-------------|
| affinity | object | `{}` | Pod 亲和性调度配置 |
| extraManifests | list | `[]` | 随 Chart 一同创建的额外 Kubernetes 资源清单 |
| fullnameOverride | string | `""` | 完全覆盖 neon-init-job.fullname 模板的字符串 |
| image.pullPolicy | string | `"IfNotPresent"` | 镜像拉取策略 |
| image.repository | string | `"curlimages/curl"` | 初始化镜像仓库 |
| image.tag | string | `"latest"` | 覆盖镜像标签 |
| imagePullSecrets | list | `[]` | 指定 docker-registry 的 Secret 名称数组 |
| nameOverride | string | `""` | 部分覆盖 neon-init-job.fullname 模板的字符串 |
| nodeSelector | object | `{}` | Pod 节点选择器标签 |
| output.configMapName | string | `"neon-compute-config"` | 输出的 ConfigMap 名称 |
| podAnnotations | object | `{}` | neon-init-job Pod 的注解 |
| podLabels | object | `{}` | neon-init-job Pod 的附加标签 |
| resources.limits.cpu | string | `"200m"` | CPU 上限 |
| resources.limits.memory | string | `"256Mi"` | 内存上限 |
| resources.requests.cpu | string | `"100m"` | CPU 请求 |
| resources.requests.memory | string | `"128Mi"` | 内存请求 |
| serviceAccount.annotations | object | `{}` | 添加到 ServiceAccount 的注解 |
| serviceAccount.create | bool | `true` | 指定是否创建 ServiceAccount |
| serviceAccount.name | string | `""` | 要使用的 ServiceAccount 名称 |
| settings.pageserverEndpoint | string | `"http://neon-pageserver:9898"` | Pageserver HTTP API 地址 |
| settings.safekeeper.headlessService | string | `"neon-safekeeper-headless"` | Safekeeper Headless Service 名称 |
| settings.safekeeper.httpPort | int | `7676` | Safekeeper HTTP 端口 |
| settings.safekeeper.pgPort | int | `5454` | Safekeeper PostgreSQL 端口 |
| settings.safekeeper.replicas | int | `3` | Safekeeper 副本数 |
| settings.storageControllerEndpoint | string | `"http://neon-storage-controller:50051"` | Storage Controller HTTP API 地址 |
| tolerations | list | `[]` | Pod 容忍调度配置 |

## 使用示例

### 查看初始化结果

```console
# 查看 Job 执行日志
$ kubectl logs job/neon-init-job

# 日志末尾输出
Done! TENANT_ID=3f8a91b2... TIMELINE_ID=a1c4d7e2...
```

### 查看生成的输出信息

```console
# 查看输出 ConfigMap
$ kubectl get configmap neon-compute-config -o yaml
```

### 重新初始化

```console
# 删除旧 Job 后重新安装
$ kubectl delete job neon-init-job
$ helm upgrade neon-stack ./charts/neon-stack --install
```

## API 调用详情

| 步骤 | 方法 | 端点 | 用途 |
|------|------|------|------|
| 1 | `GET` | `{SC}/health` | Storage Controller 健康检查 |
| 2 | `GET` | `{PS}/status` | Pageserver 状态检查 |
| 3 | `GET` | `{SK}/status` | Safekeeper 状态检查（每个副本） |
| 4 | `POST` | `{SC}/control/v1/safekeeper/{id}` | 注册 Safekeeper |
| 5 | `POST` | `{SC}/v1/tenant` | 创建 Tenant（Attached gen=0） |
| 6 | `POST` | `{SC}/v1/tenant/{id}/timeline` | 创建 Timeline（Bootstrap 模式，pg_version=16） |

----------------------------------------------
由 Chart 元数据通过 [helm-docs](https://github.com/norwoodj/helm-docs) 自动生成
