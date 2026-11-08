package domain

import (
	"testing"
	"time"
)

func newTestStore() *Store { return NewStore(15*time.Second, 1*time.Minute) }

func TestStoreCapacityFromRetention(t *testing.T) {
	s := newTestStore()
	if got := s.Capacity(); got != 5 { // 60/15 + 1 endpoints inclusive
		t.Errorf("capacity = %d, want 5", got)
	}
}

func TestStoreRecordAndRead(t *testing.T) {
	s := newTestStore()
	p := PodKey{"default", "web-1"}
	s.UpsertPods([]PodInfo{{Key: p, Node: "n1", Workload: WorkloadKey{"default", KindDeployment, "web"}}})
	s.UpsertNodes([]NodeInfo{{Name: "n1", Allocatable: Resource{CPU: 4000, Memory: 8 << 30}}})

	for i := range 3 {
		s.Record(Round{
			At:    at(i * 15),
			Pods:  map[PodKey]Resource{p: {CPU: int64(100 + i), Memory: 1 << 20}},
			Nodes: map[string]Resource{"n1": {CPU: int64(500 + i)}},
		})
	}

	ps := s.PodSamples(p, time.Time{})
	if len(ps) != 3 || ps[2].Usage.CPU != 102 {
		t.Fatalf("pod samples = %v", ps)
	}
	ns := s.NodeSamples("n1", time.Time{})
	if len(ns) != 3 || ns[2].Usage.CPU != 502 {
		t.Fatalf("node samples = %v", ns)
	}
	info, ok := s.Pod(p)
	if !ok || info.Node != "n1" {
		t.Fatalf("pod info = %+v, ok=%v", info, ok)
	}
}

func TestStoreRecordsUsageForUnknownPodAndBackfillsInfo(t *testing.T) {
	// metrics-server can report a pod before the informer has delivered it.
	s := newTestStore()
	p := PodKey{"default", "late"}
	s.Record(Round{At: at(0), Pods: map[PodKey]Resource{p: {CPU: 1}}})
	if len(s.PodSamples(p, time.Time{})) != 1 {
		t.Fatal("sample of unknown pod was dropped")
	}
	s.UpsertPods([]PodInfo{{Key: p, Node: "n1"}})
	if info, _ := s.Pod(p); info.Node != "n1" {
		t.Errorf("info not applied: %+v", info)
	}
	if len(s.PodSamples(p, time.Time{})) != 1 {
		t.Error("upsert lost the history")
	}
}

func TestStoreDeletedPodKeepsHistoryUntilRetentionThenPrunes(t *testing.T) {
	s := newTestStore()
	p := PodKey{"default", "gone"}
	s.UpsertPods([]PodInfo{{Key: p}})
	s.Record(Round{At: at(0), Pods: map[PodKey]Resource{p: {CPU: 1}}})
	s.RemovePod(p, at(10))

	if _, ok := s.Pod(p); !ok {
		t.Fatal("deleted pod should still be readable (history)")
	}
	s.Prune(at(10 + 59)) // inside retention
	if _, ok := s.Pod(p); !ok {
		t.Fatal("pruned too early")
	}
	s.Prune(at(10 + 61))
	if _, ok := s.Pod(p); ok {
		t.Fatal("not pruned after retention")
	}
}

func TestStoreListsAreSorted(t *testing.T) {
	s := newTestStore()
	s.UpsertPods([]PodInfo{
		{Key: PodKey{"b", "z"}}, {Key: PodKey{"a", "y"}}, {Key: PodKey{"a", "x"}},
	})
	s.UpsertNodes([]NodeInfo{{Name: "n2"}, {Name: "n1"}})
	pods := s.Pods()
	if pods[0].Key != (PodKey{"a", "x"}) || pods[2].Key != (PodKey{"b", "z"}) {
		t.Errorf("pods order = %v", pods)
	}
	nodes := s.Nodes()
	if nodes[0].Name != "n1" {
		t.Errorf("nodes order = %v", nodes)
	}
}

