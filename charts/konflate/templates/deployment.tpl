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
        {{- toYaml . | nindent 8 }}
        {{- end }}
      {{- with .Values.podAnnotations }}
      annotations:
        {{- toYaml . | nindent 8 }}
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
        {{- toYaml . | nindent 8 }}
      {{- end }}
      serviceAccountName: {{ include "konflate.serviceAccountName" . }}
      automountServiceAccountToken: {{ .Values.serviceAccount.automount }}
      securityContext:
        {{- toYaml .Values.podSecurityContext | nindent 8 }}
      containers:
        - name: konflate
          image: {{ include "konflate.image" . | quote }}
          imagePullPolicy: {{ .Values.image.pullPolicy }}
          securityContext:
            {{- toYaml .Values.securityContext | nindent 12 }}
          env:
            - name: KONFLATE_REPO
              value: {{ required "config.repo is required — the forge URI of the repo to review" .Values.config.repo | quote }}
            {{- with .Values.config.clusterPath }}
            - name: KONFLATE_CLUSTER_PATH
              value: {{ . | quote }}
            {{- end }}
            - name: KONFLATE_LOG_LEVEL
              value: {{ .Values.config.logLevel | quote }}
            - name: KONFLATE_LOG_FORMAT
              value: {{ .Values.config.logFormat | quote }}
            {{- if gt (int .Values.config.maxDiffConcurrency) 0 }}
            - name: KONFLATE_MAX_DIFF_CONC
              value: {{ .Values.config.maxDiffConcurrency | quote }}
            {{- end }}
            - name: KONFLATE_CLOSED_PR_MAX
              value: {{ .Values.config.closedPrMax | quote }}
            - name: KONFLATE_CLOSED_PR_TTL
              value: {{ .Values.config.closedPrTtl | quote }}
            # Writable locations under the mounted volumes (readOnlyRootFilesystem).
            - name: KONFLATE_CACHE_DIR
              value: /var/cache/konflate
            - name: KONFLATE_CLONE_DIR
              value: /tmp/konflate
            - name: HOME
              value: /tmp
            {{- with .Values.config.refreshInterval }}
            - name: KONFLATE_REFRESH_INTERVAL
              value: {{ . | quote }}
            {{- end }}
            {{- with .Values.config.extraEnv }}
            {{- toYaml . | nindent 12 }}
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
            {{- toYaml .Values.livenessProbe | nindent 12 }}
          readinessProbe:
            {{- toYaml .Values.readinessProbe | nindent 12 }}
          {{- with .Values.resources }}
          resources:
            {{- toYaml . | nindent 12 }}
          {{- end }}
          volumeMounts:
            - name: cache
              mountPath: /var/cache/konflate
            - name: tmp
              mountPath: /tmp
            {{- with .Values.volumeMounts }}
            {{- toYaml . | nindent 12 }}
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
        {{- toYaml . | nindent 8 }}
        {{- end }}
      {{- with .Values.nodeSelector }}
      nodeSelector:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with .Values.affinity }}
      affinity:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with .Values.tolerations }}
      tolerations:
        {{- toYaml . | nindent 8 }}
      {{- end }}
