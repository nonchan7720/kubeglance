package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nonchan7720/kubeglance/pkg/version"
)

// execute runs the root command with args and returns the config the run
// function received (nil when it was not called) and what was printed.
func execute(t *testing.T, args ...string) (*config, string, error) {
	t.Helper()
	var got *config
	cmd := newRootCommand(func(_ context.Context, c config, log *slog.Logger) error {
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
		"KUBEGLANCE_SHUTDOWN_GRACE", "KUBEGLANCE_LOG_LEVEL",
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
	want := config{
		addr:          ":8080",
		interval:      15 * time.Second,
		retention:     24 * time.Hour,
		peerPort:      8080,
		snapshotEvery: time.Minute,
		shutdownGrace: 5 * time.Second,
		logLevel:      "info",
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
		"--shutdown-grace=7s", "--log-level=debug",
	)
	if err != nil {
		t.Fatal(err)
	}
	want := config{
		addr:          "127.0.0.1:9090",
		kubeconfig:    "/tmp/kc",
		interval:      30 * time.Second,
		retention:     2 * time.Hour,
		peerHost:      "peers.ns.svc",
		peerPort:      9091,
		podIP:         "10.0.0.7",
		snapshotPath:  "/data/s.snap",
		snapshotEvery: 5 * time.Minute,
		shutdownGrace: 7 * time.Second,
		logLevel:      "debug",
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
	c, _, err := execute(t)
	if err != nil {
		t.Fatal(err)
	}
	want := config{
		addr:          ":9000",
		interval:      30 * time.Second,
		retention:     6 * time.Hour,
		peerHost:      "peers",
		peerPort:      9001,
		podIP:         "10.1.2.3",
		snapshotPath:  "/s",
		snapshotEvery: 2 * time.Minute,
		shutdownGrace: 9 * time.Second,
		logLevel:      "warn",
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
	if c.interval != 45*time.Second {
		t.Fatalf("interval = %v, want 45s", c.interval)
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
