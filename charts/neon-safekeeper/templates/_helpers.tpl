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
