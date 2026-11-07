package domain

import (
	"testing"
	"time"
)

func i64(v int64) *int64 { return new(v) }

func sparkEqual(t *testing.T, name string, got []*int64, want []*int64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: len = %d, want %d (%v)", name, len(got), len(want), deref(got))
	}
	for i := range want {
		switch {
		case want[i] == nil && got[i] != nil:
			t.Errorf("%s[%d] = %d, want gap", name, i, *got[i])
		case want[i] != nil && got[i] == nil:
			t.Errorf("%s[%d] = gap, want %d", name, i, *want[i])
		case want[i] != nil && *got[i] != *want[i]:
			t.Errorf("%s[%d] = %d, want %d", name, i, *got[i], *want[i])
		}
	}
}

func deref(in []*int64) []any {
	out := make([]any, len(in))
	for i, p := range in {
		if p != nil {
			out[i] = *p
		}
	}
	return out
}

func TestSparklineBucketsAndGaps(t *testing.T) {
	// window 0..120 s at 15 s: 120 / (2×15) = 4 buckets of 30 s, aligned to
	// the clock (t0 is on a 30 s boundary). The bucket that holds To is the
	// fifth, partial one.
	w := Window{From: at(0), To: at(120), Step: 15 * time.Second}
	in := []Sample{
		{At: at(0), Usage: Resource{CPU: 10, Memory: 1}},
		{At: at(15), Usage: Resource{CPU: 20, Memory: 2}},
		{At: at(30), Usage: Resource{CPU: 30, Memory: 3}},
		{At: at(45), Usage: Resource{CPU: 41, Memory: 4}},
		// 60 and 75 missing: the replica could not poll
		{At: at(90), Usage: Resource{CPU: 100, Memory: 10}},
		{At: at(105), Usage: Resource{CPU: 200, Memory: 20}},
		{At: at(120), Usage: Resource{CPU: 300, Memory: 30}},
	}
	sp := Sparkline(in, w, 60)
	if sp.Bucket != 30*time.Second || !sp.Start.Equal(at(0)) {
		t.Fatalf("bucket %v start %v", sp.Bucket, sp.Start)
	}
	// (10+20)/2 = 15, (30+41)/2 = 35.5 → 36, gap, (100+200)/2 = 150, 300
	sparkEqual(t, "cpu", sp.CPU, []*int64{i64(15), i64(36), nil, i64(150), i64(300)})
	// (1+2)/2 = 1.5 → 2, (3+4)/2 = 3.5 → 4, gap, 15, 30
	sparkEqual(t, "memory", sp.Memory, []*int64{i64(2), i64(4), nil, i64(15), i64(30)})
}

func TestSparklineDropsEmptyCurrentBucket(t *testing.T) {
	// the bucket holding To has just started and the next round has not
	// landed yet: that is not a gap, so it is left out
	w := Window{From: at(0), To: at(125), Step: 15 * time.Second}
	in := []Sample{
		{At: at(0), Usage: Resource{CPU: 1}}, {At: at(15), Usage: Resource{CPU: 1}},
		{At: at(30), Usage: Resource{CPU: 2}}, {At: at(45), Usage: Resource{CPU: 2}},
		{At: at(60), Usage: Resource{CPU: 3}}, {At: at(75), Usage: Resource{CPU: 3}},
		{At: at(90), Usage: Resource{CPU: 4}}, {At: at(105), Usage: Resource{CPU: 4}},
	}
	sp := Sparkline(in, w, 60)
	sparkEqual(t, "cpu", sp.CPU, []*int64{i64(1), i64(2), i64(3), i64(4)})
}

