#!/usr/bin/env bash
# Runs the HEALTHCHECK scenarios in test/e2e/Dockerfile under a container
# engine and checks the health status the engine itself records.
#
#   ENGINE=docker test/e2e/run.sh
#   ENGINE=podman test/e2e/run.sh
set -euo pipefail

ENGINE="${ENGINE:-docker}"
TIMEOUT="${TIMEOUT:-40}"
PROBE_IMAGE=localhost/healthprobe:e2e
cd "$(dirname "$0")/../.."

# Podman's default image format is OCI, whose config has no healthcheck field:
# HEALTHCHECK is dropped with only a warning. Docker format keeps it.
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
    # Podman runs healthchecks from systemd timers, which a rootless or
    # machine-less setup may not have; running the check by hand updates the
    # same recorded status.
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
  # Documents the format trap above rather than testing the probe.
  "$ENGINE" build -q --target healthy --build-arg "PROBE_IMAGE=${PROBE_IMAGE}" \
    -t localhost/healthprobe-e2e:oci -f test/e2e/Dockerfile . >/dev/null 2>&1
  oci=$("$ENGINE" image inspect -f '{{json .HealthCheck}}' localhost/healthprobe-e2e:oci 2>/dev/null || echo error)
  echo "note podman --format oci keeps HEALTHCHECK: $([ "$oci" = null ] || [ -z "$oci" ] && echo no || echo "yes ($oci)")"
fi

exit "$failed"
