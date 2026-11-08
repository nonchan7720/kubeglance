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
	timeline  Timeline // the sampling interval over time; never empty
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
	interval = wholeSeconds(interval)
	if retention < interval {
		retention = interval
	}
	return &Store{
		timeline:  Timeline{{Step: interval}},
		retention: retention,
		pods:      map[PodKey]*entry[PodInfo]{},
		nodes:     map[string]*entry[NodeInfo]{},
	}
}

// wholeSeconds rounds an interval to whole seconds, at least one: a Series
// keeps instants in whole seconds, so the slot grid has to be too.
func wholeSeconds(d time.Duration) time.Duration { return max(d.Round(time.Second), time.Second) }

// Interval is the sampling interval in effect now.
func (s *Store) Interval() time.Duration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.timeline.Current()
}

// Timeline is a copy of the sampling interval's history.
func (s *Store) Timeline() Timeline {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append(Timeline(nil), s.timeline...)
}

// SetInterval changes the sampling interval (metrics-server's resolution
// changed). The new grid starts at the first boundary of the new interval
// after last, the newest recorded instant, so samples stay in time order.
// Series grow at once when the interval got finer, so no history is
// dropped; they shrink in Prune once the finer history left the retention.
// Setting the current interval again does nothing.
func (s *Store) SetInterval(interval time.Duration, last time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if interval <= 0 {
		return
	}
	interval = wholeSeconds(interval)
	if interval == s.timeline.Current() {
		return
	}
	since := later(last, s.timeline[len(s.timeline)-1].Since).Truncate(interval).Add(interval)
	s.timeline = append(s.timeline, Resolution{Since: since, Step: interval})
	s.resize()
}

// Retention is how far back samples are kept.
func (s *Store) Retention() time.Duration { return s.retention }

// Slot is the instant a sample taken at t is recorded at: t rounded down to
// the interval on an absolute grid (for 15 s: :00, :15, :30, :45), the same
// on every replica whenever it started. Two replicas polling in the same
// slot therefore record the same instant, and merging their histories
// deduplicates instead of interleaving.
func (s *Store) Slot(t time.Time) time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.slot(t)
}

// slot is Slot for callers holding the lock: on the grid of the interval in
// effect at t.
func (s *Store) slot(t time.Time) time.Time { return t.Truncate(s.timeline.At(t)) }

// Capacity is the number of samples each series can hold.
func (s *Store) Capacity() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.capacity()
}

// capacity is Capacity for callers holding the lock: the retention at the
// finest interval still in the timeline (Prune drops what left the
// retention), so a series holds the whole retention at any mix of intervals.
func (s *Store) capacity() int { return int(s.retention/s.timeline.finest()) + 1 }

// resize gives every series the current capacity, keeping the newest
// samples, after the timeline changed.
func (s *Store) resize() {
	c := s.capacity()
	for _, e := range s.pods {
		if e.series.Cap() != c {
			e.series = e.series.Resized(c)
		}
		for name, cs := range e.containers {
			if cs.Cap() != c {
				e.containers[name] = cs.Resized(c)
			}
		}
	}
	for _, e := range s.nodes {
		if e.series.Cap() != c {
			e.series = e.series.Resized(c)
		}
	}
}

// Record appends one poll round at its Slot. Entities the round names but
// the store has not seen through UpsertPods / UpsertNodes are created without
// metadata; the metadata is filled in when it arrives. A second round in the
// same slot is ignored (Series.Add keeps the first).
func (s *Store) Record(r Round) {
	s.mu.Lock()
	defer s.mu.Unlock()
	at := s.slot(r.At)
	for k, u := range r.Pods {
		s.pod(k).series.Add(Sample{At: at, Usage: u})
	}
	for n, u := range r.Nodes {
		s.node(n).series.Add(Sample{At: at, Usage: u})
	}
	for k, cs := range r.Containers {
		e := s.pod(k)
		for name, u := range cs {
			e.container(name, s.capacity()).Add(Sample{At: at, Usage: u})
		}
	}
}

func (s *Store) pod(k PodKey) *entry[PodInfo] {
	return getOrCreate(s.pods, k, PodInfo{Key: k}, s.capacity())
}

