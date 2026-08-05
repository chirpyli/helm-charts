# neon部署方案

## 当前helm-chart缺失的能力

1. 缺失control plane
2. 缺失JWT
3. 缺失核心组件：compute、safekeeper、pageserver

主要工作，补充缺失的能力，新增一个最小control plane，仅实现有限的几个关键API。

代码量：6k
开源代码：2.6k
新增代码：3.4k  （go代码2K）
共计：6k

## 部署方案预期达到的目标

### 部署各组件

部署pageserver,storage controller,storage broker，safekeeper,control plane

### 注册safekeeper

由部署脚本完成safekeeper注册，需要向存储控制器storage controller注册safekeeper：

### 创建租户/timeline

暂不支持运行过程中，动态创建租户/timeline，需要在部署时，提前创建后续要使用的租户/timeline。

### 计算节点

预置默认计算节点，可通过API创建计算节点。

## 准备工作

### 预备S3（minio）

提供S3服务，用于存储neon的数据：

```sh
minio server miniodata/ --console-address :9001
```

Console: http://192.168.232.128:9001
S3-API: http://192.168.232.128:9000
RootUser: minioadmin
RootPass: minioadmin

创建桶：neondata

```yaml
---
# 外部 MinIO S3 凭证（运行在 slpc:9000，非 K8s 内部）
apiVersion: v1
kind: Secret
metadata:
  name: bucket-credentials
  namespace: neon
stringData:
  AWS_ACCESS_KEY_ID: "minioadmin"
  AWS_ENDPOINT_URL: "http://192.168.232.128:9000"
  AWS_REGION: "us-east-1"
  AWS_SECRET_ACCESS_KEY: "minioadmin"
  BUCKET_NAME: "neondata"
```

### 准备PostgreSQL数据库

创建数据库：storage_controller，创建用户密码：storage_controller

```sql
create role storage_controller with password storage_controller;
create database storage_controller;
ALTER DATABASE storage_controller OWNER TO storage_controller;
```

设置连接字符串：

```yaml
---
apiVersion: v1
kind: Secret
metadata:
  name: storage-controller-pg-cluster
  namespace: neon
type: Opaque
stringData:
  uri: "postgres://storage_controller:storage_controller@192.168.232.128:5432/storage_controller"
```

### 准备本地盘

需要先创建本地盘

### 准备JWT密钥

```sh
openssl genpkey -algorithm ed25519 -out privatekey.pem
openssl pkey -in privatekey.pem -pubout -out publickey.pem
# 一键生成所有 JWT 密钥和 token
python3 gen_jwt.py
```

```sh
cd charts/neon

# 先获取依赖子 chart
helm dependency build .

helm install neon . -n neon \
  --set neon-storage-controller.settings.databaseUrl=postgres://storage_controller:storage_controller@192.168.232.128:5432/storage_controller \
  --set-file global.jwt.publicKey=jwt-out/publicKey.pem \
  --set-file global.jwt.privateKey=jwt-out/privateKey.pem \
  --set global.jwt.pageserverJwtToken=eyJhbGciOiJFZERTQSIsInR5cCI6IkpXVCJ9.eyJzY29wZSI6InBhZ2VzZXJ2ZXJhcGkifQ.A4G8xU6JAUhyYfYFQ1b1WYDh7qa71LCBxzfFuUycnFWk6a6sR_m9N-boC4VeEvLlVL1y7r2cPTUYe4zQs_5_Dw \
  --set global.jwt.safekeeperJwtToken=eyJhbGciOiJFZERTQSIsInR5cCI6IkpXVCJ9.eyJzY29wZSI6InNhZmVrZWVwZXJkYXRhIn0.iTCFkTBFViOktpdXgSt7OJT00vW1Ir-Lbsi9G1izln9iKYUbIcQoV6-5_lAGroubPpXUqFW-E4Frrfc9aYOxDA \
  --set global.jwt.controlPlaneJwtToken=eyJhbGciOiJFZERTQSIsInR5cCI6IkpXVCJ9.eyJzY29wZSI6ImNvbnRyb2xwbGFuZSJ9.fN2I-VZV7habd6AvdBiXBYPHrmofM9L_O-4Fa628nh1eeqQXLS5MS1F_24OeWOe4UgYO_h2-PaUUQcpKv2GeCQ \
  --set global.jwt.peerJwtToken=eyJhbGciOiJFZERTQSIsInR5cCI6IkpXVCJ9.eyJzY29wZSI6ImFkbWluIn0.IM0R81MQbnKkvLTQhiVylDZGvajrtxUzuyct5x9hgd1Oj3Xtev_ohOEH5qlogxo9V9izhOfRfCmsHV9LnduLDA \
  --set global.jwt.computeJwtToken=eyJhbGciOiJFZERTQSIsInR5cCI6IkpXVCJ9.eyJzY29wZSI6InRlbmFudCIsInRlbmFudF9pZCI6IjNkMWY3NTk1YjQ2ODIzMDMwNGUwYjczY2VjYmNiMDgxIn0.MPwxJRon7Xn-OA_-seCUm7pR_QmRzQFgRfrCG4GtoWX3t1mw_FI2PWBxcWMA7d7eYZppW7DisLiMGAyFWOkyDw \
  --set global.jwt.generationsApiJwtToken=eyJhbGciOiJFZERTQSIsInR5cCI6IkpXVCJ9.eyJzY29wZSI6ImdlbmVyYXRpb25zX2FwaSJ9.Md2wPpTTIv2YvMZiLdCWcugZ50hR4wNzB1FwyUAeQHOGKkCW0LByfhQlGsllku1rwxxgmAEAdxxcGafjRx1KBg \
  --set neon-storage-controller.settings.jwtToken=eyJhbGciOiJFZERTQSIsInR5cCI6IkpXVCJ9.eyJzY29wZSI6InBhZ2VzZXJ2ZXJhcGkifQ.A4G8xU6JAUhyYfYFQ1b1WYDh7qa71LCBxzfFuUycnFWk6a6sR_m9N-boC4VeEvLlVL1y7r2cPTUYe4zQs_5_Dw \
  --set neon-storage-controller.settings.safekeeperJwtToken=eyJhbGciOiJFZERTQSIsInR5cCI6IkpXVCJ9.eyJzY29wZSI6InNhZmVrZWVwZXJkYXRhIn0.iTCFkTBFViOktpdXgSt7OJT00vW1Ir-Lbsi9G1izln9iKYUbIcQoV6-5_lAGroubPpXUqFW-E4Frrfc9aYOxDA \
  --set neon-storage-controller.settings.controlPlaneJwtToken=eyJhbGciOiJFZERTQSIsInR5cCI6IkpXVCJ9.eyJzY29wZSI6ImNvbnRyb2xwbGFuZSJ9.fN2I-VZV7habd6AvdBiXBYPHrmofM9L_O-4Fa628nh1eeqQXLS5MS1F_24OeWOe4UgYO_h2-PaUUQcpKv2GeCQ \
  --set neon-storage-controller.settings.peerJwtToken=eyJhbGciOiJFZERTQSIsInR5cCI6IkpXVCJ9.eyJzY29wZSI6ImFkbWluIn0.IM0R81MQbnKkvLTQhiVylDZGvajrtxUzuyct5x9hgd1Oj3Xtev_ohOEH5qlogxo9V9izhOfRfCmsHV9LnduLDA \
  --set-file neon-storage-controller.settings.publicKey=jwt-out/publicKey.pem
```

```sh
kubectl port-forward -n neon svc/neon-control-plane-svc 8080:8080
```