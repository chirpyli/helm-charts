{{/*
Expand the name of the chart.
*/}}
{{- define "local-path-provisioner.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "local-path-provisioner.fullname" -}}
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
{{- define "local-path-provisioner.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels.
*/}}
{{- define "local-path-provisioner.labels" -}}
helm.sh/chart: {{ include "local-path-provisioner.chart" . }}
{{ include "local-path-provisioner.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "local-path-provisioner.selectorLabels" -}}
app.kubernetes.io/name: {{ include "local-path-provisioner.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
渲染资源所属的命名空间。
默认跟随 release 命名空间（不输出 namespace 字段）；
仅当 namespace.create=true 时创建独立命名空间并显式声明 namespace，
保持与上游 local-path-storage 命名空间部署方式兼容。
*/}}
{{- define "local-path-provisioner.namespace" -}}
{{- if .Values.namespace.create -}}
{{- .Values.namespace.name | default "local-path-storage" -}}
{{- else -}}
{{- .Release.Namespace -}}
{{- end -}}
{{- end }}

