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

// Run polls until ctx is done. The first poll happens immediately.
func (c *Collector) Run(ctx context.Context) {
	c.PollOnce(ctx)
	tick := time.NewTicker(c.store.Interval())
	defer tick.Stop()
	prune := time.NewTicker(time.Minute)
	defer prune.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			c.PollOnce(ctx)
		case now := <-prune.C:
			c.store.Prune(now)
		}
	}
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
