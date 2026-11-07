package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nonchan7720/kubeglance/pkg/domain"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func at(sec int) time.Time { return t0.Add(time.Duration(sec) * time.Second) }

type fakeSource struct {
	rounds []domain.Round
	i      int
	err    error
}

func (f *fakeSource) Poll(context.Context) (domain.Round, error) {
	if f.err != nil {
		return domain.Round{}, f.err
	}
	r := f.rounds[f.i%len(f.rounds)]
	f.i++
	return r, nil
}

var (
	web1 = domain.PodKey{Namespace: "app", Name: "web-1"}
	web2 = domain.PodKey{Namespace: "app", Name: "web-2"}
	db0  = domain.PodKey{Namespace: "app", Name: "db-0"}
	webW = domain.WorkloadKey{Namespace: "app", Kind: domain.KindDeployment, Name: "web"}
	dbW  = domain.WorkloadKey{Namespace: "app", Kind: domain.KindStatefulSet, Name: "db"}
)

// fixture: 2 nodes, 3 pods, 4 rounds at 15 s
func fixture(t *testing.T) (*domain.Store, *Query) {
	t.Helper()
	s := domain.NewStore(15*time.Second, time.Hour)
	s.UpsertNodes([]domain.NodeInfo{
		{Name: "n1", Ready: true, Allocatable: domain.Resource{CPU: 4000, Memory: 8000}, Capacity: domain.Resource{CPU: 4000, Memory: 8000}},
		{Name: "n2", Ready: true, Allocatable: domain.Resource{CPU: 4000, Memory: 8000}, Capacity: domain.Resource{CPU: 4000, Memory: 8000}},
	})
	s.UpsertPods([]domain.PodInfo{
		{Key: web1, Node: "n1", Workload: webW, Requests: domain.Resource{CPU: 100, Memory: 100}, Limits: domain.Resource{CPU: 200, Memory: 200}},
		{Key: web2, Node: "n2", Workload: webW, Requests: domain.Resource{CPU: 100, Memory: 100}, Limits: domain.Resource{CPU: 200, Memory: 200}},
		{Key: db0, Node: "n1", Workload: dbW, Requests: domain.Resource{CPU: 500, Memory: 1000}},
	})
	for i := range 4 {
		s.Record(domain.Round{
			At: at(i * 15),
			Pods: map[domain.PodKey]domain.Resource{
				web1: {CPU: 50, Memory: 50}, web2: {CPU: 150, Memory: 50}, db0: {CPU: 400, Memory: 900},
			},
			Nodes: map[string]domain.Resource{"n1": {CPU: 1000, Memory: 2000}, "n2": {CPU: 500, Memory: 1000}},
		})
	}
	q := NewQuery(s)
	q.now = func() time.Time { return at(45) }
	return s, q
}

func TestClusterView(t *testing.T) {
	_, q := fixture(t)
	v := q.Cluster(30 * time.Minute)
	if v.Nodes != 2 || v.ReadyNodes != 2 || v.Pods != 3 || v.Workloads != 2 {
		t.Errorf("counts = %+v", v)
	}
	if v.Allocatable.CPU != 8000 || v.Requests.CPU != 700 || v.Limits.CPU != 400 {
		t.Errorf("resources = alloc %v req %v lim %v", v.Allocatable, v.Requests, v.Limits)
	}
	if v.Usage.Now.CPU != 1500 || v.Usage.Avg.CPU != 1500 || v.Usage.Samples != 4 {
		t.Errorf("usage = %+v", v.Usage)
	}
	if v.Window.Duration != 30*time.Minute || !v.Window.To.Equal(at(45)) {
		t.Errorf("window = %+v", v.Window)
	}
}

func TestNodesViewImbalanceAndOrder(t *testing.T) {
	_, q := fixture(t)
	v := q.Nodes(30 * time.Minute)
	if len(v.Nodes) != 2 || v.Nodes[0].Name != "n1" {
		t.Fatalf("nodes = %+v", v.Nodes)
	}
	n1 := v.Nodes[0]
	if n1.Pods != 2 || n1.Requests.CPU != 600 || n1.CPURatio != 0.25 {
		t.Errorf("n1 = %+v", n1)
	}
	// ratios 0.25 and 0.125 → spread 0.125
	if v.Imbalance.CPU.Spread < 0.124 || v.Imbalance.CPU.Spread > 0.126 {
		t.Errorf("imbalance = %+v", v.Imbalance.CPU)
	}
}