func TestSnapshotRoundTripAndMerge(t *testing.T) {
	a := newTestStore()
	p := PodKey{"default", "web-1"}
	a.UpsertPods([]PodInfo{{Key: p, Node: "n1"}})
	a.UpsertNodes([]NodeInfo{{Name: "n1"}})
	for i := range 3 {
		a.Record(Round{At: at(i * 15), Pods: map[PodKey]Resource{p: {CPU: int64(i)}}, Nodes: map[string]Resource{"n1": {CPU: int64(i)}}})
	}

	b := newTestStore()
	// b already has a newer sample and an older one a does not have
	b.Record(Round{At: at(-15), Pods: map[PodKey]Resource{p: {CPU: 99}}})
	b.Record(Round{At: at(45), Pods: map[PodKey]Resource{p: {CPU: 3}}})
	b.Merge(a.Snapshot())

	got := b.PodSamples(p, time.Time{})
	want := []int64{99, 0, 1, 2, 3}
	if len(got) != len(want) {
		t.Fatalf("merged = %v, want cpus %v", got, want)
	}
	for i, w := range want {
		if got[i].Usage.CPU != w {
			t.Errorf("merged[%d].cpu = %d, want %d", i, got[i].Usage.CPU, w)
		}
	}
	if info, ok := b.Pod(p); !ok || info.Node != "n1" {
		t.Errorf("merge did not carry pod info: %+v", info)
	}
	if _, ok := b.Node("n1"); !ok {
		t.Error("merge did not carry node info")
	}
	if len(b.NodeSamples("n1", time.Time{})) != 3 {
		t.Error("merge did not carry node samples")
	}
}

func TestSnapshotMergeDoesNotExceedCapacity(t *testing.T) {
	a := newTestStore()
	p := PodKey{"default", "p"}
	for i := range 10 {
		a.Record(Round{At: at(i * 15), Pods: map[PodKey]Resource{p: {CPU: int64(i)}}})
	}
	b := newTestStore()
	b.Merge(a.Snapshot())
	got := b.PodSamples(p, time.Time{})
	if len(got) != 5 || got[0].Usage.CPU != 5 {
		t.Errorf("merged = %v, want newest 5", got)
	}
}

func TestRecordUsesTheSlot(t *testing.T) {
	s := newTestStore()
	p := PodKey{"app", "web"}
	s.Record(Round{At: at(7), Pods: map[PodKey]Resource{p: {CPU: 1}}, Nodes: map[string]Resource{"n1": {CPU: 2}}})
	if got := s.PodSamples(p, time.Time{}); len(got) != 1 || !got[0].At.Equal(at(0)) {
		t.Errorf("pod sample = %v, want at +0s", got)
	}
	if got := s.NodeSamples("n1", time.Time{}); len(got) != 1 || !got[0].At.Equal(at(0)) {
		t.Errorf("node sample = %v, want at +0s", got)
	}
}

func TestSlotIsTheIntervalGrid(t *testing.T) {
	s := NewStore(15*time.Second, time.Hour)
	for in, want := range map[int]int{0: 0, 7: 0, 14: 0, 15: 15, 22: 15, 44: 30, 45: 45} {
		if got := s.Slot(at(in)); !got.Equal(at(want)) {
			t.Errorf("Slot(+%ds) = %v, want +%ds", in, got, want)
		}
	}
	// the grid is absolute, not relative to when a replica started, and
	// does not depend on the location
	jst := time.FixedZone("JST", 9*3600)
	if got := s.Slot(at(22).In(jst)); !got.Equal(at(15)) {
		t.Errorf("Slot in JST = %v", got)
	}
}

func TestMergeOfReplicasPollingAtDifferentOffsetsDoesNotDuplicate(t *testing.T) {
	// replica A polled at +0, +15, +30; replica B, started 7 s later and
	// running a version that recorded raw poll times, at +7, +22, +37. Both
	// read the same metrics-server values. The merged history must hold one
	// sample per slot, not two, or the capacity would hold half the retention
	// and coverage would hide gaps.
	p := PodKey{"app", "web"}
	a, b := newTestStore(), newTestStore()
	for i := range 3 {
		a.Record(Round{At: at(i * 15), Pods: map[PodKey]Resource{p: {CPU: int64(10 + i)}}})
		b.Record(Round{At: at(i*15 + 7), Pods: map[PodKey]Resource{p: {CPU: int64(10 + i)}}})
	}
	dst := newTestStore()
	dst.Merge(a.Snapshot())
	dst.Merge(b.Snapshot())
	got := dst.PodSamples(p, time.Time{})
	if len(got) != 3 {
		t.Fatalf("merged %d samples, want 3 (one per slot): %v", len(got), got)
	}
	for i, x := range got {
		if !x.At.Equal(at(i*15)) || x.Usage.CPU != int64(10+i) {
			t.Errorf("sample %d = %v, want +%ds cpu %d", i, x, i*15, 10+i)
		}
	}

	// the store's own next round in the same slot as a merged sample is
	// not a second sample either: Record slots too
	dst.Record(Round{At: at(37), Pods: map[PodKey]Resource{p: {CPU: 99}}})
	if n := len(dst.PodSamples(p, time.Time{})); n != 3 {
		t.Errorf("after recording slot +30 again: %d samples", n)
	}
}

