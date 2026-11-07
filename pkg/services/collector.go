package services

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/nonchan7720/kubeglance/pkg/domain"
)

// Collector polls a MetricsSource at the store's interval and records each
// round. It prunes the store once a minute. It never polls faster than the
// interval: metrics-server itself only refreshes every 15 s, so a shorter
// interval would read the same numbers twice.
type Collector struct {
	store  *domain.Store
	source MetricsSource
	log    *slog.Logger
	now    func() time.Time

	mu          sync.RWMutex
	lastSuccess time.Time
	lastError   error
	rounds      int
}

// NewCollector wires a source to a store.
func NewCollector(store *domain.Store, source MetricsSource, log *slog.Logger) *Collector {
	if log == nil {
		log = slog.Default()
	}
	return &Collector{store: store, source: source, log: log, now: time.Now}
}

// Run polls until ctx is done: once immediately, then in the middle of every
// following slot (Store.Slot), so every replica polls each slot once
// whenever it started, with half an interval of slack for a late timer or a
// slow poll on either side.
func (c *Collector) Run(ctx context.Context) {
	prune := time.NewTicker(time.Minute)
	defer prune.Stop()
	var last time.Time // slot of the last poll attempt
	for {
		now := c.now()
		timer := time.NewTimer(nextPoll(last, now, c.store.Interval()).Sub(now))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			last = c.store.Slot(c.now())
			c.PollOnce(ctx)
		case now := <-prune.C:
			timer.Stop()
			c.store.Prune(now)
		}
	}
}

// nextPoll is when to poll after an attempt in slot last (zero: none yet):
// the middle of the slot after last, or now when that middle has passed but
// the slot it belongs to has not been polled. A slot is never polled twice
// and never skipped while the process keeps up.
func nextPoll(last, now time.Time, interval time.Duration) time.Time {
	if last.IsZero() {
		return now
	}
	slot := last.Add(interval)
	if cur := now.Truncate(interval); cur.After(slot) {
		slot = cur // the slot after last is gone; poll the current one
	}
	if mid := slot.Add(interval / 2); mid.After(now) {
		return mid
	}
	return now
}

// PollOnce performs one poll and records it. Errors are logged and kept for
// Health; they never stop the loop.
func (c *Collector) PollOnce(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, c.store.Interval())
	defer cancel()
	round, err := c.source.Poll(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.lastError = err
		c.log.Warn("poll failed", "err", err)
		return
	}
	if round.At.IsZero() {
		round.At = c.now()
	}
	c.store.Record(round)
	c.lastSuccess, c.lastError = round.At, nil
	c.rounds++
	c.log.Debug("poll recorded", "at", round.At, "pods", len(round.Pods), "nodes", len(round.Nodes))
}

// Health describes the collector for readiness and the UI.
type Health struct {
	LastSuccess time.Time `json:"lastSuccess,omitzero"`
	LastError   string    `json:"lastError,omitempty"`
	Rounds      int       `json:"rounds"`
	Healthy     bool      `json:"healthy"` // a success within two intervals
}

// Health returns the current health.
func (c *Collector) Health() Health {
	c.mu.RLock()
	defer c.mu.RUnlock()
	h := Health{LastSuccess: c.lastSuccess, Rounds: c.rounds}
	if c.lastError != nil {
		h.LastError = c.lastError.Error()
	}
	h.Healthy = !c.lastSuccess.IsZero() && c.now().Sub(c.lastSuccess) < 2*c.store.Interval()
	return h
}
