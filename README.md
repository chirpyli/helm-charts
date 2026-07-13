# Neondatabase Kubernetes Helm 图表

[![许可证](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)

这些 Helm 图表用于部署构成 Neon 平台的若干服务。请注意，这些图表**并不能**为你提供一个完整可用的类 Neon 系统：它们仅针对各个独立服务，除非你也独立部署了其他组件（例如 postgres），否则对你而言很可能用处不大。

此功能目前处于 beta 阶段，后续可能会发生变化。代码按“现状”提供，不附带任何担保。Beta 功能不受官方 GA（正式发布）功能支持 SLA 的约束。

## 使用方法

使用这些图表需要预先安装 [Helm](https://helm.sh)。
请参考 Helm 的[文档](https://helm.sh/docs/)以开始使用。

在 Helm 正确配置完成后，按如下方式添加仓库：

```console
helm repo add neondatabase https://neondatabase.github.io/helm-charts
```

随后你可以运行 `helm search repo neondatabase` 来查看可用的图表。

## 贡献

所有 Neondatabase Helm 图表的源代码都可以从 Github 上找到：<https://github.com/neondatabase/helm-charts/>

<!-- 保留指向仓库文件的完整 URL 链接，因为本 README 会从 main 分支同步到 gh-pages 分支。 -->
我们非常欢迎你的贡献！详情请参考我们的[贡献指南](https://github.com/neondatabase/helm-charts/blob/main/CONTRIBUTING.md)。

## 许可证

<!-- 保留指向仓库文件的完整 URL 链接，因为本 README 会从 main 分支同步到 gh-pages 分支。 -->
[Apache 2.0 许可证](https://github.com/neondatabase/helm-charts/blob/main/LICENSE)。

## Helm 图表构建状态

![发布图表](https://github.com/neondatabase/helm-charts/workflows/Release%20Charts/badge.svg?branch=main) [![检查并测试图表](https://github.com/neondatabase/helm-charts/actions/workflows/lint-test.yaml/badge.svg)](https://github.com/neondatabase/helm-charts/actions/workflows/lint-test.yaml)