func (s *Store) node(n string) *entry[NodeInfo] {
	return getOrCreate(s.nodes, n, NodeInfo{Name: n}, s.capacity())
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
	// an interval that ended before the retention describes no sample any
	// more; once the finest one is gone the series shrink
	for len(s.timeline) > 1 && !s.timeline[1].Since.After(cutoff) {
		s.timeline = s.timeline[1:]
	}
	s.timeline[0].Since = time.Time{} // the first entry covers all before it
	s.resize()
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

// StoreSize is how much the store holds, for kubeglance's own /metrics.
type StoreSize struct {
	Pods     int `json:"pods"` // in history, removed ones included until pruned
	Nodes    int `json:"nodes"`
	Series   int `json:"series"`   // pod, node and per-container series
	Samples  int `json:"samples"`  // across all series
	Capacity int `json:"capacity"` // samples each series can hold
}

// Size counts what the store holds.
func (s *Store) Size() StoreSize {
	s.mu.RLock()
	defer s.mu.RUnlock()
	z := StoreSize{Pods: len(s.pods), Nodes: len(s.nodes), Capacity: s.capacity()}
	for _, e := range s.pods {
		z.Series++
		z.Samples += e.series.Len()
		for _, c := range e.containers {
			z.Series++
			z.Samples += c.Len()
		}
	}
	for _, e := range s.nodes {
		z.Series++
		z.Samples += e.series.Len()
	}
	return z
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
	TakenAt  time.Time     `json:"takenAt"`
	Interval time.Duration `json:"interval"` // current; what a replica without Timeline reads
	// Timeline is absent in snapshots from replicas that predate it; such a
	// snapshot is one Interval throughout.
	Timeline  Timeline       `json:"timeline,omitempty"`
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
	snap := Snapshot{TakenAt: time.Now(), Interval: s.timeline.Current(), Timeline: append(Timeline(nil), s.timeline...), Retention: s.retention}
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

// Merge adds a snapshot to the store: incoming samples are moved to their
// Slot, then merged by instant (a sample already present in that slot is
// kept), capacity is respected, and metadata the store lacks is taken from
// the snapshot. Slotting on the way in also collapses the history of a
// replica that recorded raw poll times (before slots existed), so the first
// rolling update onto this version does not double the samples either.
func (s *Store) Merge(snap Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// a peer's timeline in whole seconds; an entry without a step is
	// ignored rather than turned into a 1 s grid (and 15 times the memory)
	var in Timeline
	for _, r := range snap.Timeline {
		if r.Step > 0 {
			in = append(in, Resolution{Since: r.Since, Step: wholeSeconds(r.Step)})
		}
	}
	if len(in) == 0 && snap.Interval > 0 {
		in = Timeline{{Step: wholeSeconds(snap.Interval)}}
	}
	s.timeline = s.timeline.merge(in, newest(snap))
	s.resize()
	for _, p := range snap.Pods {
		e := s.pod(p.Info.Key)
		e.merge(p.Info, p.HasInfo, p.DeletedAt, s.slotted(p.Samples), s.capacity())
		for name, samples := range p.Containers {
			e.container(name, s.capacity()) // so the map entry exists
			e.containers[name] = mergeSeries(e.containers[name], s.slotted(samples), s.capacity())
		}
	}
	for _, n := range snap.Nodes {
		s.node(n.Info.Name).merge(n.Info, n.HasInfo, n.DeletedAt, s.slotted(n.Samples), s.capacity())
	}
}

// newest is the latest sample instant in a snapshot.
func newest(snap Snapshot) time.Time {
	var t time.Time
	for _, p := range snap.Pods {
		if n := len(p.Samples); n > 0 {
			t = later(t, p.Samples[n-1].At)
		}
	}
	for _, n := range snap.Nodes {
		if k := len(n.Samples); k > 0 {
			t = later(t, n.Samples[k-1].At)
		}
	}
	return t
}

// slotted returns samples moved to their Slot (a copy; the snapshot is not
// modified). Two samples that land in one slot are deduplicated by
// mergeSeries, the earlier one kept.
func (s *Store) slotted(in []Sample) []Sample {
	out := make([]Sample, len(in))
	for i, x := range in {
		out[i] = Sample{At: s.slot(x.At), Usage: x.Usage}
	}
	return out
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
