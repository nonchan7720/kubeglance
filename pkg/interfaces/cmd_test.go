package interfaces

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nonchan7720/kubeglance/pkg/version"
)

// execute runs the root command with args and returns the config the run
// function received (nil when it was not called) and what was printed.
func execute(t *testing.T, args ...string) (*Config, string, error) {
	t.Helper()
	var got *Config
	cmd := NewRootCommand(func(_ context.Context, c Config, log *slog.Logger) error {
		if log == nil {
			t.Fatal("run called without a logger")
		}
		got = &c
		return nil
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return got, out.String(), err
}

// clearEnv unsets every variable the command reads, so the developer's
// environment cannot leak into the test.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"KUBEGLANCE_ADDR", "KUBEGLANCE_INTERVAL", "KUBEGLANCE_RETENTION",
		"KUBEGLANCE_PEER_HOST", "KUBEGLANCE_PEER_PORT", "POD_IP",
		"KUBEGLANCE_SNAPSHOT", "KUBEGLANCE_SNAPSHOT_EVERY",
		"KUBEGLANCE_SHUTDOWN_GRACE", "KUBEGLANCE_LOG_LEVEL", "KUBEGLANCE_BOOTSTRAP_TIMEOUT",
	} {
		t.Setenv(k, "")
	}
}

func TestRootDefaults(t *testing.T) {
	clearEnv(t)
	c, _, err := execute(t)
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		Addr:             ":8080",
		Interval:         0, // auto: follow metrics-server's resolution
		Retention:        24 * time.Hour,
		PeerPort:         8080,
		SnapshotEvery:    time.Minute,
		ShutdownGrace:    5 * time.Second,
		LogLevel:         "info",
		BootstrapTimeout: 2 * time.Minute,
	}
	if c == nil || *c != want {
		t.Fatalf("config = %+v, want %+v", c, want)
	}
}

func TestRootFlags(t *testing.T) {
	clearEnv(t)
	c, _, err := execute(t,
		"--addr=127.0.0.1:9090", "--kubeconfig=/tmp/kc", "--interval=30s",
		"--retention=2h", "--peer-host=peers.ns.svc", "--peer-port=9091",
		"--pod-ip=10.0.0.7", "--snapshot=/data/s.snap", "--snapshot-every=5m",
		"--shutdown-grace=7s", "--log-level=debug", "--bootstrap-timeout=30s",
	)
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		Addr:             "127.0.0.1:9090",
		Kubeconfig:       "/tmp/kc",
		Interval:         30 * time.Second,
		Retention:        2 * time.Hour,
		PeerHost:         "peers.ns.svc",
		PeerPort:         9091,
		PodIP:            "10.0.0.7",
		SnapshotPath:     "/data/s.snap",
		SnapshotEvery:    5 * time.Minute,
		ShutdownGrace:    7 * time.Second,
		LogLevel:         "debug",
		BootstrapTimeout: 30 * time.Second,
	}
	if c == nil || *c != want {
		t.Fatalf("config = %+v, want %+v", c, want)
	}
}

func TestRootEnv(t *testing.T) {
	clearEnv(t)
	t.Setenv("KUBEGLANCE_ADDR", ":9000")
	t.Setenv("KUBEGLANCE_INTERVAL", "30s")
	t.Setenv("KUBEGLANCE_RETENTION", "6h")
	t.Setenv("KUBEGLANCE_PEER_HOST", "peers")
	t.Setenv("KUBEGLANCE_PEER_PORT", "9001")
	t.Setenv("POD_IP", "10.1.2.3")
	t.Setenv("KUBEGLANCE_SNAPSHOT", "/s")
	t.Setenv("KUBEGLANCE_SNAPSHOT_EVERY", "2m")
	t.Setenv("KUBEGLANCE_SHUTDOWN_GRACE", "9s")
	t.Setenv("KUBEGLANCE_LOG_LEVEL", "warn")
	t.Setenv("KUBEGLANCE_BOOTSTRAP_TIMEOUT", "45s")
	c, _, err := execute(t)
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		Addr:             ":9000",
		Interval:         30 * time.Second,
		Retention:        6 * time.Hour,
		PeerHost:         "peers",
		PeerPort:         9001,
		PodIP:            "10.1.2.3",
		SnapshotPath:     "/s",
		SnapshotEvery:    2 * time.Minute,
		ShutdownGrace:    9 * time.Second,
		LogLevel:         "warn",
		BootstrapTimeout: 45 * time.Second,
	}
	if c == nil || *c != want {
		t.Fatalf("config = %+v, want %+v", c, want)
	}
}

