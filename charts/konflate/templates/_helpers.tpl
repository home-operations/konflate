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
{{- $name := tpl (.Values.serviceAccount.name | default "") $ -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "konflate.fullname" .) $name }}
{{- else }}
{{- default "default" $name }}
{{- end }}
{{- end }}

{{/*
Container image reference. A digest pins immutably and wins when set (the
release pipeline fills it with the published image's digest); otherwise it's
repository:tag, with tag defaulting to the chart appVersion.
*/}}
{{- define "konflate.image" -}}
{{- if .Values.image.digest -}}
{{- printf "%s@%s" .Values.image.repository .Values.image.digest -}}
{{- else -}}
{{- printf "%s:%s" .Values.image.repository (.Values.image.tag | default .Chart.AppVersion) -}}
{{- end -}}
{{- end }}

{{/*
Image for the `helm test` connection pod (konflate's own image is distroless, so
the test uses a small image with a shell). The tag is pinned as
`version@sha256:digest`, so Renovate updates the version and digest together.
*/}}
{{- define "konflate.testImage" -}}
{{- $img := .Values.tests.image -}}
{{- printf "%s:%s" $img.repository $img.tag -}}
{{- end }}

{{/*
True ("true") when at least one inline secret value is set. The chart-managed
Secret is rendered (and referenced) only then; the secretName helper and
secret.tpl both gate on this, so its stringData keys stay in sync here.
*/}}
{{- define "konflate.hasInlineSecret" -}}
{{- if or .Values.secret.token .Values.secret.webhookSecret .Values.secret.pushToken .Values.secret.writeToken .Values.secret.appPrivateKey -}}
true
{{- end -}}
{{- end }}

{{/*
Name of the Secret holding the sensitive KONFLATE_* values, or "" if none:
an existing Secret wins; otherwise a chart-managed Secret is used only when at
least one inline value is set.
*/}}
{{- define "konflate.secretName" -}}
{{- if .Values.secret.existingSecret -}}
{{- tpl .Values.secret.existingSecret $ -}}
{{- else if include "konflate.hasInlineSecret" . -}}
{{- include "konflate.fullname" . -}}
{{- end -}}
{{- end }}

{{/*
Name of the PVC backing the source cache (existingClaim wins).
*/}}
{{- define "konflate.cacheClaimName" -}}
{{- default (printf "%s-cache" (include "konflate.fullname" .)) (tpl (.Values.persistence.existingClaim | default "") $) -}}
{{- end }}

{{/*
config.basePath, tpl'd (like every other config value) and normalized the way
the binary normalizes KONFLATE_BASE_PATH: surrounding whitespace and extra
slashes trimmed, "" or "/" meaning root (empty output). Values the binary would
reject at boot fail here instead, at template time. Takes the root context.
*/}}
{{- define "konflate.basePath" -}}
{{- $p := tpl (.Values.config.basePath | default "") . | trim -}}
{{- if and $p (ne $p "/") -}}
{{- if not (hasPrefix "/" $p) -}}
{{- fail (printf "config.basePath must start with / (got %q)" $p) -}}
{{- end -}}
{{- if or (regexMatch "[{}*%:$?#]" $p) (contains "..." $p) -}}
{{- fail (printf "config.basePath must not contain URL or ServeMux metacharacters (got %q)" $p) -}}
{{- end -}}
{{- range splitList "/" (trimAll "/" $p) -}}
{{- if or (eq . "") (eq . ".") (eq . "..") -}}
{{- fail (printf "config.basePath must not contain empty, ., or .. segments (got %q)" $p) -}}
{{- end -}}
{{- end -}}
{{- printf "/%s" (trimAll "/" $p) -}}
{{- end -}}
{{- end }}

{{/*
Prepend the normalized config.basePath to a probe/API path when konflate is
served under a subpath (preserve-prefix deployment). Empty basePath returns the
path unchanged. Takes (dict "path" <path> "root" $).
*/}}
{{- define "konflate.prefixedPath" -}}
{{- printf "%s%s" (include "konflate.basePath" .root) .path -}}
{{- end }}

{{/*
A probe spec with its httpGet path prefixed by config.basePath. An httpGet
override that omits path keeps kubelet's default of "/" (prefixed). Takes
(dict "probe" <probe> "root" $).
*/}}
{{- define "konflate.probeSpec" -}}
{{- $probe := deepCopy .probe -}}
{{- if $probe.httpGet -}}
{{- $_ := set $probe.httpGet "path" (include "konflate.prefixedPath" (dict "path" ($probe.httpGet.path | default "/") "root" .root)) -}}
{{- end -}}
{{- tpl (toYaml $probe) .root -}}
{{- end }}
