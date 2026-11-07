package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	store.UpsertPods([]domain.PodInfo{{Key: key, Node: "n1", Workload: domain.WorkloadKey{Namespace: "app", Kind: "Deployment", Name: "web"}}})
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
