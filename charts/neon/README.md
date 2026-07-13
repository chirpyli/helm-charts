# neon

![Version: 0.1.0](https://img.shields.io/badge/Version-0.1.0-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) [![Lint and Test Charts](https://github.com/neondatabase/helm-charts/actions/workflows/lint-test.yaml/badge.svg)](https://github.com/neondatabase/helm-charts/actions/workflows/lint-test.yaml)

Neon 生产级完整部署 umbrella chart — 一键部署 Neon 全栈（broker / storage-controller / pageserver / safekeeper / control-plane / compute）。

**Homepage:** https://neon.tech

## Source Code

* [https://github.com/neondatabase/neon](https://github.com/neondatabase/neon)
* [https://github.com/neondatabase/helm-charts](https://github.com/neondatabase/helm-charts)

## 简介

`neon` 是 Neon 平台的 **umbrella（总）chart**，通过 Helm dependencies 统一编排以下子 chart：

| 子 Chart                    | 类型        | 说明                                         | 默认        |
| --------------------------- | ----------- | -------------------------------------------- | ----------- |
| `neon-storage-broker`     | Deployment  | WAL 流节点发现 pub-sub                       | ✅ 启用     |
| `neon-storage-controller` | Deployment  | 存储调度大脑（租户/节点/分片管理）           | ✅ 启用     |
| `neon-pageserver`         | StatefulSet | 有状态存储节点（物化 tenant 层数据）         | ✅ 启用     |
| `neon-safekeeper`         | StatefulSet | 有状态 WAL 多副本存储                        | ✅ 启用     |
| `neon-control-plane`      | Deployment  | 最小控制面（project/endpoint API）           | ✅ 启用     |
| `neon-compute`            | Deployment  | 静态示例 compute 节点（回退/演示，默认关闭） | ❌ 默认关闭 |
| `neon-proxy`              | —          | phase-2 可选，默认不启用                     | ❌ 禁用     |

## 部署前提（必读）

部署前需自行准备以下外部依赖（**chart 不提供，必须自备**）：

### 1. 外部 PostgreSQL（storage-controller 元数据）

创建数据库和用户，填写连接串到 values：

```yaml
neon-storage-controller:
  settings:
    databaseUrl: "postgres://storage_controller:password@pg-host:5432/storage_controller"
```

storage-controller 启动时会自动建表/迁移。

### 2. 外部 MinIO / S3（对象存储）

在目标命名空间创建 Secret：

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: bucket-credentials
stringData:
  AWS_ACCESS_KEY_ID: "minioadmin"
  AWS_ENDPOINT_URL: "http://minio-host:9000"
  AWS_REGION: "us-east-1"
  AWS_SECRET_ACCESS_KEY: "minioadmin"
  BUCKET_NAME: "neondata"
```

### 3. Ed25519 密钥对（JWT）

自签密钥对并填充到 values：

```bash
# 生成密钥对
openssl genpkey -algorithm ed25519 -out privateKey.pem
openssl pkey -in privateKey.pem -pubout -out publicKey.pem

# 读取内容（用于 values 注入）
cat privateKey.pem   # → global.jwt.privateKey
cat publicKey.pem    # → global.jwt.publicKey
```

JWT tokens 各 scope 由 control plane 签发，values 中以任意非空字符串占位即可（SC 仅校验签名，不校验签发者）。

## 安装

```console
# 1. 创建命名空间
kubectl create namespace neon

# 2. 创建 bucket-credentials Secret（见上）

# 3. 准备 values 文件（填入 databaseUrl / MinIO endpoint / JWT 密钥等）

# 4. 下载依赖并安装
$ cd charts/neon
$ helm dependency build .
$ helm install neon . -n neon -f my-values.yaml
```

## 更新

当 chart 或 values 配置发生变更时，使用 `helm upgrade` 进行更新：

```console
# 1. 重新构建依赖（若 Chart.yaml / 子 chart 有改动）
$ cd charts/neon
$ helm dependency build .

# 2. 应用更新（values 改动或 chart 改动均走此命令）
$ helm upgrade neon . -n neon -f my-values.yaml

# 也可直接指定新版本号的打包 chart：
$ helm upgrade neon neon-0.1.0.tgz -n neon -f my-values.yaml
```

> 💡 提示：
>
> - 修改 `my-values.yaml` 后重新 `helm upgrade` 即可让配置生效（部分有状态组件如 pageserver / safekeeper 的资源变更可能需要手动滚动重启）。
> - 涉及子 chart 镜像、模板（templates）改动时，务必先 `helm dependency build .` 重新打包，否则 `helm upgrade` 仍会使用已缓存的旧依赖。
> - 使用 `--reuse-values` 可在不提供完整 values 文件时，仅覆盖个别字段：
>   ```console
>   helm upgrade neon . -n neon --reuse-values --set neon-pageserver.settings.availabilityZone=az2
>   ```

## 卸载

使用 `helm uninstall` 移除 release。**注意：Helm 不会自动删除 PVC（持久卷）**，需手动清理，否则重新安装可能复用残留数据：

```console
# 1. 卸载 release
$ helm uninstall neon -n neon

# 2. （可选）清理残留 PVC
$ kubectl delete pvc -n neon -l app.kubernetes.io/instance=neon

# 3. （可选）删除命名空间
$ kubectl delete namespace neon
```

> ⚠️ 警告：
>
> - 卸载后 **PVC 及其中的 pageserver / safekeeper 数据默认保留**。若需彻底清除，务必执行上面的 PVC 删除步骤。
> - 外部依赖（PostgreSQL、MinIO/S3、JWT 密钥 Secret）均不随 release 删除，需另行清理。

## 数据流（phase-1，无 proxy）

```
client → compute(postgres)  # 直连 compute Service
  → safekeeper(WAL 多数派)
  → pageserver(物化层)
  → 外部 MinIO(对象存储)
```

- client 使用 control plane 生成的 SCRAM 密码直连 compute
- neon-control-plane 在启动时完成 bootstrap 与节点注册
- 创建 endpoint 时 control plane 生成 ComputeSpec 并动态拉起 compute Pod

## 部署后验证

```console
# 检查所有 Pod 状态
kubectl get pods -n neon

# 检查 control plane 服务
kubectl port-forward -n neon svc/neon-control-plane-svc 8080:8080

# 创建 project
curl -X POST http://localhost:8080/projects -d '{"name":"my-project"}'

# 创建 branch
curl -X POST http://localhost:8080/projects/<project_id>/branches \
  -d '{"name":"my-branch"}'

# 创建 endpoint
curl -X POST http://localhost:8080/projects/<project_id>/endpoints \
  -d '{"branch_id":"<branch_id>","type":"read_write"}'
```

## 测试环境资源总览（umbrella 默认值）

| 组件               | 副本        | CPU req       | Memory req       | PVC            |
| ------------------ | ----------- | ------------- | ---------------- | -------------- |
| storage-broker     | 1           | 100m          | 128Mi            | —             |
| storage-controller | 1           | 1             | 512Mi            | —             |
| pageserver         | 1           | 500m          | 512Mi            | 5Gi            |
| safekeeper         | 3           | 200m ×3      | 256Mi ×3        | 2Gi ×3        |
| control-plane      | 1           | 100m          | 128Mi            | —             |
| compute            | 1           | 500m          | 512Mi            | —             |
| **合计**     | **8** | **2.8** | **~2.5Gi** | **11Gi** |

> ⚠️ 以上为测试/开发环境默认值。**生产部署请参考各子 chart values 注释中的生产建议值**调整（CPU/内存/PVC 均需显著增大）。

## 分阶段规划

- **phase-1（当前）**：broker + storage-controller + pageserver + safekeeper + control-plane + compute。client 直连 compute 验证端到端读写。
- **phase-2（后续）**：启用 neon-proxy，client 经由 proxy 路由到 compute。需补齐 TLS/SNI/内部 CA 等前置条件。

## Requirements

Kubernetes: `^1.18.x-x`

## Values

### 全局配置

| Key                             | Type   | Default        | Description                                                 |
| ------------------------------- | ------ | -------------- | ----------------------------------------------------------- |
| jwtSecretName                   | string | `"neon-jwt"` | 共享 JWT 密钥 Secret 名称（umbrella 创建，供子 chart 挂载） |
| global.region                   | string | `"local"`    | 区域标识                                                    |
| global.jwt.existingSecret       | string | `""`         | 已有 Secret 名称（若设置则不新建）                          |
| global.jwt.secretName           | string | `"neon-jwt"` | 共享 Secret 名称                                            |
| global.jwt.publicKey            | string | `""`         | Ed25519 公钥 PEM（含 -----BEGIN PUBLIC KEY-----）           |
| global.jwt.privateKey           | string | `""`         | Ed25519 私钥 PEM（含 -----BEGIN PRIVATE KEY-----）          |
| global.jwt.pageserverJwtToken   | string | `""`         | PageServerApi scope JWT（SC→pageserver）                   |
| global.jwt.safekeeperJwtToken   | string | `""`         | SafekeeperData scope JWT（SC→safekeeper）                  |
| global.jwt.controlPlaneJwtToken | string | `""`         | ControlPlane scope JWT（SC→control plane upcall）          |
| global.jwt.peerJwtToken         | string | `""`         | Admin scope JWT（SC→peer SC）                              |
| global.jwt.computeJwtToken      | string | `""`         | Tenant scope JWT（compute→pageserver/safekeeper）          |

### neon-storage-broker

| Key                                           | Type   | Default      | Description                                 |
| --------------------------------------------- | ------ | ------------ | ------------------------------------------- |
| neon-storage-broker.enabled                   | bool   | `true`     | 是否启用                                    |
| neon-storage-broker.nameOverride              | string | `"broker"` | 覆盖 name（使服务名简化为 neon-broker-svc） |
| neon-storage-broker.resources.limits.memory   | string | `"256Mi"`  | 内存上限（测试环境；生产 >= 8Gi）           |
| neon-storage-broker.resources.requests.cpu    | string | `"100m"`   | CPU 请求（测试环境；生产 >= 1）             |
| neon-storage-broker.resources.requests.memory | string | `"128Mi"`  | 内存请求（测试环境；生产 >= 2Gi）           |

### neon-storage-controller

| Key                                                   | Type   | Default                                  | Description                               |
| ----------------------------------------------------- | ------ | ---------------------------------------- | ----------------------------------------- |
| neon-storage-controller.enabled                       | bool   | `true`                                 | 是否启用                                  |
| neon-storage-controller.nameOverride                  | string | `"storage-controller"`                 | 覆盖 name                                 |
| neon-storage-controller.resources.limits.cpu          | string | `"1"`                                  | CPU 上限（测试环境；生产 >= 2）           |
| neon-storage-controller.resources.limits.memory       | string | `"512Mi"`                              | 内存上限（测试环境；生产 >= 4Gi）         |
| neon-storage-controller.resources.requests.cpu        | string | `"1"`                                  | CPU 请求（测试环境；生产 >= 2）           |
| neon-storage-controller.resources.requests.memory     | string | `"512Mi"`                              | 内存请求（测试环境；生产 >= 4Gi）         |
| neon-storage-controller.settings.databaseUrl          | string | `""`                                   | 外部 PostgreSQL 连接串（**必填**）  |
| neon-storage-controller.settings.publicKey            | string | `""`                                   | Ed25519 公钥 PEM                          |
| neon-storage-controller.settings.jwtToken             | string | `""`                                   | PageServerApi scope JWT                   |
| neon-storage-controller.settings.safekeeperJwtToken   | string | `""`                                   | SafekeeperData scope JWT                  |
| neon-storage-controller.settings.controlPlaneJwtToken | string | `""`                                   | ControlPlane scope JWT                    |
| neon-storage-controller.settings.peerJwtToken         | string | `""`                                   | Admin scope JWT                           |
| neon-storage-controller.settings.controlPlaneUrl      | string | `"http://neon-control-plane-svc:8080"` | control plane upcall 地址（严格模式必填） |

### neon-pageserver

| Key                                                   | Type   | Default                                        | Description          |
| ----------------------------------------------------- | ------ | ---------------------------------------------- | -------------------- |
| neon-pageserver.enabled                               | bool   | `true`                                       | 是否启用             |
| neon-pageserver.nameOverride                          | string | `"pageserver"`                               | 覆盖 name            |
| neon-pageserver.statefulSet.resources.limits.cpu      | string | `"500m"`                                     | CPU 上限（测试环境） |
| neon-pageserver.statefulSet.resources.limits.memory   | string | `"512Mi"`                                    | 内存上限（测试环境） |
| neon-pageserver.statefulSet.resources.requests.cpu    | string | `"500m"`                                     | CPU 请求             |
| neon-pageserver.statefulSet.resources.requests.memory | string | `"512Mi"`                                    | 内存请求             |
| neon-pageserver.statefulSet.storage.size              | string | `"5Gi"`                                      | PVC 大小（测试环境） |
| neon-pageserver.settings.brokerEndpoint               | string | `"http://neon-broker-svc:50051"`             | broker 地址          |
| neon-pageserver.settings.storageControllerUrl         | string | `"http://neon-storage-controller-svc:50051"` | SC 地址              |
| neon-pageserver.settings.availabilityZone             | string | `"az1"`                                      | 可用区               |
| neon-pageserver.settings.remoteStorage.bucketName     | string | `"neondata"`                                 | bucket 名            |
| neon-pageserver.settings.remoteStorage.bucketRegion   | string | `"us-east-1"`                                | region               |
| neon-pageserver.settings.remoteStorage.endpoint       | string | `"http://192.168.232.128:9000"`              | MinIO/S3 endpoint    |
| neon-pageserver.settings.remoteStorage.prefixInBucket | string | `"pageserver"`                               | 路径前缀             |

### neon-safekeeper

| Key                                                   | Type   | Default                            | Description                  |
| ----------------------------------------------------- | ------ | ---------------------------------- | ---------------------------- |
| neon-safekeeper.enabled                               | bool   | `true`                           | 是否启用                     |
| neon-safekeeper.nameOverride                          | string | `"safekeeper"`                   | 覆盖 name                    |
| neon-safekeeper.statefulSet.resources.limits.cpu      | string | `"200m"`                         | CPU 上限（测试环境）         |
| neon-safekeeper.statefulSet.resources.limits.memory   | string | `"256Mi"`                        | 内存上限（测试环境）         |
| neon-safekeeper.statefulSet.resources.requests.cpu    | string | `"200m"`                         | CPU 请求                     |
| neon-safekeeper.statefulSet.resources.requests.memory | string | `"256Mi"`                        | 内存请求                     |
| neon-safekeeper.statefulSet.storage.size              | string | `"2Gi"`                          | PVC 大小（测试环境）         |
| neon-safekeeper.settings.brokerEndpoint               | string | `"http://neon-broker-svc:50051"` | broker 地址                  |
| neon-safekeeper.settings.availabilityZone             | string | `"az1"`                          | 可用区                       |
| neon-safekeeper.settings.statefulSet.replicas         | int    | `3`                              | 副本数（奇数，SC 要求 >= 3） |

### neon-control-plane

| Key                                              | Type   | Default                                        | Description                   |
| ------------------------------------------------ | ------ | ---------------------------------------------- | ----------------------------- |
| neon-control-plane.enabled                       | bool   | `true`                                       | 是否启用                      |
| neon-control-plane.nameOverride                  | string | `"control-plane"`                            | 覆盖 name                     |
| neon-control-plane.resources.limits.cpu          | string | `"100m"`                                     | CPU 上限（测试环境）          |
| neon-control-plane.resources.limits.memory       | string | `"128Mi"`                                    | 内存上限（测试环境）          |
| neon-control-plane.resources.requests.cpu        | string | `"100m"`                                     | CPU 请求                      |
| neon-control-plane.resources.requests.memory     | string | `"128Mi"`                                    | 内存请求                      |
| neon-control-plane.settings.storageControllerUrl | string | `"http://neon-storage-controller-svc:50051"` | SC 地址                       |
| neon-control-plane.settings.listenPort           | int    | `8080`                                       | 监听端口                      |
| neon-control-plane.settings.defaultTenantId      | string | `"3d1f7595b468230304e0b73cecbcb081"`         | 默认租户 id                   |
| neon-control-plane.settings.computeImage         | string | `"neondatabase/neon:latest"`                 | compute 镜像                  |
| neon-control-plane.settings.enableK8sCompute     | bool   | `false`                                      | 是否启用 K8s 动态拉起 compute |
| neon-control-plane.settings.domain               | string | `"neon.local"`                               | proxy 兼容接口域名（phase-2） |

### neon-compute

| Key                                    | Type   | Default                                           | Description                                             |
| -------------------------------------- | ------ | ------------------------------------------------- | ------------------------------------------------------- |
| neon-compute.enabled                   | bool   | `false`                                         | 是否启用（生产由 control plane 动态拉起，此处默认关闭） |
| neon-compute.nameOverride              | string | `"compute"`                                     | 覆盖 name                                               |
| neon-compute.resources.limits.cpu      | string | `"500m"`                                        | CPU 上限（测试环境）                                    |
| neon-compute.resources.limits.memory   | string | `"512Mi"`                                       | 内存上限（测试环境）                                    |
| neon-compute.resources.requests.cpu    | string | `"500m"`                                        | CPU 请求                                                |
| neon-compute.resources.requests.memory | string | `"512Mi"`                                       | 内存请求                                                |
| neon-compute.settings.connstr          | string | `"postgresql://cloud_admin@localhost/postgres"` | compute 连接串                                          |

---

Autogenerated from chart metadata using [helm-docs v1.9.1](https://github.com/norwoodj/helm-docs/releases/v1.9.1)
