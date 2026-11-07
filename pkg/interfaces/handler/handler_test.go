package handler

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/nonchan7720/kubeglance/pkg/domain"
	"github.com/nonchan7720/kubeglance/pkg/services"
)

type staticSource struct{ round domain.Round }

func (s staticSource) Poll(context.Context) (domain.Round, error) { return s.round, nil }

func newServer(t *testing.T) (*httptest.Server, *services.Collector) {
	t.Helper()
	store := domain.NewStore(15*time.Second, time.Hour)
	key := domain.PodKey{Namespace: "app", Name: "web-1"}
	store.UpsertNodes([]domain.NodeInfo{{Name: "n1", Ready: true, Allocatable: domain.Resource{CPU: 4000, Memory: 1 << 30}}})
	store.UpsertPods([]domain.PodInfo{{Key: key, Node: "n1", Workload: domain.WorkloadKey{Namespace: "app", Kind: "Deployment", Name: "web"},
		Containers: []domain.ContainerInfo{{Name: "web"}}}})
	col := services.NewCollector(store, staticSource{domain.Round{
		At:    time.Now(),
		Pods:  map[domain.PodKey]domain.Resource{key: {CPU: 100, Memory: 1 << 20}},
		Nodes: map[string]domain.Resource{"n1": {CPU: 1000, Memory: 1 << 28}},
	}}, nil)
	h := New(Deps{Store: store, Query: services.NewQuery(store), Collector: col, Version: "test"})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, col
}

func get(t *testing.T, url string, into any) *http.Response {
	t.Helper()
	resp, err := http.Get(url) //nolint:gosec // test server URL
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck // test
	if into != nil && resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
			t.Fatalf("%s: decode: %v", url, err)
		}
	}
	return resp
}

func TestReadyOnlyAfterFirstRound(t *testing.T) {
	srv, col := newServer(t)
	if resp := get(t, srv.URL+"/readyz", nil); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("readyz before data = %d", resp.StatusCode)
	}
	if resp := get(t, srv.URL+"/healthz", nil); resp.StatusCode != http.StatusOK {
		t.Errorf("healthz = %d", resp.StatusCode)
	}
	col.PollOnce(context.Background())
	if resp := get(t, srv.URL+"/readyz", nil); resp.StatusCode != http.StatusOK {
		t.Errorf("readyz after data = %d", resp.StatusCode)
	}
}

func TestAPIEndpoints(t *testing.T) {
	srv, col := newServer(t)
	col.PollOnce(context.Background())

	var cluster services.ClusterView
	get(t, srv.URL+"/api/v1/cluster?window=5m", &cluster)
	if cluster.Nodes != 1 || cluster.Pods != 1 || cluster.Usage.Now.CPU != 1000 || cluster.Window.Duration != 5*time.Minute {
		t.Errorf("cluster = %+v", cluster)
	}

	var nodes services.NodesView
	get(t, srv.URL+"/api/v1/nodes", &nodes)
	if len(nodes.Nodes) != 1 || nodes.Nodes[0].CPURatio != 0.25 || nodes.Window.Duration != 30*time.Minute {
		t.Errorf("nodes = %+v", nodes)
	}

	var wl services.WorkloadsView
	get(t, srv.URL+"/api/v1/workloads?namespace=app", &wl)
	if len(wl.Workloads) != 1 || wl.Workloads[0].Name != "web" {
		t.Errorf("workloads = %+v", wl)
	}

	var pods services.PodsView
	get(t, srv.URL+"/api/v1/pods?workload=app/Deployment/web", &pods)
	if len(pods.Pods) != 1 || pods.Pods[0].Usage.Now.CPU != 100 {
		t.Errorf("pods = %+v", pods)
	}
	if resp := get(t, srv.URL+"/api/v1/pods?workload=bad", nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad workload = %d", resp.StatusCode)
	}

	var pod services.PodDetailView
	get(t, srv.URL+"/api/v1/pods/app/web-1?window=5m", &pod)
	if pod.Pod.Key.Name != "web-1" || pod.Pod.Usage.Now.CPU != 100 || pod.Window.Duration != 5*time.Minute || len(pod.Containers) != 1 {
		t.Errorf("pod detail = %+v", pod)
	}
	if resp := get(t, srv.URL+"/api/v1/pods/app/nope", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown pod = %d", resp.StatusCode)
	}

	var st Status
	resp := get(t, srv.URL+"/api/v1/status", &st)
	if st.Version != "test" || !st.Collector.Healthy || resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("status = %+v, headers %v", st, resp.Header)
	}

	var snap domain.Snapshot
	get(t, srv.URL+"/internal/snapshot", &snap)
	if len(snap.Pods) != 1 || len(snap.Nodes) != 1 {
		t.Errorf("snapshot = %+v", snap)
	}
}

