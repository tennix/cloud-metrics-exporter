apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ include "cloud-metrics-exporter.fullname" . }}
  namespace: {{ include "cloud-metrics-exporter.namespace" . }}
  labels:
    helm.sh/chart: "{{ .Chart.Name }}-{{ .Chart.Version }}"
    app.kubernetes.io/name: {{ include "cloud-metrics-exporter.name" . }}
    app.kubernetes.io/instance: "{{ .Release.Name }}"
    app.kubernetes.io/managed-by: "{{ .Release.Service }}"
    app: "{{ .Values.appLabel }}"
spec:
  replicas: {{ .Values.replicaCount }}
  selector:
    matchLabels:
      app: {{ .Values.appLabel }}
  template:
    metadata:
      labels:
        app: {{ .Values.appLabel }}
    spec:
      serviceAccountName: {{ include "cloud-metrics-exporter.serviceAccountName" . }}
      {{- with .Values.imagePullSecrets }}
      imagePullSecrets:
        {{- range . }}
        - name: {{ .name }}
        {{- end }}
      {{- end }}
      {{- with .Values.podSecurityContext }}
      securityContext:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      containers:
        - name: exporter
          image: "{{ .Values.image.repository }}:{{ .Values.image.tag }}"
          imagePullPolicy: {{ .Values.image.pullPolicy }}
          {{- with .Values.containerSecurityContext }}
          securityContext:
            {{- toYaml . | nindent 12 }}
          {{- end }}
          args:
            - -config=/config/config.yaml
          ports:
            - name: {{ .Values.service.portName }}
              containerPort: {{ .Values.service.port }}
          volumeMounts:
            - name: config
              mountPath: /config
              readOnly: true
      volumes:
        - name: config
          configMap:
            name: {{ include "cloud-metrics-exporter.fullname" . }}
