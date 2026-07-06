# aws-cost-reporter

![Version: 1.0.1](https://img.shields.io/badge/Version-1.0.1-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) [![Lint and Test Charts](https://github.com/neondatabase/helm-charts/actions/workflows/lint-test.yaml/badge.svg)](https://github.com/neondatabase/helm-charts/actions/workflows/lint-test.yaml)

AWS 成本报告器

## 源代码

* <https://github.com/neondatabase/aws-cost-reporter>

## 安装 Chart

添加 Neondatabase chart 仓库：
```console
$ helm repo add neondatabase https://neondatabase.github.io/helm-charts
```

使用 IRSA 访问 AWS 资源，安装发布名称为 `aws-cost-reporter` 的 chart：

```console
$ helm install aws-cost-reporter neondatabase/aws-cost-reporter \
  --set slack.token="<Slack App token>" \
  --set serviceAccount.roleArn="arn:aws:iam::<AWS account id>:role/<IRSA role name>"
```

使用 AWS 凭证访问 AWS 资源，安装发布名称为 `aws-cost-reporter` 的 chart：

```console
$ helm install aws-cost-reporter neondatabase/aws-cost-reporter \
  --set slack.token="<Slack App token>" \
  --set aws.awsAccessKey="<AWS access key id>" \
  --set aws.awsSecretKey="<AWS secret access key>" \
  --set aws.awsRegion="eu-east-1"
```

## 参数值

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| affinity | object | `{}` | Pod 调度亲和性配置 |
| aws | object | `{}` | 不使用 IRSA 时的 AWS 凭证配置 |
| fullnameOverride | string | `""` | 完全覆盖 aws-cost-reporter.fullname 模板的字符串 |
| image.pullPolicy | string | `"IfNotPresent"` | 镜像拉取策略 |
| image.repository | string | `"neondatabase/aws-cost-reporter"` | 镜像仓库地址 |
| image.tag | string | `""` | 覆盖镜像标签，默认为 chart 的 appVersion |
| imagePullSecrets | list | `[]` | 指定 Docker 镜像仓库密钥名称数组 |
| nameOverride | string | `""` | 部分覆盖 aws-cost-reporter.fullname 模板的字符串（保留发布名称） |
| nodeSelector | object | `{}` | Pod 调度的节点标签选择器 |
| podAnnotations | object | `{}` | aws-cost-reporter Pod 的注解 |
| podSecurityContext | object | `{}` | aws-cost-reporter Pod 的安全上下文 |
| replicaCount | int | `1` |  |
| resources | object | `{}` |  |
| securityContext | object | `{}` | aws-cost-reporter 容器的安全上下文 |
| serviceAccount.annotations | object | `{}` | 添加到 Service Account 的注解 |
| serviceAccount.create | bool | `true` | 指定是否创建 Service Account |
| serviceAccount.name | string | `""` | 使用的 Service Account 名称。如果未设置且 create 为 true，则使用 fullname 模板生成名称 |
| serviceAccount.roleArn | string | `""` | 用于访问 AWS Cost Explorer 服务的 AWS IAM Role ARN（IRSA） |
| slack.channelID | string | `"#general"` | Slack 频道 ID |
| slack.header | string | `"AWS Cost and Usage :moneybag:"` | Slack 消息标题 |
| slack.token | string | `""` | Slack App 令牌 |
| tolerations | list | `[]` | Pod 调度的容忍度配置 |

----------------------------------------------
由 [helm-docs v1.9.1](https://github.com/norwoodj/helm-docs/releases/v1.9.1) 根据 chart 元数据自动生成
