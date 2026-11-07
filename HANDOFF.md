# HANDOFF — kubeglance

Context for whoever (or whatever) picks this up next. Read [CLAUDE.md](CLAUDE.md) first: it holds the role, the product focus and the quality bar. This file holds *where things are* and *why they are that way*.

## What this is

A single-binary, in-memory, mobile-first resource viewer for Kubernetes, aimed at the CNCF Landscape (*Observability and Analysis → Monitoring*). The owner's pain: on a k3s homelab deployed by Argo CD, "which node / pod is eating resources" is not answerable from a phone in ten seconds; Grafana Cloud is too heavy for the question.

The design was settled in conversation with the owner, and these decisions are **fixed** unless the owner reopens them:

| decision | reason |
|---|---|
| Four levels, each tied to one decision (Cluster → headroom, Node → rebalance, Workload → scale, Pod → hot spot) | the owner wanted decision support, not a dashboard |
| Now / 30-min avg / median / p95, window selectable | "real-time plus 30 min average and median" was the owner's minimum |
| History in a process-local ring buffer, no external DB | owner approved the memory estimate (~10 MB / 100 pods / 24 h) |
| Zero-downtime update without clustering: `replicas: 2` + peer snapshot copy on start + preStop/shutdown grace | metrics-server is a shared source of truth, so replicas need no consensus |
| On-disk snapshot is an *optional safety net*, not the mechanism | k3s local-path storage is RWO + node-bound, so it cannot make rolling updates lossless |
| metrics-server polled at 15 s, never faster; owner mapping via informers | matches metrics-server's resolution; no load on kubelets |
| Go, layered `pkg/{domain,services,infrastructure,interfaces}`, mise, golangci-lint 2.15, release-please, goreleaser, renovate | the owner's house style (same as nonchan7720/depgraph) |
| Quality enforced by claude-hook-gate (`.claude/gate.yaml`) and depgraph (`.depgraph.yml`) | the owner's own tools; keep them green |

## State of the code (as of this hand-off)

Everything below builds, passes `go vet`, `golangci-lint run` (2.15.0, 0 issues), `go test -race ./...` (the test task enables cgo for the race detector only; the binary stays `CGO_ENABLED=0`), `helm lint` and `helm template`.

| area | file(s) | status |
|---|---|---|
| Domain: Series ring buffer, Stats, Store, Snapshot/Merge, SumByInstant, Imbalance | `pkg/domain/*.go` | done, tested (85 % coverage) |
| Collector / Query (four views) / Bootstrap / Persister | `pkg/services/*.go` | done, tested |
| metrics.k8s.io poll, informers with owner resolution (RS→Deployment, Job→CronJob, STS, DS, static pods) | `pkg/infrastructure/k8s/*.go` | done, tested with the fake clientset |
| Peer discovery (headless Service) + snapshot fetch | `pkg/infrastructure/peer/peer.go` | done, tested with httptest |
| Snapshot file (gzip JSON, atomic rename) | `pkg/infrastructure/snapshot/file.go` | done, tested |
| HTTP API + embedded UI (4 screens, drill-down node→pods, workload→pods, window selector, PWA manifest, en/ja i18n) | `pkg/interfaces/handler/**` | done; UI checked by screenshot at 390 px (dark, en and ja) |
| `main.go` wiring, graceful shutdown; cobra command, flags/env | `main.go`, `pkg/interfaces/cmd.go` | done |
| Synthetic-data dev server (`mise run dev`) | `internal/devserver/main.go` | done |
| Helm chart (Deployment w/ RollingUpdate + preStop sleep, Service + headless `-peers`, RBAC read-only, optional PVC, Ingress) | `charts/kubeglance` | done, lints and renders |
| CI (`ci.yaml`: parallel jobs `ci` = golangci-lint-action + test + build, `helm`, `depgraph` on compose Neo4j), `check-pull-request-title.yaml` (status check `pr-title`), `auto-approve.yaml` (owner + Renovate patch PRs), `release.yaml` (release-please with a GitHub App token → goreleaser stable / `-dev.<sha>` prerelease → ghcr images + OCI chart) — all in the shape of nonchan7720/manifold | `.github/workflows/*.yaml`, `release-please/` | written; needs the `RELEASE_PLEASE_*` secrets, see TODO |
| README (en/ja) | `README.md`, `README_ja.md` | written |

**Not yet done**: see [TODO.md](TODO.md). The most important gap is that nothing has been run against a real cluster.

## How to work here

```bash
mise install && mise run ci     # must be green before any PR
mise run dev                    # UI with synthetic data at :8080, no cluster
mise run run                    # real cluster via your kubeconfig
```

- Work on a branch, open a **draft PR** with a Conventional Commits title; never push to `main`.
- claude-hook-gate runs fmt/vet/package tests after every edit and the full `ci` before you stop. If `depgraph watch --root .` is running, it also blocks on new import cycles, layer violations and duplicated code. Do not weaken `.claude/gate.yaml` to get past a failure.
- Tests first. The domain package is pure; its statistics are checked against hand-computed values (see `pkg/domain/stats_test.go`): keep that style.
- Edit `README.md` and `README_ja.md` together (a gate rule fails when only one changed).
- Pin everything (mise.toml, go.mod, action SHAs). Renovate enforces 7-day release age.
- No `fmt.Print*` outside `main.go`; log with `slog`.

## Things that are easy to get wrong

- **`Series.Add` ignores a sample that is not newer than the last.** Merging (peer / disk) goes through `mergeSeries`, which sorts + dedupes by instant. Keep it that way; do not "fix" `Add` to accept out-of-order samples.
- **Aggregation relies on every entity of one poll round sharing `Round.At`.** `convertRound` truncates to the second for that reason. If you ever add a second source, give it its own round timestamp, never per-pod timestamps.
- **A deleted pod stays in the store until `Prune` drops it after the retention**, so a workload's history survives a rollout. `Query.Workloads` counts only live pods in `Pods` but sums every pod's samples. That is intentional.
- **Readiness = first round recorded.** During a rolling update the peer copy happens *before* the first poll, so a Ready replica always has the full window.
- **The UI is embedded at build time** (`go:embed`). `go run ./internal/devserver` must be restarted to see static changes.
- **UI strings live in `DICT` at the top of `app.js`** (en + ja, same keys). Every visible string goes through `t()`; static HTML uses `data-i18n` / `data-i18n-html`. Adding a language = adding one object.
- **`Limits` is zeroed when any container has no limit** (`podInfo`): a pod is only as limited as its least limited container. The UI then shows "% of request" instead.

## Branch rulesets

Copied from manifold: **CI check** (required status checks `ci` and `pr-title` on `main` and `release/v*`, no deletion / force push) and **PR Approve** (one approval, squash only; bypass for admins, Renovate and the release app). `auto-approve.yaml` is what satisfies the approval for the owner's own PRs. The `pr-title` job id must not be renamed without updating the ruleset.

## Repository bootstrap

This repository was created empty by the owner; the scaffold was pushed as the first commit on `main` because there was no base branch for a PR. From here on, PRs only.
