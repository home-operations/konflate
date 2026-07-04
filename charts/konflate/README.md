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

## Requirements

Kubernetes: `>=1.25.0-0`

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| affinity | object | `{}` | Affinity rules for pod scheduling. |
| config.appClientId | string | `""` | GitHub App client id (**GitHub only**) — the preferred write identity. With `secret.appPrivateKey`, konflate mints short-lived installation tokens instead of carrying a standing PAT; the installation is auto-detected from the repo. |
| config.cacheTtl | string | `""` | Advanced: prune source-cache entries unused longer than this (Go duration); bare git mirrors are kept. Empty = default (168h/7d); "0" disables the sweep. |
| config.closedPrMax | int | `25` | Cap on retained merged PRs (most-recent win); bounds disk + memory. 0 disables the cap (with `closedPrTtl: "0"`, merged diffs are kept forever). |
| config.closedPrTtl | string | `"336h"` | How long a merged PR is kept (Go duration); 0 disables the age cap. |
| config.clusterPath | string | `""` | Directory flate renders from; empty = repo root (correct for the standard root-relative layout). |
| config.diffTimeout | string | `""` | Hard cap on a single PR render end-to-end (Go duration). Empty = default (10m); "0" disables. Lower on untrusted instances. |
| config.extraEnv | list | `[]` | Extra raw env vars merged into the container (advanced). |
| config.fetchTimeout | string | `""` | Advanced: cap on just the git fetch within a render (Go duration); a short bound stops one slow forge fetch from stalling every render. Empty = default (2m); "0" disables. |
| config.helmRenderCacheMb | string | `""` | Advanced: persistent on-disk Helm render cache in MiB, reused across renders/PRs/restarts. Empty = default (1024); "0" disables. |
| config.helmTemplateCacheMb | string | `""` | Advanced: in-memory Helm template cache in MiB (the biggest CPU saver). Empty = auto (256 ÷ render concurrency, so it doesn't scale with the CPU limit); "0" disables. |
| config.imageVerifyTimeout | string | `""` | Timeout for a single registry existence check (see `verifyImages`). Empty = default (5s). |
| config.logFormat | string | `"json"` | Log format: json or text. |
| config.logLevel | string | `"info"` | Log level: debug, info, warn, or error. |
| config.maxDiffConcurrency | int | `0` | Max concurrent diff renders; 0 = auto (from the CPU limit, capped at 4). |
| config.maxDiffResources | string | `""` | Cap on resources fully rendered per diff (each carries highlighted rows — the main payload cost); a larger PR is truncated and flagged in the UI, while the impact banner still shows the true total. Empty = default (500); "0" disables. |
| config.mcp | bool | `false` | Serve a read-only [Model Context Protocol](https://modelcontextprotocol.io) endpoint at `/mcp` so an AI agent can query konflate's rendered-diff analysis (open PRs + their summaries). Read-only — the same data as the API, triggering no render or write. Off by default (it's a new surface); secure it at your ingress if you expose it. |
| config.mergeCommand | string | `""` | Optional Go text/template for the copy-to-merge command (only .Number / .Repo). Empty = forge default (gh/glab/tea). konflate never runs it. |
| config.prCommentTemplate | string | `""` | Optional Go text/template for the PR-comment body, replacing the built-in summary. When set, the chart mounts it (a ConfigMap) and points `KONFLATE_PR_COMMENT_TEMPLATE_FILE` at it. The konflate marker is injected automatically. Context: `.PR`, `.Diff`, `.ReviewURL`, `.Summary` (the default body). Passed verbatim (NOT tpl'd) — it's konflate's own template. See the README. |
| config.prComments | bool | `false` | Opt-in: post (and update in place) a PR comment with the rendered summary on each successful render. Needs a write credential and stays off until both are set; independent of `statusChecks`. |
| config.prFilterExpr | string | `""` | CEL expression deciding which PRs konflate renders; must return a bool. Empty = `true` (every open PR). Forks are gated separately by `renderForkPrs`, so editing this can't enable forks. A PR it excludes is listed but hidden. e.g. `pr.labels.exists(l, l.name == "cluster/production") && !pr.draft`. See the README. |
| config.publicUrl | string | `""` | konflate's externally-reachable base URL (e.g. https://konflate.example.com); a posted status/comment links back to it. Empty = posted without a link. |
| config.refreshInterval | string | `"30m"` | How often each open PR re-renders / the PR list reconciles, as a missed-webhook backstop (Go duration). "0" disables periodic refresh (webhooks only); positive values are floored to 1m. |
| config.renderConcurrency | string | `""` | Advanced: cap on reconcile goroutines within one render. Empty/"0" = auto (NumCPU*4). |
| config.renderForkPrs | bool | `false` | Render fork PRs. ⚠️ A fork runs untrusted external code through flate (SSRF / resource exhaustion). Off by default — forks are listed but hidden until you flip this. Kept separate from `prFilterExpr` so the filter can't accidentally enable them. |
| config.repo | required | `""` | Forge URI of the repository to review (github://owner/repo, gitlab://group/repo, forgejo://host/owner/repo). |
| config.rerenderInterval | string | `""` | Advanced: how often to re-render an open PR whose head is unchanged, to catch mutable-source drift (Go duration). Empty = default (6h); "0" falls back to refreshInterval. Raise it for fully digest-pinned repos. |
| config.restrictEgress | string | `""` | Override flate's SSRF egress guard on source fetches (it blocks dials to private / loopback / link-local / cloud-metadata addresses and rejects non-https/ssh git schemes). Empty = follow `renderForkPrs` (on while rendering untrusted forks, off otherwise). `"true"` guards every render; `"false"` permits private-network sources (e.g. an internal Gitea / OCI registry) even while rendering forks. |
| config.sourceRetryAttempts | string | `""` | Advanced: tries per source fetch on transient network errors. Empty = default (3); "1" disables retry. |
| config.statusCheckName | string | `""` | Name the commit status konflate posts under (the required-check name in branch-protection rules). Empty uses the default, "Konflate". |
| config.statusChecks | bool | `false` | Opt-in: post a commit status on each rendered PR head. Needs a write credential (`secret.writeToken`, or the GitHub App) and stays off until both are set. |
| config.verifyImages | bool | `false` | Verify that each container image a PR newly references exists in its registry (a HEAD on the tag/digest), raising a caution for any that is absent — catching a typo'd or not-yet-pushed image before it ImagePullBackOffs in-cluster. Off by default. Only trusted (non-fork) PRs are checked: a fork's images are attacker-chosen (an SSRF vector). For private registries, mount a dockerconfigjson and point `DOCKER_CONFIG` at it via `config.extraEnv` + `volumes`/`volumeMounts` (go-containerregistry's keychain, host-scoped); konflate never reads a manifest's `imagePullSecrets`. |
| deploymentAnnotations | object | `{}` | Annotations added to the Deployment object (e.g. `reloader.stakater.com/auto: "true"` to roll the pod when the mounted Secret / PR-comment-template ConfigMap changes). Pod-level annotations go in `podAnnotations`. |
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
| livenessProbe | object | `{"httpGet":{"path":"/healthz","port":"http"},"initialDelaySeconds":10,"periodSeconds":20}` | Liveness probe. Targets /healthz on the main http port, so it works regardless of the metrics toggle. |
| monitoring.serviceMonitor.annotations | object | `{}` | ServiceMonitor annotations. |
| monitoring.serviceMonitor.enabled | bool | `false` | Create a Prometheus Operator ServiceMonitor (requires its CRDs). |
| monitoring.serviceMonitor.interval | string | `"30s"` | Scrape interval. |
| monitoring.serviceMonitor.labels | object | `{}` | ServiceMonitor labels. |
| monitoring.serviceMonitor.metricRelabelings | list | `[]` | Prometheus metric relabelings. |
| monitoring.serviceMonitor.path | string | `"/metrics"` | Metrics path. |
| monitoring.serviceMonitor.relabelings | list | `[]` | Prometheus relabelings. |
| monitoring.serviceMonitor.scrapeTimeout | string | `"10s"` | Scrape timeout. |
| nameOverride | string | `""` | Override the chart name used in resource names. |
| networkPolicy.allowDNS | bool | `true` | Allow DNS egress (UDP/TCP 53). konflate must resolve forge/registry/git hosts to render, so leave this on unless DNS is handled out-of-band. |
| networkPolicy.egressPorts | list | `[443]` | TCP ports konflate may egress to fetch sources during a render (forge API + git/OCI/Helm over HTTPS). Add 80 / 22 for plain-HTTP or SSH git sources. |
| networkPolicy.enabled | bool | `false` | Create a NetworkPolicy for konflate. |
| networkPolicy.type | string | `"default"` | Policy flavor for your CNI: "default" (networking.k8s.io/v1 NetworkPolicy), "cilium" (CiliumNetworkPolicy), or "calico" (projectcalico.org/v3 NetworkPolicy). |
| nodeSelector | object | `{}` | Node selector for pod scheduling. |
| persistence.accessModes | list | `["ReadWriteOnce"]` | PVC access modes. |
| persistence.annotations | object | `{}` | PVC annotations. |
| persistence.enabled | bool | `false` | Persist caches, the git mirror, and rendered diffs across restarts: open PRs reload instantly and the merged shelf survives. Recommended for any non-trivial repo. |
| persistence.existingClaim | string | `""` | Use an existing PVC instead of creating one. |
| persistence.size | string | `"5Gi"` | PVC size. |
| persistence.storageClass | string | `""` | StorageClass for the created PVC. |
| podAnnotations | object | `{}` | Annotations added to the pod. |
| podDisruptionBudget.enabled | bool | `false` | Create a PodDisruptionBudget. ⚠️ With a single replica, the default `minAvailable: 1` makes a node drain block until you delete the pod yourself; set `maxUnavailable: 1` instead to let drains proceed. Off by default. |
| podDisruptionBudget.maxUnavailable | string | `""` | Maximum pods that may be unavailable, as a count or percentage; takes precedence over `minAvailable` when set. @schema type: [integer, string] @schema |
| podDisruptionBudget.minAvailable | int | `1` | Minimum pods that must stay available, as a count or percentage. Used unless `maxUnavailable` is set. @schema type: [integer, string] @schema |
| podLabels | object | `{}` | Labels added to the pod. |
| podSecurityContext | object | `{"fsGroup":65532,"fsGroupChangePolicy":"OnRootMismatch","runAsGroup":65532,"runAsNonRoot":true,"runAsUser":65532,"seccompProfile":{"type":"RuntimeDefault"}}` | Pod-level securityContext (runs as non-root uid/gid 65532 with the RuntimeDefault seccomp profile). |
| priorityClassName | string | `""` | PriorityClass for the pod, so konflate is less likely to be preempted/evicted under node pressure. Empty uses the cluster default. |
| readinessProbe | object | `{"httpGet":{"path":"/readyz","port":"http"},"initialDelaySeconds":5,"periodSeconds":10}` | Readiness probe. Targets /readyz on the main http port. |
| replicaCount | int | `1` | Replica count; konflate is single-instance, so 0 or 1 only (a value >1 is rejected at render time). |
| resources | object | `{"limits":{"memory":"1Gi"},"requests":{"cpu":"50m","memory":"256Mi"}}` | Pod resource requests/limits. The memory limit is the hard ceiling: it drives GOMEMLIMIT (90%) so the GC reclaims before the kernel OOM-kills a runaway render. Default bounds memory out of the box; raise it for very large clusters. |
| secret.appPrivateKey | string | `""` | GitHub App PEM private key (**GitHub only**); the preferred GitHub write credential, used with `config.appClientId`. |
| secret.existingSecret | string | `""` | Existing Secret holding the KONFLATE_* keys; takes precedence over the inline values below. |
| secret.pushToken | string | `""` | Push token; enables POST /api/prs/{n}/refresh (authenticated mode). |
| secret.token | string | `""` | Forge API token. Empty = anonymous, read-only mode. |
| secret.webhookSecret | string | `""` | Webhook secret; enables POST /hooks (authenticated mode). |
| secret.writeToken | string | `""` | Write credential for status write-back (commit statuses), separate from `token` so it carries only write scope. The universal option and the only one on GitLab/Forgejo. Needs `config.statusChecks: true` to take effect. |
| securityContext | object | `{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]},"readOnlyRootFilesystem":true}` | Container securityContext (no privilege escalation, read-only root filesystem, drops ALL capabilities). |
| service.metricsEnabled | bool | `true` | Expose Prometheus metrics at /metrics on metricsPort (KONFLATE_METRICS_ENABLED). Disabling removes the metrics listener, container port, and Service port entirely; health probes are unaffected (they target the http port). |
| service.metricsPort | int | `8081` | Metrics listen port (/metrics only), kept off the public port. |
| service.port | int | `8080` | UI / API / websocket port; also serves the /healthz and /readyz probes. |
| service.type | string | `"ClusterIP"` | Service type. |
| serviceAccount.annotations | object | `{}` | Annotations for the ServiceAccount. |
| serviceAccount.automount | bool | `false` | Automount the ServiceAccount API token (off by default: konflate talks to forges, not the cluster API). |
| serviceAccount.create | bool | `true` | Create a ServiceAccount. |
| serviceAccount.name | string | `""` | ServiceAccount name; generated from the release name if empty. |
| startupProbe | object | `{}` | Startup probe (optional). Gates liveness/readiness while konflate starts — useful when a large persisted store (see `persistence`) makes the cold start slow, since it loads before serving. Empty disables it. |
| strategy | object | `{"type":"Recreate"}` | Deployment update strategy. **Keep `Recreate`** — konflate is a single instance with in-memory PR/diff state and a ReadWriteOnce cache PVC, so a RollingUpdate (which surges a second pod even at replicaCount 1) diverges the in-memory stores and/or wedges on the volume's Multi-Attach error. Exposed only for parity with the other charts; `RollingUpdate` is unsupported here. |
| terminationGracePeriodSeconds | int | `30` | Grace period for a clean shutdown. konflate drains in-flight renders and closes the HTTP/websocket servers on SIGTERM (it caps its own shutdown near 15s), so the default is ample; raise it only if you expect longer drains. |
| tests.image.pullPolicy | string | `"IfNotPresent"` | `helm test` image pull policy. |
| tests.image.repository | string | `"mirror.gcr.io/curlimages/curl"` | `helm test` connection-pod image; a gcr-mirrored curl, so the test never pulls from Docker Hub. |
| tests.image.tag | string | `"8.21.0@sha256:7c12af72ceb38b7432ab85e1a265cff6ae58e06f95539d539b654f2cfa64bb13"` | `helm test` image, pinned as `tag@sha256:digest` so Renovate bumps the tag and its digest together. |
| tolerations | list | `[]` | Tolerations for pod scheduling. |
| volumeMounts | list | `[]` | Additional volume mounts on the container. |
| volumes | list | `[]` | Additional volumes on the Deployment. |

---

_This README is generated by [helm-docs](https://github.com/norwoodj/helm-docs) from `Chart.yaml` and `values.yaml`. Edit those (or `README.md.gotmpl`) and run `mise run generate`._