func TestSparklineAlignsToTheClock(t *testing.T) {
	// From is not on a bucket boundary: the partial first bucket would hold
	// half the samples of a full one, so the sparkline starts at the next
	// boundary. Refreshing 15 s later keeps the same buckets.
	in := []Sample{
		{At: at(15), Usage: Resource{CPU: 99}}, // before the first full bucket
		{At: at(30), Usage: Resource{CPU: 10}}, {At: at(45), Usage: Resource{CPU: 20}},
		{At: at(60), Usage: Resource{CPU: 30}}, {At: at(75), Usage: Resource{CPU: 40}},
		{At: at(90), Usage: Resource{CPU: 50}}, {At: at(105), Usage: Resource{CPU: 60}},
		{At: at(120), Usage: Resource{CPU: 70}}, {At: at(135), Usage: Resource{CPU: 80}},
	}
	a := Sparkline(in[:8], Window{From: at(10), To: at(130), Step: 15 * time.Second}, 60)
	if !a.Start.Equal(at(30)) {
		t.Fatalf("start = %v, want %v", a.Start, at(30))
	}
	sparkEqual(t, "a", a.CPU, []*int64{i64(15), i64(35), i64(55), i64(70)})

	b := Sparkline(in[1:], Window{From: at(25), To: at(145), Step: 15 * time.Second}, 60)
	if !b.Start.Equal(at(30)) {
		t.Fatalf("start = %v, want %v", b.Start, at(30))
	}
	sparkEqual(t, "b", b.CPU, []*int64{i64(15), i64(35), i64(55), i64(75)})
}

func TestSparklineCapsPointsAndCoversMissingHistory(t *testing.T) {
	// 30 min at 15 s would allow 60 buckets of 30 s; at most 10 → 3 min each.
	// Only the last 6 min have samples (a fresh replica with no peer): the
	// buckets before are gaps, not a shorter window.
	w := Window{From: at(0), To: at(1800), Step: 15 * time.Second}
	var in []Sample
	for s := 1440; s <= 1800; s += 15 {
		in = append(in, Sample{At: at(s), Usage: Resource{CPU: 7, Memory: 7}})
	}
	sp := Sparkline(in, w, 10)
	if sp.Bucket != 3*time.Minute {
		t.Fatalf("bucket = %v", sp.Bucket)
	}
	want := make([]*int64, 11) // 10 full buckets + the one holding To
	for i := 8; i <= 10; i++ {
		want[i] = i64(7)
	}
	sparkEqual(t, "cpu", sp.CPU, want)
}

func TestSparklineEmpty(t *testing.T) {
	sp := Sparkline(nil, Window{From: at(0), To: at(60), Step: 15 * time.Second}, 60)
	// 2 buckets of 30 s, both gaps; the current one is dropped
	sparkEqual(t, "cpu", sp.CPU, []*int64{nil, nil})
	if len(sp.Memory) != 2 {
		t.Errorf("memory len = %d", len(sp.Memory))
	}
	if sp := Sparkline(nil, Window{}, 60); len(sp.CPU) != 0 || sp.Bucket != 0 {
		t.Errorf("zero window = %+v", sp)
	}
}

func TestSparklineBucketsFitTheCoarsestIntervalInTheWindow(t *testing.T) {
	// 15 s until +300, 30 s after: 30 s buckets (2 × 15 s) would each hold
	// at most one 30 s sample and show a gap for one late round; the bucket
	// follows the coarsest interval in the window: 2 × 30 s
	tl := Timeline{{Step: 15 * time.Second}, {Since: at(300), Step: 30 * time.Second}}
	sp := Sparkline(nil, Window{From: at(0), To: at(600), Step: 30 * time.Second, Timeline: tl}, 60)
	if sp.Bucket != time.Minute {
		t.Errorf("bucket = %v, want 1m", sp.Bucket)
	}
	// a window entirely after the change: the same
	sp = Sparkline(nil, Window{From: at(300), To: at(600), Step: 30 * time.Second, Timeline: tl}, 60)
	if sp.Bucket != time.Minute {
		t.Errorf("bucket after the change = %v, want 1m", sp.Bucket)
	}
	// entirely before it: 2 × 15 s
	sp = Sparkline(nil, Window{From: at(0), To: at(240), Step: 30 * time.Second, Timeline: tl}, 60)
	if sp.Bucket != 30*time.Second {
		t.Errorf("bucket before the change = %v, want 30s", sp.Bucket)
	}
}
