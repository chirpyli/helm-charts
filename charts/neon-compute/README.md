# neon-compute

![Version: 0.1.0](https://img.shields.io/badge/Version-0.1.0-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square)

Neon Compute Node — 运行 PostgreSQL + neon 扩展的无状态计算节点。

## 简介

`neon-compute` 是 Neon 存储计算分离架构中的计算层组件。它通过 `compute_ctl` 启动一个带有
[neon 扩展](https://github.com/neondatabase/neon) 的 PostgreSQL 实例，将 WAL 写入
Safekeeper，将页面请求发送到 Pageserver。

**核心特性**

- 基于 `ghcr.io/neondatabase/compute-node-v16` 镜像，内建 neon 扩展
- 通过 ConfigMap 下发 ComputeSpec 配置（tenant_id、timeline_id、pageserver/safekeeper 连接信息）
- initContainer 等待 pageserver 和所有 safekeeper 就绪后才启动
- LFC（Local File Cache）基于 `emptyDir` 加速页面读取
- `pg_isready` 探针确保 Pod 健康状态准确
- 支持 dev 模式（`--dev`），便于调试

**前置依赖**

| 组件 | 用途 |
|------|------|
| Pageserver | 页面存储层，compute 从 pageserver 读取数据页 |
| Safekeeper | WAL 持久化层，compute 将 WAL 写入 safekeeper |
| neon-init-job（推荐） | 创建 tenant/timeline 并在 safekeeper 上注册 |
| neon-dummy-cp（或真实 CP） | 提供 compute hook 通知端点 |

## 架构

```
┌──────────────────────────────────────────────────┐
│  neon-compute Pod                                │
│                                                  │
│  ┌────────────┐    ┌──────────────────────────┐  │
│  │ initContainer│   │  compute_ctl             │  │
│  │ (wait-deps) │   │  ├─ postgres + neon ext   │  │
│  │             │   │  ├─ pg_isready probe      │  │
│  │ nc -z       │   │  └─ port 55433 (pgsql)    │  │
│  │ pageserver  │   │       port 3080  (http)    │  │
│  │ safekeepers │   │                           │  │
│  └────────────┘    └──────────────────────────┘  │
│                                                  │
│  Volumes:                                        │
│  ├─ /config  ← ConfigMap (compute spec JSON)     │
│  ├─ /lfc     ← emptyDir (Local File Cache)       │
│  └─ /var/db/postgres/compute ← emptyDir (pgdata) │
└──────────────────────────────────────────────────┘
         │                    │
         ▼                    ▼
   Pageserver            Safekeepers
   (page请求)            (WAL写入)
```

## 源代码

* <https://github.com/chirpyli/helm-charts>

## 环境要求

Kubernetes: `^1.18.x-x`

## 安装 Chart

```console
$ git clone https://github.com/chirpyli/helm-charts.git
$ cd helm-charts/charts/neon-compute
$ helm dependency update
$ helm install neon-compute . -f values.yaml
```

## 配置参数

| 参数 | 类型 | 默认值 | 描述 |
|-----|------|---------|-------------|
| affinity | object | `{}` | Pod 亲和性调度配置 |
| computeId | string | `"compute-1"` | Compute 节点唯一标识 |
| devMode | bool | `true` | 启用开发模式（跳过部分安全检查） |
| extraManifests | list | `[]` | 随 Chart 一同创建的额外 Kubernetes 资源清单 |
| fullnameOverride | string | `""` | 完全覆盖 neon-compute.fullname 模板的字符串 |
| image.pullPolicy | string | `"Always"` | 镜像拉取策略 |
| image.repository | string | `"ghcr.io/neondatabase/compute-node-v16"` | Compute 镜像仓库 |
| image.tag | string | `"latest"` | 覆盖镜像标签，默认为 Chart 的 appVersion |
| imagePullSecrets | list | `[]` | 指定 docker-registry 的 Secret 名称数组 |
| lfc.maxFileCacheSizeMB | int | `4096` | LFC 最大缓存大小（MB） |
| lfc.mountPath | string | `"/lfc"` | LFC 挂载路径 |
| nameOverride | string | `""` | 部分覆盖 neon-compute.fullname 模板的字符串 |
| nodeSelector | object | `{}` | Pod 节点选择器标签 |
| pageserver.host | string | `"neon-pageserver"` | Pageserver 主机名 |
| pageserver.port | int | `6400` | Pageserver 端口 |
| pgVersion | int | `16` | PostgreSQL 大版本号 |
| podAnnotations | object | `{}` | neon-compute Pod 的注解 |
| podLabels | object | `{}` | neon-compute Pod 的附加标签 |
| podSecurityContext | object | `{}` | neon-compute Pod 安全上下文 |
| port | int | `55433` | PostgreSQL 监听端口 |
| priorityClassName | string | `""` | Pod 优先级类 |
| replicas | int | `1` | 副本数 |
| resources.limits.cpu | string | `"2"` | CPU 上限 |
| resources.limits.memory | string | `"4Gi"` | 内存上限 |
| resources.requests.cpu | string | `"500m"` | CPU 请求 |
| resources.requests.memory | string | `"1Gi"` | 内存请求 |
| safekeepers | list | 见 values.yaml | Safekeeper 连接列表（host + port） |
| securityContext | object | `{}` | neon-compute 容器安全上下文 |
| service.httpPort | int | `3080` | HTTP 管理端口 |
| service.port | int | `55433` | PostgreSQL Service 端口 |
| service.type | string | `"ClusterIP"` | Service 类型 |
| serviceAccount.annotations | object | `{}` | 添加到 ServiceAccount 的注解 |
| serviceAccount.create | bool | `true` | 指定是否创建 ServiceAccount |
| serviceAccount.name | string | `""` | 要使用的 ServiceAccount 名称 |
| tenantId | string | `""` | 租户 ID（由 initJob 填充） |
| timelineId | string | `""` | 时间线 ID（由 initJob 填充） |
| tolerations | list | `[]` | Pod 容忍调度配置 |

## ComputeSpec 配置

ConfigMap 生成的 `config.json` 包含以下关键字段：

| 字段 | 描述 |
|------|------|
| `tenant_id` / `timeline_id` | 租户和时间线标识 |
| `pageserver_connstring` | Pageserver 连接字符串 |
| `safekeeper_connstrings` | Safekeeper 连接字符串数组 |
| `cluster.settings` | PostgreSQL 参数（neon 扩展、LFC 路径等） |
| `mode` | 运行模式（固定为 `Primary`） |

## 端口

| 端口 | 协议 | 用途 |
|------|------|------|
| `55433` | TCP | PostgreSQL 客户端连接 |
| `3080` | TCP | HTTP 管理接口 |

## 健康检查

| 探针 | 方式 | 用途 |
|------|------|------|
| startupProbe | `pg_isready -p <port>` | 等待 PostgreSQL 首次启动完成（最长 5 分钟） |
| livenessProbe | `pg_isready -p <port>` | 检查 PostgreSQL 进程是否存活 |
| readinessProbe | `pg_isready -p <port>` | 检查 PostgreSQL 是否可接受连接 |

----------------------------------------------
由 Chart 元数据通过 [helm-docs](https://github.com/norwoodj/helm-docs) 自动生成
