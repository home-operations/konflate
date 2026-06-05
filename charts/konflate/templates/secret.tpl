{{- if and (not .Values.secret.existingSecret) (or .Values.secret.token .Values.secret.webhookSecret .Values.secret.pushToken) -}}
apiVersion: v1
kind: Secret
metadata:
  name: {{ include "konflate.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "konflate.labels" . | nindent 4 }}
type: Opaque
stringData:
  {{- with .Values.secret.token }}
  KONFLATE_TOKEN: {{ . | quote }}
  {{- end }}
  {{- with .Values.secret.webhookSecret }}
  KONFLATE_WEBHOOK_SECRET: {{ . | quote }}
  {{- end }}
  {{- with .Values.secret.pushToken }}
  KONFLATE_PUSH_TOKEN: {{ . | quote }}
  {{- end }}
{{- end }}
