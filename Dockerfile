# syntax=docker/dockerfile:1

# ARGs used in a FROM must live in the global scope (before the first FROM).
# Both versions are supplied by the release workflow from mise — the single
# source of truth for the toolchain (see docs/adr/0001 and .mise/config.toml).
ARG NODE_VERSION
ARG GO_VERSION

# ---- UI build -------------------------------------------------------------
# Build the frontend bundles so the Go stage can go:embed them. The runtime
# image does not include node.
FROM node:${NODE_VERSION}-alpine AS ui
WORKDIR /ui
# Install deps against the lockfile first for layer caching.
COPY internal/web/package.json internal/web/package-lock.json ./
RUN npm ci
COPY internal/web/ ./
RUN npm run build

# ---- Go build -------------------------------------------------------------
FROM golang:${GO_VERSION} AS builder
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
USER 65532:65532
EXPOSE 8080 9090
ENTRYPOINT ["/konflate"]
