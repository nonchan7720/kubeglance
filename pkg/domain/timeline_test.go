package domain

import (
	"testing"
	"time"
)

func TestTimelineAtAndExpected(t *testing.T) {
	// 15 s until +60, 10 s from then on
	tl := Timeline{{Step: 15 * time.Second}, {Since: at(60), Step: 10 * time.Second}}
	for sec, want := range map[int]time.Duration{-100: 15 * time.Second, 0: 15 * time.Second, 59: 15 * time.Second, 60: 10 * time.Second, 500: 10 * time.Second} {
		if got := tl.At(at(sec)); got != want {
			t.Errorf("At(+%ds) = %v, want %v", sec, got, want)
		}
	}
	if got := tl.Current(); got != 10*time.Second {
		t.Errorf("Current = %v", got)
	}
	for _, c := range []struct {
		from, to, want int
	}{
		{0, 45, 4},    // one resolution: 45/15 + 1 (0, 15, 30, 45), as before
		{60, 100, 5},  // 40/10 + 1 (60, 70, 80, 90, 100)
		{0, 100, 9},   // [0, 60) at 15 s: 0, 15, 30, 45 = 4; [60, 100] at 10 s: 60..100 = 5
		{30, 70, 4},   // [30, 60): 30, 45 = 2; [60, 70]: 60, 70 = 2
		{0, 50, 4},    // one part ending at to: 0, 15, 30, 45 (50/15 + 1)
		{100, 100, 1}, // a single instant
	} {
		if got := tl.Expected(at(c.from), at(c.to)); got != c.want {
			t.Errorf("Expected(+%d, +%d) = %d, want %d", c.from, c.to, got, c.want)
		}
	}
	// a change between grid points: [0, 50) at 15 s still holds 0, 15, 30, 45
	tl2 := Timeline{{Step: 15 * time.Second}, {Since: at(50), Step: 10 * time.Second}}
	if got := tl2.Expected(at(0), at(100)); got != 10 { // 4 + 50/10 + 1
		t.Errorf("Expected over a change at +50 = %d, want 10", got)
	}
	if got := tl.Coarsest(at(0), at(100)); got != 15*time.Second {
		t.Errorf("Coarsest over the change = %v", got)
	}
	if got := tl.Coarsest(at(60), at(100)); got != 10*time.Second {
		t.Errorf("Coarsest after the change = %v", got)
	}
	if got := tl.Finest(at(0), at(100)); got != 10*time.Second {
		t.Errorf("Finest = %v", got)
	}
}

func TestStatsCoverageAcrossAResolutionChange(t *testing.T) {
	// every slot of both resolutions has its sample: full coverage, not the
	// 0.6 that counting the whole window at 10 s would claim
	tl := Timeline{{Step: 15 * time.Second}, {Since: at(60), Step: 10 * time.Second}}
	var in []Sample
	for _, s := range []int{0, 15, 30, 45, 60, 70, 80, 90, 100} {
		in = append(in, Sample{At: at(s), Usage: Resource{CPU: 1}})
	}
	st := ComputeStats(in, Window{From: at(0), To: at(100), Step: 10 * time.Second, Timeline: tl})
	if st.Expected != 9 || st.Coverage != 1 {
		t.Errorf("expected %d coverage %v, want 9 and 1", st.Expected, st.Coverage)
	}
}

func TestStoreFollowsAResolutionChange(t *testing.T) {
	s := NewStore(15*time.Second, time.Minute) // capacity 60/15 + 1 = 5
	p := PodKey{"app", "web"}
	for _, sec := range []int{0, 15, 30, 45} {
		s.Record(Round{At: at(sec + 7), Pods: map[PodKey]Resource{p: {CPU: int64(sec)}}})
	}
	// metrics-server now scrapes every 10 s; the last sample is at +45, so
	// the new grid starts at the first 10 s boundary after it
	s.SetInterval(10*time.Second, at(45))
	if s.Interval() != 10*time.Second || s.Capacity() != 7 {
		t.Fatalf("interval %v capacity %d, want 10s and 60/10 + 1 = 7", s.Interval(), s.Capacity())
	}
	if tl := s.Timeline(); len(tl) != 2 || !tl[1].Since.Equal(at(50)) {
		t.Fatalf("timeline = %v, want the change at +50", tl)
	}
	if !s.Slot(at(47)).Equal(at(45)) || !s.Slot(at(55)).Equal(at(50)) {
		t.Errorf("slots around the change: %v %v", s.Slot(at(47)), s.Slot(at(55)))
	}
	for _, sec := range []int{50, 60, 70} {
		s.Record(Round{At: at(sec + 5), Pods: map[PodKey]Resource{p: {CPU: int64(sec)}}})
	}
	got := s.PodSamples(p, time.Time{})
	if len(got) != 7 { // grown with the finer resolution: nothing dropped
		t.Fatalf("samples = %v, want all 7", got)
	}
	if !got[4].At.Equal(at(50)) || !got[6].At.Equal(at(70)) {
		t.Errorf("new samples on the 10 s grid: %v", got[4:])
	}
	// setting the same interval again changes nothing
	s.SetInterval(10*time.Second, at(70))
	if len(s.Timeline()) != 2 {
		t.Errorf("timeline grew on a no-op: %v", s.Timeline())
	}
}

