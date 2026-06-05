<div align="center">

# konflate

**Review your GitOps pull requests as _rendered_ Flux diffs — not raw file diffs.**

[![Tests](https://img.shields.io/github/actions/workflow/status/home-operations/konflate/tests.yaml?branch=main&label=tests)](https://github.com/home-operations/konflate/actions/workflows/tests.yaml)
[![Lint](https://img.shields.io/github/actions/workflow/status/home-operations/konflate/lint.yaml?branch=main&label=lint)](https://github.com/home-operations/konflate/actions/workflows/lint.yaml)
[![Release](https://img.shields.io/github/actions/workflow/status/home-operations/konflate/release.yaml?branch=main&label=release)](https://github.com/home-operations/konflate/actions/workflows/release.yaml)
[![License](https://img.shields.io/github/license/home-operations/konflate)](https://github.com/home-operations/konflate/blob/main/LICENSE)

</div>

A one-line bump to a Flux resource — a `HelmRelease` chart version, an
`OCIRepository` tag, a `Kustomization` edit — can add, remove, or mutate dozens
of rendered Kubernetes resources. The git diff shows the line; it doesn't show
that. konflate does: it renders the Flux cluster at the PR's **merge-base** and
at its **head** using [flate](https://github.com/home-operations/flate), diffs
the two, and presents the result as a GitHub-style review UI with the blast
radius, image changes, render failures, and heuristic danger flags surfaced up
front.

## How it works

1. konflate lists the open pull requests for one repository from its forge
   (GitHub / GitLab / Forgejo, cloud or self-hosted) using the native Go SDK.
2. For each PR it clones the repo, computes `merge-base(head, target)`, and
   extracts both trees (so changes that landed on the base branch _after_ the PR
   opened don't pollute the diff — exactly how GitHub computes a PR diff).
3. It renders the Flux cluster at both trees with flate (two orchestrators
   sharing one source cache) and pairs the outputs into resource-level changes.
4. It produces a `DiffResult`: per-resource YAML diffs with server-side syntax
   highlighting (with word-level intra-line highlighting and expandable folded
   context), a navigation tree (`HelmRelease`/`Kustomization` → kind →
   resource), plus the review signals — **impact** (blast radius), **image
   changes**, **render failures**, and **danger lint** (data-loss, privilege,
   RBAC, availability).

    The **image changes** signal lists the `container` and `initContainer` image
    references that changed across _every rendered workload_ — so it captures
    whatever the charts and kustomizations actually deploy: app images, sidecars,
    and controller images pulled in by OCI Helm charts alike, each keyed to the
    workloads that reference it. (A chart's own OCI **artifact** version bump
    shows up as a changed `HelmRelease`/`OCIRepository` resource in the diff; its
    effect on the running images surfaces here.)

5. The three-panel web UI renders it — PRs on the left, changed resources in the
   middle, the diff on the right — and updates live over a websocket as renders
   complete. Diff rendering runs in a bounded, per-PR-coalescing job queue.

konflate is **read-only toward your forge**: it never writes comments,
statuses, or checks. PRs refresh automatically — each open PR re-renders on a
configurable interval (the missed-webhook backstop), and an authenticated CI
push or a verified inbound webhook updates one immediately. There is no manual
refresh trigger, so a public instance exposes no unauthenticated way to make it
do work.

## Quick start

```bash
docker run --rm -p 8080:8080 \
  -e KONFLATE_REPO='github://onedr0p/home-ops' \
  -e KONFLATE_TOKEN="$GITHUB_TOKEN" \
  ghcr.io/home-operations/konflate:rolling
```

Open <http://localhost:8080>; konflate lists the open PRs and renders them. The
token is **optional** — without one it works against public repositories, just
with the forge's lower unauthenticated API rate limit (see
[Authentication](#authentication)).

## Helm (Kubernetes)

konflate publishes an **OCI** Helm chart to `oci://ghcr.io/home-operations/charts/konflate`:

```bash
helm install konflate oci://ghcr.io/home-operations/charts/konflate \
  --namespace konflate --create-namespace \
  --set config.repo='github://onedr0p/home-ops' \
  --set secret.token="$GITHUB_TOKEN"
```

Notable values (see [`charts/konflate/values.yaml`](charts/konflate/values.yaml)):

| Value                                            | Purpose                                                                      |
| ------------------------------------------------ | ---------------------------------------------------------------------------- |
| `config.repo` _(required)_                       | the [forge URI](#the-forge-uri) to review                                    |
| `config.refreshInterval`                         | per-PR auto-refresh / re-list interval (default `30m`)                       |
| `secret.token` / `.webhookSecret` / `.pushToken` | sensitive env, written to a Secret (or use `secret.existingSecret`)          |
| `persistence.enabled`                            | keep the flate source cache across restarts (PVC)                            |
| `ingress.enabled`                                | expose the UI via an Ingress                                                 |
| `httpRoute.enabled`                              | expose the UI via a Gateway API `HTTPRoute` (set `parentRefs` + `hostnames`) |
| `monitoring.serviceMonitor.enabled`              | scrape `/metrics` (Prometheus Operator)                                      |

The pod runs read-only-rootfs as nonroot (65532) with the cache + clone dirs on
mounted volumes.

## Configuration

All configuration is via environment variables.

| Variable                    | Default       | Description                                                                                                                                                             |
| --------------------------- | ------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `KONFLATE_REPO`             | _(required)_  | The repository, as a [forge URI](#the-forge-uri), e.g. `github://owner/repo`.                                                                                           |
| `KONFLATE_TOKEN`            | _(none)_      | Forge API token. **Optional** — read-only auth that raises the API rate limit and unlocks private repos. Gates no feature (see [Authentication](#authentication)).      |
| `KONFLATE_BASE_BRANCH`      | `main`        | Default target branch when a PR doesn't specify one.                                                                                                                    |
| `KONFLATE_CLUSTER_PATH`     | _(repo root)_ | Directory flate renders from (the GitRepository root that Flux `spec.path` resolves against). Empty = repo root — correct for the standard `./kubernetes/...` layout.   |
| `KONFLATE_WEBHOOK_SECRET`   | _(none)_      | Secret for verifying inbound webhooks. Set it to enable `POST /hooks`; unset ⇒ `501`.                                                                                   |
| `KONFLATE_PUSH_TOKEN`       | _(none)_      | Bearer token for the CI push endpoint. Set it to enable `POST /api/prs/{n}/refresh`; unset ⇒ `501`.                                                                     |
| `KONFLATE_PORT`             | `8080`        | Main HTTP port (UI, API, websocket, webhook).                                                                                                                           |
| `KONFLATE_METRICS_ADDR`     | `:9090`       | Listen address for the **separate** metrics server. Bind to loopback to keep it private.                                                                                |
| `KONFLATE_LOG_LEVEL`        | `info`        | `debug`, `info`, `warn`, or `error`.                                                                                                                                    |
| `KONFLATE_LOG_FORMAT`       | `json`        | `json` or `text`.                                                                                                                                                       |
| `KONFLATE_CACHE_DIR`        | XDG cache     | flate source cache (Helm charts, OCI layers, git). Persist it across restarts.                                                                                          |
| `KONFLATE_CLONE_DIR`        | `$TMPDIR`     | Base directory for ephemeral per-diff clones (cleaned up after each render).                                                                                            |
| `KONFLATE_MAX_DIFF_CONC`    | _(auto)_      | Max concurrent diff renders. Unset/`0` auto-derives from the CPU budget (GOMAXPROCS, capped at 4); higher = more throughput, more memory.                               |
| `KONFLATE_REFRESH_INTERVAL` | `30m`         | Go duration. Each open PR re-renders if no webhook refreshed it within this window, and the open-PR list is reconciled this often. The missed-webhook backstop.         |
| `KONFLATE_CLOSED_PR_MAX`    | `25`          | Max merged PRs kept on the "recently merged" shelf below the open list (most-recent win). `0` disables the count cap. This count — not the age — is what bounds memory. |
| `KONFLATE_CLOSED_PR_TTL`    | `336h`        | How long a merged PR stays on the shelf before pruning (Go duration, e.g. `720h` = 30d). `0` disables the age cap. In-memory: a restart clears the shelf regardless.    |

Merged PRs move to a collapsed **Recently merged** group below the open list (their diff is frozen at merge time); abandoned (closed-unmerged) PRs are dropped immediately.

## The forge URI

`KONFLATE_REPO` encodes the forge type, the (optional) self-hosted host, and the
repository path in one unambiguous value:

```
scheme://[host]/path
```

- **scheme** — `github`, `gitlab`, or `forgejo`.
- **host** — a self-hosted instance (`host` or `host:port`). Omit entirely for
  the cloud SaaS (github.com / gitlab.com / codeberg.org).
- **path** — `owner/repo`, or `group[/subgroup]/repo` for GitLab.

| Forge URI                               | Resolves to                  |
| --------------------------------------- | ---------------------------- |
| `github://onedr0p/home-ops`             | GitHub cloud                 |
| `github://ghe.example.com/team/cluster` | GitHub Enterprise Server     |
| `gitlab://group/subgroup/cluster`       | GitLab cloud (gitlab.com)    |
| `gitlab://gl.example.com/group/cluster` | self-hosted GitLab           |
| `forgejo://me/home-ops`                 | Forgejo cloud (codeberg.org) |
| `forgejo://git.example.com/me/home-ops` | self-hosted Forgejo          |

## Authentication

The forge token (`KONFLATE_TOKEN`) is **optional** and used only for forge read
auth — it raises the API rate limit and unlocks private repositories. It gates
no behaviour: konflate works the same with or without it.

The inbound endpoints are gated solely by **their own secret**, independent of
the token:

| Endpoint                    | Enabled when…                 | Otherwise |
| --------------------------- | ----------------------------- | --------- |
| `POST /hooks`               | `KONFLATE_WEBHOOK_SECRET` set | `501`     |
| `POST /api/prs/{n}/refresh` | `KONFLATE_PUSH_TOKEN` set     | `501`     |

So a public, secret-less instance — even one pointed at a repo you don't own —
exposes no way to make it do work: there is no manual-refresh endpoint, and the
webhook/push endpoints return `501` until you set their secret. PRs still stay
current via the per-PR auto-refresh (see [Triggering
re-renders](#triggering-re-renders)).

## HTTP endpoints

Main server (`KONFLATE_PORT`):

| Method & path                 | Purpose                                                                                    |
| ----------------------------- | ------------------------------------------------------------------------------------------ |
| `GET /`                       | The web UI.                                                                                |
| `GET /api/prs`                | Tracked PRs and each one's diff-job status.                                                |
| `GET /api/prs/{n}/diff`       | A PR's rendered diff (`200` ready/error, `202` still rendering).                           |
| `POST /api/prs/{n}/refresh`   | **Auth** (bearer `KONFLATE_PUSH_TOKEN`) — re-render one PR. `501` unless the token is set. |
| `POST /hooks`                 | Verified forge webhook — re-renders the affected PR. `501` unless the secret is set.       |
| `GET /ws`                     | Websocket stream of diff-job status events.                                                |
| `GET /healthz`, `GET /readyz` | Liveness / readiness.                                                                      |

Operational server (`KONFLATE_METRICS_ADDR`): `GET /metrics`.

## Triggering re-renders

konflate lists and renders PRs at startup; after that it keeps them current
itself, with two optional triggers for immediacy:

**Automatically (always on)** — every open PR re-renders once its last render is
older than `KONFLATE_REFRESH_INTERVAL` (default 30m), and the open-PR list is
reconciled on the same interval to pick up newly opened and merged PRs. This is
the missed-webhook backstop and needs no configuration. (Merged PRs are frozen
and never auto-refresh.) A webhook or push refreshing a PR resets its clock, so
a busy PR isn't needlessly re-rendered and load staggers across PRs.

**From a CI workflow** (`KONFLATE_PUSH_TOKEN` set) — re-render a PR immediately
after you push to it:

```bash
curl -fsS -X POST \
  -H "Authorization: Bearer ${KONFLATE_PUSH_TOKEN}" \
  https://konflate.example.com/api/prs/${PR_NUMBER}/refresh
```

**Native webhooks** (authenticated mode, `KONFLATE_WEBHOOK_SECRET` set) — point a
forge webhook at `https://konflate.example.com/hooks` with the shared secret.
konflate verifies the signature with the per-forge scheme automatically:

| Forge   | Header                | Verification                        |
| ------- | --------------------- | ----------------------------------- |
| GitHub  | `X-Hub-Signature-256` | HMAC-SHA256, `sha256=` + hex        |
| Forgejo | `X-Gitea-Signature`   | HMAC-SHA256, bare hex               |
| GitLab  | `X-Gitlab-Token`      | constant-time compare of the secret |

Rate limiting is intentionally **not** built in — put konflate behind your
reverse proxy / ingress and rate-limit there.

## Metrics

Served on the separate operational port (keep it off your public ingress):

| Metric                           | Type      | Meaning                                |
| -------------------------------- | --------- | -------------------------------------- |
| `konflate_diff_jobs_total`       | counter   | Completed renders, by `result`.        |
| `konflate_diff_duration_seconds` | histogram | Render wall-clock (clone + 2 renders). |
| `konflate_diff_queue_depth`      | gauge     | PRs queued or rendering.               |
| `konflate_pull_requests`         | gauge     | Open PRs tracked.                      |
| `konflate_http_requests_total`   | counter   | Main-server requests, by status class. |

Plus the standard Go runtime and process collectors.

## Development

[mise](https://mise.jdx.dev) is the single source of truth for the toolchain —
both the `go` and `node` versions are pinned in `.mise/config.toml`, shared with
`go.mod` and the container build, and grouped (non-automerged) in Renovate — and
it is the task runner. The UI is [Svelte 5](https://svelte.dev) + Vite +
Tailwind v4 (all latest), built into `internal/web/dist` and embedded via
`go:embed`. All UI dependencies are declared in `internal/web/package.json`.

```bash
mise run ui-install     # install UI deps (npm ci)
mise run ui-typecheck   # svelte-check
mise run ui-build       # build the UI bundles into internal/web/dist
mise run ui-test        # Playwright headless-Chromium UI tests
mise run build          # go build ./...
mise run test           # unit + server tests (race-enabled in CI)
mise run lint           # golangci-lint
mise run dev            # run konflate locally (set KONFLATE_REPO first)
```

Tests come in three tiers:

- **Unit** — pure logic (config, diff render/lint/impact, engine pairing,
  webhook crypto, provider mapping) plus the HTTP server and the websocket hub
  driven over real sockets with a fake engine. Run by `mise run test`.
- **UI** (`mise run ui-test`) — Playwright drives the real built UI in headless
  Chromium with the API and websocket stubbed by a fixture, asserting the
  3-panel render, filtering, and split view. Runs in CI.
- **Integration** (`-tags integration`, env-gated) — renders a real PR with the
  real engine; skips unless `KONFLATE_REPO` + `KONFLATE_INTEGRATION_PR` are set:

    ```bash
    KONFLATE_REPO=github://owner/repo KONFLATE_INTEGRATION_PR=123 \
      mise run test-integration
    ```

## Security

konflate is designed to be safe to expose internally, and to leak nothing even
if it were public:

- **Read-only toward forges.** It never writes comments, statuses, checks, or
  any other forge state.
- **No secret leakage.** Renders run with flate's missing-secrets allowance, so
  Kubernetes `Secret` values are never materialized; no API type or log line
  carries the forge token.
- **XSS-safe rendering.** Only chroma-produced, HTML-escaped token spans are
  inserted as markup; every other value is set as text. A strict
  `Content-Security-Policy` (`script-src 'self'`) blocks injected inline scripts
  as a backstop.
- **No unauthenticated trigger surface.** There is no manual-refresh endpoint,
  and the webhook/push endpoints return `501` until their secret is set. See
  [Authentication](#authentication).
- **Constant-time** comparison for the push token and the GitLab webhook token.

## License

See [LICENSE](LICENSE).
