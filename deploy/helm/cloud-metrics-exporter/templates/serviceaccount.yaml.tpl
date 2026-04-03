{{- if .Values.serviceAccount.create }}
apiVersion: v1
kind: ServiceAccount
metadata:
  name: {{ include "cloud-metrics-exporter.serviceAccountName" . }}
  namespace: {{ include "cloud-metrics-exporter.namespace" . }}
  labels:
    helm.sh/chart: "{{ .Chart.Name }}-{{ .Chart.Version }}"
    app.kubernetes.io/name: {{ include "cloud-metrics-exporter.name" . }}
    app.kubernetes.io/instance: "{{ .Release.Name }}"
    app.kubernetes.io/managed-by: "{{ .Release.Service }}"
    app: "{{ .Values.appLabel }}"
{{- end }}
