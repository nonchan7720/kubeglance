package domain

import (
	"testing"
	"time"
)

// mems is samples every 15 s from t0 with the given memory (CPU = memory / 1000).
func mems(vals ...int64) []Sample {
	out := make([]Sample, len(vals))
	for i, v := range vals {
		out[i] = Sample{At: at(i * 15), Usage: Resource{CPU: v / 1000, Memory: v}}
	}
	return out
}

// one minute at 15 s, endpoints inclusive: 5 expected samples
var minute = Window{From: t0, To: at(60), Step: 15 * time.Second}

func TestEvaluateFiresWhenHalfTheWindowIsAbove(t *testing.T) {
	c := Condition{Resource: ResourceMemory, Total: 1000, Threshold: 0.9}
	// 3 of 5 expected samples at or above 900: 2*3 >= 5
	v := Evaluate(mems(950, 950, 950, 100, 100), minute, c)
	if !v.Firing || v.Above != 3 || v.Stats.Expected != 5 {
		t.Errorf("firing/above/expected = %v/%d/%d, want true/3/5", v.Firing, v.Above, v.Stats.Expected)
	}
	// 2 of 5: 2*2 < 5
	v = Evaluate(mems(950, 100, 950, 100, 100), minute, c)
	if v.Firing || v.Above != 2 {
		t.Errorf("firing/above = %v/%d, want false/2", v.Firing, v.Above)
	}
}

func TestEvaluateEvenWindowFiresAtExactlyHalf(t *testing.T) {
	// 45 s at 15 s = 4 expected; 2 above is half
	w := Window{From: t0, To: at(45), Step: 15 * time.Second}
	v := Evaluate(mems(100, 100, 950, 950), w, Condition{Resource: ResourceMemory, Total: 1000, Threshold: 0.9})
	if !v.Firing || v.Above != 2 || v.Stats.Expected != 4 {
		t.Errorf("firing/above/expected = %v/%d/%d, want true/2/4", v.Firing, v.Above, v.Stats.Expected)
	}
}

func TestEvaluateThresholdIsInclusive(t *testing.T) {
	v := Evaluate(mems(900, 900, 900, 0, 0), minute, Condition{Resource: ResourceMemory, Total: 1000, Threshold: 0.9})
	if v.Above != 3 || !v.Firing {
		t.Errorf("above/firing = %d/%v, want 3/true (900 of 1000 is 90%%)", v.Above, v.Firing)
	}
}

func TestEvaluateGapsCountAsBelow(t *testing.T) {
	// two minutes at 15 s = 9 expected, only 3 present, all above: the
	// window does not shrink to the 3 samples it has (2*3 < 9)
	w := Window{From: t0, To: at(120), Step: 15 * time.Second}
	v := Evaluate(mems(990, 990, 990), w, Condition{Resource: ResourceMemory, Total: 1000, Threshold: 0.9})
	if v.Firing || v.Above != 3 || v.Stats.Expected != 9 {
		t.Errorf("firing/above/expected = %v/%d/%d, want false/3/9", v.Firing, v.Above, v.Stats.Expected)
	}
}

func TestEvaluatePicksTheResource(t *testing.T) {
	// CPU 950 of 1000 (memory 950000 is irrelevant)
	v := Evaluate(mems(950000, 950000, 950000), Window{From: t0, To: at(30), Step: 15 * time.Second},
		Condition{Resource: ResourceCPU, Total: 1000, Threshold: 0.9})
	if !v.Firing || v.Above != 3 {
		t.Errorf("firing/above = %v/%d, want true/3", v.Firing, v.Above)
	}
}

func TestEvaluateNeverFiresWithoutSomethingToCompare(t *testing.T) {
	full := mems(990, 990, 990, 990, 990)
	for name, c := range map[string]Condition{
		"no total":     {Resource: ResourceMemory, Total: 0, Threshold: 0.9},
		"disabled":     {Resource: ResourceMemory, Total: 1000, Threshold: 0},
		"no such kind": {Resource: "disk", Total: 1000, Threshold: 0.9},
	} {
		if v := Evaluate(full, minute, c); v.Firing || v.Above != 0 {
			t.Errorf("%s: firing/above = %v/%d, want false/0", name, v.Firing, v.Above)
		}
	}
	// no step: the window cannot say how many samples it should hold
	if v := Evaluate(full, Window{From: t0, To: at(60)}, Condition{Resource: ResourceMemory, Total: 1000, Threshold: 0.9}); v.Firing {
		t.Error("fired without an expected sample count")
	}
}

func TestEvaluateKeepsTheStats(t *testing.T) {
	v := Evaluate(mems(950, 950, 950, 100, 100), minute, Condition{Resource: ResourceMemory, Total: 1000, Threshold: 0.9})
	// sorted 100 100 950 950 950: median 950, p95 950, now 100, avg 610
	if v.Stats.Now.Memory != 100 || v.Stats.Median.Memory != 950 || v.Stats.P95.Memory != 950 || v.Stats.Avg.Memory != 610 {
		t.Errorf("stats = %+v", v.Stats)
	}
	if v.Condition.Total != 1000 || v.Condition.Threshold != 0.9 {
		t.Errorf("condition = %+v", v.Condition)
	}
}