func TestWorkloadsViewSumsPods(t *testing.T) {
	_, q := fixture(t)
	v := q.Workloads(30*time.Minute, "")
	if len(v.Workloads) != 2 {
		t.Fatalf("workloads = %+v", v.Workloads)
	}
	// db (400) sorts before web (200)
	if v.Workloads[0].Name != "db" || v.Workloads[1].Name != "web" {
		t.Errorf("order = %s, %s", v.Workloads[0], v.Workloads[1])
	}
	web := v.Workloads[1]
	if web.Pods != 2 || web.Usage.Now.CPU != 200 || web.PerPod.CPU != 100 || web.Requests.CPU != 200 {
		t.Errorf("web = %+v", web)
	}
	if web.RequestRatio.CPU != 1 { // 200 used / 200 requested
		t.Errorf("request ratio = %v", web.RequestRatio)
	}
	if got := q.Workloads(30*time.Minute, "other"); len(got.Workloads) != 0 {
		t.Errorf("namespace filter leaked: %+v", got.Workloads)
	}
}

func TestWorkloadKeepsHistoryOfReplacedPod(t *testing.T) {
	s, q := fixture(t)
	// web-1 is replaced by web-3 during a rollout
	s.RemovePod(web1, at(50))
	web3 := domain.PodKey{Namespace: "app", Name: "web-3"}
	s.UpsertPods([]domain.PodInfo{{Key: web3, Node: "n1", Workload: webW}})
	s.Record(domain.Round{At: at(60), Pods: map[domain.PodKey]domain.Resource{web2: {CPU: 150}, web3: {CPU: 60}}})
	q.now = func() time.Time { return at(60) }

	v := q.Workloads(30*time.Minute, "app")
	var web WorkloadView
	for _, w := range v.Workloads {
		if w.Name == "web" {
			web = w
		}
	}
	if web.Pods != 2 {
		t.Errorf("live pods = %d, want 2", web.Pods)
	}
	if web.Usage.Samples != 5 || web.Usage.Now.CPU != 210 {
		t.Errorf("usage = %+v; history of web-1 should still count", web.Usage)
	}
}

func TestPodsViewFilters(t *testing.T) {
	_, q := fixture(t)
	all := q.Pods(30*time.Minute, PodFilter{})
	if len(all.Pods) != 3 || all.Pods[0].Key != db0 {
		t.Fatalf("pods = %+v", all.Pods)
	}
	byNode := q.Pods(30*time.Minute, PodFilter{Node: "n2"})
	if len(byNode.Pods) != 1 || byNode.Pods[0].Key != web2 {
		t.Errorf("node filter = %+v", byNode.Pods)
	}
	byW := q.Pods(30*time.Minute, PodFilter{Workload: &webW})
	if len(byW.Pods) != 2 {
		t.Errorf("workload filter = %+v", byW.Pods)
	}
	w1 := byW.Pods[1] // web-1 (50 cpu) after web-2 (150)
	if w1.LimitRatio.CPU != 0.25 || w1.RequestRatio.CPU != 0.5 {
		t.Errorf("ratios = %+v", w1)
	}
}

func TestPodsViewNoLimitIsZeroRatio(t *testing.T) {
	_, q := fixture(t)
	v := q.Pods(30*time.Minute, PodFilter{Workload: &dbW})
	if v.Pods[0].LimitRatio.CPU != 0 {
		t.Errorf("unlimited pod has limit ratio %v", v.Pods[0].LimitRatio)
	}
}

