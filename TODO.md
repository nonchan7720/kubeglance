# TODO

Ordered by what unblocks the most. Tick items in the PR that does them; keep this file honest.

## 0. Prove it on a real cluster (blocks everything else)

A Claude Code cloud container cannot do this part: `dockerd` starts and the k3s server in Docker comes up, but every pod sandbox fails (`runc create failed: can't get final child's PID from pipe`), so metrics-server never runs. It has to be the owner's k3s (or any machine with a working nested runtime).

- [ ] Run `mise run run` against a k3s cluster with metrics-server; confirm all four views populate and owner resolution is right (Deployment / StatefulSet / DaemonSet / CronJob / static pods / an operator-owned pod).
- [ ] `helm install` from `charts/kubeglance` with `replicaCount: 2`; confirm `/readyz` flips only after the peer copy + first poll.
- [ ] Rolling update test: change the image tag, watch `kubectl get pods -w`, then check `GET /api/v1/cluster?window=1h` has `coverage: 1` on the new pod. This is the headline claim of the README; it must be measured, not assumed.
- [ ] Measure real memory with `kubectl top pod` after 24 h and update the README table if it is off.
- [ ] Confirm the preStop `sleep` action works on the owner's k3s version (needs ≥ 1.30); otherwise document `preStopSleepSeconds: 0` more loudly.

## 1. First release

- [ ] Repository secrets `RELEASE_PLEASE_CLIENT_ID` and `RELEASE_PLEASE_APP_PRIVATE_PEM` (the same GitHub App as manifold, installed on this repo): release-please and goreleaser run with its token. Until they exist the `Release please` workflow fails.
- [ ] Watch the first `Release please` run on `main` (prerelease path) and the first release PR; fix what breaks (the workflows were ported from manifold and have not run here yet).
- [ ] Make sure `GITHUB_TOKEN` can push to `ghcr.io/nonchan7720/kubeglance` and `ghcr.io/nonchan7720/charts` (package visibility, "Inherit access from source repository").
- [ ] Tag `v0.1.0` via release-please; verify the multi-arch image (`amd64`, `arm64`, `armv7` for Raspberry Pi) and `helm install oci://...` from a clean machine.
- [x] Add `icon` to `Chart.yaml` (helm lint INFO) and a logo SVG under `docs/` (the CNCF Landscape needs an SVG logo anyway). `docs/logo.svg` is the UI icon; a test keeps them identical. The Landscape prefers a logo that includes the name: a wordmark (with the text converted to paths) is still to do. The mark is a kingfisher (the "flash of blue": it watches from its perch and strikes in an instant), in Kubernetes blue.
- [x] `THIRD_PARTY_NOTICES.md` generation (go-licenses, every release GOOS) and a CI check that it is current and has no forbidden / restricted license. In the release archives and in the image under `/licenses/`.

## 2. Product gaps the owner will hit first

- [x] **Sparkline per row**: the numbers exist (`Series`); a tiny inline SVG of the window per bar makes "has it been like this for 30 min" visible at a glance. Keep it dependency-free.
- [x] **Namespace filter** in the Workloads / Pods tabs. Done client-side: the API already returns every row, so the namespace list stays complete without another request.
- [x] **Sort toggle** (CPU / memory / name) on Nodes, Workloads, Pods. Nodes sort by ratio of allocatable, workloads and pods by usage now.
- [x] **Container-level breakdown** on the Pod screen: tap a pod for its containers (`GET /api/v1/pods/{namespace}/{name}`). Per-container series are kept for multi-container pods only.
- [ ] **Downsampling for long retention**: raw 15 s for the last hour, 1-min means beyond, so `--retention=7d` stays small. Design note: stats over a downsampled range must be labelled as such (`coverage` already exists; add `resolution`).
- [x] **Gap rendering**: empty sparkline buckets are shaded, never bridged.
- [x] **Light theme polish**: checked at 375 px in light and dark, en and ja (avg marker colour, ja tab labels, stats row wrapping, `[hidden]` vs `display: flex`).
- [x] **i18n**: a test that `DICT.en` and `DICT.ja` have the same keys (a Go test can read the embedded `app.js` and compare), like claude-hook-gate does for its locales.

## 3. Operations

- [ ] Optional **basic auth / bearer token** flag for people who expose it without a tunnel (default off; document that Tailscale / Cloudflare Access is the recommended way).
- [ ] `/metrics` Prometheus endpoint for kubeglance itself (poll duration, rounds, store size) — small, and expected in the Landscape's Monitoring category.
- [x] PodDisruptionBudget (`minAvailable: 1`) in the chart when `replicaCount > 1`.
- [x] NetworkPolicy template (ingress from the replicas + `networkPolicy.ingressFrom`; egress to DNS, the API server and the replicas). Off by default. `charts/tests/render.sh` checks the renders; it has not been applied to a real CNI yet, see §0.
- [x] Structured startup log line that prints the effective config (`starting` → `config`; a test fails when a `Config` field is not logged).

- [x] Poll interval follows metrics-server's `--metric-resolution` (read from the `window` every poll reports), also when it changes while kubeglance runs, so managed clusters with another resolution are polled at theirs. `--interval` fixes it.

## 4. Toward the CNCF Landscape

Entry criteria (checked 2026-11): ~300 GitHub stars, fits an existing category (*Observability and Analysis → Monitoring*), SVG logo, active commits (removed after 3 months idle). The PR goes to `cncf/landscape` (`landscape.yml` + `hosted_logos/kubeglance.svg`).

- [ ] Screenshots / a 15-second GIF of the phone UI in the README (people star what they can see).
- [ ] Zenn / note article (Japanese) and a Reddit r/homelab + r/kubernetes post (English): "I built a 10 MB replacement for the one Grafana dashboard I actually looked at".
- [ ] Submit to awesome-kubernetes, awesome-k3s, k3s discussions, Argo CD examples.
- [ ] Add a `docs/comparison.md`: kubeglance vs `kubectl top` / k9s / Headlamp / Kubernetes Dashboard / Grafana (honest, including what we don't do).
- [ ] Once ≥ 300 stars and 2+ releases: open the `cncf/landscape` PR.

## Known limitations (document, maybe never fix)

- No authentication built in (by design for v0; see 3).
- Pods with no limits show "% of request"; pods with neither show raw numbers only.
- metrics-server's own ~15 s staleness means "now" is up to ~30 s old.
- With `replicaCount: 1`, a restart loses the history unless `snapshot.enabled`.
