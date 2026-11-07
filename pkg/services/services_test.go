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
		[]PeerSource{fakePeer{err: errors.New("down")}, fakePeer{snap: src.Snapshot()}}, disk, nil)
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
