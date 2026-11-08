// Package services holds the use cases of kubeglance: collecting usage into
// the store, answering the four-level queries, bootstrapping a replica from a
// peer and persisting snapshots. It depends on the domain only; the concrete
// Kubernetes, HTTP and disk adapters live in pkg/infrastructure and are
// injected through the interfaces in this file.
package services

import (
	"context"
	"io"

	"github.com/nonchan7720/kubeglance/pkg/domain"
)

// MetricsSource returns the current usage of every pod and node.
type MetricsSource interface {
	Poll(ctx context.Context) (domain.Round, error)
}

// PeerSource fetches the history of a running replica: Fetch hands read the
// snapshot as it arrives (domain.Store.ReadSnapshot), so it is merged while
// it streams in, never held whole.
type PeerSource interface {
	Fetch(ctx context.Context, read func(io.Reader) error) error
	String() string
}

// SnapshotStore saves and loads a snapshot (the optional on-disk safety
// net), streamed in both directions like PeerSource: Save hands write the
// destination (domain.Store.WriteSnapshot), Load hands read the source and
// reports ok=false when there is none.
type SnapshotStore interface {
	Save(write func(io.Writer) error) error
	Load(read func(io.Reader) error) (ok bool, err error)
}

// Notifier delivers alerts to a receiver (a webhook). It returns how many of
// the alerts, from the first, were delivered: a receiver that takes one
// request per alert can fail half-way, and those already sent must not be
// sent again.
type Notifier interface {
	Notify(ctx context.Context, alerts []domain.Alert) (int, error)
}

// SenderElection reports whether this replica is the one that sends
// notifications (exactly one Ready replica is) and how many other Ready
// replicas there are.
type SenderElection func(ctx context.Context) (sender bool, peers int, err error)
