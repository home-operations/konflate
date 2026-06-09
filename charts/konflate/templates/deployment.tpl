apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ include "konflate.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "konflate.labels" . | nindent 4 }}
spec:
  replicas: {{ .Values.replicaCount }}
  # konflate keeps PR/diff state in memory, so run a single instance and replace
  # (rather than overlap) on rollout.
  strategy:
    type: Recreate
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
      serviceAccountName: {{ include "konflate.serviceAccountName" . }}
      automountServiceAccountToken: {{ .Values.serviceAccount.automount }}
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
              value: {{ . | quote }}
            {{- end }}
            {{- if .Values.config.renderForkPrs }}
            - name: KONFLATE_RENDER_FORK_PRS
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
            {{- if ne (toString .Values.config.stageCacheMb) "" }}
            - name: KONFLATE_STAGE_CACHE_MB
              value: {{ tpl (toString .Values.config.stageCacheMb) $ | quote }}
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
            {{- with .Values.config.extraEnv }}
            {{- tpl (toYaml .) $ | nindent 12 }}
            {{- end }}
          {{- with (include "konflate.secretName" .) }}
          envFrom:
            - secretRef:
                name: {{ . }}
          {{- end }}
          ports:
            - name: http
              containerPort: 8080
              protocol: TCP
            - name: metrics
              containerPort: 9090
              protocol: TCP
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
            {{- with .Values.volumeMounts }}
            {{- tpl (toYaml .) $ | nindent 12 }}
            {{- end }}
      volumes:
        - name: cache
          {{- if .Values.persistence.enabled }}
          persistentVolumeClaim:
            claimName: {{ include "konflate.cacheClaimName" . }}
          {{- else }}
          emptyDir: {}
          {{- end }}
        - name: tmp
          emptyDir: {}
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
