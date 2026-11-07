// Package domain holds the pure model of kubeglance: resource samples, the
// bounded time series that keeps them, the statistics computed over a window
// and the aggregation of pods into workloads, nodes and the cluster.
//
// It imports nothing else in the repository and nothing from Kubernetes.
package domain

import "time"

// Resource is a CPU / memory pair. CPU is in millicores, Memory in bytes.
// The zero value means "nothing" (no usage, no request, no limit).
type Resource struct {
	CPU    int64 `json:"cpu"`
	Memory int64 `json:"memory"`
}

// Add returns r + o.
func (r Resource) Add(o Resource) Resource {
	return Resource{CPU: r.CPU + o.CPU, Memory: r.Memory + o.Memory}
}

// IsZero reports whether both values are zero.
func (r Resource) IsZero() bool { return r.CPU == 0 && r.Memory == 0 }

// Sample is the usage of one entity at one instant.
type Sample struct {
	At    time.Time `json:"at"`
	Usage Resource  `json:"usage"`
}

// Series is a fixed-capacity ring buffer of samples in ascending time order.
// Adding to a full series drops the oldest sample. A sample that is not newer
// than the newest one is ignored, so two replicas replaying the same poll
// round cannot duplicate it. Series is not goroutine-safe; Store guards it.
type Series struct {
	buf  []Sample
	head int // index of the oldest sample
	n    int
}

// NewSeries returns a series that keeps at most capacity samples.
func NewSeries(capacity int) *Series {
	if capacity < 1 {
		capacity = 1
	}
	return &Series{buf: make([]Sample, capacity)}
}

// Cap is the capacity.
func (s *Series) Cap() int { return len(s.buf) }

// Len is the number of samples held.
func (s *Series) Len() int { return s.n }

// Add appends a sample, dropping the oldest when full.
func (s *Series) Add(x Sample) {
	if last, ok := s.Last(); ok && !x.At.After(last.At) {
		return
	}
	if s.n < len(s.buf) {
		s.buf[(s.head+s.n)%len(s.buf)] = x
		s.n++
		return
	}
	s.buf[s.head] = x
	s.head = (s.head + 1) % len(s.buf)
}

// Last returns the newest sample.
func (s *Series) Last() (Sample, bool) {
	if s.n == 0 {
		return Sample{}, false
	}
	return s.buf[(s.head+s.n-1)%len(s.buf)], true
}

// Since returns the samples taken at or after from, oldest first, as a new
// slice. A zero from returns everything.
func (s *Series) Since(from time.Time) []Sample {
	out := make([]Sample, 0, s.n)
	for i := range s.n {
		x := s.buf[(s.head+i)%len(s.buf)]
		if !x.At.Before(from) {
			out = append(out, x)
		}
	}
	return out
}

// All returns every sample, oldest first.
func (s *Series) All() []Sample { return s.Since(time.Time{}) }
