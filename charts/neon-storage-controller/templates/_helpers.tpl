{{/*
Expand the name of the chart.
*/}}
{{- define "neon-storage-controller.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "neon-storage-controller.fullname" -}}
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
{{- define "neon-storage-controller.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common for Service and Deployment labels
*/}}
{{- define "neon-storage-controller.labels" -}}
helm.sh/chart: {{ include "neon-storage-controller.chart" . }}
{{ include "neon-storage-controller.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "neon-storage-controller.selectorLabels" -}}
app.kubernetes.io/name: {{ include "neon-storage-controller.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "neon-storage-controller.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "neon-storage-controller.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Returns service name
This will be use only for internal purpose e.g. ingress connecting to service
*/}}
{{- define "neon-storage-controller.serviceName" -}}
{{- printf "%s-svc" (include "neon-storage-controller.fullname" .) | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
共享 JWT Secret 名称取值链。

优先级（从高到低）：
  1. settings.jwtSecretName      —— 子 chart 独立部署时显式指定
  2. global.jwt.existingSecret   —— umbrella 部署时的主用入口，指向外部预建的 Secret
  3. global.jwt.secretName       —— 历史兼容字段

storage_controller 的密钥读取方式（见 storage_controller/src/main.rs 的 Secrets::load）：
  只支持 CLI 参数或环境变量，其中环境变量由 K8s 以 secretKeyRef 注入，
  因此本 chart 只需要 Secret 名称，不再在 values 中承载任何 token 明文。

为什么用 dig + (default dict)：
  子 chart 独立部署时 .Values.global 可能压根不存在，dig 遇到 nil 会直接报错，
  用 default dict 兜底，保证独立部署场景不会因为模板报错而不可用。

返回空串表示"未配置任何 JWT Secret"，调用方据此跳过相关 env 注入。
*/}}
{{- define "neon-storage-controller.jwtSecretName" -}}
{{- .Values.settings.jwtSecretName | default (dig "jwt" "existingSecret" "" (.Values.global | default dict)) | default (dig "jwt" "secretName" "" (.Values.global | default dict)) -}}
{{- end -}}

{{/*
外部 PostgreSQL 连接串 Secret 名称取值链。

优先级（从高到低）：
  1. settings.databaseUrlSecretName                          —— 子 chart 独立部署时显式指定
  2. global.storageController.databaseUrl.existingSecret     —— umbrella 部署时的主用入口

取值方式与 jwtSecretName 保持一致（dig + default dict）：
  子 chart 独立部署时 .Values.settings / .Values.global 都可能不存在，
  dig 遇到 nil 会直接报错，用 default dict 兜底保证独立部署场景不因模板报错而不可用。

返回空串表示"未配置外部 DB Secret"，调用方据此在渲染阶段 fail：
  storage_controller 把连接串视为必填项（缺失即 bail），静默启动只会得到一个
  CrashLoopBackOff 的 Pod，不如在 helm 阶段就给出可读的错误。
*/}}
{{- define "neon-storage-controller.databaseUrlSecretName" -}}
{{- dig "databaseUrlSecretName" "" (.Values.settings | default dict) | default (dig "storageController" "databaseUrl" "existingSecret" "" (.Values.global | default dict)) -}}
{{- end -}}

{{/*
外部 PostgreSQL 连接串 Secret 中的键名。

默认 "uri"：CloudNativePG 生成的 Secret 与 docs/neon.yaml 中的
storage-controller-pg-cluster 都用这个键。其他来源（自建库的 DATABASE_URL、
Zalando operator 的 url）通过 settings.databaseUrlSecretKey 覆盖即可。
*/}}
{{- define "neon-storage-controller.databaseUrlSecretKey" -}}
{{- dig "databaseUrlSecretKey" "" (.Values.settings | default dict) | default (dig "storageController" "databaseUrl" "secretKey" "" (.Values.global | default dict)) | default "uri" -}}
{{- end -}}