func TestRootFlagBeatsEnv(t *testing.T) {
	clearEnv(t)
	t.Setenv("KUBEGLANCE_INTERVAL", "30s")
	c, _, err := execute(t, "--interval=45s")
	if err != nil {
		t.Fatal(err)
	}
	if c.Interval != 45*time.Second {
		t.Fatalf("interval = %v, want 45s", c.Interval)
	}
}

// A malformed variable used to be ignored silently, leaving the default in
// place; now it stops the start-up and names the variable.
func TestRootInvalidEnv(t *testing.T) {
	for key, val := range map[string]string{
		"KUBEGLANCE_INTERVAL":  "fast",
		"KUBEGLANCE_PEER_PORT": "http",
	} {
		t.Run(key, func(t *testing.T) {
			clearEnv(t)
			t.Setenv(key, val)
			c, _, err := execute(t)
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("err = %v, want one naming %s", err, key)
			}
			if c != nil {
				t.Fatal("run was called")
			}
		})
	}
}

func TestRootInvalidLogLevel(t *testing.T) {
	clearEnv(t)
	c, _, err := execute(t, "--log-level=loud")
	if err == nil || !strings.Contains(err.Error(), "log-level") {
		t.Fatalf("err = %v, want a log-level error", err)
	}
	if c != nil {
		t.Fatal("run was called")
	}
}

func TestRootRejectsArgs(t *testing.T) {
	clearEnv(t)
	c, _, err := execute(t, "serve")
	if err == nil {
		t.Fatal("positional argument accepted")
	}
	if c != nil {
		t.Fatal("run was called")
	}
}

func TestRootVersion(t *testing.T) {
	clearEnv(t)
	c, out, err := execute(t, "--version")
	if err != nil {
		t.Fatal(err)
	}
	if c != nil {
		t.Fatal("run was called")
	}
	if want := "kubeglance " + version.Version + "\n"; out != want {
		t.Fatalf("output = %q, want %q", out, want)
	}
}

func TestRootHelpNamesEnv(t *testing.T) {
	clearEnv(t)
	_, out, err := execute(t, "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"--interval", "KUBEGLANCE_INTERVAL", "POD_IP"} {
		if !strings.Contains(out, s) {
			t.Errorf("help does not mention %s:\n%s", s, out)
		}
	}
}

func TestConfigLogsEveryEffectiveSetting(t *testing.T) {
	clearEnv(t)
	t.Setenv("KUBEGLANCE_RETENTION", "6h")
	c, _, err := execute(t, "--peer-host", "kg-peers.ns.svc", "--snapshot", "/data/s")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("starting", "config", *c)
	var line struct {
		Config map[string]any `json:"config"`
	}
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("%v: %s", err, buf.String())
	}
	want := map[string]any{
		"addr": ":8080", "kubeconfig": "", "interval": "auto", "retention": "6h0m0s",
		"peerHost": "kg-peers.ns.svc", "peerPort": float64(8080), "podIP": "",
		"snapshot": "/data/s", "snapshotEvery": "1m0s", "shutdownGrace": "5s", "logLevel": "info", "bootstrapTimeout": "2m0s",
	}
	if len(line.Config) != len(want) {
		t.Errorf("config has %d fields, want %d: %v", len(line.Config), len(want), line.Config)
	}
	for k, v := range want {
		if line.Config[k] != v {
			t.Errorf("config.%s = %v (%T), want %v", k, line.Config[k], line.Config[k], v)
		}
	}
}

// TestConfigLogValueCoversEveryField fails when a field is added to Config
// without adding it to the startup log line.
func TestConfigLogValueCoversEveryField(t *testing.T) {
	if got, want := len(Config{}.LogValue().Group()), reflect.TypeFor[Config]().NumField(); got != want {
		t.Errorf("LogValue has %d attributes, Config has %d fields", got, want)
	}
}

func TestConfigLogsAFixedInterval(t *testing.T) {
	if got := intervalString(30 * time.Second); got != "30s" {
		t.Errorf("intervalString(30s) = %q", got)
	}
}
