package domain

import (
	"testing"
	"time"
)

func samples(cpus ...int64) []Sample {
	out := make([]Sample, len(cpus))
	for i, c := range cpus {
		out[i] = Sample{At: at(i * 15), Usage: Resource{CPU: c, Memory: c * 1000}}
	}
	return out
}

func TestComputeStatsHandComputed(t *testing.T) {
	// 1 2 3 4 100: avg 22, median 3, p95 (nearest-rank, ceil(0.95*5)=5th) 100
	st := ComputeStats(samples(1, 2, 3, 4, 100), Window{From: t0, To: at(60), Step: 15 * time.Second})
	if st.Now.CPU != 100 {
		t.Errorf("now = %d, want 100", st.Now.CPU)
	}
	if st.Avg.CPU != 22 {
		t.Errorf("avg = %d, want 22", st.Avg.CPU)
	}
	if st.Median.CPU != 3 {
		t.Errorf("median = %d, want 3", st.Median.CPU)
	}
	if st.P95.CPU != 100 {
		t.Errorf("p95 = %d, want 100", st.P95.CPU)
	}
	if st.Max.CPU != 100 || st.Min.CPU != 1 {
		t.Errorf("min/max = %d/%d, want 1/100", st.Min.CPU, st.Max.CPU)
	}
	if st.Median.Memory != 3000 {
		t.Errorf("median mem = %d, want 3000", st.Median.Memory)
	}
	if st.Samples != 5 || st.Expected != 5 || st.Coverage != 1 {
		t.Errorf("samples/expected/coverage = %d/%d/%v, want 5/5/1", st.Samples, st.Expected, st.Coverage)
	}
}

func TestComputeStatsEvenCountMedianIsMeanOfMiddle(t *testing.T) {
	st := ComputeStats(samples(10, 20, 30, 40), Window{From: t0, To: at(45), Step: 15 * time.Second})
	if st.Median.CPU != 25 {
		t.Errorf("median = %d, want 25", st.Median.CPU)
	}
}

func TestComputeStatsReportsGaps(t *testing.T) {
	// 30 minutes at 15 s, endpoints inclusive = 121 expected samples, 5 present
	st := ComputeStats(samples(1, 2, 3, 4, 5), Window{From: t0, To: t0.Add(30 * time.Minute), Step: 15 * time.Second})
	if st.Expected != 121 {
		t.Errorf("expected = %d, want 121", st.Expected)
	}
	if st.Coverage < 0.04 || st.Coverage > 0.05 {
		t.Errorf("coverage = %v, want ~0.0413", st.Coverage)
	}
}

func TestComputeStatsEmpty(t *testing.T) {
	st := ComputeStats(nil, Window{From: t0, To: at(60), Step: 15 * time.Second})
	if st.Samples != 0 || !st.Now.IsZero() || st.Coverage != 0 {
		t.Errorf("empty stats = %+v", st)
	}
}

func TestComputeStatsDoesNotMutateInput(t *testing.T) {
	in := samples(5, 1, 3)
	ComputeStats(in, Window{From: t0, To: at(60), Step: 15 * time.Second})
	if in[0].Usage.CPU != 5 || in[1].Usage.CPU != 1 {
		t.Errorf("input reordered: %v", in)
	}
}

func TestRatio(t *testing.T) {
	if got := Ratio(500, 2000); got != 0.25 {
		t.Errorf("ratio = %v, want 0.25", got)
	}
	if got := Ratio(500, 0); got != 0 {
		t.Errorf("ratio with zero denominator = %v, want 0", got)
	}
}
