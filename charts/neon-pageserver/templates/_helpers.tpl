{{/*
Expand the name of the chart.
*/}}
{{- define "neon-pageserver.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "neon-pageserver.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "neon-pageserver.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels for Service and Deployment/StatefulSet.
*/}}
{{- define "neon-pageserver.labels" -}}
helm.sh/chart: {{ include "neon-pageserver.chart" . }}
{{ include "neon-pageserver.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "neon-pageserver.selectorLabels" -}}
app.kubernetes.io/name: {{ include "neon-pageserver.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "neon-pageserver.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "neon-pageserver.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
ClusterIP 服务名（SC / compute 访问入口）
*/}}
{{- define "neon-pageserver.serviceName" -}}
{{- printf "%s-svc" (include "neon-pageserver.fullname" .) | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Headless 服务名（StatefulSet 稳定 DNS，<pod>.<headless-svc>.<ns>.svc.cluster.local）
*/}}
{{- define "neon-pageserver.headlessServiceName" -}}
{{- printf "%s-hl-svc" (include "neon-pageserver.fullname" .) | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
可用区（AZ）探测脚本：供 init 容器在 Pod 启动时执行，把探测结果写入 shell 变量 AZ。

为什么必须运行时探测：
  AZ 静态写死在 values 里，会导致同一份 chart 部署到多可用区集群时，所有副本都上报同一个 AZ，
  storage controller 会据此做出错误的调度决策（时间线放置不合理、跨可用区 WAL 流量）。
  因此这里统一从 Pod 实际所在节点的 topology.kubernetes.io/zone 标签动态获取。

依赖（全部由 k8s 自动提供，无需额外配置）：
  - NODE_NAME：Downward API 注入（fieldPath: spec.nodeName）
  - /var/run/secrets/kubernetes.io/serviceaccount/{token,ca.crt}：Pod 的 ServiceAccount 凭据
  - KUBERNETES_SERVICE_HOST / KUBERNETES_SERVICE_PORT：kubelet 注入的 apiserver 地址

权限：需要 nodes 资源的 get 权限，见 node-reader-rbac.yaml（受 rbac.nodeReader.enabled 控制）。

失败策略：探测不到 AZ 直接 exit 1，Pod 停在 Init 状态而不启动。
  宁可启动失败，也不能带着错误（或空）的 AZ 注册进 storage controller。
*/}}
{{- define "neon-pageserver.azLookupScript" -}}
# ---- 动态探测本 Pod 所在节点的可用区 ----
SA_DIR=/var/run/secrets/kubernetes.io/serviceaccount
# 只 GET 自己所在的这一个节点对象，数据量极小；失败重试 3 次后交由下方判定退出
NODE_JSON=$(curl -sS --fail --max-time 10 --retry 3 --retry-delay 2 \
  --cacert "${SA_DIR}/ca.crt" \
  -H "Authorization: Bearer $(cat "${SA_DIR}/token")" \
  "https://${KUBERNETES_SERVICE_HOST}:${KUBERNETES_SERVICE_PORT}/api/v1/nodes/${NODE_NAME}") || {
  echo "FATAL: 读取节点 ${NODE_NAME} 信息失败（请检查 rbac.nodeReader.enabled 是否已为 ServiceAccount 授权 nodes get）"
  exit 1
}
# 用 | 作 sed 分隔符以避免转义标签名中的斜杠，只提取 zone 这一个值；
# 冒号两侧允许空白，兼容 apiserver 返回的压缩 JSON 与格式化（带缩进）JSON
AZ=$(printf '%s' "${NODE_JSON}" | sed -n 's|.*"topology\.kubernetes\.io/zone"[[:space:]]*:[[:space:]]*"\([^"]*\)".*|\1|p')
if [ -z "${AZ}" ]; then
  echo "FATAL: 节点 ${NODE_NAME} 缺少 topology.kubernetes.io/zone 标签，无法动态确定可用区"
  echo "        请先执行：kubectl label node ${NODE_NAME} topology.kubernetes.io/zone=<可用区名称>"
  exit 1
fi
echo "init-identity: 从节点 ${NODE_NAME} 探测到可用区 AZ=${AZ}"
{{- end }}

{{/*
共享 JWT Secret 名称取值链。

优先级（从高到低）：
  1. settings.jwtSecretName      —— 子 chart 独立部署时显式指定
  2. global.jwt.existingSecret   —— umbrella 部署时的主用入口，指向外部预建的 Secret
  3. global.jwt.secretName       —— 历史兼容字段

为什么必须把 settings.jwtSecretName 的默认值清空：
  该值此前默认为 "neon-jwt"（非空）；`| default` 只在前值被判定为空时才回退，
  恒为非空会让父级传入的 global.jwt.existingSecret 永远不生效（既有缺陷）。

为什么用 dig + (default dict)：
  子 chart 独立部署时 .Values.global 可能压根不存在，dig 遇到 nil 会直接报错，
  用 default dict 兜底，保证独立部署场景不会因为模板报错而不可用。

返回空串表示"未配置任何 JWT Secret"，调用方据此跳过 jwt 卷渲染。
*/}}
{{- define "neon-pageserver.jwtSecretName" -}}
{{- .Values.settings.jwtSecretName | default (dig "jwt" "existingSecret" "" (.Values.global | default dict)) | default (dig "jwt" "secretName" "" (.Values.global | default dict)) -}}
{{- end -}}

{{/*
对象存储 Secret 名称解析链（优先级从高到低）：
  1. settings.remoteStorage.existingSecret   —— 子 chart 独立部署时显式指定
  2. global.storage.bucket.existingSecret    —— umbrella 部署时的主用入口
  3. "bucket-credentials"                    —— 兜底默认名，对齐 docs/ 中已发布的示例

为什么默认名只能放在链条最后一级：
  `| default` 只在前值被判定为空时才回退，把默认名放在第 1 级会让父级传入的值永远无法生效
  （与 neon-pageserver.jwtSecretName 踩过的坑同源，见上方注释）。

为什么每一层都要 `| default dict` 兜底：
  子 chart 独立部署时 .Values.settings / .Values.global 可能压根不存在，
  直接对其取字段或 dig 遇到 nil 会直接报模板错误，导致独立部署场景不可用。
*/}}
{{- define "neon-pageserver.bucketSecretName" -}}
{{- $settings := (.Values.settings | default dict) -}}
{{- $remoteStorage := ($settings.remoteStorage | default dict) -}}
{{- $local := ($remoteStorage.existingSecret | default "") -}}
{{- $globalValue := dig "storage" "bucket" "existingSecret" "" (.Values.global | default dict) -}}
{{- $local | default $globalValue | default "bucket-credentials" -}}
{{- end -}}

{{/*
对象存储 Secret 的键名映射：内置默认 ← global.storage.bucket.keys ← settings.remoteStorage.keys（后者覆盖前者）。

为什么返回 YAML 文本而不是 dict：
  Go template 的 include 只能返回字符串，无法直接返回 map；
  因此这里 toYaml 序列化，调用侧用 `include ... | fromYaml` 还原成 dict（Helm 社区惯用法）。

为什么用 mergeOverwrite 且先 deepCopy：
  mergeOverwrite 会就地修改第一个参数，deepCopy 保证内置默认值不被本次渲染污染，
  也让"只覆盖其中一两个键"成为可能（未列出的键自动回退到默认值）。
*/}}
{{- define "neon-pageserver.bucketSecretKeys" -}}
{{- $defaults := dict "bucketName" "BUCKET_NAME" "region" "AWS_REGION" "endpoint" "AWS_ENDPOINT_URL" "accessKeyId" "AWS_ACCESS_KEY_ID" "secretAccessKey" "AWS_SECRET_ACCESS_KEY" -}}
{{- $globalKeys := dig "storage" "bucket" "keys" dict (.Values.global | default dict) | default dict -}}
{{- $settings := (.Values.settings | default dict) -}}
{{- $localKeys := (($settings.remoteStorage | default dict).keys | default dict) -}}
{{- toYaml (mergeOverwrite (deepCopy $defaults) $globalKeys $localKeys) -}}
{{- end -}}

{{/*
桶内路径前缀。

保留在 values 的原因：它是"多环境共享同一个桶时如何隔离数据"的布局参数，
而不是"连哪个对象存储"的连接坐标，Secret 中也没有对应的键，不属于本次收敛范围。
兜底 "pageserver" 与 values.yaml 中的默认值保持一致（用户显式留空时同样回退到该值）。
*/}}
{{- define "neon-pageserver.remoteStoragePrefix" -}}
{{- $settings := (.Values.settings | default dict) -}}
{{- (($settings.remoteStorage | default dict).prefixInBucket | default "pageserver") -}}
{{- end -}}

{{/*
遗留键守卫：settings.remoteStorage.bucketName / bucketRegion / endpoint 已彻底移除。

为什么必须主动 fail 而不是静默忽略：
  这三个键此前是"坐标的唯一来源"，用户升级 chart 时若沿用旧 values，
  新模板不会读取它们，配置会被静默丢弃 —— pageserver 会带着 Secret 里的另一套坐标启动，
  表现为"改了 values 没生效"甚至数据写错桶。宁可在渲染阶段就报错并给出迁移指引。

为什么用 (.Values.settings | default dict) 再取字段：
  hasKey 遇到 nil 会直接报模板错误；用户若整个删掉 settings 块，这里必须安全跳过而不是崩渲染。
*/}}
{{- define "neon-pageserver.remoteStorageGuard" -}}
{{- $root := . -}}
{{- $settings := (.Values.settings | default dict) -}}
{{- $remoteStorage := ($settings.remoteStorage | default dict) -}}
{{- range $legacyKey := (list "bucketName" "bucketRegion" "endpoint") -}}
{{- if hasKey $remoteStorage $legacyKey -}}
{{- fail (printf "settings.remoteStorage.%s 已移除：对象存储连接坐标的唯一事实来源现在是 Secret（%s），values 中不再保留副本。请删除该键，并在 Secret 中补齐对应字段（BUCKET_NAME / AWS_REGION / AWS_ENDPOINT_URL），详见 charts/neon/templates/NOTES.txt。" $legacyKey (include "neon-pageserver.bucketSecretName" $root)) -}}
{{- end -}}
{{- end -}}
{{- end -}}