func TestStoreShrinksOnceTheFinerHistoryAgedOut(t *testing.T) {
	s := NewStore(10*time.Second, time.Minute) // capacity 7
	s.SetInterval(30*time.Second, at(0))       // from +30 on (first 30 s boundary after +0)
	if s.Capacity() != 7 {
		t.Errorf("capacity right after coarsening = %d, want 7 while 10 s history is in retention", s.Capacity())
	}
	s.Prune(at(30).Add(2 * time.Minute)) // the 10 s era is older than the retention
	if s.Capacity() != 3 || len(s.Timeline()) != 1 {
		t.Errorf("after pruning: capacity %d timeline %v, want 60/30 + 1 = 3 and one entry", s.Capacity(), s.Timeline())
	}
}

func TestSnapshotCarriesTheTimeline(t *testing.T) {
	a := NewStore(15*time.Second, time.Hour)
	p := PodKey{"app", "web"}
	a.Record(Round{At: at(0), Pods: map[PodKey]Resource{p: {CPU: 1}}})
	a.SetInterval(10*time.Second, at(0))
	a.Record(Round{At: at(10), Pods: map[PodKey]Resource{p: {CPU: 2}}})

	// a fresh replica at 15 s takes the peer's history of resolutions for
	// the past, so coverage over it is right, but keeps its own current
	// interval (a manual --interval must not be overridden by a peer; in
	// auto mode the collector follows metrics-server on its own); its own
	// grid starts at the first 15 s boundary after the peer's last sample
	b := NewStore(15*time.Second, time.Hour)
	b.Merge(a.Snapshot())
	tl := b.Timeline()
	if len(tl) != 3 || !tl[1].Since.Equal(at(10)) || tl[1].Step != 10*time.Second || !tl[2].Since.Equal(at(15)) || tl[2].Step != 15*time.Second {
		t.Errorf("merged timeline = %v", tl)
	}
	if b.Interval() != 15*time.Second {
		t.Errorf("interval after merge = %v, want its own 15s", b.Interval())
	}
	if got := b.PodSamples(p, time.Time{}); len(got) != 2 || !got[1].At.Equal(at(10)) {
		t.Errorf("samples slotted by the merged timeline: %v", got)
	}

	// a disk snapshot merged after the peer does not rewrite what the peer
	// said about the time it covers
	b.Merge(a.Snapshot())
	if got := b.Timeline(); len(got) != 3 {
		t.Errorf("timeline after a second merge = %v", got)
	}

	// a snapshot from a replica that predates timelines has one interval
	old := a.Snapshot()
	old.Timeline = nil
	old.Interval = 15 * time.Second
	c := NewStore(15*time.Second, time.Hour)
	c.Merge(old)
	if tl := c.Timeline(); len(tl) != 1 || tl.Current() != 15*time.Second {
		t.Errorf("timeline from an old snapshot = %v", tl)
	}
}

func TestContainerSeriesGrowWithAFinerInterval(t *testing.T) {
	s := NewStore(15*time.Second, time.Minute) // capacity 5
	p := PodKey{"app", "web"}
	rec := func(sec int) {
		s.Record(Round{At: at(sec), Pods: map[PodKey]Resource{p: {CPU: 1}},
			Containers: map[PodKey]map[string]Resource{p: {"app": {CPU: 1}, "proxy": {CPU: 1}}}})
	}
	for _, sec := range []int{0, 15, 30, 45} {
		rec(sec)
	}
	s.SetInterval(10*time.Second, at(45)) // capacity 7, from +50
	for _, sec := range []int{50, 60, 70} {
		rec(sec)
	}
	for name, got := range s.ContainerSamples(p, time.Time{}) {
		if len(got) != 7 {
			t.Errorf("container %s: %d samples, want all 7 (resized with the pod series)", name, len(got))
		}
	}
}
