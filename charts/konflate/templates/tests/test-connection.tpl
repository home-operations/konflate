apiVersion: v1
kind: Pod
metadata:
  name: {{ include "konflate.fullname" . }}-test-connection
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "konflate.labels" . | nindent 4 }}
  annotations:
    helm.sh/hook: test
    # before-hook-creation only (no hook-succeeded): `helm test` then never deletes
    # the pod itself, so it can't block on Helm 4's wait-for-delete (kstatus) after a
    # green run — which otherwise stalled `helm test` ~5m. The pod is recreated on
    # the next run, and a failed run's pod stays for `helm test --logs` / kubectl.
    helm.sh/hook-delete-policy: before-hook-creation
spec:
  restartPolicy: Never
  securityContext:
    runAsNonRoot: true
    runAsUser: 65532
    runAsGroup: 65532
    seccompProfile:
      type: RuntimeDefault
  containers:
    - name: connection
      image: {{ include "konflate.testImage" . | quote }}
      imagePullPolicy: {{ .Values.tests.image.pullPolicy }}
      securityContext:
        allowPrivilegeEscalation: false
        readOnlyRootFilesystem: true
        capabilities:
          drop:
            - ALL
      # /readyz returns a static 200 (no repo/forge dependency), so this checks
      # purely that the Service routes to a running, listening pod. curl -f fails on a
      # non-2xx (or a refused connection), failing `helm test`; -sS stays quiet but
      # still surfaces errors, and the body goes to stdout (no file write, so the
      # rootfs stays read-only).
      command:
        - curl
      args:
        - -fsS
        - http://{{ include "konflate.fullname" . }}:{{ .Values.service.port }}/readyz
