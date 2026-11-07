// Package services holds the use cases of kubeglance: collecting usage into
// the store, answering the four-level queries, bootstrapping a replica from a
// peer and persisting snapshots. It depends on the domain only; the concrete
// Kubernetes, HTTP and disk adapters live in pkg/infrastructure and are
// injected through the interfaces in this file.
package services

import (
	"context"

	"github.com/nonchan7720/kubeglance/pkg/domain"
)

// MetricsSource returns the current usage of every pod and node.
type MetricsSource interface {
	Poll(ctx context.Context) (domain.Round, error)
}

// PeerSource fetches the history of a running replica.
type PeerSource interface {
	Fetch(ctx context.Context) (domain.Snapshot, error)
	String() string
}

// SnapshotStore saves and loads a snapshot (the optional on-disk safety net).
type SnapshotStore interface {
	Save(snap domain.Snapshot) error
	Load() (snap domain.Snapshot, ok bool, err error)
}
