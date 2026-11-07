package domain

import (
	"cmp"
	"slices"
	"sync"
	"time"
)

// Store is the in-memory history of a cluster: one Series per pod and per
// node, plus their metadata. It is goroutine-safe. Memory is bounded: every
// series holds at most Capacity() samples, and entities that disappeared are
// dropped once their last sample is older than the retention.
type Store struct {
	mu        sync.RWMutex
	interval  time.Duration
	retention time.Duration
	pods      map[PodKey]*podEntry
	nodes     map[string]*nodeEntry
}

type podEntry struct {
	info      PodInfo
	hasInfo   bool
	deletedAt time.Time // zero while the pod exists
	series    *Series
}

type nodeEntry struct {
	info      NodeInfo
	hasInfo   bool
	deletedAt time.Time
	series    *Series
}

// NewStore returns a store that keeps retention worth of samples taken every
// interval.
func NewStore(interval, retention time.Duration) *Store {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	if retention < interval {
		retention = interval
	}
	return &Store{
		interval:  interval,
		retention: retention,
		pods:      map[PodKey]*podEntry{},
		nodes:     map[string]*nodeEntry{},
	}
}

// Interval is the sampling interval the store was sized for.
func (s *Store) Interval() time.Duration { return s.interval }

// Retention is how far back samples are kept.
func (s *Store) Retention() time.Duration { return s.retention }

// Capacity is the number of samples each series can hold.
func (s *Store) Capacity() int { return int(s.retention/s.interval) + 1 }

// Record appends one poll round. Entities the round names but the store has
// not seen through UpsertPods / UpsertNodes are created without metadata; the
// metadata is filled in when it arrives.
func (s *Store) Record(r Round) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, u := range r.Pods {
		s.pod(k).series.Add(Sample{At: r.At, Usage: u})
	}
	for n, u := range r.Nodes {
		s.node(n).series.Add(Sample{At: r.At, Usage: u})
	}
}

func (s *Store) pod(k PodKey) *podEntry {
	e, ok := s.pods[k]
	if !ok {
		e = &podEntry{info: PodInfo{Key: k}, series: NewSeries(s.Capacity())}
		s.pods[k] = e
	}
	return e
}

func (s *Store) node(n string) *nodeEntry {
	e, ok := s.nodes[n]
	if !ok {
		e = &nodeEntry{info: NodeInfo{Name: n}, series: NewSeries(s.Capacity())}
		s.nodes[n] = e
	}
	return e
}

// UpsertPods sets the metadata of pods, keeping their history. A pod that
// was marked removed comes back alive.
func (s *Store) UpsertPods(infos []PodInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, info := range infos {
		e := s.pod(info.Key)
		e.info, e.hasInfo, e.deletedAt = info, true, time.Time{}
	}
}

// RemovePod marks a pod as gone at the given time. Its history stays
// readable until Prune drops it.
func (s *Store) RemovePod(k PodKey, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.pods[k]; ok {
		e.deletedAt = at
	}
}

// UpsertNodes sets the metadata of nodes, keeping their history.
func (s *Store) UpsertNodes(infos []NodeInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, info := range infos {
		e := s.node(info.Name)
		e.info, e.hasInfo, e.deletedAt = info, true, time.Time{}
	}
}

// RemoveNode marks a node as gone at the given time.
func (s *Store) RemoveNode(name string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.nodes[name]; ok {
		e.deletedAt = at
	}
}

// Prune drops entities that were removed more than the retention ago, and
// entities never described by an Upsert whose last sample is that old.
func (s *Store) Prune(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := now.Add(-s.retention)
	for k, e := range s.pods {
		if stale(e.deletedAt, e.hasInfo, e.series, cutoff) {
			delete(s.pods, k)
		}
	}
	for n, e := range s.nodes {
		if stale(e.deletedAt, e.hasInfo, e.series, cutoff) {
			delete(s.nodes, n)
		}
	}
}

func stale(deletedAt time.Time, hasInfo bool, series *Series, cutoff time.Time) bool {
	if !deletedAt.IsZero() {
		return deletedAt.Before(cutoff)
	}
	if hasInfo {
		return false
	}
	last, ok := series.Last()
	return !ok || last.At.Before(cutoff)
}

// Pod returns a pod's metadata (also for a removed pod still in history).
func (s *Store) Pod(k PodKey) (PodInfo, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.pods[k]
	if !ok {
		return PodInfo{}, false
	}
	return e.info, true
}

// PodDeletedAt returns when a pod was removed, or zero while it exists.
func (s *Store) PodDeletedAt(k PodKey) time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if e, ok := s.pods[k]; ok {
		return e.deletedAt
	}
	return time.Time{}
}

