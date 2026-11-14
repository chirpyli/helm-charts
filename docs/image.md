# 本地镜像仓库

用 Docker Registry 2 快速起一个本地仓库。

```sh
 docker run -d \
    -p 5000:5000 \
    --name registry \
    -v /srv/registry-data:/opt/registry \
    registry:2
```

如果是私有registry，则需要将开源镜像下载下来导入到私有仓库中。

下载镜像：

```sh
docker pull ghcr.io/neondatabase/neon:latest
docker pull ghcr.io/neondatabase/compute-node-v17:latest
docker pull rancher/local-path-provisioner:v0.0.36
docker pull busybox:1.35.0
```

导出镜像：

```sh
docker save ghcr.io/neondatabase/neon:latest -o neondatabase-neon.tar
docker save ghcr.io/neondatabase/compute-node-v17:latest -o neondatabase-compute-node-v17.tar
docker save rancher/local-path-provisioner:v0.0.36 -o local-path-provisioner-v0.0.36.tar
```

用私有 registry 地址重新打 tag（假设私有registry 为 192.168.232.128:5000）

```sh
REG=192.168.232.128:5000/neondatabase
docker tag ghcr.io/neondatabase/neon:latest                       $REG/neon:latest
docker tag ghcr.io/neondatabase/compute-node-v17:latest           $REG/compute-node-v17:latest
docker tag ghcr.io/neondatabase/neon-control-plane:latest         $REG/neon-control-plane:latest
docker tag busybox:1.35.0                                         $REG/busybox:1.35.0
docker tag rancher/local-path-provisioner:v0.0.36                 $REG/local-path-provisioner:v0.0.36
```

推到私有 registry

```sh
docker push $REG/neon:latest
docker push $REG/compute-node-v17:latest
docker push $REG/neon-control-plane:latest
docker push $REG/busybox:1.35.0
docker push $REG/local-path-provisioner:v0.0.36
```

## 安装部署 local-path-provisioner

仓库已提供 `charts/local-path-provisioner`，无需再手工 apply 官方 yaml。

### 方式一：独立部署（默认部署到指定命名空间）

```sh
helm install local-path-provisioner ./charts/local-path-provisioner -n neon --create-namespace
```

与上游一致、部署到独立命名空间 `local-path-storage`：

```sh
helm install local-path-provisioner ./charts/local-path-provisioner \
  --set namespace.create=true
```

### 方式二：作为 neon umbrella 的可选子 chart（默认关闭）

```sh
helm upgrade --install neon ./charts/neon -n neon \
  --set local-path-provisioner.enabled=true \
  --set neon-pageserver.statefulSet.storage.storageClassName=local-path \
  --set neon-safekeeper.statefulSet.storage.storageClassName=local-path
```

> 该子 chart 由 neon 统一调度命名空间，不要开启 `namespace.create`。

### 验证

```sh
# 确认 StorageClass（默认不设为集群默认，由组件显式引用）
kubectl get storageclass local-path

# 确认 provisioner 运行正常
kubectl get pods -n neon -l app.kubernetes.io/name=local-path-provisioner

# 如需设为集群默认（谨慎，会影响集群内所有未指定 SC 的 PVC）
kubectl patch storageclass local-path -p '{"metadata":{"annotations":{"storageclass.kubernetes.io/is-default-class":"true"}}}'
```

### 与集群内置 StorageClass 冲突

k3s / kind 等发行版已内置同名 `local-path` StorageClass，二选一：

```sh
# 复用集群内置的，不创建 SC
helm install local-path-provisioner ./charts/local-path-provisioner --set storageClass.create=false

# 或改名部署
helm install local-path-provisioner ./charts/local-path-provisioner --set storageClass.name=neon-local-path
```

### 卸载

```sh
helm uninstall local-path-provisioner -n neon
```

> 卸载不会清理已供给的宿主机目录（默认 `/opt/local-path-provisioner`）与其中数据；
> `reclaimPolicy=Delete` 仅在删除 PVC 时触发 `teardown` 脚本。

> 仓库：https://github.com/rancher/local-path-provisioner
> 版本：**v0.0.36**（具体版本，生产推荐）
> 作用：在 K8s 上提供基于节点本地磁盘的 StorageClass（`local-path`），供 Neon 等组件动态申请 PVC。
> 更多参数见：[charts/local-path-provisioner/README.md](../charts/local-path-provisioner/README.md)
