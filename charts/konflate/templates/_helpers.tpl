{{/*
Expand the name of the chart.
*/}}
{{- define "konflate.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name (truncated to the 63-char DNS limit).
*/}}
{{- define "konflate.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Chart name and version as used by the chart label.
*/}}
{{- define "konflate.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "konflate.labels" -}}
helm.sh/chart: {{ include "konflate.chart" . }}
{{ include "konflate.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "konflate.selectorLabels" -}}
app.kubernetes.io/name: {{ include "konflate.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Service account name to use.
*/}}
{{- define "konflate.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "konflate.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Container image reference (tag defaults to the chart appVersion).
*/}}
{{- define "konflate.image" -}}
{{- printf "%s:%s" .Values.image.repository (.Values.image.tag | default .Chart.AppVersion) }}
{{- end }}

{{/*
Name of the Secret holding the sensitive KONFLATE_* values, or "" if none:
an existing Secret wins; otherwise a chart-managed Secret is used only when at
least one inline value is set.
*/}}
{{- define "konflate.secretName" -}}
{{- if .Values.secret.existingSecret -}}
{{- .Values.secret.existingSecret -}}
{{- else if or .Values.secret.token .Values.secret.webhookSecret .Values.secret.pushToken -}}
{{- include "konflate.fullname" . -}}
{{- end -}}
{{- end }}

{{/*
Name of the PVC backing the source cache (existingClaim wins).
*/}}
{{- define "konflate.cacheClaimName" -}}
{{- default (printf "%s-cache" (include "konflate.fullname" .)) .Values.persistence.existingClaim -}}
{{- end }}
