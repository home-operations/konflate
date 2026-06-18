{{- /*
  konflate is single-instance: its PR/diff state lives in memory and is served
  from one pod (strategy defaults to Recreate below). Two replicas wedge on the RWO cache
  volume's Multi-Attach error with persistence, or round-robin between divergent
  in-memory stores without it. Reject >1 at render so the misconfiguration is a
  clear install error rather than a confusing runtime one; 0 is allowed (pause).
*/ -}}
{{- if gt (int .Values.replicaCount) 1 -}}
{{- fail (printf "konflate is single-instance (in-memory PR/diff state); replicaCount must be 0 or 1, got %d" (int .Values.replicaCount)) -}}
{{- end -}}
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ include "konflate.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "konflate.labels" . | nindent 4 }}
  {{- with .Values.deploymentAnnotations }}
  annotations:
    {{- toYaml . | nindent 4 }}
  {{- end }}
spec:
  replicas: {{ .Values.replicaCount }}
  # konflate keeps PR/diff state in memory, so run a single instance and replace
  # (rather than overlap) on rollout; Recreate is the only supported value (the
  # `strategy` knob is exposed for parity, but RollingUpdate is unsupported).
  {{- with .Values.strategy }}
  strategy:
    {{- toYaml . | nindent 4 }}
  {{- end }}
  selector:
    matchLabels:
      {{- include "konflate.selectorLabels" . | nindent 6 }}
  template:
    metadata:
      labels:
        {{- include "konflate.labels" . | nindent 8 }}
        {{- with .Values.podLabels }}
        {{- tpl (toYaml .) $ | nindent 8 }}
        {{- end }}
      {{- with .Values.podAnnotations }}
      annotations:
        {{- tpl (toYaml .) $ | nindent 8 }}
      {{- end }}
    spec:
      # The Service is named "konflate", so the kubelet's legacy Docker-link
      # service env vars would inject KONFLATE_PORT=tcp://<clusterIP>:8080 —
      # colliding with konflate's own KONFLATE_PORT config var and crashing
      # startup ("parsing tcp://… : invalid syntax"). konflate uses none of those
      # link vars, so turn them off.
      enableServiceLinks: false
      {{- with .Values.imagePullSecrets }}
      imagePullSecrets:
        {{- tpl (toYaml .) $ | nindent 8 }}
      {{- end }}
      serviceAccountName: {{ include "konflate.serviceAccountName" . | quote }}
      automountServiceAccountToken: {{ .Values.serviceAccount.automount }}
      {{- with .Values.priorityClassName }}
      priorityClassName: {{ tpl . $ | quote }}
      {{- end }}
      terminationGracePeriodSeconds: {{ .Values.terminationGracePeriodSeconds }}
      securityContext:
        {{- tpl (toYaml .Values.podSecurityContext) $ | nindent 8 }}
      containers:
        - name: konflate
          image: {{ include "konflate.image" . | quote }}
          imagePullPolicy: {{ .Values.image.pullPolicy }}
          securityContext:
            {{- tpl (toYaml .Values.securityContext) $ | nindent 12 }}
          env:
            - name: KONFLATE_REPO
              value: {{ tpl (required "config.repo is required — the forge URI of the repo to review" .Values.config.repo) $ | quote }}
            {{- with .Values.config.clusterPath }}
            - name: KONFLATE_CLUSTER_PATH
              value: {{ tpl . $ | quote }}
            {{- end }}
            - name: KONFLATE_LOG_LEVEL
              value: {{ tpl .Values.config.logLevel $ | quote }}
            - name: KONFLATE_LOG_FORMAT
              value: {{ tpl .Values.config.logFormat $ | quote }}
            {{- if gt (int .Values.config.maxDiffConcurrency) 0 }}
            - name: KONFLATE_MAX_DIFF_CONC
              value: {{ .Values.config.maxDiffConcurrency | quote }}
            {{- end }}
            {{- with .Values.config.prFilterExpr }}
            - name: KONFLATE_PR_FILTER_EXPR
              value: {{ tpl . $ | quote }}
            {{- end }}
            {{- if .Values.config.renderForkPrs }}
            - name: KONFLATE_RENDER_FORK_PRS
              value: "true"
            {{- end }}
            {{- if ne (toString .Values.config.restrictEgress) "" }}
            - name: KONFLATE_RESTRICT_EGRESS
              value: {{ .Values.config.restrictEgress | quote }}
            {{- end }}
            {{- if .Values.config.mcp }}
            - name: KONFLATE_MCP
              value: "true"
            {{- end }}
            {{- if ne (toString .Values.config.maxDiffResources) "" }}
            - name: KONFLATE_MAX_DIFF_RESOURCES
              value: {{ tpl (toString .Values.config.maxDiffResources) $ | quote }}
            {{- end }}
            {{- /* toString, not `with`: an explicit 0 (disable a cache) must
                   still emit — `with` would treat int 0 as empty and drop it,
                   silently reviving the default. Empty string = use the default.
                   toString also lets the value be a bare number or a template;
                   tpl then resolves any `{{ ... }}` against the release. */}}
            {{- if ne (toString .Values.config.helmTemplateCacheMb) "" }}
            - name: KONFLATE_HELM_TEMPLATE_CACHE_MB
              value: {{ tpl (toString .Values.config.helmTemplateCacheMb) $ | quote }}
            {{- end }}
            {{- if ne (toString .Values.config.helmRenderCacheMb) "" }}
            - name: KONFLATE_HELM_RENDER_CACHE_MB
              value: {{ tpl (toString .Values.config.helmRenderCacheMb) $ | quote }}
            {{- end }}
            {{- if ne (toString .Values.config.cacheTtl) "" }}
            - name: KONFLATE_CACHE_TTL
              value: {{ tpl (toString .Values.config.cacheTtl) $ | quote }}
            {{- end }}
            {{- if ne (toString .Values.config.sourceRetryAttempts) "" }}
            - name: KONFLATE_SOURCE_RETRY_ATTEMPTS
              value: {{ tpl (toString .Values.config.sourceRetryAttempts) $ | quote }}
            {{- end }}
            {{- if ne (toString .Values.config.renderConcurrency) "" }}
            - name: KONFLATE_RENDER_CONCURRENCY
              value: {{ tpl (toString .Values.config.renderConcurrency) $ | quote }}
            {{- end }}
            {{- if ne (toString .Values.config.diffTimeout) "" }}
            - name: KONFLATE_DIFF_TIMEOUT
              value: {{ tpl (toString .Values.config.diffTimeout) $ | quote }}
            {{- end }}
            {{- if ne (toString .Values.config.fetchTimeout) "" }}
            - name: KONFLATE_FETCH_TIMEOUT
              value: {{ tpl (toString .Values.config.fetchTimeout) $ | quote }}
            {{- end }}
            - name: KONFLATE_CLOSED_PR_MAX
              value: {{ .Values.config.closedPrMax | quote }}
            - name: KONFLATE_CLOSED_PR_TTL
              value: {{ tpl .Values.config.closedPrTtl $ | quote }}
            {{- with .Values.config.mergeCommand }}
            # NOT tpl'd: this value is itself a Go text/template that konflate
            # renders per-PR with .Number/.Repo. Running Helm's tpl here would try
            # to evaluate those (Helm has no such values) and fail the render.
            - name: KONFLATE_MERGE_COMMAND
              value: {{ . | quote }}
            {{- end }}
            {{- /* Write-back (opt-in): the non-secret half. The write credential
                   itself — writeToken / appPrivateKey — rides in via the Secret
                   (envFrom below). KONFLATE_STATUS_CHECKS gates the feature; it
                   does nothing without a credential, so it's safe to always emit. */}}
            {{- if .Values.config.statusChecks }}
            - name: KONFLATE_STATUS_CHECKS
              value: "true"
            {{- end }}
            {{- with .Values.config.statusCheckName }}
            - name: KONFLATE_STATUS_CHECK_NAME
              value: {{ tpl . $ | quote }}
            {{- end }}
            {{- if .Values.config.prComments }}
            - name: KONFLATE_PR_COMMENTS
              value: "true"
            {{- end }}
            {{- if .Values.config.prCommentTemplate }}
            # A custom comment template — mounted from the chart-managed ConfigMap below.
            - name: KONFLATE_PR_COMMENT_TEMPLATE_FILE
              value: /etc/konflate/pr-comment.md.gotmpl
            {{- end }}
            {{- with .Values.config.publicUrl }}
            - name: KONFLATE_PUBLIC_URL
              value: {{ tpl . $ | quote }}
            {{- end }}
            {{- with .Values.config.appClientId }}
            - name: KONFLATE_APP_CLIENT_ID
              value: {{ tpl . $ | quote }}
            {{- end }}
            # Writable locations under the mounted volumes (readOnlyRootFilesystem).
            - name: KONFLATE_CACHE_DIR
              value: /var/cache/konflate
            - name: KONFLATE_CLONE_DIR
              value: /tmp/konflate
            - name: HOME
              value: /tmp
            {{- with .Values.config.refreshInterval }}
            - name: KONFLATE_REFRESH_INTERVAL
              value: {{ tpl . $ | quote }}
            {{- end }}
            {{- /* toString, not `with`: an explicit "0" (fall back to
                   refreshInterval) must still emit — `with` drops a 0. */}}
            {{- if ne (toString .Values.config.rerenderInterval) "" }}
            - name: KONFLATE_RERENDER_INTERVAL
              value: {{ tpl (toString .Values.config.rerenderInterval) $ | quote }}
            {{- end }}
            {{- with .Values.config.extraEnv }}
            {{- tpl (toYaml .) $ | nindent 12 }}
            {{- end }}
          {{- with (include "konflate.secretName" .) }}
          envFrom:
            - secretRef:
                name: {{ . | quote }}
          {{- end }}
          ports:
            - name: http
              containerPort: 8080
              protocol: TCP
            - name: metrics
              containerPort: 8081
              protocol: TCP
          {{- with .Values.startupProbe }}
          startupProbe:
            {{- tpl (toYaml .) $ | nindent 12 }}
          {{- end }}
          livenessProbe:
            {{- tpl (toYaml .Values.livenessProbe) $ | nindent 12 }}
          readinessProbe:
            {{- tpl (toYaml .Values.readinessProbe) $ | nindent 12 }}
          {{- with .Values.resources }}
          resources:
            {{- tpl (toYaml .) $ | nindent 12 }}
          {{- end }}
          volumeMounts:
            - name: cache
              mountPath: /var/cache/konflate
            - name: tmp
              mountPath: /tmp
            {{- if .Values.config.prCommentTemplate }}
            - name: pr-comment-template
              mountPath: /etc/konflate
              readOnly: true
            {{- end }}
            {{- with .Values.volumeMounts }}
            {{- tpl (toYaml .) $ | nindent 12 }}
            {{- end }}
      volumes:
        - name: cache
          {{- if .Values.persistence.enabled }}
          persistentVolumeClaim:
            claimName: {{ include "konflate.cacheClaimName" . | quote }}
          {{- else }}
          emptyDir: {}
          {{- end }}
        - name: tmp
          emptyDir: {}
        {{- if .Values.config.prCommentTemplate }}
        - name: pr-comment-template
          configMap:
            name: {{ include "konflate.fullname" . }}-pr-comment-template
        {{- end }}
        {{- with .Values.volumes }}
        {{- tpl (toYaml .) $ | nindent 8 }}
        {{- end }}
      {{- with .Values.nodeSelector }}
      nodeSelector:
        {{- tpl (toYaml .) $ | nindent 8 }}
      {{- end }}
      {{- with .Values.affinity }}
      affinity:
        {{- tpl (toYaml .) $ | nindent 8 }}
      {{- end }}
      {{- with .Values.tolerations }}
      tolerations:
        {{- tpl (toYaml .) $ | nindent 8 }}
      {{- end }}
