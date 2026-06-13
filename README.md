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
   RBAC, availability, and immutable-field changes — a StatefulSet
   `volumeClaimTemplates` bump, a workload selector edit, a PVC storage-class
   swap or shrink, a `roleRef` change — that read like ordinary diffs but
   wedge the apply with "field is immutable" until the resource is recreated).
   The lint also reasons about what **Flux will actually do** on merge:
   changes under a suspended Kustomization/HelmRelease (they won't roll out),
   a PR flipping `spec.suspend` (resuming applies everything accumulated while
   parked, at once), and removal semantics under `prune` — a pruning
   Kustomization really deletes the resource in-cluster, a non-pruning one
   orphans it, silently left running.

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

By default konflate is **read-only toward your forge** — it writes nothing back.
[Write-back](#write-back) is **opt-in**: turn it on and konflate posts a commit
status and/or a summary comment on each rendered PR, linking back to the review.
Even then its **HTTP surface stays read-only** — writes come only from konflate's
own render loop, using a credential held by the process, never from a visitor's
request — so a public instance still exposes no way for anyone to make it write.

PRs refresh automatically — each open PR re-renders on a configurable interval
(the missed-webhook backstop), and an authenticated CI push or a verified
inbound webhook updates one immediately. There is no manual refresh trigger, so
a public instance exposes no unauthenticated way to make it do work.

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

Every value is documented in the chart's generated README,
[`charts/konflate/README.md`](charts/konflate/README.md), built from
[`values.yaml`](charts/konflate/values.yaml) — which also ships a
[`values.schema.json`](charts/konflate/values.schema.json) for editor
autocompletion and `helm install`-time validation. The `config.*` and `secret.*`
chart values map onto the `KONFLATE_*` environment variables in
[Configuration](#configuration) below.

## Configuration

All configuration is via environment variables.

| Variable                            | Default       | Description                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             |
| ----------------------------------- | ------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `KONFLATE_REPO`                     | _(required)_  | The repository, as a [forge URI](#the-forge-uri), e.g. `github://owner/repo`.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           |
| `KONFLATE_TOKEN`                    | _(none)_      | Forge API token. **Optional** — read-only auth that raises the API rate limit and unlocks private repos. Gates no feature (see [Authentication](#authentication)).                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      |
| `KONFLATE_CLUSTER_PATH`             | _(repo root)_ | Directory flate renders from (the GitRepository root that Flux `spec.path` resolves against). Empty = repo root — correct for the standard `./kubernetes/...` layout.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| `KONFLATE_PR_FILTER_EXPR`           | `true`        | [CEL](https://cel.dev) expression deciding which PRs konflate **renders** (and shows by default). PRs it excludes are still listed — greyed, under a "hidden" pill — but never rendered. Evaluates against a `pr` variable (fields: `number`, `title`, `author`, `draft`, `open`, `merged`, `state`, `fork`, `headRef`, `headSha`, `baseRef`, `url`, `createdAt`, and `labels` as `[{name, color}]`) and must return a boolean; compiled and type-checked at startup. **Empty defaults to `true`** — every open PR. Forks are gated separately by `KONFLATE_RENDER_FORK_PRS`, so editing this can't accidentally enable them — see [Filtering & forks](#filtering--forks). Example: `pr.labels.exists(l, l.name == "cluster/production") && !pr.draft`. |
| `KONFLATE_RENDER_FORK_PRS`          | `false`       | Render fork PRs. ⚠️ A fork runs untrusted external code through flate (SSRF / resource-exhaustion surface). Off by default — forks are listed but **hidden** (never rendered) until this is `true` **and** the filter admits them. Kept separate from the filter so editing the expression can't silently enable forks.                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| `KONFLATE_WEBHOOK_SECRET`           | _(none)_      | Secret for verifying inbound webhooks. Set it to enable `POST /hooks`; unset ⇒ `501`.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| `KONFLATE_PUSH_TOKEN`               | _(none)_      | Bearer token for the CI push endpoint. Set it to enable `POST /api/prs/{n}/refresh`; unset ⇒ `501`.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     |
| `KONFLATE_STATUS_CHECKS`            | `false`       | Opt-in: post a commit status on each rendered PR head. Needs a write credential (below) and stays off until both are set. See [Write-back](#write-back).                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                |
| `KONFLATE_STATUS_CHECK_NAME`        | `Konflate`    | Name the commit status konflate posts under (the required-check name in a branch-protection rule). Empty defaults to `Konflate`. See [Write-back](#write-back).                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         |
| `KONFLATE_PR_COMMENTS`              | `false`       | Opt-in: post (and update in place) a PR comment with the rendered summary on each successful render. Needs a write credential (below) and stays off until both are set; independent of `KONFLATE_STATUS_CHECKS`. See [Write-back](#write-back).                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         |
| `KONFLATE_PR_COMMENT_TEMPLATE_FILE` | _(none)_      | Path to a Go `text/template` rendering the PR-comment body, replacing the built-in summary. The konflate marker is injected automatically. Values: `.PR`, `.Diff`, `.ReviewURL`, `.Summary`. Empty = the default body. With the Helm chart, set `config.prCommentTemplate` (the content) instead. See [Write-back](#write-back).                                                                                                                                                                                                                                                                                                                                                                                                                        |
| `KONFLATE_WRITE_TOKEN`              | _(none)_      | Write credential for write-back, kept separate from `KONFLATE_TOKEN` so it carries only write scope. The universal option (and the only one on GitLab/Forgejo); on GitHub, prefer the App credentials below. See [Write-back](#write-back).                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             |
| `KONFLATE_APP_CLIENT_ID`            | _(none)_      | GitHub App client id (**GitHub only**) — the preferred identity, authenticating both reads (raising the API rate limit, so no separate `KONFLATE_TOKEN` is needed) and write-back. With the key below, konflate mints short-lived installation tokens instead of carrying a standing PAT; the installation is auto-detected from the repo.                                                                                                                                                                                                                                                                                                                                                                                                              |
| `KONFLATE_APP_PRIVATE_KEY`          | _(none)_      | GitHub App PEM private key (**GitHub only**).                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           |
| `KONFLATE_PUBLIC_URL`               | _(none)_      | konflate's externally-reachable base URL, e.g. `https://konflate.example.com`. Used only to build the review link a posted status/comment points back to; unset ⇒ posted with no link.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| `KONFLATE_PORT`                     | `8080`        | Main HTTP port (UI, API, websocket, webhook).                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           |
| `KONFLATE_METRICS_ADDR`             | `:9090`       | Listen address for the **separate** metrics server. Bind to loopback to keep it private.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                |
| `KONFLATE_LOG_LEVEL`                | `info`        | `debug`, `info`, `warn`, or `error`.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    |
| `KONFLATE_LOG_FORMAT`               | `json`        | `json` or `text`.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       |
| `KONFLATE_CACHE_DIR`                | XDG cache     | flate source cache (Helm charts, OCI layers, git) **and** konflate's rendered diffs (a `state/` subdir). Persist it across restarts so open PRs reload instantly and the merged shelf survives.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         |
| `KONFLATE_CLONE_DIR`                | `$TMPDIR`     | Base directory for ephemeral per-diff clones (cleaned up after each render).                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                            |
| `KONFLATE_MAX_DIFF_CONC`            | _(auto)_      | Max concurrent diff renders. Unset/`0` auto-derives from the CPU budget (GOMAXPROCS, capped at 4); higher = more throughput, more memory.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                               |
| `KONFLATE_REFRESH_INTERVAL`         | `30m`         | Go duration. Each open PR re-renders if no webhook refreshed it within this window, and the open-PR list is reconciled this often — the missed-webhook backstop. `0` disables periodic refresh entirely (inbound webhooks/pushes become the only triggers); a positive value is floored to `1m` so a tiny interval can't hot-loop the forge API.                                                                                                                                                                                                                                                                                                                                                                                                        |
| `KONFLATE_CLOSED_PR_MAX`            | `25`          | Max merged PRs kept on the "recently merged" shelf below the open list (most-recent win). `0` disables the count cap. Each retained PR holds its rendered diff, so this bounds disk + memory; with `KONFLATE_CLOSED_PR_TTL=0` too (and a persistent cache volume) merged diffs are kept forever — durable permalinks.                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| `KONFLATE_CLOSED_PR_TTL`            | `336h`        | How long a merged PR stays on the shelf before pruning (Go duration, e.g. `720h` = 30d). `0` disables the age cap. The shelf is persisted under `KONFLATE_CACHE_DIR`, so it survives a restart when that volume is durable.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             |
| `KONFLATE_MERGE_COMMAND`            | _(per forge)_ | Go `text/template` for the **Copy to merge** command shown on the review screen and PR list (konflate never runs it — you paste it into your own shell). Empty = the forge default (`gh`/`glab`/`tea`). Only `.Number` and `.Repo` are exposed, both shell-safe.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        |

Merged PRs move to a collapsed **Recently merged** group below the open list (their diff is frozen at merge time); abandoned (closed-unmerged) PRs are dropped immediately.

### Filtering & forks

konflate decides what to render with **two independent gates, AND-ed together**:

1. **`KONFLATE_PR_FILTER_EXPR`** — a [CEL](https://cel.dev) boolean over the `pr` fields above (compiled and type-checked at startup; a malformed expression fails fast) that says _which_ PRs to render. Default `true` (every open PR).
2. **`KONFLATE_RENDER_FORK_PRS`** — a plain on/off switch for forks, **off by default**.

A PR renders only if the expression admits it **and** (it isn't a fork, or fork rendering is on). Anything excluded by either gate is still tracked and listed — greyed, under a **"hidden"** pill, out of the default view — but **never rendered**, so its code never runs.

Forks are off by default because rendering one runs untrusted external code through flate — it fetches whatever sources the fork declares (SSRF, resource exhaustion). Crucially the fork gate is **separate from the expression**, so narrowing the filter for unrelated reasons can never accidentally enable forks. Narrow what renders with the expression:

```bash
# only PRs labelled for the production cluster, excluding drafts
KONFLATE_PR_FILTER_EXPR='pr.labels.exists(l, l.name == "cluster/production") && !pr.draft'
# everything except Renovate's PRs, on the main branch
KONFLATE_PR_FILTER_EXPR='pr.author != "renovate[bot]" && pr.baseRef == "main"'
```

**To render fork PRs**, flip the gate on — and ideally still scope which ones with the expression:

```bash
# ⚠️ renders untrusted external code from every fork the filter admits
KONFLATE_RENDER_FORK_PRS=true
# forks only from one trusted contributor (gate on + an author filter)
KONFLATE_RENDER_FORK_PRS=true
KONFLATE_PR_FILTER_EXPR='!pr.fork || pr.author == "trusted-contributor"'
```

Keep the fork gate off on any public or shared instance.

### Multi-cluster monorepos

A konflate instance tracks **one repository and renders one cluster** — the Flux
entry point at `KONFLATE_CLUSTER_PATH` (the repo root by default). It has no
built-in notion of several clusters living in one repo.

So for a monorepo that holds more than one cluster, run **one konflate per
cluster** and scope each with the PR filter (and, for a folder-per-cluster
layout, its cluster path). The usual convention is a per-cluster PR label:

```bash
# the production instance
KONFLATE_CLUSTER_PATH='kubernetes/clusters/production'   # render this cluster (folder-per-cluster)
KONFLATE_PR_FILTER_EXPR='pr.labels.exists(l, l.name == "cluster/production")'
```

```bash
# the staging instance
KONFLATE_CLUSTER_PATH='kubernetes/clusters/staging'
KONFLATE_PR_FILTER_EXPR='pr.labels.exists(l, l.name == "cluster/staging")'
```

The filter is what keeps each instance's list to its own cluster — without it
every instance would list every PR and render an empty diff for the clusters a
PR doesn't touch. (Branch-per-cluster instead? Filter on the target branch, e.g.
`pr.baseRef == "production"`.) With the Helm chart these are `config.clusterPath`
and `config.prFilterExpr` — one release per cluster.

Give each instance its **own `KONFLATE_CACHE_DIR`** — don't point two at the same
volume. Even for the same repo, konflate guards the bare mirror and the persisted
diff state with in-process locks only, so two processes sharing them would race
(the same reason konflate runs as a single instance). A separate PVC per release,
or a distinct `subPath` of one, keeps them isolated.

First-class multi-cluster support — one instance spanning a folder- or
branch-per-cluster monorepo — is tracked in
[#54](https://github.com/home-operations/konflate/issues/54).

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
auth — it authenticates both the API calls and the renderer's `git` clone/fetch,
raising the API rate limit and unlocking private repositories. It gates no
behaviour: konflate works the same with or without it.

On GitHub, configuring a **GitHub App** (`KONFLATE_APP_CLIENT_ID` +
`KONFLATE_APP_PRIVATE_KEY`) authenticates reads too — its installation token is
konflate's forge identity for the API, the renderer's `git` clone/fetch, and
[write-back](#write-back) alike (minted fresh, never a standing PAT) — so an
App-only instance clones private repos and lifts the rate limit with no separate
`KONFLATE_TOKEN`.

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

## Write-back

By default konflate writes nothing to your forge. Enable write-back and it
reports its own result back to the PR — as a **commit status**, a **PR comment**,
or both. Each is independently opt-in and off by default.

A **commit status** is a single check named `Konflate` (configurable via `KONFLATE_STATUS_CHECK_NAME`) on the PR's head commit:
`success` when the diff rendered (summarising the resource / caution / failure
counts), `failure` when it didn't, linking to the konflate review. It's the same
verdict the [summary endpoint](#http-endpoints)'s `X-Konflate-Render-Status`
header carries — now posted by konflate itself instead of by a CI step that polls.

A **PR comment** carries the rendered summary (the same Markdown the summary
endpoint serves). konflate posts it on a successful render and edits that one
comment in place on every later render — keyed by a hidden marker, so it never
piles up duplicates. Render failures stay on the commit status, so konflate won't
open a "render failed" comment on a PR it can't render.

Write-back is **off until you opt in** — it needs a write credential **and** at
least one of the feature toggles:

- `KONFLATE_STATUS_CHECKS=true` — report the render verdict as a status check.
- `KONFLATE_PR_COMMENTS=true` — post (and update in place) the summary comment.
- a write credential — a write token, or GitHub App credentials (below).

Set `KONFLATE_PUBLIC_URL` as well so the status and comment link back to the
review; without it they're still posted, just without a link.

With `KONFLATE_STATUS_CHECKS` on, a **GitHub App** that holds the **Checks**
permission posts a **Check Run** — a pass / neutral / fail conclusion plus the
rendered summary (cautions, render failures, blast radius, image changes) in the
PR's Checks tab, which you can require as a merge gate (cautions are a
non-blocking _neutral_). A write PAT, GitLab, Forgejo, or an App without
`checks:write` posts a plain commit status instead; konflate detects the missing
permission and falls back automatically, so granting `checks:write` upgrades an
existing App with no other change. The check is upserted per head commit, so a
re-render refreshes one check rather than stacking duplicates.

Write-back is **best-effort**: each write runs off the render path, a transient
forge failure (a 5xx, a blip, a brief outage) is retried a few times with
backoff, and any write that still fails is re-attempted on the PR's next render —
konflate logs it but never blocks or fails a render on it. Both writes are
idempotent (the status is overwritten; the comment is found by its marker and
edited), so a retry can't double-post.

konflate **checks the write credential once at startup**. A permanent rejection
(a 401/403/404 — a bad token, missing permission, or a wrong GitHub App
installation / unreachable repo) disables write-back with a single clear log line
rather than warning on every render; a transient failure leaves it enabled to
recover on a later render.

**This does not make konflate writable from the outside.** Its HTTP surface
stays read-only — no request, authenticated or not, can trigger a write. The
status and comment are posted only by konflate's own render loop, using a
credential the process holds. The change is operational, not in the request
surface: a standing write credential now lives in the deployment, so scope it
narrowly and treat a konflate compromise as able to write commit statuses and PR
comments on that repo.

### Credentials

Scopes depend on which write-back you enable: commit statuses and PR comments are
governed by different permissions.

| Forge           | Credential                                                             | Scope                                                                                                                                                 |
| --------------- | ---------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------- |
| GitHub          | **GitHub App** (preferred) — `KONFLATE_APP_CLIENT_ID` + `_PRIVATE_KEY` | App permissions, installed on the repo: **Checks: R/W** (Check Run; falls back to **Commit statuses: R/W**) and/or **Pull requests: R/W** (comments). |
| GitHub          | or a write PAT — `KONFLATE_WRITE_TOKEN`                                | Fine-grained **Commit statuses** and/or **Pull requests** (R/W); or a classic `repo:status` (statuses only) / `repo` (also comments).                 |
| GitLab          | `KONFLATE_WRITE_TOKEN`                                                 | Token with the `api` scope and at least **Developer** on the project (covers both statuses and notes).                                                |
| Forgejo / Gitea | `KONFLATE_WRITE_TOKEN`                                                 | Token with `write:repository` (statuses) and, for comments, `write:issue`.                                                                            |

On GitHub the **App is preferred**: konflate authenticates as the App and mints
short-lived installation tokens, so no long-lived PAT sits in the deployment, the
bot has its own identity on the status, and access is revocable by uninstalling
the App. (konflate uses the App's **client id** as the JWT issuer, which GitHub
now recommends — no numeric app id needed.) konflate **auto-detects the App's
installation** for the repo, so there's no installation id to configure. A
fully-configured App takes precedence over `KONFLATE_WRITE_TOKEN`; a partial App
config is a startup error rather than a silent fallback. The write token is the
universal option and the only one on GitLab and Forgejo — keep it separate from
`KONFLATE_TOKEN` so it carries only the write scope a read token shouldn't.

```bash
# GitHub App (preferred): post the status and the summary comment, linking back
# to the review. Enable either toggle on its own — they're independent.
KONFLATE_STATUS_CHECKS=true
KONFLATE_PR_COMMENTS=true
KONFLATE_APP_CLIENT_ID=Iv23li...
KONFLATE_APP_PRIVATE_KEY="$(cat konflate.private-key.pem)"
KONFLATE_PUBLIC_URL=https://konflate.example.com

# …or a write token (the only option on GitLab / Forgejo)
KONFLATE_STATUS_CHECKS=true
KONFLATE_PR_COMMENTS=true
KONFLATE_WRITE_TOKEN=...
KONFLATE_PUBLIC_URL=https://konflate.example.com
```

The [summary endpoint](#http-endpoints) remains available either way — for
pulling the Markdown into a CI step yourself, rather than letting konflate post.

### Custom comment body

By default the comment is konflate's summary. To control it, point
`KONFLATE_PR_COMMENT_TEMPLATE_FILE` at a Go [`text/template`](https://pkg.go.dev/text/template):

```gotmpl
## konflate · #{{ .PR.Number }} — {{ .PR.Title }}

{{ .Summary }}

_Rendered `{{ .PR.HeadSHA }}` · [review →]({{ .ReviewURL }})_
```

It renders against:

| Field        | What                                                                                                                                                                                                                                                                              |
| ------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `.PR`        | the pull request — `.PR.Number`, `.PR.Title`, `.PR.Author`, `.PR.HeadRef`, `.PR.HeadSHA`, `.PR.BaseRef`, `.PR.URL`                                                                                                                                                                |
| `.Diff`      | the rendered diff — `.Diff.Impact.Resources`, `.Diff.Warnings`, `.Diff.Images`, `.Diff.Failures`, …                                                                                                                                                                               |
| `.ReviewURL` | konflate's review link (from `KONFLATE_PUBLIC_URL`), or empty                                                                                                                                                                                                                     |
| `.Summary`   | konflate's default summary body, so you can wrap or extend it with `{{ .Summary }}`                                                                                                                                                                                               |
| `.Sections`  | the summary's blocks _individually_, as rendered Markdown — `.Sections.Impact`, `.Sections.Cautions`, `.Sections.Failures`, `.Sections.Images`, `.Sections.BlastRadius` — to place à la carte instead of the whole `.Summary`. Each is empty when that block has nothing to show. |

So you can drop the cautions and image changes wherever you like and skip the rest:

```gotmpl
{{ if .Sections.Cautions }}> Heads up:
{{ .Sections.Cautions }}
{{ end }}
**Images bumped on #{{ .PR.Number }}**
{{ .Sections.Images }}
```

konflate **injects its hidden marker automatically**, so the comment is still found and edited in place — your template needn't include it. The template is parsed once at startup; one that fails to parse, or to render for a given PR, falls back to the built-in summary (and logs) rather than dropping the comment.

With the Helm chart, put the template **content** in `config.prCommentTemplate` (a multi-line string) — the chart mounts it as a ConfigMap and wires `KONFLATE_PR_COMMENT_TEMPLATE_FILE` to it:

```yaml
config:
    prComments: true
    # Passed to konflate verbatim — write plain Go-template braces, no escaping.
    prCommentTemplate: |
        ## konflate · #{{ .PR.Number }} — {{ .PR.Title }}
        {{ .Summary }}
```

## HTTP endpoints

Main server (`KONFLATE_PORT`):

| Method & path                 | Purpose                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        |
| ----------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `GET /`                       | The web UI.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    |
| `GET /api/prs`                | Tracked PRs and each one's diff-job status.                                                                                                                                                                                                                                                                                                                                                                                                                                                                    |
| `GET /api/prs/{n}/diff`       | A PR's rendered diff (`200` ready/error, `202` still rendering).                                                                                                                                                                                                                                                                                                                                                                                                                                               |
| `GET /api/prs/{n}/summary`    | The diff's headline facts only — impact, cautions, image bumps, failures — without the per-resource render. JSON by default; `Accept: text/markdown` returns a paste-ready comment body (`?forge=github` for `[!CAUTION]` admonitions, else plain). Ready ⇒ `200`; while rendering, JSON returns `202` and Markdown returns `503` + `Retry-After` (so `curl --retry` waits it out). Every response carries an `X-Konflate-Render-Status` header (`ok`/`failures`/`error`/`pending`) for CI gating — see below. |
| `POST /api/prs/{n}/refresh`   | **Auth** (bearer `KONFLATE_PUSH_TOKEN`) — re-render one PR. `501` unless the token is set.                                                                                                                                                                                                                                                                                                                                                                                                                     |
| `POST /hooks`                 | Verified forge webhook — re-renders the affected PR. `501` unless the secret is set.                                                                                                                                                                                                                                                                                                                                                                                                                           |
| `GET /ws`                     | Websocket stream of diff-job status events.                                                                                                                                                                                                                                                                                                                                                                                                                                                                    |
| `GET /healthz`, `GET /readyz` | Liveness / readiness.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          |

Operational server (`KONFLATE_METRICS_ADDR`): `GET /metrics`.

The summary endpoint doubles as a PR-comment source for CI — ask for Markdown
and post it straight back (one comment, edited in place on each push). While a
render is in flight it answers `503` + `Retry-After`, so `curl --retry` waits it
out with no polling loop of your own:

```bash
curl -fsS --retry 10 --retry-delay 3 -H 'Accept: text/markdown' \
  "https://konflate.example.com/api/prs/${PR_NUMBER}/summary" \
  | gh pr comment "${PR_NUMBER}" --body-file - --edit-last
```

The comment carries a `<!-- konflate:pr-N -->` marker so a poster can find and
update its own comment. konflate keeps PRs rendered on its own (webhook /
interval), so the diff is normally ready by the time CI asks anyway.

**Gating the workflow on the render.** Every summary response (Markdown or JSON)
carries an `X-Konflate-Render-Status` header, so the same request that fetches
the comment also tells CI whether to pass:

| Value      | Meaning                                                                                                 |
| ---------- | ------------------------------------------------------------------------------------------------------- |
| `ok`       | Rendered cleanly.                                                                                       |
| `failures` | Rendered, but one or more resources failed to render (the diff is shown, minus those).                  |
| `error`    | The render itself errored — no diff produced.                                                           |
| `pending`  | Still rendering. The Markdown path `503`s until a terminal verdict, so `--retry` never leaves you here. |

```bash
status=$(curl -fsS --retry 10 --retry-delay 3 -H 'Accept: text/markdown' \
  -o body.md -w '%header{x-konflate-render-status}' \
  "https://konflate.example.com/api/prs/${PR_NUMBER}/summary")
gh pr comment "${PR_NUMBER}" --body-file body.md --edit-last   # always post what rendered
[ "$status" = ok ] || { echo "::error::konflate render: ${status}"; exit 1; }
```

The check above blocks on both `error` and `failures`; relax it to
`case "$status" in ok | failures) ;; *) exit 1 ;; esac` if a partial render
shouldn't fail the PR. (`%header{}` needs curl ≥ 8.3; older curl can `-D -` and
grep the header instead.)

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

Select these events when creating the webhook. Pull-request events keep the PR
list and diffs live; CI-status events drive the per-PR check indicator — without
them the indicator still updates on the `KONFLATE_REFRESH_INTERVAL` poll, just
not instantly:

| Forge   | Pull-request events  | CI-status events                                    |
| ------- | -------------------- | --------------------------------------------------- |
| GitHub  | Pull requests        | Statuses, Check runs, Check suites                  |
| Forgejo | Pull Request         | Commit status (+ Workflow Run / Job for Actions CI) |
| GitLab  | Merge request events | Pipeline events (+ Job events)                      |

Rate limiting is intentionally **not** built in — put konflate behind your
reverse proxy / ingress and rate-limit there.

## Metrics

Served on the separate operational port (keep it off your public ingress):

| Metric                                              | Type      | Meaning                                                     |
| --------------------------------------------------- | --------- | ----------------------------------------------------------- |
| `konflate_diff_jobs_total`                          | counter   | Completed renders, by `result`.                             |
| `konflate_diff_duration_seconds`                    | histogram | Render wall-clock (clone + 2 renders).                      |
| `konflate_diff_queue_depth`                         | gauge     | PRs queued or rendering.                                    |
| `konflate_pull_requests`                            | gauge     | Open PRs tracked.                                           |
| `konflate_http_requests_total`                      | counter   | Main-server requests, by status class.                      |
| `konflate_forge_list_errors_total`                  | counter   | Failed PR-list polls, by `reason` (`rate_limited`/`error`). |
| `konflate_forge_rate_limited`                       | gauge     | `1` when the last PR-list poll hit a rate limit, else `0`.  |
| `konflate_forge_rate_limit_reset_timestamp_seconds` | gauge     | Unix time the rate limit resets (`0` when not limited).     |

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
mise run generate       # regenerate the chart README + values.schema.json
mise run helm-lint      # lint the Helm chart
mise run helm-unittest  # helm-unittest template tests
mise run dev            # run konflate locally (set KONFLATE_REPO first)
```

Tests come in four tiers:

- **Unit** — pure logic (config, diff render/lint/impact, engine pairing,
  webhook crypto, provider mapping) plus the HTTP server and the websocket hub
  driven over real sockets with a fake engine. Run by `mise run test`.
- **UI** (`mise run ui-test`) — Playwright drives the real built UI in headless
  Chromium with the API and websocket stubbed by a fixture, asserting the
  3-panel render, filtering, and split view. Runs in CI.
- **Chart** — `helm lint`, [`helm-unittest`](charts/konflate/tests) template
  tests (image/digest, secret conditionals, verbatim `mergeCommand`, conditional
  env), and a kind-backed `helm test` smoke check that installs the chart and
  probes `/readyz` (`mise run helm-test`). Run in CI.
- **Integration** (`-tags integration`, env-gated) — renders a real PR with the
  real engine; skips unless `KONFLATE_REPO` + `KONFLATE_INTEGRATION_PR` are set:

    ```bash
    KONFLATE_REPO=github://owner/repo KONFLATE_INTEGRATION_PR=123 \
      mise run test-integration
    ```

## Security

konflate is designed to be safe to expose internally, and to leak nothing even
if it were public:

- **Read-only by default; read-only request surface always.** Out of the box
  konflate writes nothing to your forge. [Write-back](#write-back) (commit
  statuses) is opt-in and posts only from konflate's own render loop using a
  process-held credential — no request, authenticated or not, can make konflate
  write. Leave it off and konflate never touches forge state.
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
