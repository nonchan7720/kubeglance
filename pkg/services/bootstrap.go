package services

import (
	"context"
	"log/slog"
	"time"

	"github.com/nonchan7720/kubeglance/pkg/domain"
)

// PeerFinder lists the replicas to copy history from right now: the Ready
// endpoints of the headless Service, this pod excluded. It is called again
// on every retry, so a peer that became Ready meanwhile is found.
type PeerFinder func(ctx context.Context) ([]PeerSource, error)

// BootstrapPolicy bounds how long a starting replica waits for a peer.
type BootstrapPolicy struct {
	Timeout time.Duration // give up on peers after this long (0: one try)
	Retry   time.Duration // pause between tries
}

// Bootstrap fills an empty store at start-up so a rolling update loses no
// history. It copies from every peer the finder lists and merges them all
// (a gap one replica has, another fills; Store.Merge deduplicates by slot),
// then merges the on-disk snapshot, the safety net when every replica was
// down.
//
// When peers exist but none could be copied from, it looks them up and
// tries again until the policy's timeout: becoming Ready without the copy
// would let the old replica be terminated and its history lost. No peers at
// all (the first replica of an install) is not a failure and does not wait.
// A peer that answered had itself copied before it became Ready, so one
// answer is a complete history.
//
// Bootstrap returns the number of sources merged.
func Bootstrap(ctx context.Context, store *domain.Store, find PeerFinder, disk SnapshotStore, pol BootstrapPolicy, log *slog.Logger) int {
	if log == nil {
		log = slog.Default()
	}
	merged := copyFromPeers(ctx, store, find, pol, log)
	if disk != nil {
		snap, ok, err := disk.Load()
		switch {
		case err != nil:
			log.Warn("snapshot on disk could not be read", "err", err)
		case ok:
			store.Merge(snap)
			merged++
			log.Info("history loaded from disk", "pods", len(snap.Pods), "nodes", len(snap.Nodes), "takenAt", snap.TakenAt)
		}
	}
	return merged
}

func copyFromPeers(ctx context.Context, store *domain.Store, find PeerFinder, pol BootstrapPolicy, log *slog.Logger) int {
	deadline := time.Now().Add(pol.Timeout)
	for attempt := 1; ; attempt++ {
		peers, err := find(ctx)
		switch {
		case err != nil:
			log.Warn("peer discovery failed", "attempt", attempt, "err", err)
		case len(peers) == 0:
			log.Info("no peers; starting without peer history")
			return 0
		default:
			if n := fetchAll(ctx, store, peers, log); n > 0 {
				return n
			}
		}
		left := time.Until(deadline)
		if ctx.Err() != nil || left <= 0 {
			log.Error("no peer history copied; starting without it", "attempts", attempt, "timeout", pol.Timeout)
			return 0
		}
		select {
		case <-ctx.Done():
		case <-time.After(min(pol.Retry, left)): // the last try lands on the deadline
		}
	}
}

func fetchAll(ctx context.Context, store *domain.Store, peers []PeerSource, log *slog.Logger) int {
	merged := 0
	for _, p := range peers {
		pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		snap, err := p.Fetch(pctx)
		cancel()
		if err != nil {
			log.Info("peer not available for bootstrap", "peer", p.String(), "err", err)
			continue
		}
		store.Merge(snap)
		merged++
		log.Info("history copied from peer", "peer", p.String(), "pods", len(snap.Pods), "nodes", len(snap.Nodes), "takenAt", snap.TakenAt)
	}
	return merged
}

// Persister writes a snapshot to disk every interval and once more when its
// context ends (SIGTERM), so a full restart of every replica resumes from at
// most one interval back.
type Persister struct {
	store    *domain.Store
	disk     SnapshotStore
	interval time.Duration
	log      *slog.Logger
}

// NewPersister returns a Persister; a nil disk disables it (Run returns at once).
func NewPersister(store *domain.Store, disk SnapshotStore, interval time.Duration, log *slog.Logger) *Persister {
	if log == nil {
		log = slog.Default()
	}
	if interval <= 0 {
		interval = time.Minute
	}
	return &Persister{store: store, disk: disk, interval: interval, log: log}
}

// Run saves until ctx is done, then saves a final time.
func (p *Persister) Run(ctx context.Context) {
	if p.disk == nil {
		return
	}
	tick := time.NewTicker(p.interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			p.save()
			return
		case <-tick.C:
			p.save()
		}
	}
}

func (p *Persister) save() {
	if err := p.disk.Save(p.store.Snapshot()); err != nil {
		p.log.Warn("snapshot not saved", "err", err)
	}
}
