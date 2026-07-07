# Neon 私有化部署 Helm Charts

[![License](<https://img.shields.io/badge/License-Apache%202.0-blue.svg>)](https://opensource.org/licenses/Apache-2.0)

基于 [neondatabase/helm-charts](https://github.com/neondatabase/helm-charts) 扩展的 Neon 私有化部署 Helm Charts，新增了 pageserver、safekeeper、compute、dummy-cp 等组件 chart，以及一键部署的 neon-stack umbrella chart。

上游 chart（storage-broker、storage-controller）保持与官方仓库同步，自建 chart 用于补全私有化部署所需的缺失组件。本仓库仅供学习使用，未达到生产环境可用。

## 使用方法

使用 charts 必须安装 [Helm](https://helm.sh)。
请参考 Helm 的[文档](https://helm.sh/docs/)来开始使用。

### 部署完整 Neon 集群

```console
# 1. 添加上游依赖仓库
helm repo add neondatabase https://neondatabase.github.io/helm-charts
helm repo update

# 2. 克隆本项目
git clone https://github.com/chirpyli/helm-charts.git
cd helm-charts

# 3. 在 neon-stack 目录下更新依赖
cd charts/neon-stack
helm dependency update

# 4. 部署
helm install neon-stack . -f values.yaml --namespace neon
```

### 单独部署某个组件

```console
helm install neon-dummy-cp ./charts/neon-dummy-cp
```

## 感谢

上游代码请参考 [neondatabase/helm-charts](https://github.com/neondatabase/helm-charts/)。

## 许可证

[Apache 2.0 许可证](https://github.com/chirpyli/helm-charts/blob/develop/LICENSE)。
