// Package interfaces holds what kubeglance exposes: the command line here,
// the HTTP API and UI in handler.
package interfaces

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
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
	// AuthTokenFile holds the token required on the UI and API (a mounted
	// Secret); AuthToken is what it contains, read at start. Never logged.
	AuthTokenFile string
	AuthToken     string

	// AlertWebhook receives alerts; empty disables them. It may itself be a
	// credential (a Slack-style URL): only its scheme and host are logged.
	AlertWebhook string
	AlertFormat  string // json | pushshell
	// AlertHeadersFile holds "Name: value" lines sent with every alert (an
	// API key; a mounted Secret); AlertHeaders is its content. Never logged.
	AlertHeadersFile string
	AlertHeaders     string
	// AlertWindow is how far back a rule looks; it fires when at least half
	// of it is at or above the threshold.
	AlertWindow time.Duration
	// AlertMemory and AlertCPU are thresholds as a fraction of a container's
	// limit and a node's allocatable; 0 disables the resource's rules.
	AlertMemory   float64
	AlertCPU      float64
	AlertLanguage string // of the notification text: en | ja
	// PublicURL is where a phone reaches kubeglance; alerts link to it.
	PublicURL string
}

// LogValue makes the effective configuration (flags, environment and
// defaults combined) one "config" group on the startup line, so a log shows
// what a replica actually ran with. Durations are written as Go durations
// ("15s"), not nanoseconds. Config holds no secrets; keep it that way, or
// leave the secret out here.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("addr", c.Addr),
		slog.String("kubeconfig", c.Kubeconfig),
		slog.String("interval", intervalString(c.Interval)),
		slog.String("retention", c.Retention.String()),
		slog.String("peerHost", c.PeerHost),
		slog.Int("peerPort", c.PeerPort),
		slog.String("podIP", c.PodIP),
		slog.String("snapshot", c.SnapshotPath),
		slog.String("snapshotEvery", c.SnapshotEvery.String()),
		slog.String("shutdownGrace", c.ShutdownGrace.String()),
		slog.String("logLevel", c.LogLevel),
		slog.String("bootstrapTimeout", c.BootstrapTimeout.String()),
		slog.String("authTokenFile", c.AuthTokenFile),
		slog.String("authToken", secretState(c.AuthToken)), // never the value
		slog.String("alertWebhook", redactURL(c.AlertWebhook)),
		slog.String("alertFormat", c.AlertFormat),
		slog.String("alertHeadersFile", c.AlertHeadersFile),
		slog.String("alertHeaders", secretState(c.AlertHeaders)), // never the value
		slog.String("alertWindow", c.AlertWindow.String()),
		slog.Float64("alertMemory", c.AlertMemory),
		slog.Float64("alertCPU", c.AlertCPU),
		slog.String("alertLanguage", c.AlertLanguage),
		slog.String("publicURL", c.PublicURL),
	)
}

// secretState says whether a secret is set without saying what it is.
func secretState(s string) string {
	if s == "" {
		return "none"
	}
	return "set"
}

// redactURL keeps the scheme and host of a URL whose path or query may be a
// credential.
func redactURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(invalid)"
	}
	return u.Scheme + "://" + u.Host
}

