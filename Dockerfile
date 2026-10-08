ARG BASE_IMAGE=gcr.io/distroless/static-debian12:nonroot
ARG BASE_IMAGE_DIGEST=sha256:1b7b9f0f0e0a1d2155f531db587cc48ec26aaf97ab64364225f5bf18a054e66a

# Cross-compile natively instead of running the Go toolchain under QEMU.
FROM --platform=$BUILDPLATFORM golang:1.27.1-alpine AS builder

WORKDIR /src

# Glob: with no dependencies there is no go.sum, and a missing file fails COPY.
COPY go.mod go.su[m] ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/healthprobe . && \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath -tags notls -ldflags="-s -w -X main.version=${VERSION}" -o /out/healthprobe-notls .

# hadolint ignore=DL3006
FROM ${BASE_IMAGE}@${BASE_IMAGE_DIGEST}

ARG VERSION=dev

LABEL org.opencontainers.image.title="healthprobe" \
      org.opencontainers.image.description="Static, dependency-free HTTP health probe for container HEALTHCHECKs in scratch and distroless images" \
      org.opencontainers.image.source="https://github.com/container-registry/healthprobe" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.vendor="8gears AG"

COPY --from=builder /out/healthprobe /out/healthprobe-notls /

USER 65532:65532

ENTRYPOINT ["/healthprobe"]
