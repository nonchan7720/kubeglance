// kubeglance: see what is eating your cluster, from your phone.
//
// One static binary. It polls metrics-server at its own resolution, keeps a bounded
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
	"runtime/debug"
	"syscall"
	"time"

	"github.com/nonchan7720/kubeglance/pkg/domain"
	"github.com/nonchan7720/kubeglance/pkg/infrastructure/k8s"
	"github.com/nonchan7720/kubeglance/pkg/infrastructure/peer"
	"github.com/nonchan7720/kubeglance/pkg/infrastructure/snapshot"
	"github.com/nonchan7720/kubeglance/pkg/infrastructure/webhook"
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
	if limit, ok := interfaces.GoMemoryLimit(os.Getenv); ok {
		debug.SetMemoryLimit(limit)
		log.Info("Go memory limit set from the container's", "bytes", limit)
	}

	// the receiver is checked before anything starts: a typo in the URL or
	// the headers stops the start-up instead of failing every alert later
	var notifier services.Notifier
	if c.AlertWebhook != "" {
		wh, err := webhook.New(webhook.Settings{
			URL: c.AlertWebhook, Format: c.AlertFormat, Headers: c.AlertHeaders, PublicURL: c.PublicURL,
			Language: c.AlertLanguage, Window: c.AlertWindow, UserAgent: "kubeglance/" + version.Version,
		})
		if err != nil {
			return err
		}
		notifier = wh
	}

	clients, err := k8s.NewClients(c.Kubeconfig)
	if err != nil {
		return err
	}
	metrics := k8s.NewMetricsSource(clients.Metrics)

	// poll exactly at metrics-server's resolution unless --interval says
	// otherwise: faster reads every value twice, slower misses values. The
	// start-up guess only has to be close: in auto mode the collector follows
	// the resolution every round reports, also when it changes later
	interval := services.ResolveInterval(ctx, c.Interval, metrics.Resolution,
		services.IntervalPolicy{Timeout: 20 * time.Second, Retry: 5 * time.Second, Fallback: 15 * time.Second}, log)
	store := domain.NewStore(interval, c.Retention)
	log.Info("history sized", "interval", interval, "samplesPerEntity", store.Capacity())

	watcher, err := k8s.NewWatcher(clients.Core, store, log)
	if err != nil {
		return err
	}
	collector := services.NewCollector(store, metrics, log)
	collector.FollowResolution(c.Interval == 0)
	query := services.NewQuery(store)

	// 1. serve at once: /healthz answers, so the liveness probe does not
	// restart a replica that is still waiting for a peer; /readyz stays
	// false until the history is copied and the first round is recorded, and
	// only Ready replicas are in the headless Service, so nobody copies from
	// this one before then
	srv := &http.Server{
		Addr:              c.Addr,
		Handler:           handler.New(handler.Deps{Store: store, Query: query, Collector: collector, Version: version.Version, Log: log, AuthToken: c.AuthToken}),
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
		peers, err := peer.Discover(ctx, net.DefaultResolver, c.PeerHost, c.PeerPort, c.PodIP, c.AuthToken)
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
		if notifier != nil {
			// every replica evaluates; the one with the lowest address among
			// the Ready replicas sends (peer.Sender)
			elect := func(ctx context.Context) (bool, int, error) {
				return peer.Sender(ctx, net.DefaultResolver, c.PeerHost, c.PodIP)
			}
			policy := services.AlertPolicy{Window: c.AlertWindow, Memory: c.AlertMemory, CPU: c.AlertCPU}
			go services.NewAlerter(store, notifier, elect, policy, log).Run(ctx, collector.Rounds())
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
