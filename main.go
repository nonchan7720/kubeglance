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
	"flag"
	"fmt"
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
	"github.com/nonchan7720/kubeglance/pkg/interfaces/handler"
	"github.com/nonchan7720/kubeglance/pkg/services"
	"github.com/nonchan7720/kubeglance/pkg/version"
)

type config struct {
	addr          string
	kubeconfig    string
	interval      time.Duration
	retention     time.Duration
	peerHost      string
	peerPort      int
	podIP         string
	snapshotPath  string
	snapshotEvery time.Duration
	shutdownGrace time.Duration
	logLevel      string
	showVersion   bool
}

func parseFlags() config {
	var c config
	flag.StringVar(&c.addr, "addr", env("KUBEGLANCE_ADDR", ":8080"), "listen address")
	flag.StringVar(&c.kubeconfig, "kubeconfig", "", "kubeconfig path (default: in-cluster, then $KUBECONFIG, then ~/.kube/config)")
	flag.DurationVar(&c.interval, "interval", envDuration("KUBEGLANCE_INTERVAL", 15*time.Second), "poll interval; metrics-server refreshes every 15s, shorter is pointless")
	flag.DurationVar(&c.retention, "retention", envDuration("KUBEGLANCE_RETENTION", 24*time.Hour), "how much history to keep in memory")
	flag.StringVar(&c.peerHost, "peer-host", env("KUBEGLANCE_PEER_HOST", ""), "headless Service name of the replicas; empty disables peer bootstrap")
	flag.IntVar(&c.peerPort, "peer-port", envInt("KUBEGLANCE_PEER_PORT", 8080), "port peers serve on")
	flag.StringVar(&c.podIP, "pod-ip", env("POD_IP", ""), "this pod's IP, excluded from peers (downward API)")
	flag.StringVar(&c.snapshotPath, "snapshot", env("KUBEGLANCE_SNAPSHOT", ""), "optional file to persist history to (gzip JSON); empty disables")
	flag.DurationVar(&c.snapshotEvery, "snapshot-every", envDuration("KUBEGLANCE_SNAPSHOT_EVERY", time.Minute), "how often to write the snapshot")
	flag.DurationVar(&c.shutdownGrace, "shutdown-grace", envDuration("KUBEGLANCE_SHUTDOWN_GRACE", 5*time.Second), "time to keep serving peers after SIGTERM so a new replica can copy the history")
	flag.StringVar(&c.logLevel, "log-level", env("KUBEGLANCE_LOG_LEVEL", "info"), "debug | info | warn | error")
	flag.BoolVar(&c.showVersion, "version", false, "print the version and exit")
	flag.Parse()
	return c
}

func main() {
	c := parseFlags()
	if c.showVersion {
		fmt.Println("kubeglance", version.Version)
		return
	}
	log := newLogger(c.logLevel)
	if err := run(c, log); err != nil {
		log.Error("kubeglance exited", "err", err)
		os.Exit(1)
	}
}

func run(c config, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Info("starting", "version", version.Version, "interval", c.interval, "retention", c.retention)

	store := domain.NewStore(c.interval, c.retention)
	log.Info("history sized", "samplesPerEntity", store.Capacity())

	clients, err := k8s.NewClients(c.kubeconfig)
	if err != nil {
		return err
	}
	watcher, err := k8s.NewWatcher(clients.Core, store, log)
	if err != nil {
		return err
	}
	collector := services.NewCollector(store, k8s.NewMetricsSource(clients.Metrics), log)
	query := services.NewQuery(store)

	// 1. bootstrap history from peers, then disk
	peers, err := peer.Discover(ctx, net.DefaultResolver, c.peerHost, c.peerPort, c.podIP)
	if err != nil {
		log.Warn("peer discovery failed; starting without peer history", "err", err)
	}
	sources := make([]services.PeerSource, 0, len(peers))
	for _, p := range peers {
		sources = append(sources, p)
	}
	var disk services.SnapshotStore
	if c.snapshotPath != "" {
		disk = snapshot.NewFile(c.snapshotPath)
	}
	services.Bootstrap(ctx, store, sources, disk, log)

	// 2. metadata (informers), then usage (poll loop)
	if err := watcher.Run(ctx); err != nil {
		return err
	}
	go collector.Run(ctx)
	go services.NewPersister(store, disk, c.snapshotEvery, log).Run(ctx)

	// 3. serve
	srv := &http.Server{
		Addr:              c.addr,
		Handler:           handler.New(handler.Deps{Store: store, Query: query, Collector: collector, Version: version.Version, Log: log}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", c.addr)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	// On SIGTERM, keep serving for the grace period: the replacing replica
	// may be copying the history right now, and the preStop hook plus this
	// delay is what makes the hand-over lossless.
	log.Info("shutting down", "grace", c.shutdownGrace)
	time.Sleep(c.shutdownGrace)
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func newLogger(level string) *slog.Logger {
	var lv slog.Level
	if err := lv.UnmarshalText([]byte(level)); err != nil {
		lv = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lv}))
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return def
}
