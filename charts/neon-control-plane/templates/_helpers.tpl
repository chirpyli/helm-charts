{{/*
Expand the name of the chart.
*/}}
{{- define "neon-control-plane.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "neon-control-plane.fullname" -}}
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
{{- define "neon-control-plane.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels.
*/}}
{{- define "neon-control-plane.labels" -}}
helm.sh/chart: {{ include "neon-control-plane.chart" . }}
{{ include "neon-control-plane.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "neon-control-plane.selectorLabels" -}}
app.kubernetes.io/name: {{ include "neon-control-plane.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "neon-control-plane.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "neon-control-plane.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Service name.
*/}}
{{- define "neon-control-plane.serviceName" -}}
{{- printf "%s-svc" (include "neon-control-plane.fullname" .) | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
共享 JWT Secret 名称取值链。

优先级（从高到低）：
  1. settings.jwtSecretName      —— 子 chart 独立部署时显式指定
  2. global.jwt.existingSecret   —— umbrella 部署时的主用入口，指向外部预建的 Secret
  3. global.jwt.secretName       —— 历史兼容字段

说明：本 chart 是唯一需要挂载 privateKey.pem 的组件（控制面持有私钥用于签发 token），
其余组件只应投影 publicKey.pem 与各自所需的 token 键，避免私钥扩散。

为什么必须把 settings.jwtSecretName 的默认值清空：
  该值此前默认为 "neon-jwt"（非空）；`| default` 只在前值被判定为空时才回退，
  恒为非空会让父级传入的 global.jwt.existingSecret 永远不生效（既有缺陷）。

为什么用 dig + (default dict)：
  子 chart 独立部署时 .Values.global 可能压根不存在，dig 遇到 nil 会直接报错，
  用 default dict 兜底，保证独立部署场景不会因为模板报错而不可用。

返回空串表示"未配置任何 JWT Secret"，调用方据此跳过 jwt 卷渲染。
*/}}
{{- define "neon-control-plane.jwtSecretName" -}}
{{- .Values.settings.jwtSecretName | default (dig "jwt" "existingSecret" "" (.Values.global | default dict)) | default (dig "jwt" "secretName" "" (.Values.global | default dict)) -}}
{{- end -}}

{{/*
控制面独立库连接串 Secret 名称取值链。

优先级（从高到低）：
  1. settings.stateStore.databaseUrlSecretName —— 子 chart 独立部署时显式指定
  2. global.controlPlane.databaseUrl.existingSecret —— umbrella 部署时的主用入口

与 storage-controller 的 databaseUrl Secret 完全不同：指向不同库、不同账号。
返回空串时由 deployment.yaml 在渲染阶段 fail，避免 Pod 启动后 CrashLoopBackOff。
*/}}
{{- define "neon-control-plane.databaseUrlSecretName" -}}
{{- dig "databaseUrlSecretName" "" (.Values.settings.stateStore | default dict) | default (dig "controlPlane" "databaseUrl" "existingSecret" "" (.Values.global | default dict)) -}}
{{- end -}}

{{/*
控制面独立库连接串 Secret 键名，默认 uri（与 CloudNativePG / docs 示例一致）。
*/}}
{{- define "neon-control-plane.databaseUrlSecretKey" -}}
{{- dig "databaseUrlSecretKey" "" (.Values.settings.stateStore | default dict) | default (dig "controlPlane" "databaseUrl" "secretKey" "" (.Values.global | default dict)) | default "uri" -}}
{{- end -}}
