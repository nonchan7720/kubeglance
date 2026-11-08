package services

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nonchan7720/kubeglance/pkg/domain"
)

type fakeNotifier struct {
	mu    sync.Mutex
	got   [][]domain.Alert
	limit int // deliver at most this many per call (0: all)
	err   error
}

func (f *fakeNotifier) Notify(_ context.Context, alerts []domain.Alert) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, alerts)
	if f.limit > 0 && f.limit < len(alerts) {
		return f.limit, f.err
	}
	if f.err != nil {
		return 0, f.err
	}
	return len(alerts), nil
}

func (f *fakeNotifier) sent() []domain.Alert {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Alert
	for _, call := range f.got {
		out = append(out, call...)
	}
	return out
}

type election struct {
	sender bool
	peers  int
	err    error
}

func (e *election) elect(context.Context) (bool, int, error) { return e.sender, e.peers, e.err }

var (
	api   = domain.PodKey{Namespace: "app", Name: "api"}
	multi = domain.PodKey{Namespace: "app", Name: "multi"}
)

// alertFixture: node n1 (allocatable 4000m / 8000), pod api (one container
// "app", limits 1000m / 1000) and pod multi (containers "app", limit memory
// 1000, and "proxy", no limits), five rounds at 15 s from t0 with the given
// usage, evaluated at 60 s over one minute (5 expected samples).
func alertFixture(t *testing.T, apiMem, multiAppMem, n1CPU int64) (*domain.Store, *Alerter, *fakeNotifier, *election) {
	t.Helper()
	s := domain.NewStore(15*time.Second, time.Hour)
	s.UpsertNodes([]domain.NodeInfo{{Name: "n1", Ready: true, Allocatable: domain.Resource{CPU: 4000, Memory: 8000}}})
	s.UpsertPods([]domain.PodInfo{
		{Key: api, Node: "n1", Limits: domain.Resource{CPU: 1000, Memory: 1000}, Containers: []domain.ContainerInfo{
			{Name: "app", Limits: domain.Resource{CPU: 1000, Memory: 1000}},
		}},
		{Key: multi, Node: "n1", Containers: []domain.ContainerInfo{
			{Name: "app", Limits: domain.Resource{Memory: 1000}},
			{Name: "proxy"},
		}},
	})
	for i := range 5 {
		s.Record(domain.Round{
			At:    at(i * 15),
			Pods:  map[domain.PodKey]domain.Resource{api: {CPU: 10, Memory: apiMem}, multi: {CPU: 10, Memory: multiAppMem + 990}},
			Nodes: map[string]domain.Resource{"n1": {CPU: n1CPU, Memory: 100}},
			// the pod sum of multi is far over 1000; only the container counts
			Containers: map[domain.PodKey]map[string]domain.Resource{multi: {"app": {Memory: multiAppMem}, "proxy": {Memory: 990}}},
		})
	}
	n := &fakeNotifier{}
	e := &election{sender: true}
	a := NewAlerter(s, n, e.elect, AlertPolicy{Window: time.Minute, Memory: 0.9, CPU: 0.9}, nil)
	a.now = func() time.Time { return at(60) }
	return s, a, n, e
}

func keys(alerts []domain.Alert) []string {
	out := make([]string, len(alerts))
	for i, a := range alerts {
		out[i] = string(a.Status) + " " + a.Key.String()
	}
	return out
}

