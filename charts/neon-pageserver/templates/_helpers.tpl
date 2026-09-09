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
