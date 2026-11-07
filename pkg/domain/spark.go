package domain

import (
	"math"
	"time"
)

// Spark is the shape of a window, for drawing a sparkline next to the
// numbers. It is a picture, not a statistic: every Stats field is still
// computed from the raw samples.
//
// The window is cut into buckets of Bucket width aligned to the clock (Unix
// time), so a refresh 15 s later re-uses the same buckets and the picture only
// slides instead of changing shape. Point i covers
// [Start + i·Bucket, Start + (i+1)·Bucket) and holds the mean of the samples
// in it, or nil when there are none: a gap, drawn as a gap.
type Spark struct {
	Start  time.Time     `json:"start"`
	Bucket time.Duration `json:"bucket"`
	CPU    []*int64      `json:"cpu"`
	Memory []*int64      `json:"memory"`
}

// Sparkline buckets samples (oldest first) over w into at most about
// maxPoints buckets. A bucket is at least two sampling steps wide, so a
// single late poll round does not show as a gap; Stats.Coverage reports
// those. The partial bucket before the first clock boundary is left out (it
// would hold fewer samples than the others), and so is the bucket holding
// w.To while it is still empty: the next round has not landed yet, which is
// not a gap.
func Sparkline(in []Sample, w Window, maxPoints int) Spark {
	if w.Step <= 0 || !w.To.After(w.From) || maxPoints < 1 {
		return Spark{CPU: []*int64{}, Memory: []*int64{}}
	}
	d := w.To.Sub(w.From)
	bucket := max(2*w.Step, roundUp(d/time.Duration(maxPoints), w.Step))

	first := ceilDiv(w.From.UnixNano(), int64(bucket))
	last := floorDiv(w.To.UnixNano(), int64(bucket))
	n := max(0, int(last-first+1))

	type sum struct {
		cpu, mem int64
		count    int
	}
	sums := make([]sum, n)
	for _, s := range in {
		if s.At.Before(w.From) || s.At.After(w.To) {
			continue
		}
		i := int(floorDiv(s.At.UnixNano(), int64(bucket)) - first)
		if i < 0 || i >= n {
			continue
		}
		sums[i].cpu += s.Usage.CPU
		sums[i].mem += s.Usage.Memory
		sums[i].count++
	}
	if n > 0 && sums[n-1].count == 0 {
		n--
	}

	sp := Spark{
		Start:  time.Unix(0, first*int64(bucket)).In(w.From.Location()),
		Bucket: bucket,
		CPU:    make([]*int64, n),
		Memory: make([]*int64, n),
	}
	for i, s := range sums[:n] {
		if s.count == 0 {
			continue
		}
		c := int64(math.Round(float64(s.cpu) / float64(s.count)))
		m := int64(math.Round(float64(s.mem) / float64(s.count)))
		sp.CPU[i], sp.Memory[i] = &c, &m
	}
	return sp
}

// roundUp rounds d up to a multiple of step.
func roundUp(d, step time.Duration) time.Duration {
	return (d + step - 1) / step * step
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

func ceilDiv(a, b int64) int64 {
	return -floorDiv(-a, b)
}
