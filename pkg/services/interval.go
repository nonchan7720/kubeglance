package services

import (
	"context"
	"log/slog"
	"time"
)

// IntervalPolicy bounds the detection of metrics-server's resolution.
type IntervalPolicy struct {
	Timeout  time.Duration // keep trying this long (metrics-server answers once it scraped twice)
	Retry    time.Duration // pause between tries
	Fallback time.Duration // used when nothing believable was detected
}

// metrics-server refuses a --metric-resolution below 10 s; a detected value
// outside these bounds is not its resolution.
const (
	minResolution = 10 * time.Second
	maxResolution = 5 * time.Minute
)

// ResolveInterval returns the poll interval: configured when it is set (> 0),
// else metrics-server's resolution as detect reports it. Polling at exactly
// that resolution reads every new value once: faster would read the same
// value twice (more memory, no information), slower would miss values. When
// detection keeps failing or reports something outside [10s, 5m], it returns
// the policy's fallback and says so.
func ResolveInterval(ctx context.Context, configured time.Duration, detect func(context.Context) (time.Duration, error), pol IntervalPolicy, log *slog.Logger) time.Duration {
	if log == nil {
		log = slog.Default()
	}
	if configured > 0 {
		log.Info("poll interval set by configuration", "interval", configured)
		return configured
	}
	deadline := time.Now().Add(pol.Timeout)
	for attempt := 1; ; attempt++ {
		d, err := detect(ctx)
		switch {
		case err == nil && d >= minResolution && d <= maxResolution:
			log.Info("poll interval follows metrics-server's resolution", "interval", d)
			return d
		case err == nil:
			log.Warn("detected metrics-server resolution is implausible; using the fallback", "detected", d, "interval", pol.Fallback)
			return pol.Fallback
		}
		left := time.Until(deadline)
		if ctx.Err() != nil || left <= 0 {
			log.Warn("metrics-server resolution not detected; using the fallback", "attempts", attempt, "err", err, "interval", pol.Fallback)
			return pol.Fallback
		}
		select {
		case <-ctx.Done():
		case <-time.After(min(pol.Retry, left)):
		}
	}
}
