{{- if .Values.networkPolicy.enabled }}
{{- $np := .Values.networkPolicy }}
{{- /* konflate's container ports (see deployment.tpl): the HTTP UI/API and the
       separate monitoring server (/metrics + health probes). Ingress is limited
       to these; egress is shaped to DNS + the configured TCP ports (konflate
       needs broad egress to render). */}}
{{- $http := 8080 }}
{{- $metrics := 8081 }}
{{- if eq $np.type "cilium" }}
apiVersion: cilium.io/v2
kind: CiliumNetworkPolicy
metadata:
  name: {{ include "konflate.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "konflate.labels" . | nindent 4 }}
spec:
  endpointSelector:
    matchLabels:
      {{- include "konflate.selectorLabels" . | nindent 6 }}
  ingress:
    - fromEntities:
        - all
      toPorts:
        - ports:
            - port: {{ $http | quote }}
              protocol: TCP
            - port: {{ $metrics | quote }}
              protocol: TCP
  egress:
    {{- if $np.allowDNS }}
    - toEndpoints:
        - matchLabels:
            k8s:io.kubernetes.pod.namespace: kube-system
            k8s-app: kube-dns
      toPorts:
        - ports:
            - port: "53"
              protocol: UDP
            - port: "53"
              protocol: TCP
    {{- end }}
    {{- with $np.egressPorts }}
    - toEntities:
        - cluster
        - world
      toPorts:
        - ports:
            {{- range . }}
            - port: {{ . | quote }}
              protocol: TCP
            {{- end }}
    {{- end }}
{{- else if eq $np.type "calico" }}
apiVersion: projectcalico.org/v3
kind: NetworkPolicy
metadata:
  name: {{ include "konflate.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "konflate.labels" . | nindent 4 }}
spec:
  selector: app.kubernetes.io/name == '{{ include "konflate.name" . }}' && app.kubernetes.io/instance == '{{ .Release.Name }}'
  types:
    - Ingress
    - Egress
  ingress:
    - action: Allow
      protocol: TCP
      destination:
        ports:
          - {{ $http }}
          - {{ $metrics }}
  egress:
    {{- if $np.allowDNS }}
    - action: Allow
      protocol: UDP
      destination:
        ports:
          - 53
    - action: Allow
      protocol: TCP
      destination:
        ports:
          - 53
    {{- end }}
    {{- with $np.egressPorts }}
    - action: Allow
      protocol: TCP
      destination:
        ports:
          {{- range . }}
          - {{ . }}
          {{- end }}
    {{- end }}
{{- else if eq $np.type "default" }}
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: {{ include "konflate.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "konflate.labels" . | nindent 4 }}
spec:
  podSelector:
    matchLabels:
      {{- include "konflate.selectorLabels" . | nindent 6 }}
  policyTypes:
    - Ingress
    - Egress
  ingress:
    # No `from` — ingress is limited to konflate's ports, reachable by any peer
    # (lock down per cluster with your own policy if needed).
    - ports:
        - port: {{ $http }}
          protocol: TCP
        - port: {{ $metrics }}
          protocol: TCP
  egress:
    {{- if $np.allowDNS }}
    - ports:
        - port: 53
          protocol: UDP
        - port: 53
          protocol: TCP
    {{- end }}
    {{- with $np.egressPorts }}
    - ports:
        {{- range . }}
        - port: {{ . }}
          protocol: TCP
        {{- end }}
    {{- end }}
{{- else }}
{{- fail (printf "networkPolicy.type must be one of: default, cilium, calico (got %q)" $np.type) }}
{{- end }}
{{- end }}
