package domain

import (
	"testing"
	"time"
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
