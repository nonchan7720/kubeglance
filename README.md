# <img src="docs/logo.svg" alt="" width="40" align="top"> kubeglance

English | [日本語](README_ja.md)

**See what is eating your cluster, from your phone.**

kubeglance answers one question in under ten seconds: *what is using my cluster's CPU and memory right now, and has it been like this for the last 30 minutes?* It shows four levels, each made for one decision:

| level | question | decision |
|---|---|---|
| **Cluster** | how much headroom is left? | can I add workloads? |
| **Node** | is the load spread evenly? | should I rebalance? |
| **Workload** (Deployment / StatefulSet / DaemonSet / CronJob) | how loaded is this service, per pod? | should I scale it? |
| **Pod** | which single pod is hot? | what do I look at first? |

Every number is shown as **now · average · median · p95** over a window (5 m to 24 h), against the pod's requests and limits or the node's allocatable, as a bar you can read with one thumb. Under each bar a sparkline shows the window's shape on the same scale, with the request as a dashed line and gaps shaded, so "has it been like this for 30 minutes?" is one glance. Workloads and pods can be filtered by namespace and sorted by CPU, memory or name.

- **One static binary, one Pod.** No database, no Prometheus, no Grafana. metrics-server (which k3s ships with) is the only data source.
- **Short-term history in memory.** A bounded ring buffer per pod and node; about 10 MB for 100 pods over 24 h at 15 s. See [Memory](#memory).
- **Zero-downtime updates with no history loss.** With two replicas a starting replica copies the history from the running one before it becomes Ready. See [How updates keep the history](#how-updates-keep-the-history).
- **Mobile first.** A PWA you add to your home screen. Put it behind Tailscale, Cloudflare Tunnel or an authenticating Ingress and check your homelab from the train.
- **English and Japanese UI.** Follows the browser language; `?lang=ja` / `?lang=en` or the EN/JA button overrides it, remembered per device.
- **Alerts to your phone, optional.** A webhook hears when a container stays near its memory or CPU limit, or a node near its allocatable, for at least half of the last 10 minutes, and again when it is over. See [Alerts](#alerts).
- **Read-only, tiny footprint.** list/watch on pods, nodes, replicasets, jobs and the metrics API; 20 m CPU and 64 Mi memory requested.

> Status: early. The core (collector, history, four-level API, UI, Helm chart) works; see [TODO.md](TODO.md) for what is next.

## Install

```bash
helm install kubeglance oci://ghcr.io/nonchan7720/charts/kubeglance -n kubeglance --create-namespace
kubectl -n kubeglance port-forward svc/kubeglance 8080:80
# → http://127.0.0.1:8080
```

With Argo CD, point an Application at the chart `oci://ghcr.io/nonchan7720/charts/kubeglance` or at `charts/kubeglance` of this repository.

Requirements: Kubernetes ≥ 1.30 (for the preStop `sleep` action; set `preStopSleepSeconds: 0` on older clusters) and metrics-server.

With two replicas the chart also renders a PodDisruptionBudget (`minAvailable: 1`), so a node drain never takes both replicas, and the history, at once. `networkPolicy.enabled: true` adds a NetworkPolicy: ingress on 8080 from the other replicas and from `networkPolicy.ingressFrom` (your Ingress controller), egress to DNS, the API server (`networkPolicy.apiServer`, which also serves the metrics API) and the replicas. It is off by default because those sources differ per cluster.

The first log line, `starting`, carries the effective configuration (flags, environment and defaults combined) as a `config` object.

### Access

kubeglance shows what runs in your cluster, so keep it off the open internet: reach it through Tailscale, Cloudflare Access or an authenticating Ingress. If you cannot, give it a token:

```bash
kubectl -n kubeglance create secret generic kubeglance-auth --from-literal=token="$(openssl rand -hex 24)"
helm upgrade kubeglance oci://ghcr.io/nonchan7720/charts/kubeglance -n kubeglance --reuse-values --set auth.existingSecret=kubeglance-auth
```

The browser then asks for a user name (anything) and a password (the token) once. API clients and Prometheus can send `Authorization: Bearer <token>`. `/healthz`, `/readyz`, the web manifest and the icon stay open for the probes and for adding the app to a home screen. The replicas send the token to each other for the history copy. The token is read from a file (the mounted Secret), never from a flag, and the startup log only says whether one is set.

### Alerts

Off by default. Give kubeglance a webhook and it posts when, for at least half of `--alert-window` (10 minutes by default):

| rule | compares | means |
|---|---|---|
| `container-memory` | a container's memory with its limit | the OOM kill is near |
| `container-cpu` | a container's CPU with its limit | it is being throttled |
| `node-memory` / `node-cpu` | a node's usage with its allocatable | the node is full |

at or above the threshold (`--alert-memory`, `--alert-cpu`: 90 % by default; 0 turns a resource off), and once more when it is over (`resolved`, also when the pod or node is gone). "Half of the window", not "now": a sustained problem fires through a dip or two, a single spike does not, and the alert resolves only once most of the window is calm again. Samples missing from the window count as below, so a gap delays an alert but never raises one. A container without a limit is never compared; a pod with several containers is checked per container, since the OOM kill is per container.

```bash
helm upgrade kubeglance oci://ghcr.io/nonchan7720/charts/kubeglance -n kubeglance --reuse-values \
  --set alerts.webhook=https://receiver.example.com/kubeglance \
  --set alerts.publicURL=https://kubeglance.example.ts.net   # alerts link to their pod or node
```

`--alert-format=json` (the default) sends every change of one evaluation in one request, `{"version": 1, "alerts": [...]}`; each alert has its `key` (rule and target), `status` (`firing` / `resolved`), `since`, `at`, `gone`, the `verdict` (threshold, limit, the window's now / avg / median / p95 and coverage), a ready-made `title` and `body` (`--alert-language=en|ja`) and a `url`. `--alert-format=pushshell` sends each alert as a notification to a [pushshell](https://github.com/nonchan7720/pushshell) backend (`POST /v1/notifications`, to the login ID `kubeglance`; the resolve replaces the firing on the phone); more than three at once become one summary. Opened inside a pushshell app, the kubeglance UI registers the phone under that login ID by itself. Headers such as an API key come from a Secret, never a value:

```bash
kubectl -n kubeglance create secret generic kubeglance-alerts --from-literal=headers='X-API-Key: ...'
helm upgrade ... --set alerts.format=pushshell --set alerts.webhook=https://push.example.com/v1/notifications \
  --set alerts.existingSecret=kubeglance-alerts --set alerts.headersKey=headers
```

When the URL itself is a credential (a Slack-style webhook), put it in the same Secret and set `alerts.urlKey` instead of `alerts.webhook`; the startup log shows only its scheme and host. With `networkPolicy.enabled` the chart adds egress to the receiver (`alerts.egress`).

Every replica evaluates the rules over the same history and reaches the same verdicts; only the Ready replica with the lowest pod IP sends, so two replicas do not notify twice and no write access to the API server is needed. When it goes away, the other one carries on without repeating what was already sent. A failed delivery is retried with the next evaluation. One limit of keeping no state outside memory: a replica that starts next to a running one assumes that one already sent what fires at that moment, so alerts that already fire when alerts are first switched on are not sent until they fire again.

Binaries for linux / darwin / windows (amd64, arm64, armv7) are on [Releases](https://github.com/nonchan7720/kubeglance/releases); run `kubeglance --kubeconfig ~/.kube/config` outside the cluster.

## Configuration

Flags, or the environment variable in brackets; a flag on the command line wins over the variable, and a malformed variable stops the start-up instead of falling back to the default. `kubeglance --help` lists them, `kubeglance --version` prints the version. The Helm chart exposes the useful ones in `values.yaml`.

| flag | default | meaning |
|---|---|---|
| `--addr` (`KUBEGLANCE_ADDR`) | `:8080` | listen address |
| `--interval` (`KUBEGLANCE_INTERVAL`) | auto | poll interval. By default it follows metrics-server's `--metric-resolution` (15 s by default; managed clusters may use another value), told from how far apart the kubelet samples metrics-server reports are over the last three minutes. Not from a single `window`: that is the gap between the two samples metrics-server holds, which is longer than the resolution whenever a scrape brought no new kubelet sample. A change is followed without a restart once three polls in a row report it; until the first value is known it is 15 s. Statistics over a window that spans a change count each part at its own interval. Polling faster than the resolution reads the same numbers twice; set this only to fix the interval, which then never changes |
| `--retention` (`KUBEGLANCE_RETENTION`) | `24h` | history kept in memory; the maximum window |
| `--peer-host` (`KUBEGLANCE_PEER_HOST`) | | headless Service of the replicas; empty disables the peer copy |
| `--peer-port` (`KUBEGLANCE_PEER_PORT`) | `8080` | port peers serve on |
| `--pod-ip` (`POD_IP`) | | this pod's IP, excluded from peers (downward API) |
| `--snapshot` (`KUBEGLANCE_SNAPSHOT`) | | optional gzip JSON file to persist the history to |
| `--snapshot-every` (`KUBEGLANCE_SNAPSHOT_EVERY`) | `1m` | how often to write it |
| `--shutdown-grace` (`KUBEGLANCE_SHUTDOWN_GRACE`) | `5s` | keep serving after SIGTERM so a new replica can finish copying |
| `--bootstrap-timeout` (`KUBEGLANCE_BOOTSTRAP_TIMEOUT`) | `2m` | how long a starting replica retries copying the history while peers exist but none answers; it is not Ready meanwhile |
| `--auth-token-file` (`KUBEGLANCE_AUTH_TOKEN_FILE`) | | file with a token required on the UI, the API, `/metrics` and the peer copy; off when empty. See [Access](#access) |
| `--alert-webhook` (`KUBEGLANCE_ALERT_WEBHOOK`) | | URL that receives alerts; empty disables them. See [Alerts](#alerts) |
| `--alert-format` (`KUBEGLANCE_ALERT_FORMAT`) | `json` | `json` (every alert in one request) or `pushshell` (a pushshell notification per alert) |
| `--alert-headers-file` (`KUBEGLANCE_ALERT_HEADERS_FILE`) | | file with `Name: value` lines sent with every alert, such as an API key |
| `--alert-window` (`KUBEGLANCE_ALERT_WINDOW`) | `10m` | a rule fires when at least half of this window is at or above its threshold |
| `--alert-memory` (`KUBEGLANCE_ALERT_MEMORY`) | `0.9` | memory threshold, a fraction of a container's limit and a node's allocatable; `0` disables |
| `--alert-cpu` (`KUBEGLANCE_ALERT_CPU`) | `0.9` | the same for CPU |
| `--alert-language` (`KUBEGLANCE_ALERT_LANGUAGE`) | `en` | language of the alert text: `en` / `ja` |
| `--public-url` (`KUBEGLANCE_PUBLIC_URL`) | | where a phone reaches kubeglance; alerts link to `?pod=namespace/name` or `?node=name` there |
| `--kubeconfig` | | out-of-cluster; else in-cluster, `$KUBECONFIG`, `~/.kube/config` |
| `--log-level` (`KUBEGLANCE_LOG_LEVEL`) | `info` | `debug` / `info` / `warn` / `error` (JSON to stderr) |

## API

Everything the UI shows is JSON under `/api/v1`, with `?window=30m` (default) on each:

| path | view |
|---|---|
| `GET /api/v1/cluster` | totals, allocatable, requested, usage stats |
| `GET /api/v1/nodes` | per node: usage stats, ratio of allocatable, pods and requests scheduled; plus the imbalance (spread, stddev) across nodes |
| `GET /api/v1/workloads?namespace=` | per controller: pods, requests, limits, summed usage stats, per-pod usage, usage/request ratio |
| `GET /api/v1/pods?namespace=&node=&workload=ns/Kind/name` | per pod: usage stats, usage/limit and usage/request ratios; pods gone during the window are included and flagged |
| `GET /api/v1/pods/{namespace}/{name}` | one pod and each of its containers (spec order): usage stats against the container's own requests and limits; 404 when the pod is not in history |
| `GET /api/v1/status` | version, interval, retention, collector health |
| `GET /healthz`, `GET /readyz` | liveness; readiness once the peer copy is done and the first round is collected |
| `GET /metrics` | kubeglance's own metrics for Prometheus: polls, poll errors and duration, last success, current interval, history size (pods, nodes, series, samples, capacity). With `networkPolicy.enabled`, add your Prometheus to `networkPolicy.ingressFrom` |
| `GET /internal/snapshot` | the whole history (what peers copy) |

Statistics are computed from raw samples, never from averages of averages. `coverage` on every stats object is the fraction of the window that has samples; the UI shows gaps instead of silently shrinking the window.

Every row also carries a `spark` object for drawing: about 60 buckets (`bucket` wide, from `start`, aligned to the clock so a refresh slides the picture instead of reshaping it), each the mean of its samples, or `null` for a gap. It is a picture only; no statistic is derived from it. Responses are gzipped when the client sends `Accept-Encoding: gzip`.

## Memory

A sample is stored in 16 bytes (the instant in whole seconds, CPU millicores, memory bytes; a test keeps it at 16). At 15 s, 24 h is 5,761 samples per entity, 7 days 40,321 (measured heap):

| pods | history (24 h) | history (7 d) |
|---|---|---|
| 50 | ~5 MB | ~32 MB |
| 100 | ~10 MB | ~65 MB |
| 300 | ~30 MB | ~195 MB |

A pod with more than one container keeps one more series per container for the Pod screen's breakdown (a single-container pod's series already is its container's), so a pod with one sidecar costs three series instead of one.

On top of that come the Go runtime, the HTTP server and the client-go caches (10–20 MB). The chart requests 64 Mi and limits 128 Mi, which fits a Raspberry Pi k3s. Lower `--retention` to shrink it; the window selector is capped to the retention.

There is no downsampling: every statistic, over any window, is computed from the raw samples. Memory therefore grows linearly with `--retention`; when you raise it, raise `resources.limits.memory` with it (for example `retention: 7d` with 100 pods: ~65 MB of history plus the runtime, so `limits.memory: 192Mi`).

Handing the history to a starting replica costs a few MB, not a second copy of it: the snapshot is streamed a pod at a time on both ends. The chart also passes the memory limit to the Go runtime as a soft limit of 90 %, so the garbage collector works harder before garbage reaches the container limit. It is not a cap: the history itself is live memory, so size `limits.memory` from the table above.

## How updates keep the history

metrics-server returns instant values only, so a gap in polling is a gap forever. kubeglance avoids gaps without a database:

1. `replicas: 2`, `RollingUpdate` with `maxUnavailable: 0`, `maxSurge: 1`.
2. A new pod resolves the headless Service `<release>-peers` (which lists Ready replicas only), fetches `/internal/snapshot` from every other replica and merges them, then polls once and becomes Ready. If peers exist but none answers, it looks them up again and retries until `--bootstrap-timeout` instead of becoming Ready with an empty history; meanwhile `/healthz` answers, so it is not restarted.
3. Only then is an old pod terminated. It sleeps `preStopSleepSeconds` and keeps serving for `--shutdown-grace` after SIGTERM, so a copy in flight finishes.
4. Both replicas poll the same metrics-server, and every sample is recorded at its slot on an absolute grid of `--interval` (`:00`, `:15`, `:30`, `:45` for 15 s), not at the moment a replica happened to poll. Each replica polls in the middle of every slot, whenever it started. Two replicas therefore record the same instants, and merging their histories deduplicates instead of interleaving them; no leader, no consensus.

The optional `--snapshot` file (`snapshot.enabled` in the chart) is a safety net for the case where every replica is down at once (a cluster reboot). It is not what makes updates lossless, and with k3s's node-local storage it cannot be: that is the peer copy.

## Architecture

```
main.go              wires everything
pkg/domain           samples, the ring-buffer Series, Stats (avg / median / p95 / coverage), Store, aggregation, alert rules. No Kubernetes.
pkg/services         Collector (poll → store), Query (the four views), Bootstrap (peers, disk), Persister, Alerter
pkg/infrastructure   k8s (metrics.k8s.io poll, informers for pod → owner, node), peer (HTTP, alert sender election), snapshot (file), webhook (alerts)
pkg/interfaces       the command line (cobra: flags, env) and the HTTP API with the embedded UI (plain HTML / CSS / JS, no build, no CDN)
charts/kubeglance    Helm chart
internal/devserver   `mise run dev`: the UI with synthetic data, no cluster needed
```

The layers are enforced by [depgraph](https://github.com/nonchan7720/depgraph) (`.depgraph.yml`) and the per-edit checks by [claude-hook-gate](https://github.com/nonchan7720/claude-hook-gate) (`.claude/gate.yaml`).

## Development

```bash
mise install         # Go, golangci-lint, helm, goreleaser, kind, kubectl (pinned in mise.toml)
mise run ci          # lint (incl. govet) + test + helm lint + build + notices check
mise run notices     # regenerate THIRD_PARTY_NOTICES.md after adding a dependency
mise run dev         # UI with synthetic data at http://127.0.0.1:8080
mise run run         # against your current kubeconfig context
mise run e2e         # end-to-end on kind (needs Docker): see below
mise run depgraph:watch   # dependency graph + findings (needs Docker for Neo4j)
```

`mise run e2e` (`scripts/e2e.sh`, also the `e2e` CI job) creates a two-node kind cluster, installs metrics-server and the chart with two replicas and alerts to a test receiver, and runs `go test -tags e2e ./e2e/`: every owner kind resolves in the views, the poll interval follows metrics-server, a pod over 90 % of its memory limit is alerted once (not once per replica) and resolved once when it is deleted, and after a rolling update the new replicas still cover the last three minutes (the history copy). Everything runs from one helper image built from `e2e/helper`, so nothing but kind's node image and metrics-server is pulled. Set `E2E_KEEP=1` to keep the cluster.

Releases: release-please opens a release PR from Conventional Commits on `main`; merging it tags `vX.Y.Z` and goreleaser publishes binaries, multi-arch images (`ghcr.io/nonchan7720/kubeglance`) and the Helm chart (`oci://ghcr.io/nonchan7720/charts/kubeglance`). Every other push to `main` publishes a `vX.Y.Z-dev.<sha>` prerelease.

## License

Apache-2.0. See [LICENSE](LICENSE).
