// kubeglance: see what is eating your cluster, from your phone.
//
// One static binary. It polls metrics-server every 15 s, keeps a bounded
// in-memory history, and serves a mobile-first UI and a JSON API with
// cluster / node / workload / pod usage as now, average, median and p95
// over a window. With two replicas, a starting replica copies the history
// from the running one, so rolling updates lose nothing.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nonchan7720/kubeglance/pkg/domain"
	"github.com/nonchan7720/kubeglance/pkg/infrastructure/k8s"
	"github.com/nonchan7720/kubeglance/pkg/infrastructure/peer"
	"github.com/nonchan7720/kubeglance/pkg/infrastructure/snapshot"
	"github.com/nonchan7720/kubeglance/pkg/interfaces"
	"github.com/nonchan7720/kubeglance/pkg/interfaces/handler"
	"github.com/nonchan7720/kubeglance/pkg/services"
	"github.com/nonchan7720/kubeglance/pkg/version"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	err := interfaces.NewRootCommand(run).ExecuteContext(ctx)
	stop()
	if err != nil {
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).Error("kubeglance exited", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, c interfaces.Config, log *slog.Logger) error {
	log.Info("starting", "version", version.Version, "config", c)

	store := domain.NewStore(c.Interval, c.Retention)
	log.Info("history sized", "samplesPerEntity", store.Capacity())

	clients, err := k8s.NewClients(c.Kubeconfig)
	if err != nil {
		return err
	}
	watcher, err := k8s.NewWatcher(clients.Core, store, log)
	if err != nil {
		return err
	}
	collector := services.NewCollector(store, k8s.NewMetricsSource(clients.Metrics), log)
	query := services.NewQuery(store)

	// 1. serve at once: /healthz answers, so the liveness probe does not
	// restart a replica that is still waiting for a peer; /readyz stays
	// false until the history is copied and the first round is recorded, and
	// only Ready replicas are in the headless Service, so nobody copies from
	// this one before then
	srv := &http.Server{
		Addr:              c.Addr,
		Handler:           handler.New(handler.Deps{Store: store, Query: query, Collector: collector, Version: version.Version, Log: log}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", c.Addr)
		errc <- srv.ListenAndServe()
	}()

	// 2. history from every peer (retried until --bootstrap-timeout while
	// peers exist but none answers), then disk
	var disk services.SnapshotStore
	if c.SnapshotPath != "" {
		disk = snapshot.NewFile(c.SnapshotPath)
	}
	findPeers := func(ctx context.Context) ([]services.PeerSource, error) {
		peers, err := peer.Discover(ctx, net.DefaultResolver, c.PeerHost, c.PeerPort, c.PodIP)
		sources := make([]services.PeerSource, 0, len(peers))
		for _, p := range peers {
			sources = append(sources, p)
		}
		return sources, err
	}
	services.Bootstrap(ctx, store, findPeers, disk, services.BootstrapPolicy{Timeout: c.BootstrapTimeout, Retry: 2 * time.Second}, log)

	// 3. metadata (informers), then usage (poll loop)
	if ctx.Err() == nil {
		if err := watcher.Run(ctx); err != nil {
			return err
		}
		go collector.Run(ctx)
		go services.NewPersister(store, disk, c.SnapshotEvery, log).Run(ctx)
	}

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	// On SIGTERM, keep serving for the grace period: the replacing replica
	// may be copying the history right now, and the preStop hook plus this
	// delay is what makes the hand-over lossless.
	log.Info("shutting down", "grace", c.ShutdownGrace)
	time.Sleep(c.ShutdownGrace)
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
