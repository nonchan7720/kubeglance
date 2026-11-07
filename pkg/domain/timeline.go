package domain

import "time"

// Resolution is the sampling interval in effect from Since on.
type Resolution struct {
	Since time.Time     `json:"since,omitzero"`
	Step  time.Duration `json:"step"`
}

// Timeline is the history of a store's sampling interval, oldest first. The
// first entry also applies to everything before it, so a timeline is never
// empty in use. The interval changes when metrics-server's resolution does
// (kubeglance follows it without a restart); statistics over a window that
// spans a change count the samples each part should hold at its own step.
type Timeline []Resolution

func (t Timeline) entry(at time.Time) int {
	i := 0
	for j := 1; j < len(t); j++ {
		if !t[j].Since.After(at) {
			i = j
		}
	}
	return i
}

// At is the step in effect at the given time.
func (t Timeline) At(at time.Time) time.Duration { return t[t.entry(at)].Step }

// Current is the step in effect now (the last entry).
func (t Timeline) Current() time.Duration { return t[len(t)-1].Step }

// span calls f with every part of [from, to] and its step.
func (t Timeline) span(from, to time.Time, f func(a, b time.Time, step time.Duration)) {
	for i, r := range t {
		start, end := r.Since, to
		if i == 0 {
			start = from
		}
		if i+1 < len(t) {
			end = t[i+1].Since
		}
		a, b := later(start, from), earlier(end, to)
		// every part but the last is half-open [Since, next Since)
		if (i+1 < len(t) && !b.After(a)) || b.Before(a) {
			continue
		}
		f(a, b, r.Step)
	}
}

// Expected is how many samples a gap-free [from, to] holds, each part at
// its own step: a part that ends before to is half-open and holds
// ceil(length / step) instants; the part that ends at to also holds its end.
// With one resolution this is (to - from) / step + 1.
func (t Timeline) Expected(from, to time.Time) int {
	n := 0
	t.span(from, to, func(a, b time.Time, step time.Duration) {
		d := b.Sub(a)
		if b.Before(to) {
			n += int((d + step - 1) / step)
		} else {
			n += int(d/step) + 1
		}
	})
	return n
}

// Coarsest is the largest step in effect anywhere in [from, to].
func (t Timeline) Coarsest(from, to time.Time) time.Duration {
	var c time.Duration
	t.span(from, to, func(_, _ time.Time, step time.Duration) { c = max(c, step) })
	return c
}

// Finest is the smallest step in effect anywhere in [from, to].
func (t Timeline) Finest(from, to time.Time) time.Duration {
	var c time.Duration
	t.span(from, to, func(_, _ time.Time, step time.Duration) {
		if c == 0 || step < c {
			c = step
		}
	})
	return c
}

// finest is the smallest step of the whole timeline.
func (t Timeline) finest() time.Duration {
	f := t[0].Step
	for _, r := range t[1:] {
		f = min(f, r.Step)
	}
	return f
}

// merge combines a store's own timeline with one from a snapshot: the
// incoming one describes the time before own's first change (own's first
// entry was only its start-up guess for the past), own describes the rest.
// own's current step stays current: it is the store owner's to set. When the
// combination ends on another step, own's resumes at its first boundary after
// last (the newest instant the incoming history covers).
func (t Timeline) merge(in Timeline, last time.Time) Timeline {
	if len(in) == 0 {
		return t
	}
	var out Timeline
	if len(t) > 1 {
		cutoff := t[1].Since
		for _, r := range in {
			if r.Since.Before(cutoff) {
				out = append(out, r)
			}
		}
		out = append(out, t[1:]...)
	} else {
		out = append(out, in...)
	}
	if cur := t.Current(); out.Current() != cur {
		since := later(last, out[len(out)-1].Since).Truncate(cur).Add(cur)
		out = append(out, Resolution{Since: since, Step: cur})
	}
	return out
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func earlier(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
