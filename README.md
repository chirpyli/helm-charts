# Neondatabase Kubernetes Helm Charts

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)

这些 helm charts 用于部署构成 Neon 平台的多个服务。请注意，这些 charts 不会为您提供一个完全可用的类 Neon 系统：它们仅适用于各个独立服务，除非您也独立部署其他组件（例如 postgres），否则它们不太可能对您有用。

此功能处于测试阶段，可能会发生变化。代码按"原样"提供，不提供任何保证。测试版功能不受官方正式版功能的支持 SLA 约束。

## 使用方法

使用 charts 必须安装 [Helm](https://helm.sh)。
请参考 Helm 的[文档](https://helm.sh/docs/)来开始使用。

一旦 Helm 设置完成，可以按如下方式添加仓库：

```console
helm repo add neondatabase https://neondatabase.github.io/helm-charts
```

然后您可以运行 `helm search repo neondatabase` 来查看可用的 charts。

## 贡献

所有 Neondatabase Helm charts 的源代码都可以在 Github 上找到：<https://github.com/neondatabase/helm-charts/>

<!-- 保留完整的仓库文件 URL 链接，因为此 README 会从 main 同步到 gh-pages。 -->
我们欢迎您的贡献！请参考我们的[贡献指南](https://github.com/neondatabase/helm-charts/blob/main/CONTRIBUTING.md)了解详细信息。

## 许可证

<!-- 保留完整的仓库文件 URL 链接，因为此 README 会从 main 同步到 gh-pages。 -->
[Apache 2.0 许可证](https://github.com/neondatabase/helm-charts/blob/main/LICENSE)。

## Helm charts 构建状态

![Release Charts](https://github.com/neondatabase/helm-charts/workflows/Release%20Charts/badge.svg?branch=main) [![Lint and Test Charts](https://github.com/neondatabase/helm-charts/actions/workflows/lint-test.yaml/badge.svg)](https://github.com/neondatabase/helm-charts/actions/workflows/lint-test.yaml)
