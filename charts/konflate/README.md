# konflate

![Version: 0.0.0](https://img.shields.io/badge/Version-0.0.0-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: 0.0.0](https://img.shields.io/badge/AppVersion-0.0.0-informational?style=flat-square)

A web UI for reviewing GitOps pull requests as rendered Flux diffs

**Homepage:** <https://github.com/home-operations/konflate>

## Usage

konflate ships as an OCI Helm chart. The only required value is `config.repo` — the
forge URI of the repository to review:

```sh
helm install konflate oci://ghcr.io/home-operations/charts/konflate \
  --set config.repo=github://owner/repo
```

By default konflate runs **anonymous and read-only**: no forge token, and the inbound
webhook/refresh endpoints are disabled. To raise API rate limits or review private
repositories set a token (`secret.token`); to enable webhook/push refreshes set
`secret.webhookSecret` / `secret.pushToken` (or point `secret.existingSecret` at a
Secret holding the `KONFLATE_*` keys).

Persisting the on-disk caches and the git mirror is recommended for any non-trivial
repo — set `persistence.enabled=true`. Expose the UI with either `ingress` or a
Gateway API `httpRoute`.

## Maintainers

| Name | Email | Url |
| ---- | ------ | --- |
| home-operations | <contact@home-operations.com> |  |

## Source Code

* <https://github.com/home-operations/konflate>

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| affinity | object | `{}` | Affinity rules for pod scheduling. |
| config.closedPrMax | int | `25` | Cap on retained merged PRs (most-recent win); this count, not the age, bounds memory. 0 disables the cap. |
| config.closedPrTtl | string | `"336h"` | How long a merged PR is kept (Go duration); 0 disables the age cap. In-memory — a restart clears the shelf. |
| config.clusterPath | string | `""` | Directory flate renders from; empty = repo root (correct for the standard root-relative layout). |
| config.diffTimeout | string | `""` | Hard cap on a single PR render end-to-end (Go duration). Empty = default (10m); "0" disables. Lower on untrusted instances. |
| config.extraEnv | list | `[]` | Extra raw env vars merged into the container (advanced). |
| config.helmRenderCacheMb | string | `""` | Advanced: persistent on-disk Helm render cache in MiB, reused across renders/PRs/restarts. Empty = default (1024); "0" disables. |
| config.helmTemplateCacheMb | string | `""` | Advanced: in-memory Helm template cache in MiB (the biggest CPU saver). Empty = flate default (256); "0" disables. |
| config.logFormat | string | `"json"` | Log format: json or text. |
| config.logLevel | string | `"info"` | Log level: debug, info, warn, or error. |
| config.maxDiffConcurrency | int | `0` | Max concurrent diff renders; 0 = auto (from the CPU limit, capped at 4). |
| config.mergeCommand | string | `""` | Optional Go text/template for the copy-to-merge command (only .Number / .Repo). Empty = forge default (gh/glab/tea). konflate never runs it. |
| config.refreshInterval | string | `"30m"` | How often each open PR re-renders / the PR list reconciles, as a missed-webhook backstop (Go duration). |
| config.renderConcurrency | string | `""` | Advanced: cap on reconcile goroutines within one render. Empty/"0" = auto (NumCPU*4). |
| config.repo | required | `""` | Forge URI of the repository to review (github://owner/repo, gitlab://group/repo, forgejo://host/owner/repo). |
| config.sourceRetryAttempts | string | `""` | Advanced: tries per source fetch on transient network errors. Empty = default (3); "1" disables retry. |
| config.stageCacheMb | string | `""` | Advanced: persistent kustomize stage cache in MiB. Empty = default (2048); "0" disables size-based eviction. |
| fullnameOverride | string | `""` | Override the full release name. |
| httpRoute.additionalRules | list | `[]` | Custom rules prepended before the default rule (templated). |
| httpRoute.annotations | object | `{}` | HTTPRoute annotations. |
| httpRoute.apiVersion | string | `""` | HTTPRoute apiVersion; empty defaults to gateway.networking.k8s.io/v1. |
| httpRoute.enabled | bool | `false` | Expose the UI via a Gateway API HTTPRoute (alternative to ingress). |
| httpRoute.filters | list | `[]` | Filters applied to the default rule. |
| httpRoute.hostnames | list | `[]` | Hostnames matched against the Host header (templated). |
| httpRoute.httpsRedirect | bool | `false` | Redirect HTTP→HTTPS (301) instead of routing to the backend (needs HTTP+HTTPS listeners). |
| httpRoute.kind | string | `""` | HTTPRoute kind; empty defaults to HTTPRoute. |
| httpRoute.labels | object | `{}` | HTTPRoute labels. |
| httpRoute.matches | list | `[{"path":{"type":"PathPrefix","value":"/"}}]` | Match conditions for the default rule. |
| httpRoute.parentRefs | list | `[]` | Gateways (and listeners) this route attaches to. |
| image.digest | string | `""` | Pin the image by digest (sha256:…); when set, overrides the tag. |
| image.pullPolicy | string | `"IfNotPresent"` | Image pull policy. |
| image.repository | string | `"ghcr.io/home-operations/konflate"` | Image repository. |
| image.tag | string | `""` | Overrides the image tag; defaults to the chart appVersion. |
| imagePullSecrets | list | `[]` | Image pull secrets for private registries. |
| ingress.annotations | object | `{}` | Ingress annotations. |
| ingress.className | string | `""` | IngressClass name. |
| ingress.enabled | bool | `false` | Expose the UI via an Ingress. |
| ingress.hosts | list | `[{"host":"konflate.example.com","paths":[{"path":"/","pathType":"Prefix"}]}]` | Ingress hosts and their paths. |
| ingress.tls | list | `[]` | Ingress TLS configuration. |
| livenessProbe | object | `{"httpGet":{"path":"/healthz","port":"http"},"initialDelaySeconds":10,"periodSeconds":20}` | Liveness probe. |
| monitoring.serviceMonitor.annotations | object | `{}` | ServiceMonitor annotations. |
| monitoring.serviceMonitor.enabled | bool | `false` | Create a Prometheus Operator ServiceMonitor (requires its CRDs). |
| monitoring.serviceMonitor.interval | string | `"30s"` | Scrape interval. |
| monitoring.serviceMonitor.labels | object | `{}` | ServiceMonitor labels. |
| monitoring.serviceMonitor.metricRelabelings | list | `[]` | Prometheus metric relabelings. |
| monitoring.serviceMonitor.path | string | `"/metrics"` | Metrics path. |
| monitoring.serviceMonitor.relabelings | list | `[]` | Prometheus relabelings. |
| monitoring.serviceMonitor.scrapeTimeout | string | `"10s"` | Scrape timeout. |
| nameOverride | string | `""` | Override the chart name used in resource names. |
| nodeSelector | object | `{}` | Node selector for pod scheduling. |
| persistence.accessModes | list | `["ReadWriteOnce"]` | PVC access modes. |
| persistence.annotations | object | `{}` | PVC annotations. |
| persistence.enabled | bool | `false` | Persist caches + the git mirror across restarts (recommended for any non-trivial repo). |
| persistence.existingClaim | string | `""` | Use an existing PVC instead of creating one. |
| persistence.size | string | `"5Gi"` | PVC size. |
| persistence.storageClass | string | `""` | StorageClass for the created PVC. |
| podAnnotations | object | `{}` | Annotations added to the pod. |
| podLabels | object | `{}` | Labels added to the pod. |
| podSecurityContext | object | `{"fsGroup":65532,"runAsGroup":65532,"runAsNonRoot":true,"runAsUser":65532,"seccompProfile":{"type":"RuntimeDefault"}}` | Pod-level securityContext (runs as non-root uid/gid 65532 with the RuntimeDefault seccomp profile). |
| readinessProbe | object | `{"httpGet":{"path":"/readyz","port":"http"},"initialDelaySeconds":5,"periodSeconds":10}` | Readiness probe. |
| replicaCount | int | `1` | Number of konflate replicas (it's stateless behind the Service). |
| resources | object | `{}` | Pod resource requests/limits. A memory limit drives GOMEMLIMIT (set to 90%); a CPU limit right-sizes GOMAXPROCS. |
| secret.existingSecret | string | `""` | Existing Secret holding the KONFLATE_* keys; takes precedence over the inline values below. |
| secret.pushToken | string | `""` | Push token; enables POST /api/prs/{n}/refresh (authenticated mode). |
| secret.token | string | `""` | Forge API token. Empty = anonymous, read-only mode. |
| secret.webhookSecret | string | `""` | Webhook secret; enables POST /hooks (authenticated mode). |
| securityContext | object | `{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]},"readOnlyRootFilesystem":true}` | Container securityContext (no privilege escalation, read-only root filesystem, drops ALL capabilities). |
| service.metricsPort | int | `9090` | Operational /metrics port. |
| service.port | int | `8080` | UI / API / websocket port. |
| service.type | string | `"ClusterIP"` | Service type. |
| serviceAccount.annotations | object | `{}` | Annotations for the ServiceAccount. |
| serviceAccount.automount | bool | `true` | Automount the ServiceAccount API token. |
| serviceAccount.create | bool | `true` | Create a ServiceAccount. |
| serviceAccount.name | string | `""` | ServiceAccount name; generated from the release name if empty. |
| tests.image.digest | string | `"sha256:9532d8c39891ca2ecde4d30d7710e01fb739c87a8b9299685c63704296b16028"` | `helm test` image digest (sha256:…); pins immutably and wins over the tag when set. |
| tests.image.pullPolicy | string | `"IfNotPresent"` | `helm test` image pull policy. |
| tests.image.repository | string | `"docker.io/library/busybox"` | `helm test` pod image; needs a shell with wget (konflate's own image is distroless). |
| tests.image.tag | string | `"1.37.0"` | `helm test` image tag. |
| tolerations | list | `[]` | Tolerations for pod scheduling. |
| volumeMounts | list | `[]` | Additional volume mounts on the container. |
| volumes | list | `[]` | Additional volumes on the Deployment. |

---

_This README is generated by [helm-docs](https://github.com/norwoodj/helm-docs) from `Chart.yaml` and `values.yaml`. Edit those (or `README.md.gotmpl`) and run `mise run generate`._
