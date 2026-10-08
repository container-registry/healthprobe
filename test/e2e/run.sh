#!/usr/bin/env bash
# Usage: ENGINE=docker|podman test/e2e/run.sh
set -euo pipefail

ENGINE="${ENGINE:-docker}"
TIMEOUT="${TIMEOUT:-40}"
PROBE_IMAGE=localhost/healthprobe:e2e
cd "$(dirname "$0")/../.."

# The OCI image format has no healthcheck field; podman drops HEALTHCHECK without it.
build_flags=()
[ "$ENGINE" = podman ] && build_flags=(--format docker)

containers=()
cleanup() {
  [ "${#containers[@]}" -eq 0 ] || "$ENGINE" rm -f "${containers[@]}" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "--- building ${PROBE_IMAGE} with ${ENGINE}"
"$ENGINE" build "${build_flags[@]}" -q -t "$PROBE_IMAGE" . >/dev/null

failed=0
check() {
  local target=$1 want=$2 image="localhost/healthprobe-e2e:$1" name="healthprobe-e2e-$1-$$" status=""
  "$ENGINE" build "${build_flags[@]}" -q --target "$target" --build-arg "PROBE_IMAGE=${PROBE_IMAGE}" \
    -t "$image" -f test/e2e/Dockerfile . >/dev/null
  "$ENGINE" run -d --name "$name" "$image" >/dev/null
  containers+=("$name")

  for _ in $(seq "$TIMEOUT"); do
    status=$("$ENGINE" inspect -f '{{.State.Health.Status}}' "$name" 2>/dev/null || true)
    [ "$status" = "$want" ] && break
    # Podman's healthcheck timers need systemd, which CI runners may lack.
    [ "$ENGINE" = podman ] && "$ENGINE" healthcheck run "$name" >/dev/null 2>&1 || true
    sleep 1
  done

  if [ "$status" = "$want" ]; then
    echo "ok   ${ENGINE} ${target}: ${status}"
  else
    echo "FAIL ${ENGINE} ${target}: status '${status}', want '${want}'"
    "$ENGINE" inspect -f '{{json .State.Health}}' "$name" || true
    failed=1
  fi
}

check healthy healthy
check unhealthy unhealthy
check tls healthy
check notls healthy

if [ "$ENGINE" = podman ]; then
  "$ENGINE" build -q --target healthy --build-arg "PROBE_IMAGE=${PROBE_IMAGE}" \
    -t localhost/healthprobe-e2e:oci -f test/e2e/Dockerfile . >/dev/null 2>&1
  oci=$("$ENGINE" image inspect -f '{{json .HealthCheck}}' localhost/healthprobe-e2e:oci 2>/dev/null || echo error)
  echo "note podman --format oci keeps HEALTHCHECK: $([ "$oci" = null ] || [ -z "$oci" ] && echo no || echo "yes ($oci)")"
fi

exit "$failed"