func TestViewsCarrySparklines(t *testing.T) {
	_, q := fixture(t)
	// 30 min window ending at t0+45 s: 60 clock-aligned buckets of 30 s, the
	// last two ([t0, t0+30), [t0+30, t0+60)) hold the 4 rounds; the 58
	// before are gaps because the store has no older history.
	last2 := func(t *testing.T, name string, sp domain.Spark, cpu int64) {
		t.Helper()
		if len(sp.CPU) != 60 || sp.Bucket != 30*time.Second {
			t.Fatalf("%s: %d points of %v", name, len(sp.CPU), sp.Bucket)
		}
		if sp.CPU[0] != nil {
			t.Errorf("%s: first point = %d, want gap", name, *sp.CPU[0])
		}
		for _, p := range sp.CPU[58:] {
			if p == nil || *p != cpu {
				t.Errorf("%s: last points = %v, want %d", name, sp.CPU[58:], cpu)
			}
		}
	}
	last2(t, "cluster", q.Cluster(30*time.Minute).Spark, 1500)
	last2(t, "node n1", q.Nodes(30 * time.Minute).Nodes[0].Spark, 1000)
	last2(t, "workload db", q.Workloads(30*time.Minute, "").Workloads[0].Spark, 400)
	last2(t, "workload web", q.Workloads(30*time.Minute, "").Workloads[1].Spark, 200)
	last2(t, "pod db-0", q.Pods(30*time.Minute, PodFilter{}).Pods[0].Spark, 400)
}

func TestWindowIsCappedToRetention(t *testing.T) {
	_, q := fixture(t)
	v := q.Cluster(48 * time.Hour)
	if v.Window.Duration != time.Hour {
		t.Errorf("window = %v, want 1h", v.Window.Duration)
	}
}

func TestCollectorRecordsAndReportsHealth(t *testing.T) {
	s := domain.NewStore(15*time.Second, time.Minute)
	src := &fakeSource{rounds: []domain.Round{{At: at(0), Pods: map[domain.PodKey]domain.Resource{web1: {CPU: 1}}}}}
	c := NewCollector(s, src, nil)
	c.now = func() time.Time { return at(5) }
	c.PollOnce(context.Background())
	if len(s.PodSamples(web1, time.Time{})) != 1 {
		t.Fatal("round not recorded")
	}
	h := c.Health()
	if !h.Healthy || h.Rounds != 1 || h.LastError != "" {
		t.Errorf("health = %+v", h)
	}

	src.err = errors.New("metrics.k8s.io unavailable")
	c.PollOnce(context.Background())
	h = c.Health()
	if h.LastError == "" || h.Rounds != 1 {
		t.Errorf("health after error = %+v", h)
	}
	c.now = func() time.Time { return at(60) }
	if c.Health().Healthy {
		t.Error("healthy with a stale last success")
	}
}

type fakePeer struct {
	snap domain.Snapshot
	err  error
}

func (f fakePeer) Fetch(context.Context) (domain.Snapshot, error) { return f.snap, f.err }
func (f fakePeer) String() string                                 { return "peer" }

type fakeDisk struct {
	snap  domain.Snapshot
	ok    bool
	saved int
}

func (d *fakeDisk) Save(s domain.Snapshot) error {
	d.snap, d.ok, d.saved = s, true, d.saved+1
	return nil
}
func (d *fakeDisk) Load() (domain.Snapshot, bool, error) { return d.snap, d.ok, nil }

func TestBootstrapMergesPeerThenDiskAndSkipsFailures(t *testing.T) {
	src := domain.NewStore(15*time.Second, time.Minute)
	src.Record(domain.Round{At: at(0), Pods: map[domain.PodKey]domain.Resource{web1: {CPU: 7}}})
	disk := &fakeDisk{}
	_ = disk.Save(func() domain.Snapshot {
		d := domain.NewStore(15*time.Second, time.Minute)
		d.Record(domain.Round{At: at(15), Pods: map[domain.PodKey]domain.Resource{web1: {CPU: 8}}})
		return d.Snapshot()
	}())

	dst := domain.NewStore(15*time.Second, time.Minute)
	n := Bootstrap(context.Background(), dst,
		peersOnce([]PeerSource{fakePeer{err: errors.New("down")}, fakePeer{snap: src.Snapshot()}}), disk, fastRetry, nil)
	if n != 2 {
		t.Errorf("merged = %d, want 2", n)
	}
	got := dst.PodSamples(web1, time.Time{})
	if len(got) != 2 || got[0].Usage.CPU != 7 || got[1].Usage.CPU != 8 {
		t.Errorf("merged samples = %v", got)
	}
}

