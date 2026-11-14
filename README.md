# Neondatabase Kubernetes Helm Charts

[![许可证](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)

本项目仅供学习Neon使用。

## 使用方法

限制：

- 暂不考虑密钥轮换，JWT不过期，仅供开发测试用。
- control plane不提供鉴权机制，无需认证即可访问，仅供开发测试用。
- storage controller暂只使用1个主节点。

### 部署前提

部署前需自行准备以下外部依赖

#### PostgreSQL数据库

创建storage_controller数据库和storage_controller用户：

```sql
CREATE USER storage_controller WITH PASSWORD '123456';
CREATE DATABASE storage_controller owner storage_controller;
```

设置连接字符串：**不要写进 values**，请在目标命名空间创建 Secret 后由 chart 引用：

```yaml
# kubectl apply -f neon-db-secret.yaml
apiVersion: v1
kind: Secret
metadata:
  name: storage-controller-pg-cluster
  namespace: neon
type: Opaque
stringData:
  # 键名默认 uri（CloudNativePG 约定），可用 settings.databaseUrlSecretKey 覆盖
  uri: "postgres://storage_controller:123456@192.168.232.128:5432/storage_controller"
```

再在 values 中只填"引用"：

```yaml
global:
  storageController:
    databaseUrl:
      existingSecret: "storage-controller-pg-cluster"
      secretKey: "uri"
```

为什么这样设计：连接串若写在 values 里，会同时落进 Git、Helm release（`helm get values` 可取回）
和渲染后的 manifest（`helm get manifest` 里的 base64 可直接解码），且每次改密码都必须 `helm upgrade`。
改为引用外部 Secret 后，轮换只需更新 Secret + `kubectl rollout restart deploy/<release>-storage-controller`。

storage-controller 启动时会自动建表/迁移。

#### 对象存储

使用minio，并创建桶neondata

```sh
minio server miniodata/ --console-address :9001
```

Console: http://192.168.232.128:9001
S3-API: http://192.168.232.128:9000
RootUser: minioadmin
RootPass: minioadmin

在目标命名空间创建 Secret（**对象存储的唯一事实来源**：凭证与连接坐标都在这里，values 中不再有副本）：

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: bucket-credentials
stringData:
  AWS_ACCESS_KEY_ID: "minioadmin"
  AWS_ENDPOINT_URL: "http://192.168.232.128:9000"
  AWS_REGION: "us-east-1"
  AWS_SECRET_ACCESS_KEY: "minioadmin"
  BUCKET_NAME: "neondata"
```

#### 本地存储（StorageClass）

pageserver / safekeeper 为有状态组件，需要 PVC。集群需具备可用的 StorageClass：

- 若集群已有可用 StorageClass（含 k3s/kind 内置的 `local-path`），在部署 neon 时显式指定即可：

```yaml
neon-pageserver:
  statefulSet:
    storage:
      storageClassName: "local-path"
neon-safekeeper:
  statefulSet:
    storage:
      storageClassName: "local-path"
```

- 若集群没有可用 StorageClass，可通过本仓库的 `local-path-provisioner` chart 提供节点本地盘：

```sh
# 独立部署
helm install local-path-provisioner ./charts/local-path-provisioner -n neon --create-namespace

# 或作为 neon umbrella 的可选子 chart（默认关闭）
helm upgrade --install neon ./charts/neon -n neon \
  --set local-path-provisioner.enabled=true \
  --set neon-pageserver.statefulSet.storage.storageClassName=local-path \
  --set neon-safekeeper.statefulSet.storage.storageClassName=local-path
```

> 默认**不**设为集群默认 StorageClass，避免影响集群内其他组件；详见 [image.md](./image.md#安装部署-local-path-provisioner)。
> 本地盘不具备跨节点高可用，仅用于测试/开发环境。

#### k8s

k8s集群要求与minio时间保持同步。并且节点需要标记可用区（至少3个）和地域信息。

> 重要：pageserver / safekeeper 的可用区**不再由 values 静态指定**，而是在 Pod 启动时动态读取
> 所在节点的 `topology.kubernetes.io/zone` 标签（需要 `nodes: get` 权限，默认由子 chart 自动创建 RBAC）。
> 未打该标签的节点上，对应 Pod 会停在 Init 状态并输出告警日志。

```sh
# 打标签
kubectl label node node1 topology.kubernetes.io/zone=az1 topology.kubernetes.io/region=region1
# ...
# 查看可用区和region
kubectl get nodes -L topology.kubernetes.io/zone -L topology.kubernetes.io/region
```

#### 镜像

除了开源镜像，本项目开发了control-plane服务，在开发测试阶段可使用本地仓库。

### 部署neon

#### 1. 生成并应用共享 JWT Secret 与 PostgreSQL 连接串 Secret

chart 只**引用**外部预建的 Secret，不接收任何密钥/连接串明文。

```sh
kubectl create namespace neon

# 生成 Ed25519 密钥对 + 各 scope JWT，并输出 Secret 清单（jwt-out/neon-jwt.secret.yaml）
python3 jwt.py --namespace neon --secret-name neon-jwt

# 应用（如需入库，请先交给 SOPS / Sealed Secrets / External Secrets 加密）
kubectl apply -f jwt-out/neon-jwt.secret.yaml

# 应用 PostgreSQL 连接串 Secret（docs/neon.yaml 中已给出，键名 uri）
kubectl apply -f docs/neon.yaml
```

Secret 的键契约（键名与 chart 模板一一对应，勿改）：

| 键                         | 说明                                                              |
| -------------------------- | ----------------------------------------------------------------- |
| `publicKey.pem`          | Ed25519 公钥 PEM（pageserver / safekeeper / SC / 控制面校验签名） |
| `privateKey.pem`         | Ed25519 私钥 PEM（**仅** control-plane 持有并用于签发）     |
| `pageserverJwtToken`     | scope`pageserverapi`                                            |
| `safekeeperJwtToken`     | scope`safekeeperdata`                                           |
| `controlPlaneJwtToken`   | scope`controlplane`                                             |
| `peerJwtToken`           | scope`admin`                                                    |
| `generationsApiJwtToken` | scope`generations_api`（pageserver → SC 的 upcall）            |
| `computeJwtToken`        | scope`tenant`（预留，当前无消费方）                             |

#### 2. 安装

```sh
helm dependency build charts/neon
helm install neon charts/neon -n neon \
  --set global.storageController.databaseUrl.existingSecret=storage-controller-pg-cluster \
  --set global.jwt.existingSecret=neon-jwt
```

`global.jwt.existingSecret` 为空时渲染阶段会直接报错并给出处置命令，不会静默起一个无认证/半认证的集群。
`global.storageController.databaseUrl.existingSecret` 同理：缺失直接渲染失败，不会放出一个必然 CrashLoopBackOff 的 storage-controller。

> 破坏性变更：早期版本用 `--set neon-storage-controller.settings.databaseUrl='postgres://...'` 传明文连接串，
> 该入口已移除。Helm 3 对未定义的 values 键只告警不报错，因此旧命令会**静默失效**并被上面的渲染期校验拦下，
> 请改用 Secret 引用方式。

#### 3. 从旧版本升级（重要）

旧版本由 chart 创建 `neon-jwt` Secret；本次起 chart 不再管理它，`helm upgrade` 会将其删除。
**升级前务必先备份并重建该 Secret**：

```sh
kubectl get secret neon-jwt -n neon -o yaml > neon-jwt.backup.yaml   # 备份
python3 jwt.py --namespace neon --secret-name neon-jwt               # 生成新的
kubectl apply -f jwt-out/neon-jwt.secret.yaml
# 若想沿用旧密钥：kubectl apply -f neon-jwt.backup.yaml
```

#### 4. 密钥轮换

token 由 jwt.py 一次性签发且不含 `exp`（上游 neon 的 `JwtAuth` 显式不校验过期），
因此泄漏即长期有效，只能靠"换密钥 + 重签 + 重启"来轮换：

```sh
# 连私钥一起换
python3 jwt.py --namespace neon --secret-name neon-jwt
# 或沿用原私钥只重签 token
python3 jwt.py --key-file jwt-out/privateKey.pem --namespace neon --secret-name neon-jwt

kubectl apply -f jwt-out/neon-jwt.secret.yaml
kubectl rollout restart sts,deploy -n neon
# pageserver 也可调用其 reload_auth_validation_keys 接口热加载公钥，无需重启
```

端口转发：

```sh
kubectl port-forward -n neon svc/neon-control-plane-svc 8080:8080
```

#### upgrade

使用 `--reuse-values` 可在不提供完整 values 文件时，仅覆盖个别字段：

```sh
helm upgrade neon . -n neon --reuse-values --set neon-pageserver.statefulSet.replicas=3

helm upgrade neon charts/neon -n neon --reuse-values
```

#### 卸载

```sh
helm list -A

helm uninstall neon -n neon
```




## 许可证

<!-- 保留指向仓库文件的完整 URL 链接，因为本 README 会从 main 分支同步到 gh-pages 分支。 -->
[Apache 2.0 许可证](https://github.com/neondatabase/helm-charts/blob/main/LICENSE)。

## Helm 图表构建状态

![发布图表](https://github.com/neondatabase/helm-charts/workflows/Release%20Charts/badge.svg?branch=main) [![检查并测试图表](https://github.com/neondatabase/helm-charts/actions/workflows/lint-test.yaml/badge.svg)](https://github.com/neondatabase/helm-charts/actions/workflows/lint-test.yaml)
