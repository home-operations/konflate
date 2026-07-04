apiVersion: v1
kind: Service
metadata:
  name: {{ include "konflate.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "konflate.labels" . | nindent 4 }}
spec:
  type: {{ .Values.service.type }}
  ports:
    - name: http
      port: {{ .Values.service.port }}
      targetPort: http
      protocol: TCP
    {{- if .Values.service.metricsEnabled }}
    - name: metrics
      port: {{ .Values.service.metricsPort }}
      targetPort: metrics
      protocol: TCP
    {{- end }}
  selector:
    {{- include "konflate.selectorLabels" . | nindent 4 }}