func TestPersisterSavesOnShutdown(t *testing.T) {
	s := domain.NewStore(15*time.Second, time.Minute)
	disk := &fakeDisk{}
	p := NewPersister(s, disk, time.Hour, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p.Run(ctx)
	if disk.saved != 1 {
		t.Errorf("saved %d times, want 1 (final save)", disk.saved)
	}
}

func TestNextPollIsMidSlotAndNeverSkipsOne(t *testing.T) {
	const iv = 15 * time.Second
	ms := func(sec float64) time.Time { return t0.Add(time.Duration(sec * float64(time.Second))) }
	for _, c := range []struct {
		name      string
		last, now time.Time
		want      time.Time
	}{
		{"nothing polled yet: now", time.Time{}, ms(3), ms(3)},
		{"early in the next slot's wait: its middle", ms(0), ms(3), ms(22.5)},
		{"just before the middle", ms(0), ms(20), ms(22.5)},
		{"the middle passed (slow poll): now, still in slot +15", ms(0), ms(25), ms(25)},
		{"slot +15 missed entirely, +30 past its middle: now", ms(0), ms(40), ms(40)},
		{"slot +15 missed, early in +30: its middle", ms(0), ms(31), ms(37.5)},
	} {
		if got := nextPoll(c.last, c.now, iv); !got.Equal(c.want) {
			t.Errorf("%s: nextPoll(%v, %v) = %v, want %v", c.name, c.last.Sub(t0), c.now.Sub(t0), got.Sub(t0), c.want.Sub(t0))
		}
	}
}

var fastRetry = BootstrapPolicy{Timeout: 200 * time.Millisecond, Retry: 5 * time.Millisecond}

func peersOnce(p []PeerSource) PeerFinder {
	return func(context.Context) ([]PeerSource, error) { return p, nil }
}

// flakyPeer fails its first `fails` fetches, then serves snap.
type flakyPeer struct {
	snap  domain.Snapshot
	fails int
	calls *int
}

func (f flakyPeer) Fetch(context.Context) (domain.Snapshot, error) {
	*f.calls++
	if *f.calls <= f.fails {
		return domain.Snapshot{}, errors.New("connection refused")
	}
	return f.snap, nil
}
func (f flakyPeer) String() string { return "flaky" }

func TestBootstrapRetriesUntilAPeerAnswers(t *testing.T) {
	src := domain.NewStore(15*time.Second, time.Minute)
	src.Record(domain.Round{At: at(0), Pods: map[domain.PodKey]domain.Resource{web1: {CPU: 7}}})
	calls, finds := 0, 0
	find := func(context.Context) ([]PeerSource, error) {
		finds++
		if finds == 1 {
			return nil, errors.New("dns timeout") // discovery itself can fail too
		}
		return []PeerSource{flakyPeer{snap: src.Snapshot(), fails: 2, calls: &calls}}, nil
	}
	dst := domain.NewStore(15*time.Second, time.Minute)
	if n := Bootstrap(context.Background(), dst, find, nil, fastRetry, nil); n != 1 {
		t.Fatalf("merged = %d, want 1", n)
	}
	if calls != 3 || finds != 4 {
		t.Errorf("fetches = %d (want 3), discoveries = %d (want 4: peers are looked up again each try)", calls, finds)
	}
	if len(dst.PodSamples(web1, time.Time{})) != 1 {
		t.Error("history not copied")
	}
}

func TestBootstrapGivesUpAfterTheTimeoutButStillLoadsDisk(t *testing.T) {
	disk := &fakeDisk{}
	_ = disk.Save(domain.NewStore(15*time.Second, time.Minute).Snapshot())
	calls := 0
	start := time.Now()
	n := Bootstrap(context.Background(), domain.NewStore(15*time.Second, time.Minute),
		peersOnce([]PeerSource{flakyPeer{fails: 1 << 30, calls: &calls}}), disk, fastRetry, nil)
	if took := time.Since(start); took < fastRetry.Timeout || took > 5*fastRetry.Timeout {
		t.Errorf("gave up after %v, want about %v", took, fastRetry.Timeout)
	}
	if n != 1 || calls < 2 {
		t.Errorf("merged = %d (want 1: disk only), fetches = %d (want retries)", n, calls)
	}
}

func TestBootstrapWithoutPeersDoesNotWait(t *testing.T) {
	finds := 0
	find := func(context.Context) ([]PeerSource, error) { finds++; return nil, nil }
	start := time.Now()
	if n := Bootstrap(context.Background(), domain.NewStore(15*time.Second, time.Minute), find, nil, fastRetry, nil); n != 0 {
		t.Errorf("merged = %d", n)
	}
	if finds != 1 || time.Since(start) > fastRetry.Timeout/2 {
		t.Errorf("first replica of an install waited: %d lookups in %v", finds, time.Since(start))
	}
}

func TestBootstrapStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	start := time.Now()
	Bootstrap(ctx, domain.NewStore(15*time.Second, time.Minute),
		peersOnce([]PeerSource{flakyPeer{fails: 1 << 30, calls: &calls}}), nil, BootstrapPolicy{Timeout: time.Hour, Retry: time.Hour}, nil)
	if time.Since(start) > time.Second {
		t.Errorf("did not stop on cancel: %v", time.Since(start))
	}
}

