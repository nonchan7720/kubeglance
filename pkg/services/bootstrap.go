package services

import (
	"context"
	"log/slog"
	"time"

	"github.com/nonchan7720/kubeglance/pkg/domain"
)

// Bootstrap fills an empty store at start-up so a rolling update loses no
// history. The order is: every peer (a running replica has the freshest
// data), then the on-disk snapshot (the safety net when every replica was
// down). Whatever is found is merged; a failure to reach a source is logged
// and skipped, so a fresh single-replica install just starts empty.
//
// Bootstrap returns the number of sources merged.
func Bootstrap(ctx context.Context, store *domain.Store, peers []PeerSource, disk SnapshotStore, log *slog.Logger) int {
	if log == nil {
		log = slog.Default()
	}
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
