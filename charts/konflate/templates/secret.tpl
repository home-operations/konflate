{{- if and (not .Values.secret.existingSecret) (include "konflate.hasInlineSecret" .) -}}
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
  KONFLATE_TOKEN: {{ tpl . $ | quote }}
  {{- end }}
  {{- with .Values.secret.webhookSecret }}
  KONFLATE_WEBHOOK_SECRET: {{ tpl . $ | quote }}
  {{- end }}
  {{- with .Values.secret.pushToken }}
  KONFLATE_PUSH_TOKEN: {{ tpl . $ | quote }}
  {{- end }}
  {{- with .Values.secret.writeToken }}
  KONFLATE_WRITE_TOKEN: {{ tpl . $ | quote }}
  {{- end }}
  {{- with .Values.secret.appPrivateKey }}
  KONFLATE_APP_PRIVATE_KEY: {{ tpl . $ | quote }}
  {{- end }}
{{- end }}
