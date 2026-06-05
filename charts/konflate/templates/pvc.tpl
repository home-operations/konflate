{{- if and .Values.persistence.enabled (not .Values.persistence.existingClaim) -}}
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: {{ include "konflate.cacheClaimName" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "konflate.labels" . | nindent 4 }}
  {{- with .Values.persistence.annotations }}
  annotations:
    {{- toYaml . | nindent 4 }}
  {{- end }}
spec:
  accessModes:
    {{- toYaml .Values.persistence.accessModes | nindent 4 }}
  resources:
    requests:
      storage: {{ .Values.persistence.size | quote }}
  {{- with .Values.persistence.storageClass }}
  storageClassName: {{ . }}
  {{- end }}
{{- end }}
