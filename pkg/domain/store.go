package domain

import (
	"cmp"
	"maps"
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
	pods      map[PodKey]*entry[PodInfo]
	nodes     map[string]*entry[NodeInfo]
}

// entry is one pod or node: its metadata (once an Upsert delivered it), when
// it was removed (zero while it exists) and its samples.
type entry[I any] struct {
	info      I
	hasInfo   bool
	deletedAt time.Time
	series    *Series
	// containers is the per-container history of a multi-container pod
	// (nil otherwise, and always nil for nodes)
	containers map[string]*Series
}

// container returns the series of one container, creating it.
func (e *entry[I]) container(name string, capacity int) *Series {
	if e.containers == nil {
		e.containers = map[string]*Series{}
	}
	c, ok := e.containers[name]
	if !ok {
		c = NewSeries(capacity)
		e.containers[name] = c
	}
	return c
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
		pods:      map[PodKey]*entry[PodInfo]{},
		nodes:     map[string]*entry[NodeInfo]{},
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
	for k, cs := range r.Containers {
		e := s.pod(k)
		for name, u := range cs {
			e.container(name, s.Capacity()).Add(Sample{At: r.At, Usage: u})
		}
	}
}

func (s *Store) pod(k PodKey) *entry[PodInfo] {
	return getOrCreate(s.pods, k, PodInfo{Key: k}, s.Capacity())
}

func (s *Store) node(n string) *entry[NodeInfo] {
	return getOrCreate(s.nodes, n, NodeInfo{Name: n}, s.Capacity())
}

func getOrCreate[K comparable, I any](m map[K]*entry[I], k K, blank I, capacity int) *entry[I] {
	e, ok := m[k]
	if !ok {
		e = &entry[I]{info: blank, series: NewSeries(capacity)}
		m[k] = e
	}
	return e
}

// upsert sets an entry's metadata and marks it alive again.
func (e *entry[I]) upsert(info I) {
	e.info, e.hasInfo, e.deletedAt = info, true, time.Time{}
}

// UpsertPods sets the metadata of pods, keeping their history. A pod that
// was marked removed comes back alive.
func (s *Store) UpsertPods(infos []PodInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, info := range infos {
		s.pod(info.Key).upsert(info)
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
		s.node(info.Name).upsert(info)
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
	maps.DeleteFunc(s.pods, func(_ PodKey, e *entry[PodInfo]) bool { return e.stale(cutoff) })
	maps.DeleteFunc(s.nodes, func(_ string, e *entry[NodeInfo]) bool { return e.stale(cutoff) })
}

func (e *entry[I]) stale(cutoff time.Time) bool {
	if !e.deletedAt.IsZero() {
		return e.deletedAt.Before(cutoff)
	}
	if e.hasInfo {
		return false
	}
	last, ok := e.series.Last()
	return !ok || last.At.Before(cutoff)
}

// infos lists the metadata of every entry in m, sorted by cmp.
func infos[K comparable, I any](m map[K]*entry[I], cmp func(a, b I) int) []I {
	out := make([]I, 0, len(m))
	for _, e := range m {
		out = append(out, e.info)
	}
	slices.SortFunc(out, cmp)
	return out
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
	return infos(s.pods, func(a, b PodInfo) int {
		return cmp.Or(cmp.Compare(a.Key.Namespace, b.Key.Namespace), cmp.Compare(a.Key.Name, b.Key.Name))
	})
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

// ContainerSamples returns the samples at or after from of each container
// of a multi-container pod, by container name, oldest first. It is empty for
// a single-container pod (its pod series is the container's) and nil for an
// unknown pod.
func (s *Store) ContainerSamples(k PodKey, from time.Time) map[string][]Sample {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.pods[k]
	if !ok {
		return nil
	}
	out := make(map[string][]Sample, len(e.containers))
	for name, c := range e.containers {
		out[name] = c.Since(from)
	}
	return out
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
	return infos(s.nodes, func(a, b NodeInfo) int { return cmp.Compare(a.Name, b.Name) })
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
	// Containers is absent in snapshots from replicas that predate it,
	// which merge as pods without a container breakdown.
	Containers map[string][]Sample `json:"containers,omitempty"`
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
		ps := PodSnapshot{Info: e.info, HasInfo: e.hasInfo, DeletedAt: e.deletedAt, Samples: e.series.All()}
		if len(e.containers) > 0 {
			ps.Containers = make(map[string][]Sample, len(e.containers))
			for name, c := range e.containers {
				ps.Containers[name] = c.All()
			}
		}
		snap.Pods = append(snap.Pods, ps)
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
		e.merge(p.Info, p.HasInfo, p.DeletedAt, p.Samples, s.Capacity())
		for name, samples := range p.Containers {
			e.container(name, s.Capacity()) // so the map entry exists
			e.containers[name] = mergeSeries(e.containers[name], samples, s.Capacity())
		}
	}
	for _, n := range snap.Nodes {
		s.node(n.Info.Name).merge(n.Info, n.HasInfo, n.DeletedAt, n.Samples, s.Capacity())
	}
}

// merge folds one snapshot entry into e: metadata only where e has none,
// samples merged by instant.
func (e *entry[I]) merge(info I, hasInfo bool, deletedAt time.Time, samples []Sample, capacity int) {
	if !e.hasInfo && hasInfo {
		e.info, e.hasInfo = info, true
	}
	if e.deletedAt.IsZero() && !e.hasInfo {
		e.deletedAt = deletedAt
	}
	e.series = mergeSeries(e.series, samples, capacity)
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
