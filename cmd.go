package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

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
}

// runFunc starts kubeglance with a parsed config; tests replace it.
type runFunc func(ctx context.Context, c config, log *slog.Logger) error

// envAnnotation marks a flag that falls back to an environment variable.
const envAnnotation = "kubeglance/env"

func newRootCommand(run runFunc) *cobra.Command {
	var c config
	cmd := &cobra.Command{
		Use:   "kubeglance",
		Short: "See what is eating your cluster, from your phone",
		Long: `kubeglance polls metrics-server every 15 s, keeps a bounded in-memory
history, and serves a mobile-first UI and a JSON API with cluster / node /
workload / pod usage as now, average, median and p95 over a window.

Every flag can also be set with the environment variable shown in its help;
a flag on the command line wins over the variable.`,
		Version: version.Version,
		Args:    cobra.NoArgs,
		// main logs the error as JSON, like every other line kubeglance writes
		SilenceErrors: true,
		PreRunE: func(cmd *cobra.Command, _ []string) error {
			// the command line parsed; from here on an error is not a
			// usage mistake, so do not bury it under the usage text
			cmd.SilenceUsage = true
			return applyEnv(cmd.Flags())
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			log, err := newLogger(c.logLevel)
			if err != nil {
				return err
			}
			return run(cmd.Context(), c, log)
		},
	}
	cmd.SetVersionTemplate("kubeglance {{.Version}}\n")

	f := cmd.Flags()
	f.SortFlags = false
	f.StringVar(&c.addr, "addr", ":8080", "listen address")
	f.StringVar(&c.kubeconfig, "kubeconfig", "", "kubeconfig path (default: in-cluster, then $KUBECONFIG, then ~/.kube/config)")
	f.DurationVar(&c.interval, "interval", 15*time.Second, "poll interval; metrics-server refreshes every 15s, shorter is pointless")
	f.DurationVar(&c.retention, "retention", 24*time.Hour, "how much history to keep in memory")
	f.StringVar(&c.peerHost, "peer-host", "", "headless Service name of the replicas; empty disables peer bootstrap")
	f.IntVar(&c.peerPort, "peer-port", 8080, "port peers serve on")
	f.StringVar(&c.podIP, "pod-ip", "", "this pod's IP, excluded from peers (downward API)")
	f.StringVar(&c.snapshotPath, "snapshot", "", "optional file to persist history to (gzip JSON); empty disables")
	f.DurationVar(&c.snapshotEvery, "snapshot-every", time.Minute, "how often to write the snapshot")
	f.DurationVar(&c.shutdownGrace, "shutdown-grace", 5*time.Second, "time to keep serving peers after SIGTERM so a new replica can copy the history")
	f.StringVar(&c.logLevel, "log-level", "info", "debug | info | warn | error")

	for flag, key := range map[string]string{
		"addr":           "KUBEGLANCE_ADDR",
		"interval":       "KUBEGLANCE_INTERVAL",
		"retention":      "KUBEGLANCE_RETENTION",
		"peer-host":      "KUBEGLANCE_PEER_HOST",
		"peer-port":      "KUBEGLANCE_PEER_PORT",
		"pod-ip":         "POD_IP",
		"snapshot":       "KUBEGLANCE_SNAPSHOT",
		"snapshot-every": "KUBEGLANCE_SNAPSHOT_EVERY",
		"shutdown-grace": "KUBEGLANCE_SHUTDOWN_GRACE",
		"log-level":      "KUBEGLANCE_LOG_LEVEL",
	} {
		bindEnv(f, flag, key)
	}
	return cmd
}

// bindEnv lets the environment variable key set flag when the flag is not
// given on the command line, and names the variable in the help.
func bindEnv(f *pflag.FlagSet, flag, key string) {
	fl := f.Lookup(flag)
	fl.Usage += " [$" + key + "]"
	// Lookup above succeeded, so SetAnnotation cannot fail
	_ = f.SetAnnotation(flag, envAnnotation, []string{key})
}

// applyEnv copies bound environment variables into the flags the command
// line left unset. An empty variable counts as unset; a malformed one is an
// error, not a silent fall back to the default.
func applyEnv(f *pflag.FlagSet) error {
	var err error
	f.VisitAll(func(fl *pflag.Flag) {
		keys := fl.Annotations[envAnnotation]
		if err != nil || fl.Changed || len(keys) == 0 {
			return
		}
		v := os.Getenv(keys[0])
		if v == "" {
			return
		}
		if e := f.Set(fl.Name, v); e != nil {
			err = fmt.Errorf("%s: %w", keys[0], e)
		}
	})
	return err
}

func newLogger(level string) (*slog.Logger, error) {
	var lv slog.Level
	if err := lv.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("--log-level: %w", err)
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lv})), nil
}
