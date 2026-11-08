package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// richStore has what a snapshot must carry: pods with and without info, a
// multi-container pod, a deleted pod, nodes, and a change of interval.
func richStore() *Store {
	s := NewStore(15*time.Second, 10*time.Minute)
	web, side, gone := PodKey{"app", "web"}, PodKey{"app", "side"}, PodKey{"app", "gone"}
	s.UpsertPods([]PodInfo{{Key: web, Node: "n1"}, {Key: side, Node: "n2"}, {Key: gone, Node: "n1"}})
	s.UpsertNodes([]NodeInfo{{Name: "n1"}, {Name: "n2"}})
	for i := range 8 {
		s.Record(Round{
			At:         at(i * 15),
			Pods:       map[PodKey]Resource{web: {CPU: int64(i), Memory: int64(i << 20)}, side: {CPU: 5}, gone: {CPU: 1}, {"x", "noinfo"}: {CPU: 2}},
			Containers: map[PodKey]map[string]Resource{side: {"app": {CPU: 3}, "proxy": {CPU: 2}}},
			Nodes:      map[string]Resource{"n1": {CPU: 100 + int64(i)}, "n2": {CPU: 7}},
		})
	}
	s.RemovePod(gone, at(110))
	s.SetInterval(30*time.Second, at(105))
	s.Record(Round{At: at(150), Pods: map[PodKey]Resource{web: {CPU: 50}}, Nodes: map[string]Resource{"n1": {CPU: 150}}})
	return s
}

// sorted is a snapshot with pods and nodes in key order and the parts that
// legitimately differ between two snapshots of one store (when it was taken)
// cleared, so two can be compared with reflect.DeepEqual.
func sorted(s Snapshot) Snapshot {
	s.TakenAt = time.Time{}
	slices.SortFunc(s.Pods, func(a, b PodSnapshot) int {
		return strings.Compare(a.Info.Key.Namespace+"/"+a.Info.Key.Name, b.Info.Key.Namespace+"/"+b.Info.Key.Name)
	})
	slices.SortFunc(s.Nodes, func(a, b NodeSnapshot) int { return strings.Compare(a.Info.Name, b.Info.Name) })
	return s
}

func TestWriteSnapshotIsTheSnapshotAsJSON(t *testing.T) {
	// the streamed form is byte-compatible with encoding a Snapshot, so a
	// replica of the previous version can read it during a rolling update
	s := richStore()
	var buf bytes.Buffer
	if err := s.WriteSnapshot(&buf); err != nil {
		t.Fatal(err)
	}
	var got Snapshot
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, buf.String())
	}
	if want := sorted(s.Snapshot()); !reflect.DeepEqual(sorted(got), want) {
		t.Errorf("streamed snapshot differs:\n got %+v\nwant %+v", sorted(got), want)
	}
	if got.TakenAt.IsZero() || !got.Newest.Equal(at(150)) {
		t.Errorf("takenAt %v newest %v, want newest +150s", got.TakenAt, got.Newest)
	}
}

