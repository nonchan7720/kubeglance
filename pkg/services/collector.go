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

	mu           sync.RWMutex
	lastSuccess  time.Time
	lastError    error
	rounds       int
	errors       int
	lastDuration time.Duration

	// following metrics-server's resolution (off when --interval is set)
	follow    bool
	candidate time.Duration // a new resolution seen in `seen` rounds in a row
	seen      int

	roundCh chan struct{} // signalled after every recorded round (Rounds)
}

// roundsToSwitch is how many rounds in a row must report a new resolution
// before the interval follows it: one slow scrape or a restarting
// metrics-server must not move the grid.
const roundsToSwitch = 3

// FollowResolution makes the collector change the store's interval, without
// a restart, when the rounds report that metrics-server's resolution
// changed. Off when the interval was set by hand.
func (c *Collector) FollowResolution(on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.follow = on
}

// NewCollector wires a source to a store.
func NewCollector(store *domain.Store, source MetricsSource, log *slog.Logger) *Collector {
	if log == nil {
		log = slog.Default()
	}
	return &Collector{store: store, source: source, log: log, now: time.Now, roundCh: make(chan struct{}, 1)}
}

// Rounds is signalled after every recorded round. The signal never blocks
// the poll: rounds nobody has taken yet collapse into one.
func (c *Collector) Rounds() <-chan struct{} { return c.roundCh }

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
	// on the current grid even right after the interval changed
	slot := last.Add(interval).Truncate(interval)
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
	start := c.now()
	round, err := c.source.Poll(ctx)
	took := c.now().Sub(start)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastDuration = took
	if err != nil {
		c.lastError = err
		c.errors++
		c.log.Warn("poll failed", "err", err)
		return
	}
	if round.At.IsZero() {
		round.At = c.now()
	}
	c.store.Record(round)
	c.lastSuccess, c.lastError = round.At, nil
	c.rounds++
	c.followLocked(round)
	select {
	case c.roundCh <- struct{}{}:
	default: // one is already waiting
	}
	c.log.Debug("poll recorded", "at", round.At, "pods", len(round.Pods), "nodes", len(round.Nodes))
}

// followLocked counts rounds that report a resolution other than the
// interval and switches after roundsToSwitch of them in a row.
func (c *Collector) followLocked(round domain.Round) {
	r := round.Resolution
	if !c.follow || r < minResolution || r > maxResolution || r == c.store.Interval() {
		c.candidate, c.seen = 0, 0
		return
	}
	if r != c.candidate {
		c.candidate, c.seen = r, 0
	}
	c.seen++
	if c.seen < roundsToSwitch {
		return
	}
	from := c.store.Interval()
	c.store.SetInterval(r, c.store.Slot(round.At))
	c.candidate, c.seen = 0, 0
	c.log.Info("metrics-server resolution changed; poll interval follows", "from", from, "to", r)
}

// Health describes the collector for readiness and the UI.
type Health struct {
	LastSuccess time.Time `json:"lastSuccess,omitzero"`
	LastError   string    `json:"lastError,omitempty"`
	Rounds      int       `json:"rounds"`
	Healthy     bool      `json:"healthy"` // a success within two intervals
	// Errors counts failed polls since start; LastDuration is how long the
	// last poll (success or not) took. Both are for /metrics.
	Errors       int           `json:"errors"`
	LastDuration time.Duration `json:"lastDuration"`
}

// Health returns the current health.
func (c *Collector) Health() Health {
	c.mu.RLock()
	defer c.mu.RUnlock()
	h := Health{LastSuccess: c.lastSuccess, Rounds: c.rounds, Errors: c.errors, LastDuration: c.lastDuration}
	if c.lastError != nil {
		h.LastError = c.lastError.Error()
	}
	h.Healthy = !c.lastSuccess.IsZero() && c.now().Sub(c.lastSuccess) < 2*c.store.Interval()
	return h
}
