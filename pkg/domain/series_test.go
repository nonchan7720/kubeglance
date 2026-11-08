package domain

import (
	"math"
	"testing"
	"time"
	"unsafe"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func at(sec int) time.Time { return t0.Add(time.Duration(sec) * time.Second) }

func TestSeriesKeepsOnlyCapacityNewestInOrder(t *testing.T) {
	s := NewSeries(3)
	for i := range 5 {
		s.Add(Sample{At: at(i), Usage: Resource{CPU: int64(i), Memory: int64(i * 10)}})
	}
	if s.Len() != 3 {
		t.Fatalf("len = %d, want 3", s.Len())
	}
	got := s.Since(t0)
	want := []int64{2, 3, 4}
	if len(got) != len(want) {
		t.Fatalf("got %d samples, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Usage.CPU != w {
			t.Errorf("sample %d cpu = %d, want %d", i, got[i].Usage.CPU, w)
		}
	}
	last, ok := s.Last()
	if !ok || last.Usage.CPU != 4 {
		t.Errorf("last = %+v, ok=%v; want cpu 4", last, ok)
	}
}

func TestSeriesSinceFiltersByTime(t *testing.T) {
	s := NewSeries(10)
	for i := range 10 {
		s.Add(Sample{At: at(i * 15)})
	}
	got := s.Since(at(100)) // 105, 120, 135
	if len(got) != 3 {
		t.Fatalf("got %d samples, want 3: %v", len(got), got)
	}
	if !got[0].At.Equal(at(105)) {
		t.Errorf("first = %v, want %v", got[0].At, at(105))
	}
}

func TestSeriesIgnoresOutOfOrderSamples(t *testing.T) {
	s := NewSeries(4)
	s.Add(Sample{At: at(30)})
	s.Add(Sample{At: at(15)}) // older than the newest: dropped
	s.Add(Sample{At: at(30)}) // same instant: dropped
	if s.Len() != 1 {
		t.Fatalf("len = %d, want 1", s.Len())
	}
}

func TestEmptySeries(t *testing.T) {
	s := NewSeries(2)
	if _, ok := s.Last(); ok {
		t.Error("Last on empty series reported ok")
	}
	if got := s.Since(t0); len(got) != 0 {
		t.Errorf("Since on empty = %v", got)
	}
}

func TestSeriesSampleTakes16Bytes(t *testing.T) {
	// README "Memory" counts 16 bytes a sample: the size of what a series
	// holds, not of Sample (whose time.Time alone is 24 bytes)
	if n := unsafe.Sizeof(packed{}); n != 16 {
		t.Errorf("a stored sample is %d bytes, README says 16", n)
	}
}

func TestSeriesKeepsValuesAndInstantsExactly(t *testing.T) {
	s := NewSeries(4)
	in := []Sample{
		{At: at(0), Usage: Resource{CPU: 0, Memory: 0}},
		{At: at(15), Usage: Resource{CPU: 3_999_999, Memory: 1 << 40}}, // 4,000 cores, 1 TiB
		{At: time.Date(2099, 12, 31, 23, 59, 45, 0, time.UTC), Usage: Resource{CPU: 1, Memory: 123_456_789}},
	}
	for _, x := range in {
		s.Add(x)
	}
	got := s.All()
	if len(got) != len(in) {
		t.Fatalf("len = %d", len(got))
	}
	for i := range in {
		if !got[i].At.Equal(in[i].At) || got[i].At.Location() != time.UTC || got[i].Usage != in[i].Usage {
			t.Errorf("sample %d = %+v, want %+v (UTC)", i, got[i], in[i])
		}
	}
}

func TestSeriesSaturatesOutOfRangeCPU(t *testing.T) {
	// beyond 4,294 cores per pod or node the stored CPU saturates instead of
	// wrapping round to a small number; usage is never negative
	s := NewSeries(2)
	s.Add(Sample{At: at(0), Usage: Resource{CPU: 1 << 40}})
	s.Add(Sample{At: at(15), Usage: Resource{CPU: -5}})
	got := s.All()
	if got[0].Usage.CPU != math.MaxUint32 || got[1].Usage.CPU != 0 {
		t.Errorf("cpu = %d, %d", got[0].Usage.CPU, got[1].Usage.CPU)
	}
}
