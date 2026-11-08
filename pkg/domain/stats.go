package domain

import (
	"math"
	"slices"
	"time"
)

// Window is the time range statistics are computed over. Step is the
// sampling interval, used only to say how many samples the window should
// hold (Expected) so gaps can be reported.
type Window struct {
	From time.Time
	To   time.Time
	Step time.Duration
	// Timeline, when set, replaces Step for Expected: a window that spans a
	// change of the interval counts each part at its own step.
	Timeline Timeline
}

// Stats summarizes the samples of one entity (or an aggregate) in a window.
// Every Resource field is computed independently for CPU and memory.
type Stats struct {
	Now    Resource `json:"now"`    // newest sample in the window
	Avg    Resource `json:"avg"`    // arithmetic mean
	Median Resource `json:"median"` // 50th percentile (mean of the two middle values for an even count)
	P95    Resource `json:"p95"`    // 95th percentile, nearest-rank
	Min    Resource `json:"min"`
	Max    Resource `json:"max"`

	Samples  int     `json:"samples"`  // samples actually present
	Expected int     `json:"expected"` // samples a gap-free window would hold
	Coverage float64 `json:"coverage"` // Samples / Expected, capped at 1
}

// ComputeStats computes Stats over samples (oldest first). The input is not
// modified. Samples is the length of the input: the caller selects the window
// (Series.Since), this function only describes it.
func ComputeStats(in []Sample, w Window) Stats {
	st := Stats{Samples: len(in)}
	switch {
	case len(w.Timeline) > 0 && w.To.After(w.From):
		st.Expected = w.Timeline.Expected(w.From, w.To)
	case w.Step > 0 && w.To.After(w.From):
		st.Expected = gridInstants(w.From, w.To, w.Step, true)
	}
	if st.Expected > 0 {
		st.Coverage = min(1, float64(len(in))/float64(st.Expected))
	}
	if len(in) == 0 {
		return st
	}
	st.Now = in[len(in)-1].Usage

	cpu := make([]int64, len(in))
	mem := make([]int64, len(in))
	for i, s := range in {
		cpu[i] = s.Usage.CPU
		mem[i] = s.Usage.Memory
	}
	slices.Sort(cpu)
	slices.Sort(mem)

	st.Min = Resource{CPU: cpu[0], Memory: mem[0]}
	st.Max = Resource{CPU: cpu[len(cpu)-1], Memory: mem[len(mem)-1]}
	st.Avg = Resource{CPU: mean(cpu), Memory: mean(mem)}
	st.Median = Resource{CPU: median(cpu), Memory: median(mem)}
	st.P95 = Resource{CPU: percentile(cpu, 0.95), Memory: percentile(mem, 0.95)}
	return st
}

func mean(sorted []int64) int64 {
	var sum int64
	for _, v := range sorted {
		sum += v
	}
	return int64(math.Round(float64(sum) / float64(len(sorted))))
}

func median(sorted []int64) int64 {
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return int64(math.Round(float64(sorted[n/2-1]+sorted[n/2]) / 2))
}

// percentile is the nearest-rank percentile of a sorted slice.
func percentile(sorted []int64, p float64) int64 {
	rank := max(1, int(math.Ceil(p*float64(len(sorted)))))
	return sorted[rank-1]
}

// Ratio returns used / total, or 0 when total is 0 (no request or limit set).
func Ratio(used, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(used) / float64(total)
}