func TestAlerterFiresPerContainerAndNode(t *testing.T) {
	_, a, n, _ := alertFixture(t, 950, 950, 3800)
	a.EvaluateOnce(context.Background())
	got := keys(n.sent())
	want := []string{
		"firing container-memory/app/api/app",
		"firing container-memory/app/multi/app",
		"firing node-cpu/n1",
	}
	if len(got) != len(want) {
		t.Fatalf("sent %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sent[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	al := n.sent()[0]
	if al.Verdict.Condition.Total != 1000 || al.Verdict.Above != 5 || al.Verdict.Stats.Median.Memory != 950 {
		t.Errorf("verdict = %+v", al.Verdict)
	}

	// the next round says nothing new
	a.EvaluateOnce(context.Background())
	if len(n.got) != 1 {
		t.Errorf("notified %d times, want once", len(n.got))
	}
}

func TestAlerterCalmClusterSendsNothing(t *testing.T) {
	_, a, n, _ := alertFixture(t, 500, 500, 100)
	a.EvaluateOnce(context.Background())
	if len(n.got) != 0 {
		t.Errorf("sent %v", keys(n.sent()))
	}
}

func TestAlerterDisabledRules(t *testing.T) {
	_, a, n, _ := alertFixture(t, 950, 950, 3800)
	a.policy.Memory = 0
	a.EvaluateOnce(context.Background())
	if got := keys(n.sent()); len(got) != 1 || got[0] != "firing node-cpu/n1" {
		t.Errorf("sent %v, want only the node CPU", got)
	}
}

func TestAlerterResolvesADeletedPodAsGone(t *testing.T) {
	s, a, n, _ := alertFixture(t, 950, 500, 100)
	a.EvaluateOnce(context.Background())
	s.RemovePod(api, at(61))
	a.now = func() time.Time { return at(75) }
	a.EvaluateOnce(context.Background())
	if len(n.got) != 2 || len(n.got[1]) != 1 || n.got[1][0].Status != domain.AlertResolved || !n.got[1][0].Gone {
		t.Errorf("calls = %+v, want a gone resolve second", n.got)
	}
}

func TestAlerterOnlyTheSenderSends(t *testing.T) {
	_, a, n, e := alertFixture(t, 950, 500, 100)
	e.sender = false
	a.EvaluateOnce(context.Background())
	if len(n.got) != 0 {
		t.Fatalf("a non-sender sent %v", keys(n.sent()))
	}
	// it takes over (the sender went away): what the old sender already
	// sent is not repeated
	e.sender = true
	a.EvaluateOnce(context.Background())
	if len(n.got) != 0 {
		t.Errorf("the new sender repeated %v", keys(n.sent()))
	}
}

func TestAlerterFirstRoundWithPeersAssumesTheyNotified(t *testing.T) {
	s, a, n, e := alertFixture(t, 950, 500, 100)
	e.peers = 1
	a.EvaluateOnce(context.Background())
	if len(n.got) != 0 {
		t.Fatalf("a replica that joined a running one sent %v", keys(n.sent()))
	}
	// a firing that starts afterwards is its own to send
	for _, sec := range []int{75, 90, 105} {
		s.Record(domain.Round{
			At:    at(sec),
			Pods:  map[domain.PodKey]domain.Resource{api: {Memory: 950}}, // still firing
			Nodes: map[string]domain.Resource{"n1": {CPU: 3900}},
		})
	}
	a.now = func() time.Time { return at(105) }
	a.EvaluateOnce(context.Background())
	if got := keys(n.sent()); len(got) != 1 || got[0] != "firing node-cpu/n1" {
		t.Errorf("sent %v, want the new node alert only", got)
	}
}

func TestAlerterFirstRoundAloneSends(t *testing.T) {
	_, a, n, _ := alertFixture(t, 950, 500, 100)
	a.EvaluateOnce(context.Background())
	if len(n.sent()) != 1 {
		t.Errorf("sent %v, want the firing alert", keys(n.sent()))
	}
}

func TestAlerterRetriesWhatWasNotDelivered(t *testing.T) {
	_, a, n, _ := alertFixture(t, 950, 950, 100)
	n.limit, n.err = 1, errors.New("receiver down")
	a.EvaluateOnce(context.Background())
	n.limit, n.err = 0, nil
	a.EvaluateOnce(context.Background())
	if len(n.got) != 2 || len(n.got[1]) != 1 || n.got[1][0].Key.Pod != "multi" {
		t.Errorf("calls = %v, want the undelivered second alert retried alone", n.got)
	}
}

func TestAlerterElectionFailureKeepsAlertsPending(t *testing.T) {
	_, a, n, e := alertFixture(t, 950, 500, 100)
	e.err = errors.New("dns down")
	a.EvaluateOnce(context.Background())
	if len(n.got) != 0 {
		t.Fatal("sent without knowing whether this replica is the sender")
	}
	e.err = nil
	a.EvaluateOnce(context.Background())
	if len(n.sent()) != 1 {
		t.Errorf("sent %v after the election recovered, want the firing alert", keys(n.sent()))
	}
}

func TestAlerterWindowIsCappedToRetention(t *testing.T) {
	_, a, _, _ := alertFixture(t, 950, 500, 100)
	a.policy.Window = 48 * time.Hour
	if w := a.window(at(60)); w.To.Sub(w.From) != time.Hour {
		t.Errorf("window = %v, want the retention (1h)", w.To.Sub(w.From))
	}
}

func TestAlerterRunsAfterEveryRound(t *testing.T) {
	s, a, n, _ := alertFixture(t, 950, 500, 100)
	src := &fakeSource{rounds: []domain.Round{{At: at(60), Pods: map[domain.PodKey]domain.Resource{api: {Memory: 950}}}}}
	c := NewCollector(s, src, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx, c.Rounds()); close(done) }()
	c.PollOnce(ctx)
	c.PollOnce(ctx) // the second signal collapses into the first if not taken yet
	deadline := time.Now().Add(5 * time.Second)
	for len(n.sent()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if len(n.sent()) != 1 {
		t.Errorf("sent %v, want the firing alert once", keys(n.sent()))
	}
}
