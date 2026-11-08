#!/usr/bin/env bash
# End-to-end tests on a kind cluster (mise run e2e; CI job `e2e`): builds the
# kubeglance image and the test helper, installs metrics-server and the chart
# with two replicas and alerts on, then runs `go test -tags e2e ./e2e/`.
#
# A cluster this script creates is deleted after a green run; keep it with
# E2E_KEEP=1, or reuse an existing one by name (E2E_CLUSTER).
set -euo pipefail

cluster=${E2E_CLUSTER:-kubeglance-e2e}
ctx=kind-$cluster
# metrics-server, pinned with the checksum of its release manifest
ms_version=v0.9.1
ms_sha256=3c7f57cdbee0f57fb0259a150e8f6d6cb4f29d72d1aa5dce8803151dd6d4c2a1

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
k() { kubectl --context "$ctx" "$@"; }
step() { echo "--- $*"; }

created=0
if ! kind get clusters 2>/dev/null | grep -qx "$cluster"; then
  step "kind cluster $cluster"
  kind create cluster --name "$cluster" --config e2e/kind.yaml --wait 180s
  created=1
fi

step "images"
mkdir -p "$work/kubeglance" "$work/helper"
CGO_ENABLED=0 go build -trimpath -ldflags '-s -w -X github.com/nonchan7720/kubeglance/pkg/version.Version=e2e' -o "$work/kubeglance/kubeglance" .
cp LICENSE NOTICE THIRD_PARTY_NOTICES.md "$work/kubeglance/"
docker build -q -t kubeglance:e2e -f Dockerfile "$work/kubeglance"
CGO_ENABLED=0 go build -trimpath -o "$work/helper/helper" ./e2e/helper
docker build -q -t kubeglance-e2e-helper:e2e -f e2e/helper/Dockerfile "$work/helper"
kind load docker-image --name "$cluster" kubeglance:e2e kubeglance-e2e-helper:e2e

step "metrics-server $ms_version"
curl -sSLf -o "$work/metrics-server.yaml" "https://github.com/kubernetes-sigs/metrics-server/releases/download/$ms_version/components.yaml"
echo "$ms_sha256  $work/metrics-server.yaml" | sha256sum -c --quiet -
k apply -f "$work/metrics-server.yaml"
# kind's kubelets serve self-signed certificates
k -n kube-system patch deployment metrics-server --type=json \
  -p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]'
k -n kube-system rollout status deployment/metrics-server --timeout=180s

step "workloads and the alert receiver"
k apply -f e2e/testdata/workloads.yaml
k -n e2e rollout status deployment/receiver --timeout=120s

step "kubeglance (two replicas, alerts on)"
# a one-minute alert window keeps the alert test short; CPU alerts off, so
# only the memory hog the test starts can fire
helm upgrade --install kubeglance charts/kubeglance --kube-context "$ctx" -n kubeglance --create-namespace \
  --set image.repository=kubeglance --set image.tag=e2e --set image.pullPolicy=Never \
  --set alerts.webhook=http://receiver.e2e.svc/hook --set alerts.window=1m --set alerts.cpu=0 \
  --set alerts.publicURL=https://kubeglance.e2e.example \
  --wait --timeout 5m

diagnose() {
  step "diagnostics"
  k get nodes -o wide || true
  k get pods -A -o wide || true
  k -n kubeglance logs -l app.kubernetes.io/name=kubeglance --tail=200 --prefix || true
  k -n e2e logs deployment/receiver --tail=100 || true
  k get events -A --sort-by=.lastTimestamp | tail -40 || true
}

step "go test -tags e2e ./e2e/"
if ! E2E_CONTEXT=$ctx go test -tags e2e -count=1 -v -timeout 25m ./e2e/; then
  diagnose
  exit 1
fi

if [[ $created == 1 && -z ${E2E_KEEP:-} ]]; then
  step "deleting $cluster"
  kind delete cluster --name "$cluster"
fi
