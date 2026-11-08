package domain

import (
	"cmp"
	"slices"
	"time"
)

// Alert rules. Each compares one resource of one target with a total: a
// container with its limit (memory: the OOM kill; CPU: throttling), a node
// with its allocatable.
const (
	RuleContainerMemory = "container-memory"
	RuleContainerCPU    = "container-cpu"
	RuleNodeMemory      = "node-memory"
	RuleNodeCPU         = "node-cpu"
)

// Resource names a Condition compares.
const (
	ResourceCPU    = "cpu"
	ResourceMemory = "memory"
)

// AlertKey identifies one rule on one target: a container (Namespace, Pod,
// Container) or a node (Node).
type AlertKey struct {
	Rule      string `json:"rule"`
	Namespace string `json:"namespace,omitempty"`
	Pod       string `json:"pod,omitempty"`
	Container string `json:"container,omitempty"`
	Node      string `json:"node,omitempty"`
}

func (k AlertKey) String() string {
	if k.Node != "" {
		return k.Rule + "/" + k.Node
	}
	return k.Rule + "/" + k.Namespace + "/" + k.Pod + "/" + k.Container
}

// Condition is what a rule checks: one resource against Threshold × Total.
type Condition struct {
	Resource  string  `json:"resource"`  // ResourceCPU or ResourceMemory
	Total     int64   `json:"total"`     // the limit or allocatable; 0: nothing to compare with, never fires
	Threshold float64 `json:"threshold"` // fraction of Total; 0 disables the rule
}

// Verdict is one evaluation of a Condition over a window.
type Verdict struct {
	Firing    bool      `json:"firing"`
	Above     int       `json:"above"` // samples at or above the threshold
	Condition Condition `json:"condition"`
	Stats     Stats     `json:"stats"` // of the window, as every view shows it
}

// Evaluate checks whether the resource stayed at or above the threshold for
// at least half of the window: the samples at or above it must be at least
// half of the samples a gap-free window holds (Stats.Expected). A gap counts
// as below, so missing samples can delay an alert but never raise one, and
// the window never silently shrinks to the samples it has. Half, not all:
// a sustained problem fires even with a dip or two, a single spike does not.
func Evaluate(samples []Sample, w Window, c Condition) Verdict {
	v := Verdict{Condition: c, Stats: ComputeStats(samples, w)}
	if c.Total <= 0 || c.Threshold <= 0 || (c.Resource != ResourceCPU && c.Resource != ResourceMemory) {
		return v
	}
	limit := c.Threshold * float64(c.Total)
	for _, s := range samples {
		if float64(c.of(s.Usage)) >= limit {
			v.Above++
		}
	}
	v.Firing = v.Stats.Expected > 0 && v.Above > 0 && 2*v.Above >= v.Stats.Expected
	return v
}

func (c Condition) of(r Resource) int64 {
	if c.Resource == ResourceCPU {
		return r.CPU
	}
	return r.Memory
}

// AlertStatus is what a notification says about an alert.
type AlertStatus string

// The two statuses a receiver is told.
const (
	AlertFiring   AlertStatus = "firing"
	AlertResolved AlertStatus = "resolved"
)

// Alert is a change a receiver has to be told about.
type Alert struct {
	Key    AlertKey    `json:"key"`
	Status AlertStatus `json:"status"`
	// Since is the evaluation that started the firing; for a resolved alert
	// too, so At - Since is how long it fired.
	Since time.Time `json:"since"`
	At    time.Time `json:"at"` // the evaluation this alert describes
	// Verdict is the latest evaluation; for a target that is gone, the last
	// one it had.
	Verdict Verdict `json:"verdict"`
	Gone    bool    `json:"gone"` // resolved because the target no longer exists
}

// Alerts tracks which alerts fire and what the receiver has been told. It
// is level-triggered: Pending lists every alert whose state differs from
// what was delivered, so a failed delivery is simply retried with the next
// evaluation, and a firing that resolves before it was delivered is never
// sent at all. Not safe for concurrent use.
type Alerts struct {
	m map[AlertKey]*alertEntry
}

type alertEntry struct {
	firing   bool
	notified bool // the receiver was told it fires
	gone     bool
	since    time.Time
	at       time.Time
	verdict  Verdict
}

// NewAlerts returns an empty tracker.
func NewAlerts() *Alerts { return &Alerts{m: map[AlertKey]*alertEntry{}} }

// Observe records one evaluation at `at`. verdicts holds every target that
// still exists; an alert whose target is not in it resolves as gone.
func (a *Alerts) Observe(at time.Time, verdicts map[AlertKey]Verdict) {
	for k, v := range verdicts {
		e := a.m[k]
		if e == nil {
			if !v.Firing {
				continue
			}
			e = &alertEntry{}
			a.m[k] = e
		}
		if v.Firing && !e.firing && !e.notified {
			e.since = at // a new firing; a re-fire the receiver never saw end keeps its start
		}
		e.firing, e.gone, e.at, e.verdict = v.Firing, false, at, v
	}
	for k, e := range a.m {
		if _, ok := verdicts[k]; !ok && e.firing {
			e.firing, e.gone, e.at = false, true, at
		}
	}
	a.compact()
}

// Pending returns the alerts the receiver has not been told about, ordered
// by key.
func (a *Alerts) Pending() []Alert {
	var out []Alert
	for k, e := range a.m {
		if e.firing == e.notified {
			continue
		}
		al := Alert{Key: k, Status: AlertResolved, Since: e.since, At: e.at, Verdict: e.verdict, Gone: e.gone}
		if e.firing {
			al.Status = AlertFiring
		}
		out = append(out, al)
	}
	slices.SortFunc(out, func(x, y Alert) int { return cmp.Compare(x.Key.String(), y.Key.String()) })
	return out
}

// Delivered records that the receiver was told these alerts. An alert whose
// status has changed since Pending returned it is ignored: the new status
// is still pending.
func (a *Alerts) Delivered(alerts []Alert) {
	for _, al := range alerts {
		e := a.m[al.Key]
		if e == nil || e.firing != (al.Status == AlertFiring) {
			continue
		}
		e.notified = e.firing
	}
	a.compact()
}

// compact drops what nobody needs to hear about: a calm target the receiver
// does not believe fires.
func (a *Alerts) compact() {
	for k, e := range a.m {
		if !e.firing && !e.notified {
			delete(a.m, k)
		}
	}
}
