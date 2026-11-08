package services

import (
	"context"
	"log/slog"
	"time"

	"github.com/nonchan7720/kubeglance/pkg/domain"
)

// AlertPolicy is what the alert rules check.
type AlertPolicy struct {
	// Window is how far back each evaluation looks; a rule fires when at
	// least half of it is at or above the threshold (domain.Evaluate).
	Window time.Duration
	// Memory and CPU are thresholds as a fraction of a container's limit and
	// of a node's allocatable; 0 disables the resource's rules.
	Memory float64
	CPU    float64
}

// Alerter evaluates the alert rules over the store after every round and
// hands what changed to a Notifier.
//
// Every replica evaluates; only the elected sender delivers. The histories
// of the replicas are identical (every sample lives at its slot), so they
// reach the same verdicts, and a replica that is not the sender records what
// the sender delivered as delivered. When the sender goes away, the next one
// therefore repeats nothing. A replica that starts next to a running one
// likewise assumes that one told the receiver about what fires already.
type Alerter struct {
	store    *domain.Store
	notifier Notifier
	elect    SenderElection
	policy   AlertPolicy
	alerts   *domain.Alerts
	log      *slog.Logger
	now      func() time.Time
	settled  bool // the first round that had something to say is behind
}

// NewAlerter wires the rules to a store and a notifier.
func NewAlerter(store *domain.Store, n Notifier, elect SenderElection, p AlertPolicy, log *slog.Logger) *Alerter {
	if log == nil {
		log = slog.Default()
	}
	return &Alerter{store: store, notifier: n, elect: elect, policy: p, alerts: domain.NewAlerts(), log: log, now: time.Now}
}

// Run evaluates after every recorded round (Collector.Rounds) until ctx is
// done. A slow receiver delays the next evaluation, never the poll: rounds
// that arrive meanwhile collapse into one evaluation, over the store as it
// is then.
func (a *Alerter) Run(ctx context.Context, rounds <-chan struct{}) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-rounds:
			a.EvaluateOnce(ctx)
		}
	}
}

// EvaluateOnce evaluates every rule and delivers what changed. A failed
// delivery stays pending and is retried with the next evaluation.
func (a *Alerter) EvaluateOnce(ctx context.Context) {
	now := a.now()
	a.alerts.Observe(now, a.evaluate(now))
	pending := a.alerts.Pending()
	if len(pending) == 0 {
		a.settled = true
		return
	}
	sender, peers, err := a.elect(ctx)
	if err != nil {
		a.log.Warn("cannot tell whether this replica sends alerts; retrying next round", "err", err)
		return
	}
	first := !a.settled
	a.settled = true
	if !sender || (first && peers > 0) {
		a.alerts.Delivered(pending) // another replica delivers, or already did
		return
	}
	sent, err := a.notifier.Notify(ctx, pending)
	a.alerts.Delivered(pending[:sent])
	for _, al := range pending[:sent] {
		a.log.Info("alert sent", "status", al.Status, "alert", al.Key.String())
	}
	if err != nil {
		a.log.Warn("alert delivery failed; retrying next round", "pending", len(pending)-sent, "err", err)
	}
}

func (a *Alerter) window(now time.Time) domain.Window {
	return dueWindow(a.store, min(a.policy.Window, a.store.Retention()), now)
}

// evaluate returns a verdict for every enabled rule on every live container
// and node. A target without a limit (or allocatable) still gets a verdict,
// which never fires: an alert whose limit was removed resolves, it is not
// reported as gone.
func (a *Alerter) evaluate(now time.Time) map[domain.AlertKey]domain.Verdict {
	w := a.window(now)
	out := map[domain.AlertKey]domain.Verdict{}
	check := func(k domain.AlertKey, samples []domain.Sample, resource string, total int64, threshold float64) {
		if threshold > 0 {
			out[k] = domain.Evaluate(samples, w, domain.Condition{Resource: resource, Total: total, Threshold: threshold})
		}
	}
	for _, n := range a.store.Nodes() {
		if a.store.NodeDeleted(n.Name) {
			continue
		}
		samples := a.store.NodeSamples(n.Name, w.From)
		check(domain.AlertKey{Rule: domain.RuleNodeMemory, Node: n.Name}, samples, domain.ResourceMemory, n.Allocatable.Memory, a.policy.Memory)
		check(domain.AlertKey{Rule: domain.RuleNodeCPU, Node: n.Name}, samples, domain.ResourceCPU, n.Allocatable.CPU, a.policy.CPU)
	}
	for _, p := range a.store.Pods() {
		if a.store.PodDeleted(p.Key) {
			continue
		}
		// per-container series exist for multi-container pods only; a
		// single container's series is the pod's (Round.Containers)
		byName := a.store.ContainerSamples(p.Key, w.From)
		for _, c := range p.Containers {
			samples, ok := byName[c.Name]
			if !ok && len(p.Containers) == 1 {
				samples = a.store.PodSamples(p.Key, w.From)
			}
			k := domain.AlertKey{Namespace: p.Key.Namespace, Pod: p.Key.Name, Container: c.Name}
			k.Rule = domain.RuleContainerMemory
			check(k, samples, domain.ResourceMemory, c.Limits.Memory, a.policy.Memory)
			k.Rule = domain.RuleContainerCPU
			check(k, samples, domain.ResourceCPU, c.Limits.CPU, a.policy.CPU)
		}
	}
	return out
}
