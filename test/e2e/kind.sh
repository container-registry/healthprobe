#!/usr/bin/env bash
# Runs the probe as Kubernetes exec readiness and liveness probes in a
# throwaway kind cluster. Kubernetes ignores the image HEALTHCHECK, so this
# covers the other way a shell-less image gets probed.
#
#   test/e2e/kind.sh          # needs the Docker images test/e2e/run.sh builds
#   KEEP=1 test/e2e/kind.sh   # leave the cluster running afterwards
set -euo pipefail

CLUSTER="${CLUSTER:-healthprobe-e2e}"
CTX="kind-${CLUSTER}"
IMAGE=localhost/healthprobe-e2e:healthy
# Pod names and the cleanup selector carry a per-run suffix, so a run against a
# reused cluster never collides with, or deletes, anything it did not create.
RUN="r$$"
cd "$(dirname "$0")/../.."

# kind writes its context into KUBECONFIG and makes it current, and deleting
# the cluster then leaves no current context at all. A private file keeps the
# caller's kubeconfig untouched.
KUBECONFIG="$(mktemp "${TMPDIR:-/tmp}/healthprobe-kind.XXXXXX")"
export KUBECONFIG

created=0
cleanup() {
  if [ "$created" -eq 1 ] && [ -z "${KEEP:-}" ]; then
    kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
  fi
  if [ -n "${KEEP:-}" ]; then
    echo "cluster kept; KUBECONFIG=${KUBECONFIG}"
  else
    rm -f "$KUBECONFIG"
  fi
}
trap cleanup EXIT

if kind get clusters 2>/dev/null | grep -Fqx "$CLUSTER"; then
  kind export kubeconfig --name "$CLUSTER" >/dev/null
else
  kind create cluster --name "$CLUSTER" --wait 120s
  created=1
fi
docker image inspect "$IMAGE" >/dev/null 2>&1 || ENGINE=docker test/e2e/run.sh
kind load docker-image --name "$CLUSTER" "$IMAGE"

pod() {
  local name=$1; shift
  local cmd
  cmd=$(printf '"%s",' "$@")
  cat <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: ${name}-${RUN}
  labels: {app: healthprobe-e2e, healthprobe-e2e/run: ${RUN}}
spec:
  securityContext: {runAsNonRoot: true, runAsUser: 65532}
  containers:
    - name: server
      image: ${IMAGE}
      imagePullPolicy: Never
      securityContext: {readOnlyRootFilesystem: true, allowPrivilegeEscalation: false, capabilities: {drop: [ALL]}}
      readinessProbe:
        exec: {command: [${cmd%,}]}
        periodSeconds: 2
        timeoutSeconds: 3
      livenessProbe:
        exec: {command: ["/healthprobe", "-port", "8080", "-endpoint", "/healthz"]}
        periodSeconds: 5
---
EOF
}

{
  pod ready-http /healthprobe -port 8080 -endpoint /healthz
  pod ready-tls /healthprobe -port 8443 -endpoint /healthz -tls -tls-no-verify
  pod ready-notls /healthprobe-notls -mode=http -port=8080 -endpoint=/healthz
  pod not-ready /healthprobe -port 8080 -endpoint /fail
} | kubectl --context "$CTX" apply -f -

failed=0
for p in ready-http ready-tls ready-notls; do
  if kubectl --context "$CTX" wait --for=condition=Ready "pod/${p}-${RUN}" --timeout=90s >/dev/null; then
    echo "ok   kind ${p}: Ready"
  else
    echo "FAIL kind ${p}: not Ready"
    kubectl --context "$CTX" describe "pod/${p}-${RUN}" | tail -20
    failed=1
  fi
done

# Not-ready is only meaningful once the container runs and the probe has
# failed a few times, so wait for the failure event rather than a fixed sleep.
kubectl --context "$CTX" wait --for=jsonpath='{.status.containerStatuses[0].started}'=true "pod/not-ready-${RUN}" --timeout=90s >/dev/null
event=""
for _ in $(seq 30); do
  event=$(kubectl --context "$CTX" get events --field-selector "involvedObject.name=not-ready-${RUN},reason=Unhealthy" \
    -o jsonpath='{.items[0].message}' 2>/dev/null || true)
  [ -n "$event" ] && break
  sleep 2
done
ready=$(kubectl --context "$CTX" get pod "not-ready-${RUN}" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')
restarts=$(kubectl --context "$CTX" get pod "not-ready-${RUN}" -o jsonpath='{.status.containerStatuses[0].restartCount}')
if [ "$ready" = "False" ] && [[ "$event" == *503* ]] && [ "$restarts" = 0 ]; then
  echo "ok   kind not-ready: Ready=False, liveness kept it running, event: ${event}"
else
  echo "FAIL kind not-ready: Ready=${ready} restarts=${restarts} event='${event}'"
  failed=1
fi

kubectl --context "$CTX" delete pod -l "healthprobe-e2e/run=${RUN}" --wait=false >/dev/null
exit "$failed"
