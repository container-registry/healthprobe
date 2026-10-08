# healthprobe

Static, dependency-free HTTP health probe for container HEALTHCHECKs in scratch and distroless images


`healthprobe` sends one HTTP GET to a port on the container's own loopback interface and exits 0 when the
answer has an accepted status code, 1 otherwise. That is the whole job of a Docker or Podman `HEALTHCHECK`,
and of a Kubernetes exec probe, in an image that has no shell, `curl` or `wget`.

It is a single static binary built from the Go standard library alone, with no third-party modules. The
build fails if one is linked in, and also fails if a binary grows past its size budget.

| Binary | Size (linux/amd64) | Use it when |
|--------|--------------------|-------------|
| `healthprobe` | 4.7 MB | the endpoint may be HTTPS |
| `healthprobe-notls` | 2.3 MB | the endpoint is plain HTTP |

## Install

Copy it out of the release image. Both binaries are at the image root, and the image is published for
`linux/amd64` and `linux/arm64`:

```dockerfile
FROM scratch
COPY --from=ghcr.io/container-registry/healthprobe:v1.0.0 /healthprobe-notls /healthprobe
COPY my-service /my-service
HEALTHCHECK --interval=10s --timeout=5s --retries=3 CMD ["/healthprobe", "-port", "8080", "-endpoint", "/healthz"]
ENTRYPOINT ["/my-service"]
```

Pin the image by digest in production. Standalone binaries for Linux, macOS and Windows, `checksums.txt` and
an SPDX SBOM are attached to every [GitHub release](https://github.com/container-registry/healthprobe/releases).

## Usage

```text
healthprobe [-port 8080] [-endpoint /] [-tls [-tls-no-verify | -tls-ca-cert FILE]] [-timeout 5s]
```

| Flag | Default | Meaning |
|------|---------|---------|
| `-port` | `8080` | port to connect to |
| `-endpoint` | `/` | request path, query string allowed |
| `-host` | `127.0.0.1` | address to connect to |
| `-ipv6` | off | connect to `::1` instead |
| `-http-codes` | `200-299` | accepted status codes, e.g. `200,204,300-399` |
| `-timeout` | `5s` | deadline for connect, TLS handshake, request and status line together |
| `-tls` | off | use HTTPS, verifying the certificate against the system roots |
| `-tls-no-verify` | off | with `-tls`, skip certificate verification (self-signed listeners) |
| `-tls-ca-cert` | | with `-tls`, PEM file of CAs to verify against |
| `-tls-server-name` | | with `-tls`, name to verify the certificate against |
| `-user-agent` | `healthprobe/<version>` | User-Agent header |
| `-v` | off | print the request and the status line to stderr |
| `-version` | | print the version |

On failure it prints one line saying why, which Docker keeps in `docker inspect --format '{{json .State.Health}}'`.
Every failure exits 1, because Docker reserves exit code 2.

Redirects are not followed: a `3xx` is unhealthy unless `-http-codes` accepts it. The probe sends
`Host: localhost:<port>` for loopback targets, so name-based virtual hosts still match.

### Docker Compose

```yaml
healthcheck:
  test: ["CMD", "/healthprobe", "-port", "8080", "-endpoint", "/healthz"]
```

### Podman

Podman's default image format is OCI, and the OCI image config has no healthcheck field, so `podman build`
drops `HEALTHCHECK` with only a warning. Build with `podman build --format docker`.

### Kubernetes

Kubernetes ignores the image `HEALTHCHECK`. Prefer an `httpGet` probe; use the binary when the probe has to
run inside the container, for example against a listener bound to loopback only:

```yaml
readinessProbe:
  exec:
    command: ["/healthprobe", "-port", "8080", "-endpoint", "/healthz"]
```

### Migrating from lprobe

`healthprobe` accepts the flags of [lprobe](https://github.com/fivexl/lprobe)'s HTTP mode as they are:
`-mode=http`, `-port`, `-endpoint`, `-ipv6`, `-http-codes`, `-user-agent`, `-connect-timeout` (an alias of
`-timeout`), `-tls`, `-tls-no-verify`, `-tls-ca-cert`, `-tls-server-name` and `-v`. Swap the binary and keep the
`HEALTHCHECK` lines. The differences:

- gRPC mode, SPIFFE, ALTS and TLS client certificates are not supported.
- The default timeout is 5s rather than 1s, and it covers the whole check.
- Redirects are not followed.

## Development

```bash
task setup    # install the pinned tools and the git hooks
task check    # run the gates CI runs
task --list   # everything else
```


Contributions are welcome: see [CONTRIBUTING.md](CONTRIBUTING.md). Repository automation is documented in
[docs/repo-automation.md](docs/repo-automation.md), and the release process in [docs/RELEASES.md](docs/RELEASES.md).

## Security

Report vulnerabilities privately. See [SECURITY.md](SECURITY.md).

## License

Apache-2.0. See [LICENSE](LICENSE).
