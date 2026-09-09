{{/*
Expand the name of the chart.
*/}}
{{- define "neon-safekeeper.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "neon-safekeeper.fullname" -}}
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
{{- define "neon-safekeeper.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels.
*/}}
{{- define "neon-safekeeper.labels" -}}
helm.sh/chart: {{ include "neon-safekeeper.chart" . }}
{{ include "neon-safekeeper.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "neon-safekeeper.selectorLabels" -}}
app.kubernetes.io/name: {{ include "neon-safekeeper.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "neon-safekeeper.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "neon-safekeeper.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
ClusterIP 服务名
*/}}
{{- define "neon-safekeeper.serviceName" -}}
{{- printf "%s-svc" (include "neon-safekeeper.fullname" .) | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Headless 服务名（StatefulSet 稳定 DNS）
*/}}
{{- define "neon-safekeeper.headlessServiceName" -}}
{{- printf "%s-hl-svc" (include "neon-safekeeper.fullname" .) | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
可用区（AZ）探测脚本：供 init 容器在 Pod 启动时执行，把探测结果写入 shell 变量 AZ。

为什么必须运行时探测：
  AZ 静态写死在 values 里，会导致同一份 chart 部署到多可用区集群时，所有副本都上报同一个 AZ，
  storage controller 会据此做出错误的调度决策（时间线放置不合理、pageserver 与 safekeeper 跨可用区通信）。
  因此这里统一从 Pod 实际所在节点的 topology.kubernetes.io/zone 标签动态获取。

依赖（全部由 k8s 自动提供，无需额外配置）：
  - NODE_NAME：Downward API 注入（fieldPath: spec.nodeName）
  - /var/run/secrets/kubernetes.io/serviceaccount/{token,ca.crt}：Pod 的 ServiceAccount 凭据
  - KUBERNETES_SERVICE_HOST / KUBERNETES_SERVICE_PORT：kubelet 注入的 apiserver 地址

权限：需要 nodes 资源的 get 权限，见 node-reader-rbac.yaml（受 rbac.nodeReader.enabled 控制）。

失败策略：探测不到 AZ 直接 exit 1，Pod 停在 Init 状态而不启动。
  宁可启动失败，也不能带着错误（或空）的 AZ 注册进 storage controller。
*/}}
{{- define "neon-safekeeper.azLookupScript" -}}
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
echo "init-node-id: 从节点 ${NODE_NAME} 探测到可用区 AZ=${AZ}"
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
{{- define "neon-safekeeper.jwtSecretName" -}}
{{- .Values.settings.jwtSecretName | default (dig "jwt" "existingSecret" "" (.Values.global | default dict)) | default (dig "jwt" "secretName" "" (.Values.global | default dict)) -}}
{{- end -}}
