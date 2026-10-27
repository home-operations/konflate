# syntax=docker/dockerfile:1

# ARGs used in a FROM must live in the global scope (before the first FROM).
# All versions are supplied by the release workflow from mise — the single
# source of truth for the toolchain (see docs/adr/0001 and .mise/config.toml).
ARG NODE_VERSION
ARG GO_VERSION

# ---- UI build -------------------------------------------------------------
# Build the frontend bundles so the Go stage can go:embed them. The runtime
# image does not include node.
FROM node:${NODE_VERSION}-alpine AS ui
# The Node image ships no pnpm, and its npm package's install script only
# swaps the wrappers that run it through Node for its native binary, so it is
# installed without scripts.
ARG PNPM_VERSION
RUN npm install --global --ignore-scripts "pnpm@${PNPM_VERSION:?PNPM_VERSION is required}"
WORKDIR /ui
# Install deps against the lockfile first for layer caching. The wildcard
# takes pnpm-workspace.yaml too once it exists: its allowBuilds and
# minimumReleaseAgeExclude decide whether the install passes.
COPY internal/web/package.json internal/web/pnpm-*.yaml ./
RUN --mount=type=cache,target=/root/.local/share/pnpm/store pnpm install --frozen-lockfile
COPY internal/web/ ./
RUN pnpm run build

# ---- Go build -------------------------------------------------------------
FROM golang:${GO_VERSION}-alpine AS builder
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG REVISION=dev

WORKDIR /workspace
# Cache module downloads before copying source.
COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ cmd/
COPY internal/ internal/
# Overlay the freshly built UI so go:embed picks up the real assets (the
# committed placeholder, if present, is replaced).
COPY --from=ui /ui/dist/ internal/web/dist/

# Static, stripped, reproducible binary. GOARCH is left to the platform.
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${REVISION}" \
    -o konflate ./cmd/konflate

# ---- Runtime --------------------------------------------------------------
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/konflate /konflate
EXPOSE 8080 8081
ENTRYPOINT ["/konflate"]
