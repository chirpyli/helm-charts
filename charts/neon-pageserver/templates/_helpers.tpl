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