func TestReadSnapshotMergesLikeMerge(t *testing.T) {
	src := richStore()
	var buf bytes.Buffer
	if err := src.WriteSnapshot(&buf); err != nil {
		t.Fatal(err)
	}
	// both destinations hold a sample the source lacks, and one it has
	prime := func(s *Store) {
		s.Record(Round{At: at(-15), Pods: map[PodKey]Resource{{"app", "web"}: {CPU: 99}}})
		s.Record(Round{At: at(0), Pods: map[PodKey]Resource{{"app", "web"}: {CPU: 98}}})
	}
	merged, streamed := NewStore(15*time.Second, 10*time.Minute), NewStore(15*time.Second, 10*time.Minute)
	prime(merged)
	prime(streamed)
	merged.Merge(src.Snapshot())
	info, err := streamed.ReadSnapshot(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if info.Pods != 4 || info.Nodes != 2 || info.TakenAt.IsZero() {
		t.Errorf("info = %+v, want 4 pods, 2 nodes", info)
	}
	if got, want := sorted(streamed.Snapshot()), sorted(merged.Snapshot()); !reflect.DeepEqual(got, want) {
		t.Errorf("ReadSnapshot differs from Merge:\n got %+v\nwant %+v", got, want)
	}
	if !reflect.DeepEqual(streamed.Timeline(), merged.Timeline()) {
		t.Errorf("timeline %v, want %v", streamed.Timeline(), merged.Timeline())
	}
}

func TestReadSnapshotReadsThePreviousFormat(t *testing.T) {
	// a replica of the previous version encodes the Snapshot struct: no
	// "newest", "pods": null when empty, fields it may add later unknown
	src := richStore()
	snap := src.Snapshot()
	snap.Newest = time.Time{}
	snap.TakenAt = at(160) // a peer is taken just after its newest round
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	old := strings.Replace(string(b), `"pods":`, `"future":{"x":[1,{"y":"]"}]},"pods":`, 1)
	dst := NewStore(15*time.Second, 10*time.Minute)
	if _, err := dst.ReadSnapshot(strings.NewReader(old)); err != nil {
		t.Fatal(err)
	}
	want := NewStore(15*time.Second, 10*time.Minute)
	want.Merge(snap)
	if got, w := sorted(dst.Snapshot()), sorted(want.Snapshot()); !reflect.DeepEqual(got, w) {
		t.Errorf("previous format merged differently:\n got %+v\nwant %+v", got, w)
	}

	empty, _ := json.Marshal(NewStore(15*time.Second, time.Minute).Snapshot())
	if !strings.Contains(string(empty), `"pods":null`) {
		t.Fatalf("expected a null pods list in %s", empty)
	}
	if info, err := NewStore(15*time.Second, time.Minute).ReadSnapshot(bytes.NewReader(empty)); err != nil || info.Pods != 0 {
		t.Errorf("empty previous-format snapshot: %+v %v", info, err)
	}
}

func TestReadSnapshotRejectsBrokenInput(t *testing.T) {
	src := richStore()
	var buf bytes.Buffer
	if err := src.WriteSnapshot(&buf); err != nil {
		t.Fatal(err)
	}
	for name, in := range map[string]string{
		"truncated": buf.String()[:buf.Len()/2],
		"not json":  "<html>",
		"array":     "[]",
		"pods":      `{"pods":{"a":1}}`,
	} {
		if _, err := NewStore(15*time.Second, time.Minute).ReadSnapshot(strings.NewReader(in)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// chunks records the size of every write.
type chunks struct{ sizes []int }

func (c *chunks) Write(p []byte) (int, error) { c.sizes = append(c.sizes, len(p)); return len(p), nil }

func TestWriteSnapshotStreams(t *testing.T) {
	// the snapshot is written as it is encoded, a pod at a time, never
	// built in memory whole: that is what made serving it to a starting
	// peer cost many times the history
	s := NewStore(15*time.Second, time.Hour)
	for i := range 240 {
		r := Round{At: at(i * 15), Pods: map[PodKey]Resource{}}
		for p := range 50 {
			r.Pods[PodKey{"ns", string(rune('a'+p%26)) + string(rune('a'+p/26))}] = Resource{CPU: int64(p), Memory: int64(i)}
		}
		s.Record(r)
	}
	var c chunks
	if err := s.WriteSnapshot(&c); err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, n := range c.sizes {
		total += n
	}
	if largest := slices.Max(c.sizes); largest > total/10 {
		t.Errorf("largest write %d bytes of %d: not streamed", largest, total)
	}
}

// failAfter fails every write after n bytes.
type failAfter struct{ n int }

func (f *failAfter) Write(p []byte) (int, error) {
	if f.n -= len(p); f.n < 0 {
		return 0, errors.New("peer went away")
	}
	return len(p), nil
}

func TestWriteSnapshotReportsAWriteError(t *testing.T) {
	if err := richStore().WriteSnapshot(&failAfter{n: 100}); err == nil {
		t.Error("no error from a failing writer")
	}
}

// recordOnFirstWrite records a round into the store the first time the
// snapshot reaches the writer: the collector keeps running while a peer
// copies.
type recordOnFirstWrite struct {
	buf   bytes.Buffer
	store *Store
	round Round
	done  bool
}

func (r *recordOnFirstWrite) Write(p []byte) (int, error) {
	if !r.done {
		r.done = true
		r.store.SetInterval(5*time.Second, at(3600))
		r.store.Record(r.round)
	}
	return r.buf.Write(p)
}

func TestWriteSnapshotStopsAtItsHeader(t *testing.T) {
	// a round recorded (here after a change of interval) while the snapshot
	// is being written is not in it: the header's timeline and newest
	// describe every sample sent, as when the snapshot was taken at once
	s := NewStore(15*time.Second, time.Hour)
	next := Round{At: at(3610), Pods: map[PodKey]Resource{}}
	for i := range 240 {
		r := Round{At: at(i * 15), Pods: map[PodKey]Resource{}}
		for p := range 50 {
			k := PodKey{"ns", string(rune('a'+p%26)) + string(rune('a'+p/26))}
			r.Pods[k] = Resource{CPU: int64(p)}
			next.Pods[k] = Resource{CPU: 999}
		}
		s.Record(r)
	}
	w := &recordOnFirstWrite{store: s, round: next}
	if err := s.WriteSnapshot(w); err != nil {
		t.Fatal(err)
	}
	var got Snapshot
	if err := json.Unmarshal(w.buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Newest.Equal(at(3585)) || len(got.Timeline) != 1 {
		t.Fatalf("header: newest %v timeline %v", got.Newest, got.Timeline)
	}
	for _, p := range got.Pods {
		if last := p.Samples[len(p.Samples)-1]; last.At.After(got.Newest) {
			t.Fatalf("pod %v has a sample at %v, after the header's newest %v", p.Info.Key, last.At, got.Newest)
		}
	}
}

func TestReadSnapshotRejectsAHeaderAfterTheSamples(t *testing.T) {
	// every writer puts the header first; one after the samples would have
	// been needed to slot them, so it is an error, not silently ignored
	in := `{"pods":[],"timeline":[{"since":"0001-01-01T00:00:00Z","step":30000000000}]}`
	if _, err := NewStore(15*time.Second, time.Minute).ReadSnapshot(strings.NewReader(in)); err == nil {
		t.Error("no error for a timeline after the pods")
	}
}
