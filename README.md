# kubeglance

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

Binaries for linux / darwin / windows (amd64, arm64, armv7) are on [Releases](https://github.com/nonchan7720/kubeglance/releases); run `kubeglance --kubeconfig ~/.kube/config` outside the cluster.

## Configuration

Flags, or the environment variable in brackets; a flag on the command line wins over the variable, and a malformed variable stops the start-up instead of falling back to the default. `kubeglance --help` lists them, `kubeglance --version` prints the version. The Helm chart exposes the useful ones in `values.yaml`.

| flag | default | meaning |
|---|---|---|
| `--addr` (`KUBEGLANCE_ADDR`) | `:8080` | listen address |
| `--interval` (`KUBEGLANCE_INTERVAL`) | `15s` | poll interval. metrics-server refreshes every 15 s; shorter reads the same numbers twice |
| `--retention` (`KUBEGLANCE_RETENTION`) | `24h` | history kept in memory; the maximum window |
| `--peer-host` (`KUBEGLANCE_PEER_HOST`) | | headless Service of the replicas; empty disables the peer copy |
| `--peer-port` (`KUBEGLANCE_PEER_PORT`) | `8080` | port peers serve on |
| `--pod-ip` (`POD_IP`) | | this pod's IP, excluded from peers (downward API) |
| `--snapshot` (`KUBEGLANCE_SNAPSHOT`) | | optional gzip JSON file to persist the history to |
| `--snapshot-every` (`KUBEGLANCE_SNAPSHOT_EVERY`) | `1m` | how often to write it |
| `--shutdown-grace` (`KUBEGLANCE_SHUTDOWN_GRACE`) | `5s` | keep serving after SIGTERM so a new replica can finish copying |
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
| `GET /healthz`, `GET /readyz` | liveness; readiness once the first round is collected |
| `GET /internal/snapshot` | the whole history (what peers copy) |

Statistics are computed from raw samples, never from averages of averages. `coverage` on every stats object is the fraction of the window that has samples; the UI shows gaps instead of silently shrinking the window.

Every row also carries a `spark` object for drawing: about 60 buckets (`bucket` wide, from `start`, aligned to the clock so a refresh slides the picture instead of reshaping it), each the mean of its samples, or `null` for a gap. It is a picture only; no statistic is derived from it. Responses are gzipped when the client sends `Accept-Encoding: gzip`.

## Memory

A sample is 16 bytes (CPU millicores + memory bytes; the timestamp is shared by the round). At 15 s, 24 h is 5,761 samples per entity:

| pods | history (24 h) |
|---|---|
| 50 | ~5 MB |
| 100 | ~9 MB |
| 300 | ~28 MB |

A pod with more than one container keeps one more series per container for the Pod screen's breakdown (a single-container pod's series already is its container's), so a pod with one sidecar costs three series instead of one.

On top of that come the Go runtime, the HTTP server and the client-go caches (10–20 MB). The chart requests 64 Mi and limits 128 Mi, which fits a Raspberry Pi k3s. Lower `--retention` to shrink it; the window selector is capped to the retention.

## How updates keep the history

metrics-server returns instant values only, so a gap in polling is a gap forever. kubeglance avoids gaps without a database:

1. `replicas: 2`, `RollingUpdate` with `maxUnavailable: 0`, `maxSurge: 1`.
2. A new pod resolves the headless Service `<release>-peers`, fetches `/internal/snapshot` from every other replica, merges it, then polls once and becomes Ready.
3. Only then is an old pod terminated. It sleeps `preStopSleepSeconds` and keeps serving for `--shutdown-grace` after SIGTERM, so a copy in flight finishes.
4. Both replicas poll the same metrics-server, so their histories are identical by construction; no leader, no consensus.

The optional `--snapshot` file (`snapshot.enabled` in the chart) is a safety net for the case where every replica is down at once (a cluster reboot). It is not what makes updates lossless, and with k3s's node-local storage it cannot be: that is the peer copy.

## Architecture

```
main.go              wires everything
pkg/domain           samples, the ring-buffer Series, Stats (avg / median / p95 / coverage), Store, aggregation. No Kubernetes.
pkg/services         Collector (poll → store), Query (the four views), Bootstrap (peers, disk), Persister
pkg/infrastructure   k8s (metrics.k8s.io poll, informers for pod → owner, node), peer (HTTP), snapshot (file)
pkg/interfaces       the command line (cobra: flags, env) and the HTTP API with the embedded UI (plain HTML / CSS / JS, no build, no CDN)
charts/kubeglance    Helm chart
internal/devserver   `mise run dev`: the UI with synthetic data, no cluster needed
```

The layers are enforced by [depgraph](https://github.com/nonchan7720/depgraph) (`.depgraph.yml`) and the per-edit checks by [claude-hook-gate](https://github.com/nonchan7720/claude-hook-gate) (`.claude/gate.yaml`).

## Development

```bash
mise install         # Go, golangci-lint, helm, goreleaser (pinned in mise.toml)
mise run ci          # lint (incl. govet) + test + helm lint + build
mise run dev         # UI with synthetic data at http://127.0.0.1:8080
mise run run         # against your current kubeconfig context
mise run depgraph:watch   # dependency graph + findings (needs Docker for Neo4j)
```

Releases: release-please opens a release PR from Conventional Commits on `main`; merging it tags `vX.Y.Z` and goreleaser publishes binaries, multi-arch images (`ghcr.io/nonchan7720/kubeglance`) and the Helm chart (`oci://ghcr.io/nonchan7720/charts/kubeglance`). Every other push to `main` publishes a `vX.Y.Z-dev.<sha>` prerelease.

## License

Apache-2.0. See [LICENSE](LICENSE).
