# Neondatabase Kubernetes Helm Charts

[![许可证](<https://img.shields.io/badge/License-Apache%202.0-blue.svg>)](https://opensource.org/licenses/Apache-2.0)

本项目仅供学习Neon使用。

## 使用方法

限制：

- 暂不考虑密钥轮换，JWT不过期，仅供开发测试用。
- control plane不提供鉴权机制，无需认证即可访问，仅供开发测试用。
- storage controller暂只使用1个主节点。

### 部署前提

部署前需自行准备以下外部依赖

#### PostgreSQL数据库

Neon 需要两个**互相独立**的 PostgreSQL 库（分别属于两个组件，不要共用一个库或账号）：

| 数据库 | 用途 | 连接账号 |
| --- | --- | --- |
| `storage_controller` | storage controller 元数据（tenant/shard/node/timeline 注册表） | `storage_controller` |
| `neon_control_plane` | control plane 业务状态（project/branch/endpoint/凭据引用/迁移版本） | `neon_control_plane` |

创建 storage_controller 数据库和用户：

```sql
CREATE USER storage_controller WITH PASSWORD '123456';
CREATE DATABASE storage_controller OWNER storage_controller;
```

创建 control plane 数据库和用户（表结构由服务启动时自动迁移，账号必须是库 owner）：

```sql
CREATE USER neon_control_plane WITH PASSWORD '123456';
CREATE DATABASE neon_control_plane OWNER neon_control_plane;
-- 建议收紧默认权限：只允许该账号连接
REVOKE ALL ON DATABASE neon_control_plane FROM PUBLIC;
GRANT CONNECT, TEMPORARY ON DATABASE neon_control_plane TO neon_control_plane;
```

> 账号权限说明：
>
> - 只需要 `CONNECT` + 在本库 `public` schema 建表/索引（owner 天然满足）；
> - **不要**复用 `storage_controller` 账号或 `postgres` 超级用户，避免两个组件的 DDL 与故障域互相影响；
> - 控制面启动时用 `pg_advisory_xact_lock` 串行执行内嵌迁移（见后文「neon_control_plane 库结构」）。

设置连接字符串：**不要写进 values**，请在目标命名空间创建 Secret 后由 chart 引用。

storage controller 的 Secret：

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

control plane 的 Secret：

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: neon-control-plane-pg
  namespace: neon
type: Opaque
stringData:
  # Deployment 会以环境变量 NEON_CP_DATABASE_URL 注入，不会出现在 ConfigMap / values / Helm release 中
  uri: "postgres://neon_control_plane:123456@192.168.232.128:5432/neon_control_plane"
```

再在 values 中只填“引用”：

```yaml
global:
  storageController:
    databaseUrl:
      existingSecret: "storage-controller-pg-cluster"
      secretKey: "uri"
  controlPlane:
    databaseUrl:
      existingSecret: "neon-control-plane-pg"
      secretKey: "uri"
```

为什么这样设计：连接串若写在 values 里，会同时落进 Git、Helm release（`helm get values` 可取回）
和渲染后的 manifest（`helm get manifest` 里的 base64 可直接解码），且每次改密码都必须 `helm upgrade`。
改为引用外部 Secret 后，轮换只需更新 Secret + `kubectl rollout restart deploy/<release>-storage-controller`
（控制面的环境变量同样不会自动刷新，需要 `kubectl rollout restart deploy/neon-control-plane`）。

两个组件启动时都会自动建表/迁移；`neon-control-plane` 在 `stateStore.backend=postgres` 且未配置
Secret 时由 Helm 渲染直接失败（`fail`），不会带着空连接串进入 CrashLoop。

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

#### 3. 从旧版本升级

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

#### 示例

```sh
# 创建Project
postgres@slpc:~$ curl -X POST http://localhost:8080/projects \
  -H "Content-Type: application/json" \
  -d '{"project":{"name":"myproject","branch":{"name":"main","role_name":"sally","database_name":"mydb"}}}' | jq .
  % Total    % Received % Xferd  Average Speed   Time    Time     Time  Current
                                 Dload  Upload   Total   Spent    Left  Speed
100   774  100   674  100   100    152     22  0:00:04  0:00:04 --:--:--   159
{
  "connection_uris": [
    {
      "connection_parameters": "{\"database\":\"mydb\",\"role\":\"sally\"}",
      "connection_uri": "postgresql://sally@192.168.232.128:30551/mydb"    # 连接字符串，拼接密码后可直连
    }
  ],
  "databases": [
    {
      "database_name": "mydb",   # 该分支上创建的数据库及属主
      "owner_name": "sally"
    }
  ],
  "default_branch": {
    "branch_id": "br-9dded7d644b4b07d",
    "name": "main",    # 自动创建的默认分支
    "timeline_id": "c9861e81828e9fe4f477b1d35e7c14b0"
  },
  "operations": [   # 创建过程
    {
      "action": "create_tenant",
      "status": "finished"
    },
    {
      "action": "create_timeline",
      "status": "finished"
    }
  ],
  "project": {
    "project_id": "proj-6bfbbd0c5edcd85d",
    "tenant_id": "03c7583d70ca80a5c25b354f0bb92d11",
    "name": "myproject",    # 创建的项目名
    "created_at": "2026-11-16T03:02:34Z"
  },
  "roles": [
    {
      "password": "a909e2a33b7e706b7bde6f0a94ed44a2",
      "role_name": "sally"   # 该分支上创建的用户及密码
    }
  ]
}
```

> `branch.role_name` 未指定时按官方语义默认 `<database_name>_owner`（如 `neondb_owner`），
> `branch.database_name` 默认 `neondb`；密码只在创建/重置时返回一次。

查看neon_control_plane库：

```sql
neon_control_plane=# select * from projects;
      project_id       |            tenant_id             |   name    |       created_at       |          updated_at           | deleted_at 
-----------------------+----------------------------------+-----------+------------------------+-------------------------------+------------
 proj-6bfbbd0c5edcd85d | 03c7583d70ca80a5c25b354f0bb92d11 | myproject | 2026-11-16 11:02:34+08 | 2026-11-16 11:02:34.222396+08 | 
(1 row)
neon_control_plane=# select * from branches ;
      branch_id      |      project_id       |            tenant_id             |           timeline_id            | name | parent_id | is_default |          created_at   
        |          updated_at           | deleted_at 
---------------------+-----------------------+----------------------------------+----------------------------------+------+-----------+------------+-----------------------
--------+-------------------------------+------------
 br-9dded7d644b4b07d | proj-6bfbbd0c5edcd85d | 03c7583d70ca80a5c25b354f0bb92d11 | c9861e81828e9fe4f477b1d35e7c14b0 | main |           | t          | 2026-11-16 11:02:34.22
2396+08 | 2026-11-16 11:02:34.222396+08 | 
(1 row)
neon_control_plane=# select * from endpoints ;
     endpoint_id     |      project_id       |      branch_id      |            tenant_id             |           timeline_id            |                       compute_im
age                       | status  |                        route_fingerprint                         | safekeeper_generation | pending_reconfigure |          created_at 
          |          updated_at           | deleted_at 
---------------------+-----------------------+---------------------+----------------------------------+----------------------------------+---------------------------------
--------------------------+---------+------------------------------------------------------------------+-----------------------+---------------------+---------------------
----------+-------------------------------+------------
 ep-494cbecf0886e027 | proj-6bfbbd0c5edcd85d | br-9dded7d644b4b07d | 03c7583d70ca80a5c25b354f0bb92d11 | c9861e81828e9fe4f477b1d35e7c14b0 | 192.168.232.128:5000/neondatabas
e/compute-node-v17:latest | running | 7345f70d35db321dc829fcbecb94367104a0ba6e416a0181d90d8f542f962917 |                     0 | f                   | 2026-11-16 11:02:34.
222396+08 | 2026-11-16 11:10:45.330713+08 | 
(1 row)
```

> 上例是 0002 迁移之前的输出；现在 `endpoints` 多了 `type` 列（`read_write` / `read_only`），
> 查端点状态建议用：`select endpoint_id, type, status from endpoints where deleted_at is null;`

查看minio对象存储：

```sh
postgres@slpc:~/miniodata/neondata/pageserver/tenants$ ls
03c7583d70ca80a5c25b354f0bb92d11  03c7583d70ca80a5c25b354f0bb92d11-0001
postgres@slpc:~/miniodata/neondata/pageserver/tenants$ tree .
.
├── 03c7583d70ca80a5c25b354f0bb92d11
│   └── timelines
│       └── c9861e81828e9fe4f477b1d35e7c14b0
│           └── initdb.tar.zst
│               ├── 39dcf0a4-5309-4a90-bbc6-bf34c90f62fb
│               │   └── part.1
│               └── xl.meta
└── 03c7583d70ca80a5c25b354f0bb92d11-0001
    ├── tenant-manifest-00000001.json
    │   └── xl.meta
    └── timelines
        └── c9861e81828e9fe4f477b1d35e7c14b0
            ├── 000000000000000000000000000000000000-FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF__00000000014E8F20-00000000014E8F99-00000001
            │   ├── 72d438e7-0ab2-4b5d-ab07-fb7c2bde1e9c
            │   │   └── part.1
            │   └── xl.meta
            └── index_part.json-00000001
                └── xl.meta

13 directories, 6 files
```

创建分支：

```sh
# 查看分支
postgres@slpc:~$ curl http://localhost:8080/projects/proj-6bfbbd0c5edcd85d/branches | jq .
  % Total    % Received % Xferd  Average Speed   Time    Time     Time  Current
                                 Dload  Upload   Total   Spent    Left  Speed
100   200  100   200    0     0  18876      0 --:--:-- --:--:-- --:--:-- 20000
[
  {
    "branch_id": "br-9dded7d644b4b07d",
    "project_id": "proj-6bfbbd0c5edcd85d",
    "tenant_id": "03c7583d70ca80a5c25b354f0bb92d11",
    "timeline_id": "c9861e81828e9fe4f477b1d35e7c14b0",
    "name": "main",
    "default": true
  }
]
# 创建子分支
postgres@slpc:~$ curl -X POST http://localhost:8080/projects/proj-6bfbbd0c5edcd85d/branches \
  -d '{"name":"dev", "parent_branch_id":"br-9dded7d644b4b07d"}' | jq .
  % Total    % Received % Xferd  Average Speed   Time    Time     Time  Current
                                 Dload  Upload   Total   Spent    Left  Speed
100   288  100   232  100    56   4652   1122 --:--:-- --:--:-- --:--:--  5877
{
  "branch_id": "br-d4dbc56b276d495a",
  "project_id": "proj-6bfbbd0c5edcd85d",
  "tenant_id": "03c7583d70ca80a5c25b354f0bb92d11",
  "timeline_id": "eb472eec69dc52906b6b762952f2cd88",
  "name": "dev",
  "parent_id": "br-9dded7d644b4b07d",
  "default": false
}
```

查看neon_control_plane库：

```sql
neon_control_plane=# select * from branches ;
      branch_id      |      project_id       |            tenant_id             |           timeline_id            | name |      parent_id      | is_default |          cre
ated_at           |          updated_at           | deleted_at 
---------------------+-----------------------+----------------------------------+----------------------------------+------+---------------------+------------+-------------
------------------+-------------------------------+------------
 br-9dded7d644b4b07d | proj-6bfbbd0c5edcd85d | 03c7583d70ca80a5c25b354f0bb92d11 | c9861e81828e9fe4f477b1d35e7c14b0 | main |                     | t          | 2026-11-16 1
1:02:34.222396+08 | 2026-11-16 11:02:34.222396+08 | 
 br-d4dbc56b276d495a | proj-6bfbbd0c5edcd85d | 03c7583d70ca80a5c25b354f0bb92d11 | eb472eec69dc52906b6b762952f2cd88 | dev  | br-9dded7d644b4b07d | f          | 2026-11-16 1
1:34:46.244089+08 | 2026-11-16 11:34:46.244089+08 | 
(2 rows)
```

查看minio对象存储：

```sh
postgres@slpc:~/miniodata/neondata/pageserver/tenants$ ls
03c7583d70ca80a5c25b354f0bb92d11  03c7583d70ca80a5c25b354f0bb92d11-0001
postgres@slpc:~/miniodata/neondata/pageserver/tenants$ cd 03c7583d70ca80a5c25b354f0bb92d11-0001/
postgres@slpc:~/miniodata/neondata/pageserver/tenants/03c7583d70ca80a5c25b354f0bb92d11-0001$ ls
tenant-manifest-00000001.json  timelines
postgres@slpc:~/miniodata/neondata/pageserver/tenants/03c7583d70ca80a5c25b354f0bb92d11-0001$ cd timelines/
postgres@slpc:~/miniodata/neondata/pageserver/tenants/03c7583d70ca80a5c25b354f0bb92d11-0001/timelines$ ls
c9861e81828e9fe4f477b1d35e7c14b0  eb472eec69dc52906b6b762952f2cd88
postgres@slpc:~/miniodata/neondata/pageserver/tenants/03c7583d70ca80a5c25b354f0bb92d11-0001/timelines$ tree .
.
├── c9861e81828e9fe4f477b1d35e7c14b0
│   ├── 000000000000000000000000000000000000-FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF__00000000014E8F20-00000000014E8F99-00000001
│   │   ├── 72d438e7-0ab2-4b5d-ab07-fb7c2bde1e9c
│   │   │   └── part.1
│   │   └── xl.meta
│   ├── 000000000000000000000000000000000000-FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF__00000000014E8F99-00000000020A81E9-00000001
│   │   ├── c4eea5d9-9a4e-42db-b375-e346851185c4
│   │   │   └── part.1
│   │   └── xl.meta
│   └── index_part.json-00000001
│       └── xl.meta
└── eb472eec69dc52906b6b762952f2cd88
    └── index_part.json-00000001
        └── xl.meta

9 directories, 6 files
```

创建只读节点（请求体对齐 Neon API：嵌套在 `endpoint` 下，`type` 必填）：
```sh
postgres@slpc:~$ curl -X POST http://localhost:8080/projects/proj-6bfbbd0c5edcd85d/endpoints \
  -H "Content-Type: application/json" \
  -d '{"endpoint":{"branch_id":"br-9dded7d644b4b07d","type":"read_only"}}' | jq .
  % Total    % Received % Xferd  Average Speed   Time    Time     Time  Current
                                 Dload  Upload   Total   Spent    Left  Speed
100   578  100   524  100    54   5068    522 --:--:-- --:--:-- --:--:--  5611
{
  "endpoint": {
    "endpoint_id": "ep-344d132557631bc7",
    "project_id": "proj-6bfbbd0c5edcd85d",
    "tenant_id": "03c7583d70ca80a5c25b354f0bb92d11",
    "timeline_id": "c9861e81828e9fe4f477b1d35e7c14b0",
    "branch_id": "br-9dded7d644b4b07d",
    "compute_image": "192.168.232.128:5000/neondatabase/compute-node-v17:latest",
    "type": "read_only",          # read_write（每分支最多 1 个）| read_only（可多个）
    "status": "provisioning",
    "current_state": "init",      # Neon EndpointState：init / active / idle
    "route_fingerprint": "7345f70d35db321dc829fcbecb94367104a0ba6e416a0181d90d8f542f962917"
  },
  "operations": [
    { "action": "start_compute", "status": "finished" }
  ]
}

# 创建 endpoint 不返回 connection_uri / password（不会创建角色，也不会产生新密码）。
# 连接串通过独立接口获取，database_name / role_name 必填（pooled=true 暂不支持，返回 400）：
postgres@slpc:~$ curl "http://localhost:8080/projects/proj-6bfbbd0c5edcd85d/connection_uri?branch_id=br-9dded7d644b4b07d&database_name=mydb&role_name=sally" | jq .
{
  "uri": "postgresql://sally@192.168.232.128:31266/mydb"
}

# 密码使用项目创建时返回的 sally 密码（只读副本复用同一角色的 SCRAM 凭据）
psql "postgresql://sally:<password>@192.168.232.128:31266/mydb"
# PGPASSWORD=a909e2a33b7e706b7bde6f0a94ed44a2 psql "postgresql://sally@192.168.232.128:30551/mydb"
```

> 同一分支再创建 `read_write` 会返回 `409 Conflict`（一个分支最多一个主节点）；
> `read_only` 可以创建多个。`type` 缺失或非法值返回 `400`。

查看只读节点配置：
```sh
postgres@neon-compute-ep-344d132557631bc7-5968999664-x82zz:~/data$ cat postgresql.conf 
# Neon storage settings

# from compute spec's pageserver_connection_info.stripe_size field
neon.stripe_size=1024
# derived from compute spec's pageserver_connection_info field
neon.pageserver_connstring='postgresql://neon-pageserver-1.neon-pageserver-hl-svc.neon.svc.cluster.local:64000'
neon.safekeepers='neon-safekeeper-0.neon-safekeeper-hl-svc.neon.svc.cluster.local:5454,neon-safekeeper-1.neon-safekeeper-hl-svc.neon.svc.cluster.local:5454,neon-safekeeper-2.neon-safekeeper-hl-svc.neon.svc.cluster.local:5454'
neon.tenant_id='03c7583d70ca80a5c25b354f0bb92d11'
neon.timeline_id='c9861e81828e9fe4f477b1d35e7c14b0'
neon.project_id='proj-6bfbbd0c5edcd85d'
neon.branch_id='br-9dded7d644b4b07d'
neon.endpoint_id='ep-344d132557631bc7'
lc_messages='C.UTF-8'
lc_monetary='C.UTF-8'
lc_time='C.UTF-8'
lc_numeric='C.UTF-8'
neon.compute_mode=replica
hot_standby=on
neon.privileged_role_name='neon_superuser'
# Managed by compute_ctl: begin
shared_preload_libraries = 'neon'
listen_addresses = '*'
# Managed by compute_ctl: end
neon.extension_server_port=3081
neon.disable_logical_replication_subscribers=false
include_if_exists = 'compute_ctl_temp_override.conf'
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

[Apache 2.0 许可证](https://github.com/neondatabase/helm-charts/blob/main/LICENSE)。

## Helm 图表检查状态

[![检查并测试图表](https://github.com/neondatabase/helm-charts/actions/workflows/lint-test.yaml/badge.svg)](https://github.com/neondatabase/helm-charts/actions/workflows/lint-test.yaml)
