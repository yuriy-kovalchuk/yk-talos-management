# syntax=docker/dockerfile:1

# ─── Build ────────────────────────────────────────────────────────────────────
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
ARG VERSION_PKG=github.com/yuriy-kovalchuk/yk-talos-management/internal/version
ARG TARGETOS=linux
ARG TARGETARCH=amd64

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build \
    -trimpath \
    -ldflags="-s -w \
      -X ${VERSION_PKG}.Version=${VERSION} \
      -X ${VERSION_PKG}.Commit=${COMMIT} \
      -X ${VERSION_PKG}.BuildDate=${BUILD_DATE}" \
    -o manager ./cmd/

# ─── Runtime ─────────────────────────────────────────────────────────────────
FROM gcr.io/distroless/static:nonroot@sha256:d29e660cc75a5b6b1334e03c5c81ccf9bc0884a002c6000dbf0fb96034814478

LABEL org.opencontainers.image.title="yk-talos-management" \
      org.opencontainers.image.description="Kubernetes operator for declaratively managing Talos Linux clusters" \
      org.opencontainers.image.source="https://github.com/yuriy-kovalchuk/yk-talos-management" \
      org.opencontainers.image.licenses="Apache-2.0"

COPY --from=builder /src/manager /manager

USER nonroot:nonroot

ENTRYPOINT ["/manager"]