// Pods lists every pod in history, sorted by namespace then name.
func (s *Store) Pods() []PodInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]PodInfo, 0, len(s.pods))
	for _, e := range s.pods {
		out = append(out, e.info)
	}
	slices.SortFunc(out, func(a, b PodInfo) int {
		return cmp.Or(cmp.Compare(a.Key.Namespace, b.Key.Namespace), cmp.Compare(a.Key.Name, b.Key.Name))
	})
	return out
}

// PodSamples returns a pod's samples at or after from, oldest first.
func (s *Store) PodSamples(k PodKey, from time.Time) []Sample {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if e, ok := s.pods[k]; ok {
		return e.series.Since(from)
	}
	return nil
}

// Node returns a node's metadata.
func (s *Store) Node(name string) (NodeInfo, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.nodes[name]
	if !ok {
		return NodeInfo{}, false
	}
	return e.info, true
}

// Nodes lists every node in history, sorted by name.
func (s *Store) Nodes() []NodeInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]NodeInfo, 0, len(s.nodes))
	for _, e := range s.nodes {
		out = append(out, e.info)
	}
	slices.SortFunc(out, func(a, b NodeInfo) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

// NodeSamples returns a node's samples at or after from, oldest first.
func (s *Store) NodeSamples(name string, from time.Time) []Sample {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if e, ok := s.nodes[name]; ok {
		return e.series.Since(from)
	}
	return nil
}

// Snapshot is the whole store as plain data: what one replica hands to a
// starting peer, and what the optional persistence writes to disk.
type Snapshot struct {
	TakenAt   time.Time      `json:"takenAt"`
	Interval  time.Duration  `json:"interval"`
	Retention time.Duration  `json:"retention"`
	Pods      []PodSnapshot  `json:"pods"`
	Nodes     []NodeSnapshot `json:"nodes"`
}

// PodSnapshot is one pod in a Snapshot.
type PodSnapshot struct {
	Info      PodInfo   `json:"info"`
	HasInfo   bool      `json:"hasInfo"`
	DeletedAt time.Time `json:"deletedAt,omitzero"`
	Samples   []Sample  `json:"samples"`
}

// NodeSnapshot is one node in a Snapshot.
type NodeSnapshot struct {
	Info      NodeInfo  `json:"info"`
	HasInfo   bool      `json:"hasInfo"`
	DeletedAt time.Time `json:"deletedAt,omitzero"`
	Samples   []Sample  `json:"samples"`
}

// Snapshot copies the store.
func (s *Store) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snap := Snapshot{TakenAt: time.Now(), Interval: s.interval, Retention: s.retention}
	for _, e := range s.pods {
		snap.Pods = append(snap.Pods, PodSnapshot{Info: e.info, HasInfo: e.hasInfo, DeletedAt: e.deletedAt, Samples: e.series.All()})
	}
	for _, e := range s.nodes {
		snap.Nodes = append(snap.Nodes, NodeSnapshot{Info: e.info, HasInfo: e.hasInfo, DeletedAt: e.deletedAt, Samples: e.series.All()})
	}
	return snap
}

// Merge adds a snapshot to the store: samples are merged by time (a sample
// already present at the same instant is kept), capacity is respected, and
// metadata the store lacks is taken from the snapshot.
func (s *Store) Merge(snap Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range snap.Pods {
		e := s.pod(p.Info.Key)
		if !e.hasInfo && p.HasInfo {
			e.info, e.hasInfo = p.Info, true
		}
		if e.deletedAt.IsZero() && !e.hasInfo {
			e.deletedAt = p.DeletedAt
		}
		e.series = mergeSeries(e.series, p.Samples, s.Capacity())
	}
	for _, n := range snap.Nodes {
		e := s.node(n.Info.Name)
		if !e.hasInfo && n.HasInfo {
			e.info, e.hasInfo = n.Info, true
		}
		if e.deletedAt.IsZero() && !e.hasInfo {
			e.deletedAt = n.DeletedAt
		}
		e.series = mergeSeries(e.series, n.Samples, s.Capacity())
	}
}

// mergeSeries returns a new series holding the union of the existing series
// and incoming samples, deduplicated by instant, newest capacity samples kept.
func mergeSeries(existing *Series, incoming []Sample, capacity int) *Series {
	all := append(existing.All(), incoming...)
	slices.SortStableFunc(all, func(a, b Sample) int { return a.At.Compare(b.At) })
	all = slices.CompactFunc(all, func(a, b Sample) bool { return a.At.Equal(b.At) })
	if len(all) > capacity {
		all = all[len(all)-capacity:]
	}
	out := NewSeries(capacity)
	for _, x := range all {
		out.Add(x)
	}
	return out
}

// PodDeleted reports whether a pod is in history only (removed from the cluster).
func (s *Store) PodDeleted(k PodKey) bool { return !s.PodDeletedAt(k).IsZero() }

// NodeDeleted reports whether a node is in history only.
func (s *Store) NodeDeleted(name string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.nodes[name]
	return ok && !e.deletedAt.IsZero()
}
