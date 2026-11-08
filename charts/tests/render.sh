#!/usr/bin/env bash
# Renders the chart with the value combinations that switch templates on and
# off, and checks what comes out. Run by `mise run helm:lint` (and so by CI).
set -euo pipefail
chart=charts/kubeglance
fail=0

# kinds RELEASE [--set ...]: the kinds the chart renders, one per line
kinds() { helm template kg "$chart" "$@" | awk '/^kind:/ {print $2}' | sort; }
has() { grep -qx "$2" <<<"$1" || { echo "FAIL: $3: no $2"; fail=1; }; }
hasnt() { ! grep -qx "$2" <<<"$1" || { echo "FAIL: $3: unexpected $2"; fail=1; }; }
render() { helm template kg "$chart" "$@"; }
# doc KIND [--set ...]: only the rendered document(s) of that kind
doc() { local kind=$1; shift; render "$@" | awk -v k="kind: $kind" 'BEGIN{RS="\n---\n"} index($0, k) {print}'; }
contains() { grep -qF -- "$2" <<<"$1" || { echo "FAIL: $3: missing '$2'"; fail=1; }; }

# defaults: two replicas, so a PDB keeps one through a drain; no NetworkPolicy
k=$(kinds)
has "$k" PodDisruptionBudget "defaults"
hasnt "$k" NetworkPolicy "defaults"
contains "$(doc PodDisruptionBudget)" "minAvailable: 1" "defaults PDB"

# one replica: minAvailable 1 would block every drain
hasnt "$(kinds --set replicaCount=1)" PodDisruptionBudget "replicaCount=1"
hasnt "$(kinds --set podDisruptionBudget.enabled=false)" PodDisruptionBudget "podDisruptionBudget.enabled=false"

# NetworkPolicy: peers and DNS always; the API server on 443/6443
out=$(doc NetworkPolicy --set networkPolicy.enabled=true)
has "$(kinds --set networkPolicy.enabled=true)" NetworkPolicy "networkPolicy.enabled"
contains "$out" "k8s-app: kube-dns" "networkPolicy DNS egress"
contains "$out" "port: 6443" "networkPolicy apiserver egress"
contains "$out" "app.kubernetes.io/instance: kg" "networkPolicy peers"

# extra ingress sources and a pinned API server CIDR are passed through
out=$(doc NetworkPolicy --set networkPolicy.enabled=true \
  --set 'networkPolicy.ingressFrom[0].namespaceSelector.matchLabels.kubernetes\.io/metadata\.name=ingress-nginx' \
  --set 'networkPolicy.apiServer.cidrs[0]=10.0.0.1/32')
contains "$out" "kubernetes.io/metadata.name: ingress-nginx" "networkPolicy ingressFrom"
contains "$out" "cidr: 10.0.0.1/32" "networkPolicy apiServer.cidrs"

# auth: off by default; a Secret the user created is mounted, never a value
hasnt_text() { ! grep -qF -- "$2" <<<"$1" || { echo "FAIL: $3: unexpected '$2'"; fail=1; }; }
hasnt_text "$(doc Deployment)" "--auth-token-file" "defaults auth"
out=$(doc Deployment --set auth.existingSecret=kg-auth)
contains "$out" "--auth-token-file=/etc/kubeglance/auth/token" "auth flag"
contains "$out" "secretName: kg-auth" "auth secret volume"
contains "$out" "mountPath: /etc/kubeglance/auth" "auth mount"
out=$(doc Deployment --set auth.existingSecret=kg-auth --set auth.key=password)
contains "$out" "key: password" "auth.key"
# with the snapshot volume too, both are mounted
out=$(doc Deployment --set auth.existingSecret=kg-auth --set snapshot.enabled=true)
contains "$out" "mountPath: /data" "auth + snapshot: data mount"
contains "$out" "mountPath: /etc/kubeglance/auth" "auth + snapshot: auth mount"

# the Go runtime learns the container's memory limit (downward API)
out=$(doc Deployment)
# the env entry itself (its next lines), in bytes: no divisor
env=$(awk '/- name: KUBEGLANCE_MEMORY_LIMIT$/ {f=1} f {print; if (++n == 4) exit}' <<<"$out")
contains "$env" "resourceFieldRef:" "memory limit env from the downward API"
contains "$env" "resource: limits.memory" "memory limit env is limits.memory"
hasnt_text "$env" "divisor" "memory limit env in bytes"

# every combination above also lints
for set in "" "--set replicaCount=1" "--set networkPolicy.enabled=true" "--set auth.existingSecret=kg-auth --set snapshot.enabled=true"; do
  # shellcheck disable=SC2086 # word splitting is the point
  helm lint --quiet "$chart" $set >/dev/null || { echo "FAIL: helm lint $set"; fail=1; }
done

# the chart has an icon (Artifact Hub shows it; helm lint asks for one)
! helm lint "$chart" 2>&1 | grep -q "icon is recommended" || { echo "FAIL: Chart.yaml has no icon"; fail=1; }
grep -q '^icon: https://raw.githubusercontent.com/nonchan7720/kubeglance/main/docs/logo.svg$' "$chart/Chart.yaml" ||
  { echo "FAIL: Chart.yaml icon is not docs/logo.svg"; fail=1; }

exit "$fail"
