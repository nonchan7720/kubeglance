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

# alerts: off by default; a plain URL goes in an env var, never a flag
out=$(doc Deployment)
hasnt_text "$out" "KUBEGLANCE_ALERT_WEBHOOK" "defaults alerts"
hasnt_text "$out" "--alert-" "defaults alert flags"
out=$(doc Deployment --set alerts.webhook=https://push.example.com/v1/notifications --set alerts.format=pushshell \
  --set alerts.publicURL=https://kg.example --set alerts.language=ja)
contains "$out" "name: KUBEGLANCE_ALERT_WEBHOOK" "alerts webhook env"
contains "$out" 'value: "https://push.example.com/v1/notifications"' "alerts webhook value"
contains "$out" "--alert-format=pushshell" "alerts format"
contains "$out" "--alert-window=10m" "alerts window"
contains "$out" "--alert-memory=0.9" "alerts memory"
contains "$out" "--alert-language=ja" "alerts language"
contains "$out" "--public-url=https://kg.example" "alerts public URL"
hasnt_text "$out" "--alert-headers-file" "alerts without headers"
# the URL and headers from a Secret the user created
out=$(doc Deployment --set alerts.existingSecret=kg-alerts --set alerts.urlKey=url --set alerts.headersKey=headers)
contains "$out" "secretKeyRef:" "alerts URL from the Secret"
contains "$out" "key: url" "alerts urlKey"
contains "$out" "--alert-headers-file=/etc/kubeglance/alerts/headers" "alerts headers flag"
contains "$out" "mountPath: /etc/kubeglance/alerts" "alerts headers mount"
contains "$out" "secretName: kg-alerts" "alerts headers volume"
# a Secret without a URL key is not a receiver
hasnt_text "$(doc Deployment --set alerts.existingSecret=kg-alerts --set alerts.headersKey=headers)" "--alert-" "alerts Secret without urlKey"
# NetworkPolicy: egress to the receiver only with alerts on
# (8443, not the default 443, which the API server rule has as well)
hasnt_text "$(doc NetworkPolicy --set networkPolicy.enabled=true --set 'alerts.egress.ports[0]=8443')" "port: 8443" "networkPolicy without alerts"
out=$(doc NetworkPolicy --set networkPolicy.enabled=true --set alerts.webhook=https://h --set 'alerts.egress.ports[0]=8443' --set 'alerts.egress.cidrs[0]=203.0.113.7/32')
contains "$out" "cidr: 203.0.113.7/32" "networkPolicy alerts egress"
contains "$out" "port: 8443" "networkPolicy alerts port"

# every combination above also lints
for set in "" "--set replicaCount=1" "--set networkPolicy.enabled=true" "--set auth.existingSecret=kg-auth --set snapshot.enabled=true" \
  "--set alerts.existingSecret=kg-alerts --set alerts.urlKey=url --set alerts.headersKey=headers --set auth.existingSecret=kg-auth --set networkPolicy.enabled=true"; do
  # shellcheck disable=SC2086 # word splitting is the point
  helm lint --quiet "$chart" $set >/dev/null || { echo "FAIL: helm lint $set"; fail=1; }
done

# the chart has an icon (Artifact Hub shows it; helm lint asks for one)
! helm lint "$chart" 2>&1 | grep -q "icon is recommended" || { echo "FAIL: Chart.yaml has no icon"; fail=1; }
grep -q '^icon: https://raw.githubusercontent.com/nonchan7720/kubeglance/main/docs/logo.svg$' "$chart/Chart.yaml" ||
  { echo "FAIL: Chart.yaml icon is not docs/logo.svg"; fail=1; }

exit "$fail"