func TestAPIIsGzippedWhenAccepted(t *testing.T) {
	srv, col := newServer(t)
	col.PollOnce(context.Background())
	for _, path := range []string{"/api/v1/pods", "/internal/snapshot"} {
		req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		// set by hand, so the client does not decompress transparently
		req.Header.Set("Accept-Encoding", "gzip")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close() //nolint:errcheck // test
		if resp.Header.Get("Content-Encoding") != "gzip" || !strings.Contains(resp.Header.Get("Vary"), "Accept-Encoding") {
			t.Fatalf("%s: headers %v", path, resp.Header)
		}
		zr, err := gzip.NewReader(resp.Body)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		var v map[string]any
		if err := json.NewDecoder(zr).Decode(&v); err != nil {
			t.Fatalf("%s: decode: %v", path, err)
		}
	}

	var pods services.PodsView
	resp := get(t, srv.URL+"/api/v1/pods", &pods) // the Go client asks for gzip itself
	if !resp.Uncompressed || len(pods.Pods) != 1 || len(pods.Pods[0].Spark.CPU) == 0 {
		t.Errorf("transparent gzip: uncompressed=%v pods=%+v", resp.Uncompressed, pods.Pods)
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/status", nil)
	req.Header.Set("Accept-Encoding", "identity")
	plain, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Body.Close() //nolint:errcheck // test
	if plain.Header.Get("Content-Encoding") != "" {
		t.Errorf("gzip without Accept-Encoding: %v", plain.Header)
	}
}

func TestUIIsServed(t *testing.T) {
	srv, _ := newServer(t)
	resp, err := http.Get(srv.URL + "/") //nolint:gosec // test server URL
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck // test
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Errorf("index: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	for _, p := range []string{"/app.js", "/style.css", "/manifest.webmanifest", "/icon.svg"} {
		if r := get(t, srv.URL+p, nil); r.StatusCode != http.StatusOK {
			t.Errorf("%s = %d", p, r.StatusCode)
		}
	}
}

func TestAcceptsGzip(t *testing.T) {
	for header, want := range map[string]bool{
		"":                      false,
		"gzip":                  true,
		"gzip, deflate, br":     true,
		"br;q=1.0, GZIP;q=0.5":  true,
		"gzip;q=0":              false,
		"gzip; q=0.000, br":     false,
		"identity":              false,
		"x-gzip":                false,
		"deflate, gzip;q=0.001": true,
	} {
		if got := acceptsGzip(header); got != want {
			t.Errorf("acceptsGzip(%q) = %v, want %v", header, got, want)
		}
	}
}

func TestMetricsEndpoint(t *testing.T) {
	srv, col := newServer(t)
	col.PollOnce(context.Background())
	resp, err := http.Get(srv.URL + "/metrics") //nolint:gosec // test server URL
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck // test
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/plain; version=0.0.4; charset=utf-8" {
		t.Fatalf("%d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	body, _ := io.ReadAll(resp.Body)

	// every sample line belongs to a metric announced by # HELP and # TYPE,
	// and has a numeric value (the text exposition format)
	sample := regexp.MustCompile(`^([a-z_]+)(\{[a-z_]+="[^"]*"(,[a-z_]+="[^"]*")*\})? (-?[0-9.e+-]+|NaN|\+Inf)$`)
	typed := map[string]string{}
	values := map[string]string{}
	for line := range strings.SplitSeq(strings.TrimRight(string(body), "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "# HELP "):
		case strings.HasPrefix(line, "# TYPE "):
			f := strings.Fields(line)
			typed[f[2]] = f[3]
		default:
			m := sample.FindStringSubmatch(line)
			if m == nil {
				t.Errorf("not a sample line: %q", line)
				continue
			}
			if _, ok := typed[m[1]]; !ok {
				t.Errorf("%s has no # TYPE before it", m[1])
			}
			values[m[1]+m[2]] = m[4]
		}
	}
	for k, want := range map[string]string{
		`kubeglance_build_info{version="test"}`: "1",
		"kubeglance_poll_rounds_total":           "1",
		"kubeglance_poll_errors_total":           "0",
		"kubeglance_poll_interval_seconds":       "15",
		"kubeglance_store_pods":                  "1",
		"kubeglance_store_nodes":                 "1",
		"kubeglance_store_series":                "2",
		"kubeglance_store_samples":               "2",
		"kubeglance_store_series_capacity":       "241",
		"kubeglance_ready":                       "1",
	} {
		if values[k] != want {
			t.Errorf("%s = %q, want %s", k, values[k], want)
		}
	}
	for _, k := range []string{"kubeglance_poll_last_duration_seconds", "kubeglance_poll_last_success_timestamp_seconds"} {
		if _, ok := values[k]; !ok {
			t.Errorf("%s missing", k)
		}
	}
	if typed["kubeglance_poll_rounds_total"] != "counter" || typed["kubeglance_store_samples"] != "gauge" {
		t.Errorf("types = %v", typed)
	}
}
