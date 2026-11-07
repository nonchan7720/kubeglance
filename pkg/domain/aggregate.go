package domain

import (
	"math"
	"slices"
	"time"
)

// SumByInstant adds several series together: samples taken at the same
// instant are summed, so the result is the usage of the group over time.
// All pods of a poll round share the round's timestamp, which is what makes
// this exact rather than approximate. The result is oldest first. A series
// that lacks an instant contributes nothing to it (the pod did not exist or
// was not reported), which is the correct total for that instant.
func SumByInstant(series ...[]Sample) []Sample {
	sums := map[time.Time]Resource{}
	for _, s := range series {
		for _, x := range s {
			sums[x.At] = sums[x.At].Add(x.Usage)
		}
	}
	out := make([]Sample, 0, len(sums))
	for at, u := range sums {
		out = append(out, Sample{At: at, Usage: u})
	}
	slices.SortFunc(out, func(a, b Sample) int { return a.At.Compare(b.At) })
	return out
}

// ImbalanceStats describes how unevenly a set of ratios (one per node) is
// spread. It is what the Node view uses to answer "should I rebalance?".
type ImbalanceStats struct {
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
	Mean   float64 `json:"mean"`
	Spread float64 `json:"spread"` // Max - Min
	StdDev float64 `json:"stddev"` // population standard deviation
}

// Imbalance computes ImbalanceStats over ratios. Fewer than two values give
// a zero spread.
func Imbalance(ratios []float64) ImbalanceStats {
	if len(ratios) == 0 {
		return ImbalanceStats{}
	}
	st := ImbalanceStats{Min: ratios[0], Max: ratios[0]}
	var sum float64
	for _, r := range ratios {
		st.Min = min(st.Min, r)
		st.Max = max(st.Max, r)
		sum += r
	}
	st.Mean = sum / float64(len(ratios))
	st.Spread = st.Max - st.Min
	var sq float64
	for _, r := range ratios {
		d := r - st.Mean
		sq += d * d
	}
	st.StdDev = math.Sqrt(sq / float64(len(ratios)))
	return st
}