func TestContainerSeries(t *testing.T) {
	s := newTestStore()
	p := PodKey{"app", "web-1"}
	s.UpsertPods([]PodInfo{{Key: p, Containers: []ContainerInfo{{Name: "app"}, {Name: "proxy"}}}})
	for i := range 3 {
		s.Record(Round{
			At:         at(i * 15),
			Pods:       map[PodKey]Resource{p: {CPU: int64(10 + i), Memory: 30}},
			Containers: map[PodKey]map[string]Resource{p: {"app": {CPU: int64(8 + i), Memory: 20}, "proxy": {CPU: 2, Memory: 10}}},
		})
	}
	got := s.ContainerSamples(p, at(15))
	if len(got) != 2 || len(got["app"]) != 2 || len(got["proxy"]) != 2 {
		t.Fatalf("containers since 15 s = %v", got)
	}
	if got["app"][0].Usage.CPU != 9 || got["app"][1].Usage.CPU != 10 || got["proxy"][1].Usage.Memory != 10 {
		t.Errorf("app = %v, proxy = %v", got["app"], got["proxy"])
	}
	if s.ContainerSamples(PodKey{"app", "other"}, time.Time{}) != nil {
		t.Error("unknown pod has container samples")
	}

	// a peer copy carries the container series and merges them by instant
	b := newTestStore()
	b.Record(Round{At: at(45), Pods: map[PodKey]Resource{p: {CPU: 13}},
		Containers: map[PodKey]map[string]Resource{p: {"app": {CPU: 11}, "proxy": {CPU: 2}}}})
	b.Merge(s.Snapshot())
	merged := b.ContainerSamples(p, time.Time{})
	if len(merged["app"]) != 4 || merged["app"][3].Usage.CPU != 11 || len(merged["proxy"]) != 4 {
		t.Errorf("merged containers = %v", merged)
	}

	// a snapshot from a replica that predates container series merges too
	old := s.Snapshot()
	for i := range old.Pods {
		old.Pods[i].Containers = nil
	}
	c := newTestStore()
	c.Merge(old)
	if len(c.PodSamples(p, time.Time{})) != 3 || len(c.ContainerSamples(p, time.Time{})) != 0 {
		t.Error("snapshot without containers did not merge cleanly")
	}

	// the container series go with their pod
	s.RemovePod(p, at(50))
	s.Prune(at(50).Add(2 * time.Minute))
	if s.ContainerSamples(p, time.Time{}) != nil {
		t.Error("container series outlived the pruned pod")
	}
}

func TestContainerSeriesAreSlottedToo(t *testing.T) {
	// replica A at +0/+15, replica B (older version, raw poll times) at
	// +7/+22, same readings: the container series must dedupe like the pod's
	p := PodKey{"app", "web"}
	round := func(sec int, cpu int64) Round {
		return Round{At: at(sec), Pods: map[PodKey]Resource{p: {CPU: cpu}},
			Containers: map[PodKey]map[string]Resource{p: {"app": {CPU: cpu - 1}, "proxy": {CPU: 1}}}}
	}
	a := newTestStore()
	a.Record(round(0, 10))
	a.Record(round(15, 11))
	b := newTestStore()
	b.Record(round(7, 10)) // Record slots: +0
	if got := b.ContainerSamples(p, time.Time{})["app"]; len(got) != 1 || !got[0].At.Equal(at(0)) {
		t.Fatalf("recorded container sample = %v, want at +0s", got)
	}
	old := b.Snapshot()
	for i := range old.Pods { // as an older replica would send it
		old.Pods[i].Containers["app"] = []Sample{{At: at(7), Usage: Resource{CPU: 9}}, {At: at(22), Usage: Resource{CPU: 10}}}
	}
	dst := newTestStore()
	dst.Merge(a.Snapshot())
	dst.Merge(old)
	if got := dst.ContainerSamples(p, time.Time{})["app"]; len(got) != 2 || !got[0].At.Equal(at(0)) || !got[1].At.Equal(at(15)) {
		t.Errorf("merged container samples = %v, want one per slot at +0, +15", got)
	}
}

func TestStoreSize(t *testing.T) {
	s := newTestStore() // capacity 5
	p, q := PodKey{"app", "a"}, PodKey{"app", "b"}
	for i := range 3 {
		s.Record(Round{At: at(i * 15), Pods: map[PodKey]Resource{p: {CPU: 1}, q: {CPU: 1}}, Nodes: map[string]Resource{"n1": {CPU: 1}},
			Containers: map[PodKey]map[string]Resource{p: {"app": {CPU: 1}, "proxy": {CPU: 1}}}})
	}
	got := s.Size()
	// 2 pod + 1 node + 2 container series, 3 samples each
	want := StoreSize{Pods: 2, Nodes: 1, Series: 5, Samples: 15, Capacity: 5}
	if got != want {
		t.Errorf("Size = %+v, want %+v", got, want)
	}
}
