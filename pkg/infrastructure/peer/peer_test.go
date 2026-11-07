package peer

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/nonchan7720/kubeglance/pkg/domain"
)

func TestClientFetch(t *testing.T) {
	store := domain.NewStore(15*time.Second, time.Minute)
	key := domain.PodKey{Namespace: "a", Name: "b"}
	store.Record(domain.Round{At: time.Now(), Pods: map[domain.PodKey]domain.Resource{key: {CPU: 5}}})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != SnapshotPath {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(store.Snapshot())
	}))
	defer srv.Close()
	host, portStr, _ := net.SplitHostPort(srv.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)

	snap, err := NewClient(host, port).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Pods) != 1 || snap.Pods[0].Samples[0].Usage.CPU != 5 {
		t.Errorf("snapshot = %+v", snap)
	}
}

func TestClientFetchNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }))
	defer srv.Close()
	host, portStr, _ := net.SplitHostPort(srv.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)
	if _, err := NewClient(host, port).Fetch(context.Background()); err == nil {
		t.Error("expected error on 503")
	}
}

type fakeResolver struct {
	addrs []net.IPAddr
	err   error
}

func (f fakeResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return f.addrs, f.err
}

func TestDiscoverExcludesSelf(t *testing.T) {
	r := fakeResolver{addrs: []net.IPAddr{{IP: net.ParseIP("10.0.0.1")}, {IP: net.ParseIP("10.0.0.2")}}}
	peers, err := Discover(context.Background(), r, "kubeglance-headless", 8080, "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 1 || peers[0].String() != "http://10.0.0.2:8080"+SnapshotPath {
		t.Errorf("peers = %v", peers)
	}
}

func TestDiscoverNoHostNoPeers(t *testing.T) {
	peers, err := Discover(context.Background(), fakeResolver{}, "", 8080, "")
	if err != nil || peers != nil {
		t.Errorf("peers=%v err=%v", peers, err)
	}
}

func TestDiscoverNotFoundIsEmpty(t *testing.T) {
	r := fakeResolver{err: &net.DNSError{IsNotFound: true}}
	peers, err := Discover(context.Background(), r, "x", 8080, "")
	if err != nil || len(peers) != 0 {
		t.Errorf("peers=%v err=%v", peers, err)
	}
}
