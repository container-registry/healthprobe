# No shell in scratch, so every HEALTHCHECK must work in exec form.

ARG PROBE_IMAGE=localhost/healthprobe:e2e

# hadolint ignore=DL3006
FROM ${PROBE_IMAGE} AS probe

FROM --platform=$BUILDPLATFORM golang:1.27.1-alpine AS server
WORKDIR /src
COPY go.mod ./
COPY test/e2e/server ./test/e2e/server
ARG TARGETARCH
RUN CGO_ENABLED=0 GOARCH=${TARGETARCH} go build -trimpath -o /server ./test/e2e/server

FROM scratch AS base
COPY --from=probe /healthprobe /healthprobe-notls /
COPY --from=server /server /server
USER 65532:65532
ENTRYPOINT ["/server"]

FROM base AS healthy
HEALTHCHECK --interval=2s --timeout=3s --retries=2 CMD ["/healthprobe", "-port", "8080", "-endpoint", "/healthz"]

FROM base AS unhealthy
HEALTHCHECK --interval=2s --timeout=3s --retries=2 CMD ["/healthprobe", "-port", "8080", "-endpoint", "/fail"]

FROM base AS tls
HEALTHCHECK --interval=2s --timeout=3s --retries=2 CMD ["/healthprobe", "-port", "8443", "-endpoint", "/healthz", "-tls", "-tls-no-verify"]

FROM base AS notls
HEALTHCHECK --interval=2s --timeout=3s --retries=2 CMD ["/healthprobe-notls", "-mode=http", "-port=8080", "-endpoint=/healthz"]
