{{/*
Expand the name of the chart.
*/}}
{{- define "cloud-metrics-exporter.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "cloud-metrics-exporter.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := include "cloud-metrics-exporter.name" . -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Namespace helper: default to raw-manifest namespace unless overridden.
*/}}
{{- define "cloud-metrics-exporter.namespace" -}}
{{- if .Values.namespaceOverride -}}
{{- .Values.namespaceOverride -}}
{{- else -}}
{{- .Release.Namespace -}}
{{- end -}}
{{- end -}}

{{/*
Selector labels (keep stable `app: cloud-metrics-exporter` by default).
*/}}
{{- define "cloud-metrics-exporter.selectorLabels" -}}
app: {{ .Values.appLabel | quote }}
{{- end -}}

{{/*
Common labels.
*/}}
{{- define "cloud-metrics-exporter.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | quote }}
app.kubernetes.io/name: {{ include "cloud-metrics-exporter.name" . | quote }}
app.kubernetes.io/instance: {{ .Release.Name | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service | quote }}
{{ include "cloud-metrics-exporter.selectorLabels" . }}
{{- end -}}

{{/*
ServiceAccount name.
*/}}
{{- define "cloud-metrics-exporter.serviceAccountName" -}}
{{- if .Values.serviceAccount.name -}}
{{- .Values.serviceAccount.name -}}
{{- else -}}
{{- include "cloud-metrics-exporter.fullname" . -}}
{{- end -}}
{{- end -}}
