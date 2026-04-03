{{- if .Values.rbac.create }}
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: {{ include "cloud-metrics-exporter.fullname" . }}
  labels:
    helm.sh/chart: "{{ .Chart.Name }}-{{ .Chart.Version }}"
    app.kubernetes.io/name: {{ include "cloud-metrics-exporter.name" . }}
    app.kubernetes.io/instance: "{{ .Release.Name }}"
    app.kubernetes.io/managed-by: "{{ .Release.Service }}"
    app: "{{ .Values.appLabel }}"
subjects:
  - kind: ServiceAccount
    name: {{ include "cloud-metrics-exporter.serviceAccountName" . }}
    namespace: {{ include "cloud-metrics-exporter.namespace" . }}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: {{ include "cloud-metrics-exporter.fullname" . }}
{{- end }}
