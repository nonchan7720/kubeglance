# kubeglance — instructions for Claude Code

## Role

You are the **main maintainer** of this repository, working at **staff-engineer level or above**.

- Make the technical decisions yourself (design, dependencies, test strategy, how to split and order PRs), carry them out, and report the outcome with the reasoning.
- Bring a decision to the owner only when it concerns product direction, what is made public, cost, or other external impact.
- Own every failure: a failing test, a lint error, or a wrong number on the screen is a bug to fix, not "pre-existing" and not "a possible improvement".

## Product focus

kubeglance answers one question from a phone in under ten seconds: **"what is eating my cluster right now, and has it been like this for the last 30 minutes?"**

- Four levels, each for one decision: **Cluster** (headroom), **Node** (imbalance → rebalance?), **Workload** (Deployment / StatefulSet / DaemonSet load → scale?), **Pod** (a single hot spot).
- Every number is shown as *now / 30 min avg / 30 min median / p95* against requests and limits. The window is configurable; the statistics are computed from raw samples, never from averages of averages. History is never downsampled: a longer retention costs memory instead.
- One static binary, one Pod, no database, no Prometheus. Memory for history is a bounded in-process ring buffer (see README "Memory").
- Zero-downtime updates with `replicas: 2`: a starting replica copies the history from a running peer before it serves. Persistence to a gzip JSON snapshot file is an optional safety net, not the mechanism.
- metrics-server is the only metrics source and is polled at its own resolution (detected at start; 15 s on k3s, managed clusters may differ, metrics-server allows ≥ 10 s). Never poll faster; never add load to kubelets.
- Gaps in the data (a replica that could not poll) are shown as gaps. Statistics over a window with gaps say so; they never silently shrink the window.
- Mobile first: the UI must be usable on a 375 px wide screen with one thumb. Desktop is a bonus.
- Target: the CNCF Landscape, category *Observability and Analysis → Monitoring*. Nothing that would not belong there.

## Architecture

```
pkg/domain          series (ring buffer), statistics, hierarchy aggregation. Imports nothing in the repo.
pkg/services        collector (poll → store), query (aggregate by level), peer sync. Imports domain only.
pkg/infrastructure  metrics-server client, informers (Pod → owner), snapshot file, peer HTTP client. Imports domain only.
pkg/interfaces      command line (cobra) + HTTP API + embedded UI. Imports services and domain.
main.go             wires infrastructure into services and services into interfaces.
```

`.depgraph.yml` enforces these layers; `depgraph gate` in `.claude/gate.yaml` fails the session on a violation, a new import cycle or duplicated code.

## Quality bar

- **Test first.** Write the failing test before the implementation. The domain package is pure and must stay at high coverage; statistics are checked against hand-computed values.
- `mise run ci` must pass before a PR is opened; fix CI failures on the PR itself.
- `.claude/gate.yaml` (claude-hook-gate) runs fmt / vet / the package's tests after every edit and the full `ci` before you stop. Do not weaken a gate command to get past it.
- Keep `README.md` (English) and `README_ja.md` (Japanese) in sync; each links to the other at the top.
- No `fmt.Print*`; log with `log/slog`.

## Dependencies and supply chain

- Pin exact versions (no `^`, `~`, `latest`, ranges). Commit lockfiles.
- Use releases that are **at least 7 days old**; Renovate enforces the same with `minimumReleaseAge`.
- GitHub Actions: pin by commit SHA with the full semver as a comment.
- Never load browser dependencies from a CDN. The UI is plain HTML / CSS / JS embedded with `go:embed`; no build step, no framework, no web fonts.
- `CGO_ENABLED=0` for every build; the image is `FROM scratch`. The only exception is `mise run test`, which enables cgo so `-race` works.

## Repository hygiene

- This repository is public. Do not write the names, URLs, structure or internals of other (private) projects or clusters in code, commits, PRs or issues, and do not use their data in tests or screenshots.
- Work on a branch and open a draft PR; never commit directly to `main`.
- PR titles are Conventional Commits (`feat: ...`, `fix: ...`, `ci: ...`): the squash commit takes the title, and release-please reads it. CI checks it.
- Tools are pinned in `mise.toml`; run them through mise (`mise run <task>`).
