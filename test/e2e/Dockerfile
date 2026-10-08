# End-to-end fixtures: the probe from the release image next to a test server,
# in a FROM scratch image with no shell, so every HEALTHCHECK below can only
# work in exec form. Each target is one scenario; test/e2e/run.sh builds them.

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

# The lprobe spelling of the flags, against the plain-HTTP variant.
FROM base AS notls
HEALTHCHECK --interval=2s --timeout=3s --retries=2 CMD ["/healthprobe-notls", "-mode=http", "-port=8080", "-endpoint=/healthz"]
