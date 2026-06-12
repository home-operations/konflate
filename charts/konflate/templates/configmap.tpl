{{- with .Values.config.prCommentTemplate }}
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ include "konflate.fullname" $ }}-pr-comment-template
  namespace: {{ $.Release.Namespace }}
  labels:
    {{- include "konflate.labels" $ | nindent 4 }}
data:
  # NOT tpl'd: this is konflate's own Go text/template, rendered per-PR with
  # .PR / .Diff / .ReviewURL / .Summary, so Helm passes it through verbatim.
  pr-comment.md.gotmpl: |
{{- . | nindent 4 }}
{{- end }}