// intervalString is "auto" for 0 (follow metrics-server), else the duration.
func intervalString(d time.Duration) string {
	if d == 0 {
		return "auto"
	}
	return d.String()
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
			if err := applyEnv(cmd.Flags()); err != nil {
				return err
			}
			if err := readToken(&c); err != nil {
				return err
			}
			return checkAlerts(&c)
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
	f.DurationVar(&c.Interval, "interval", 0, "poll interval; 0 follows metrics-server's --metric-resolution, also when it changes while running (15s until it is known); a value fixes it")
	f.DurationVar(&c.Retention, "retention", 24*time.Hour, "how much history to keep in memory")
	f.StringVar(&c.PeerHost, "peer-host", "", "headless Service name of the replicas; empty disables peer bootstrap")
	f.IntVar(&c.PeerPort, "peer-port", 8080, "port peers serve on")
	f.StringVar(&c.PodIP, "pod-ip", "", "this pod's IP, excluded from peers (downward API)")
	f.StringVar(&c.SnapshotPath, "snapshot", "", "optional file to persist history to (gzip JSON); empty disables")
	f.DurationVar(&c.SnapshotEvery, "snapshot-every", time.Minute, "how often to write the snapshot")
	f.DurationVar(&c.ShutdownGrace, "shutdown-grace", 5*time.Second, "time to keep serving peers after SIGTERM so a new replica can copy the history")
	f.StringVar(&c.LogLevel, "log-level", "info", "debug | info | warn | error")
	f.StringVar(&c.AuthTokenFile, "auth-token-file", "", "file with a token required on the UI, the API and /metrics (Basic auth password or Bearer token); empty disables. A file, not a flag value, so it is not in the process list")
	f.DurationVar(&c.BootstrapTimeout, "bootstrap-timeout", 2*time.Minute, "how long a starting replica retries copying the history from a peer before it starts without it (it is not Ready meanwhile)")
	f.StringVar(&c.AlertWebhook, "alert-webhook", "", "URL that receives alerts (HTTP POST); empty disables alerts")
	f.StringVar(&c.AlertFormat, "alert-format", "json", "json (kubeglance's own, every alert in one request) | pushshell (a pushshell /v1/notifications request per alert)")
	f.StringVar(&c.AlertHeadersFile, "alert-headers-file", "", "file with \"Name: value\" lines sent with every alert, such as an API key")
	f.DurationVar(&c.AlertWindow, "alert-window", 10*time.Minute, "a rule fires when at least half of this window is at or above its threshold")
	f.Float64Var(&c.AlertMemory, "alert-memory", 0.9, "memory threshold as a fraction of a container's limit and a node's allocatable; 0 disables")
	f.Float64Var(&c.AlertCPU, "alert-cpu", 0.9, "CPU threshold as a fraction of a container's limit and a node's allocatable; 0 disables")
	f.StringVar(&c.AlertLanguage, "alert-language", "en", "language of the alert text: en | ja")
	f.StringVar(&c.PublicURL, "public-url", "", "where a phone reaches kubeglance (https://...); alerts link to their pod or node")

	for flag, key := range map[string]string{ //nolint:gosec // G101: environment variable names, not credentials
		"addr":               "KUBEGLANCE_ADDR",
		"interval":           "KUBEGLANCE_INTERVAL",
		"retention":          "KUBEGLANCE_RETENTION",
		"peer-host":          "KUBEGLANCE_PEER_HOST",
		"peer-port":          "KUBEGLANCE_PEER_PORT",
		"pod-ip":             "POD_IP",
		"snapshot":           "KUBEGLANCE_SNAPSHOT",
		"snapshot-every":     "KUBEGLANCE_SNAPSHOT_EVERY",
		"shutdown-grace":     "KUBEGLANCE_SHUTDOWN_GRACE",
		"log-level":          "KUBEGLANCE_LOG_LEVEL",
		"bootstrap-timeout":  "KUBEGLANCE_BOOTSTRAP_TIMEOUT",
		"auth-token-file":    "KUBEGLANCE_AUTH_TOKEN_FILE",
		"alert-webhook":      "KUBEGLANCE_ALERT_WEBHOOK",
		"alert-format":       "KUBEGLANCE_ALERT_FORMAT",
		"alert-headers-file": "KUBEGLANCE_ALERT_HEADERS_FILE",
		"alert-window":       "KUBEGLANCE_ALERT_WINDOW",
		"alert-memory":       "KUBEGLANCE_ALERT_MEMORY",
		"alert-cpu":          "KUBEGLANCE_ALERT_CPU",
		"alert-language":     "KUBEGLANCE_ALERT_LANGUAGE",
		"public-url":         "KUBEGLANCE_PUBLIC_URL",
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

// readToken loads the token from --auth-token-file. A missing or empty file
// is an error: starting unprotected after asking for protection is worse
// than not starting.
func readToken(c *Config) error {
	if c.AuthTokenFile == "" {
		return nil
	}
	b, err := os.ReadFile(c.AuthTokenFile)
	if err != nil {
		return fmt.Errorf("--auth-token-file: %w", err)
	}
	c.AuthToken = strings.TrimSpace(string(b))
	if c.AuthToken == "" {
		return fmt.Errorf("--auth-token-file: %s is empty", c.AuthTokenFile)
	}
	return nil
}

// checkAlerts validates the alert rules and loads --alert-headers-file. The
// receiver itself (URL, format, language, headers) is checked when main
// builds the webhook client, before anything starts.
func checkAlerts(c *Config) error {
	if c.AlertWindow <= 0 {
		return fmt.Errorf("--alert-window: %s is not a window", c.AlertWindow)
	}
	for name, v := range map[string]float64{"--alert-memory": c.AlertMemory, "--alert-cpu": c.AlertCPU} {
		if v < 0 || v > 1 {
			return fmt.Errorf("%s: %v is not a fraction between 0 and 1", name, v)
		}
	}
	if c.AlertHeadersFile == "" {
		return nil
	}
	b, err := os.ReadFile(c.AlertHeadersFile)
	if err != nil {
		return fmt.Errorf("--alert-headers-file: %w", err)
	}
	c.AlertHeaders = string(b)
	return nil
}

func newLogger(level string) (*slog.Logger, error) {
	var lv slog.Level
	if err := lv.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("--log-level: %w", err)
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lv})), nil
}
