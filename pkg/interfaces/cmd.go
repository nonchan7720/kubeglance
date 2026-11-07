// Package interfaces holds what kubeglance exposes: the command line here,
// the HTTP API and UI in handler.
package interfaces

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

// Config is what the command line and environment configure; main wires
// it into the services.
type Config struct {
	Addr          string
	Kubeconfig    string
	Interval      time.Duration
	Retention     time.Duration
	PeerHost      string
	PeerPort      int
	PodIP         string
	SnapshotPath  string
	SnapshotEvery time.Duration
	ShutdownGrace time.Duration
	LogLevel      string
	// BootstrapTimeout is how long a starting replica keeps trying to copy
	// the history from a peer before it starts without it.
	BootstrapTimeout time.Duration
}

// RunFunc starts kubeglance with a parsed Config. It lives in main, which
// is the only place allowed to wire infrastructure into services.
type RunFunc func(ctx context.Context, c Config, log *slog.Logger) error

// envAnnotation marks a flag that falls back to an environment variable.
const envAnnotation = "kubeglance/env"

// NewRootCommand returns the kubeglance command; run is called once the
// flags and environment are parsed and validated.
func NewRootCommand(run RunFunc) *cobra.Command {
	var c Config
	cmd := &cobra.Command{
		Use:   "kubeglance",
		Short: "See what is eating your cluster, from your phone",
		Long: `kubeglance polls metrics-server at its own resolution, keeps a bounded in-memory
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
			log, err := newLogger(c.LogLevel)
			if err != nil {
				return err
			}
			return run(cmd.Context(), c, log)
		},
	}
	cmd.SetVersionTemplate("kubeglance {{.Version}}\n")

	f := cmd.Flags()
	f.SortFlags = false
	f.StringVar(&c.Addr, "addr", ":8080", "listen address")
	f.StringVar(&c.Kubeconfig, "kubeconfig", "", "kubeconfig path (default: in-cluster, then $KUBECONFIG, then ~/.kube/config)")
	f.DurationVar(&c.Interval, "interval", 0, "poll interval; 0 follows metrics-server's --metric-resolution, detected at start (15s when it cannot be)")
	f.DurationVar(&c.Retention, "retention", 24*time.Hour, "how much history to keep in memory")
	f.StringVar(&c.PeerHost, "peer-host", "", "headless Service name of the replicas; empty disables peer bootstrap")
	f.IntVar(&c.PeerPort, "peer-port", 8080, "port peers serve on")
	f.StringVar(&c.PodIP, "pod-ip", "", "this pod's IP, excluded from peers (downward API)")
	f.StringVar(&c.SnapshotPath, "snapshot", "", "optional file to persist history to (gzip JSON); empty disables")
	f.DurationVar(&c.SnapshotEvery, "snapshot-every", time.Minute, "how often to write the snapshot")
	f.DurationVar(&c.ShutdownGrace, "shutdown-grace", 5*time.Second, "time to keep serving peers after SIGTERM so a new replica can copy the history")
	f.StringVar(&c.LogLevel, "log-level", "info", "debug | info | warn | error")
	f.DurationVar(&c.BootstrapTimeout, "bootstrap-timeout", 2*time.Minute, "how long a starting replica retries copying the history from a peer before it starts without it (it is not Ready meanwhile)")

	for flag, key := range map[string]string{
		"addr":              "KUBEGLANCE_ADDR",
		"interval":          "KUBEGLANCE_INTERVAL",
		"retention":         "KUBEGLANCE_RETENTION",
		"peer-host":         "KUBEGLANCE_PEER_HOST",
		"peer-port":         "KUBEGLANCE_PEER_PORT",
		"pod-ip":            "POD_IP",
		"snapshot":          "KUBEGLANCE_SNAPSHOT",
		"snapshot-every":    "KUBEGLANCE_SNAPSHOT_EVERY",
		"shutdown-grace":    "KUBEGLANCE_SHUTDOWN_GRACE",
		"log-level":         "KUBEGLANCE_LOG_LEVEL",
		"bootstrap-timeout": "KUBEGLANCE_BOOTSTRAP_TIMEOUT",
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
