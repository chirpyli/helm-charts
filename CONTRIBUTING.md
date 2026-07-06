# 贡献指南

欢迎通过 GitHub Pull Request 提交贡献。本文档概述了帮助您的贡献被接受的流程。

## 如何贡献

1. Fork 此仓库，开发并测试您的更改
1. 提交 Pull Request

***注意***：为了简化 PR 的测试和合并流程，请将对多个 charts 的更改分别提交到不同的 PR 中。

### 技术要求

* 必须遵循 [Charts 最佳实践](https://helm.sh/docs/topics/chart_best_practices/)
* 必须通过 CI 作业的 lint 检查和使用 [chart-testing](https://github.com/helm/chart-testing) 工具安装变更后的 charts
* 对 chart 的任何更改都需要按照 [semver](https://semver.org/) 原则进行版本号升级。请参阅下方的 [不可变性](#immutability) 和 [版本控制](#versioning)

更改合并后，发布作业将自动运行，打包并发布变更的 charts。

### 不可变性

Chart 发布必须是不可变的。对 chart 的任何更改都需要升级版本号，即使只是修改了文档。

### 版本控制

Chart 的 `version` 应遵循 [semver](https://semver.org/)。

Charts 应从 `1.0.0` 开始。对 chart 的任何破坏性（向后不兼容）更改应：

1. 在相应的 `Chart.yaml` 中升级 MAJOR 版本号
2. 使用 [helm-docs](https://github.com/norwoodj/helm-docs) 生成文档。
   注意：CI 需要特定版本的 `helm-docs` 才能通过，因此如果您使用不同版本生成文档，CI 可能仍会失败
   生成正确的 `README.md` 的最简单方法是按照 CI 中的方式运行生成命令，即：
   ```bash
   docker run --rm --volume "$(pwd):/helm-docs" -u "$(id -u)" jnorwood/helm-docs:v1.9.1
   ```
