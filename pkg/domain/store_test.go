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