func TestPodDetailBreaksDownContainers(t *testing.T) {
	s := domain.NewStore(15*time.Second, time.Hour)
	multi := domain.PodKey{Namespace: "app", Name: "web-1"}
	single := domain.PodKey{Namespace: "app", Name: "db-0"}
	s.UpsertPods([]domain.PodInfo{
		{Key: multi, Requests: domain.Resource{CPU: 150}, Containers: []domain.ContainerInfo{
			{Name: "proxy", Requests: domain.Resource{CPU: 50}, Limits: domain.Resource{CPU: 100}}, // spec order: proxy first
			{Name: "app", Requests: domain.Resource{CPU: 100, Memory: 100}, Limits: domain.Resource{CPU: 200, Memory: 400}},
		}},
		{Key: single, Containers: []domain.ContainerInfo{{Name: "postgres", Requests: domain.Resource{CPU: 500}}}},
	})
	for i := range 4 {
		s.Record(domain.Round{
			At:   at(i * 15),
			Pods: map[domain.PodKey]domain.Resource{multi: {CPU: int64(60 + i*10)}, single: {CPU: 400}},
			// app: 40, 50, 60, 70; proxy: 20 every round
			Containers: map[domain.PodKey]map[string]domain.Resource{multi: {"app": {CPU: int64(40 + i*10), Memory: 100}, "proxy": {CPU: 20}}},
		})
	}
	q := NewQuery(s)
	q.now = func() time.Time { return at(45) }

	v, ok := q.Pod(30*time.Minute, multi)
	if !ok {
		t.Fatal("pod not found")
	}
	if v.Pod.Usage.Now.CPU != 90 || len(v.Pod.Spark.CPU) == 0 {
		t.Errorf("pod = %+v", v.Pod)
	}
	if len(v.Containers) != 2 || v.Containers[0].Name != "proxy" || v.Containers[1].Name != "app" {
		t.Fatalf("containers = %+v, want proxy then app (spec order)", v.Containers)
	}
	app := v.Containers[1]
	// 40 50 60 70: avg 55, median (50+60)/2 = 55, p95 nearest-rank ceil(3.8)=4th = 70
	if app.Usage.Now.CPU != 70 || app.Usage.Avg.CPU != 55 || app.Usage.Median.CPU != 55 || app.Usage.P95.CPU != 70 {
		t.Errorf("app usage = %+v", app.Usage)
	}
	if app.LimitRatio.CPU != 0.35 || app.RequestRatio.CPU != 0.7 || app.LimitRatio.Memory != 0.25 {
		t.Errorf("app ratios = limit %+v request %+v", app.LimitRatio, app.RequestRatio)
	}
	if v.Containers[0].Usage.Now.CPU != 20 || v.Containers[0].LimitRatio.CPU != 0.2 || len(v.Containers[0].Spark.CPU) == 0 {
		t.Errorf("proxy = %+v", v.Containers[0])
	}

	// a single-container pod's container is the pod's own series
	d, ok := q.Pod(30*time.Minute, single)
	if !ok || len(d.Containers) != 1 || d.Containers[0].Name != "postgres" || d.Containers[0].Usage.Now.CPU != 400 || d.Containers[0].RequestRatio.CPU != 0.8 {
		t.Errorf("single = %+v", d.Containers)
	}

	if _, ok := q.Pod(30*time.Minute, domain.PodKey{Namespace: "app", Name: "nope"}); ok {
		t.Error("unknown pod found")
	}
}
