{{- if .Values.monitoring.serviceMonitor.enabled -}}
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: {{ include "konflate.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "konflate.labels" . | nindent 4 }}
    {{- with .Values.monitoring.serviceMonitor.labels }}
    {{- tpl (toYaml .) $ | nindent 4 }}
    {{- end }}
  {{- with .Values.monitoring.serviceMonitor.annotations }}
  annotations:
    {{- tpl (toYaml .) $ | nindent 4 }}
  {{- end }}
spec:
  selector:
    matchLabels:
      {{- include "konflate.selectorLabels" . | nindent 6 }}
  endpoints:
    - port: metrics
      interval: {{ .Values.monitoring.serviceMonitor.interval | default "30s" }}
      scrapeTimeout: {{ .Values.monitoring.serviceMonitor.scrapeTimeout | default "10s" }}
      path: {{ .Values.monitoring.serviceMonitor.path | default "/metrics" }}
      {{- with .Values.monitoring.serviceMonitor.metricRelabelings }}
      metricRelabelings:
        {{- tpl (toYaml .) $ | nindent 8 }}
      {{- end }}
      {{- with .Values.monitoring.serviceMonitor.relabelings }}
      relabelings:
        {{- tpl (toYaml .) $ | nindent 8 }}
      {{- end }}
{{- end }}