func TestAlertKeyString(t *testing.T) {
	for k, want := range map[AlertKey]string{
		{Rule: RuleContainerMemory, Namespace: "default", Pod: "api-1", Container: "app"}: "container-memory/default/api-1/app",
		{Rule: RuleNodeCPU, Node: "node-1"}:                                               "node-cpu/node-1",
	} {
		if got := k.String(); got != want {
			t.Errorf("String() = %q, want %q", got, want)
		}
	}
}

var (
	podMem  = AlertKey{Rule: RuleContainerMemory, Namespace: "default", Pod: "api-1", Container: "app"}
	nodeCPU = AlertKey{Rule: RuleNodeCPU, Node: "node-1"}
	firing  = Verdict{Firing: true, Above: 3}
	calm    = Verdict{Above: 0}
)

func pendingOf(t *testing.T, a *Alerts, n int) []Alert {
	t.Helper()
	p := a.Pending()
	if len(p) != n {
		t.Fatalf("pending = %d (%+v), want %d", len(p), p, n)
	}
	return p
}

func TestAlertsFireOnceUntilResolved(t *testing.T) {
	a := NewAlerts()
	a.Observe(at(0), map[AlertKey]Verdict{podMem: firing})
	p := pendingOf(t, a, 1)
	if p[0].Key != podMem || p[0].Status != AlertFiring || !p[0].Since.Equal(at(0)) || !p[0].At.Equal(at(0)) {
		t.Errorf("pending = %+v", p[0])
	}
	a.Delivered(p)
	pendingOf(t, a, 0)

	a.Observe(at(15), map[AlertKey]Verdict{podMem: firing})
	pendingOf(t, a, 0) // still firing: nothing new to say

	a.Observe(at(30), map[AlertKey]Verdict{podMem: calm})
	p = pendingOf(t, a, 1)
	if p[0].Status != AlertResolved || !p[0].Since.Equal(at(0)) || !p[0].At.Equal(at(30)) || p[0].Gone {
		t.Errorf("resolved = %+v, want since t0, at 30 s, not gone", p[0])
	}
	a.Delivered(p)
	pendingOf(t, a, 0)
	if len(a.m) != 0 {
		t.Errorf("state kept after the resolve was delivered: %+v", a.m)
	}
}

func TestAlertsCalmTargetsKeepNoState(t *testing.T) {
	a := NewAlerts()
	a.Observe(at(0), map[AlertKey]Verdict{podMem: calm, nodeCPU: calm})
	pendingOf(t, a, 0)
	if len(a.m) != 0 {
		t.Errorf("state for calm targets: %+v", a.m)
	}
}

func TestAlertsGoneTargetResolves(t *testing.T) {
	a := NewAlerts()
	a.Observe(at(0), map[AlertKey]Verdict{podMem: firing})
	a.Delivered(a.Pending())
	a.Observe(at(15), map[AlertKey]Verdict{}) // the pod is deleted
	p := pendingOf(t, a, 1)
	if p[0].Status != AlertResolved || !p[0].Gone || !p[0].At.Equal(at(15)) || p[0].Verdict.Above != 3 {
		t.Errorf("gone = %+v, want resolved, gone, at 15 s, the last verdict kept", p[0])
	}
}

func TestAlertsRetryUntilDelivered(t *testing.T) {
	a := NewAlerts()
	a.Observe(at(0), map[AlertKey]Verdict{podMem: firing})
	pendingOf(t, a, 1) // delivery failed: no Delivered
	a.Observe(at(15), map[AlertKey]Verdict{podMem: firing})
	p := pendingOf(t, a, 1)
	if !p[0].Since.Equal(at(0)) || !p[0].At.Equal(at(15)) {
		t.Errorf("retried = %+v, want since t0 (not reset), at 15 s", p[0])
	}
}

func TestAlertsFiredAndResolvedBeforeDeliverySaysNothing(t *testing.T) {
	a := NewAlerts()
	a.Observe(at(0), map[AlertKey]Verdict{podMem: firing})
	a.Observe(at(15), map[AlertKey]Verdict{podMem: calm})
	pendingOf(t, a, 0)
}

func TestAlertsDeliveredIgnoresAStaleStatus(t *testing.T) {
	a := NewAlerts()
	a.Observe(at(0), map[AlertKey]Verdict{podMem: firing})
	a.Delivered(a.Pending())
	a.Observe(at(15), map[AlertKey]Verdict{podMem: calm})
	resolved := pendingOf(t, a, 1)
	// it fires again before the resolve went out: the receiver still
	// believes it is firing, which is right, so nothing is pending
	a.Observe(at(30), map[AlertKey]Verdict{podMem: firing})
	pendingOf(t, a, 0)
	a.Delivered(resolved) // the late acknowledgement of the old resolve
	a.Observe(at(45), map[AlertKey]Verdict{podMem: calm})
	if p := pendingOf(t, a, 1); p[0].Status != AlertResolved {
		t.Errorf("pending = %+v, want the resolve (the stale Delivered must not have cleared it)", p[0])
	}
}

func TestAlertsPendingIsOrderedByKey(t *testing.T) {
	a := NewAlerts()
	a.Observe(at(0), map[AlertKey]Verdict{podMem: firing, nodeCPU: firing})
	p := pendingOf(t, a, 2)
	if p[0].Key != podMem || p[1].Key != nodeCPU {
		t.Errorf("order = %s, %s; want container-memory/... before node-cpu/...", p[0].Key, p[1].Key)
	}
}
