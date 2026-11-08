package peer

import (
	"context"
	"errors"
	"io"
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
		_ = store.WriteSnapshot(w)
	}))
	defer srv.Close()
	host, portStr, _ := net.SplitHostPort(srv.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)

	dst := domain.NewStore(15*time.Second, time.Minute)
	if err := NewClient(host, port, "").Fetch(context.Background(), readInto(dst)); err != nil {
		t.Fatal(err)
	}
	if got := dst.PodSamples(key, time.Time{}); len(got) != 1 || got[0].Usage.CPU != 5 {
		t.Errorf("copied samples = %+v", got)
	}
}

func TestClientFetchNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }))
	defer srv.Close()
	host, portStr, _ := net.SplitHostPort(srv.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)
	if err := NewClient(host, port, "").Fetch(context.Background(), readInto(domain.NewStore(15*time.Second, time.Minute))); err == nil {
		t.Error("expected error on 503")
	}
}

func TestClientFetchReportsABrokenBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"pods":[{`)) }))
	defer srv.Close()
	host, portStr, _ := net.SplitHostPort(srv.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)
	if err := NewClient(host, port, "").Fetch(context.Background(), readInto(domain.NewStore(15*time.Second, time.Minute))); err == nil {
		t.Error("expected an error on a truncated snapshot")
	}
}

func readInto(s *domain.Store) func(io.Reader) error {
	return func(r io.Reader) error {
		_, err := s.ReadSnapshot(r)
		return err
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
	peers, err := Discover(context.Background(), r, "kubeglance-headless", 8080, "10.0.0.1", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 1 || peers[0].String() != "http://10.0.0.2:8080"+SnapshotPath {
		t.Errorf("peers = %v", peers)
	}
}

func TestDiscoverNoHostNoPeers(t *testing.T) {
	peers, err := Discover(context.Background(), fakeResolver{}, "", 8080, "", "")
	if err != nil || peers != nil {
		t.Errorf("peers=%v err=%v", peers, err)
	}
}

func TestDiscoverNotFoundIsEmpty(t *testing.T) {
	r := fakeResolver{err: &net.DNSError{IsNotFound: true}}
	peers, err := Discover(context.Background(), r, "x", 8080, "", "")
	if err != nil || len(peers) != 0 {
		t.Errorf("peers=%v err=%v", peers, err)
	}
}

func TestClientSendsTheAuthToken(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		_ = domain.NewStore(15*time.Second, time.Minute).WriteSnapshot(w)
	}))
	defer srv.Close()
	host, portStr, _ := net.SplitHostPort(srv.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)

	r := fakeResolver{addrs: []net.IPAddr{{IP: net.ParseIP(host)}}}
	peers, err := Discover(context.Background(), r, "peers", port, "", "s3cret")
	if err != nil || len(peers) != 1 {
		t.Fatalf("peers = %v, %v", peers, err)
	}
	if err := peers[0].Fetch(context.Background(), readInto(domain.NewStore(15*time.Second, time.Minute))); err != nil {
		t.Fatal(err)
	}
	if got != "Bearer s3cret" {
		t.Errorf("Authorization = %q, want the replicas' shared token", got)
	}
	got = ""
	if err := NewClient(host, port, "").Fetch(context.Background(), readInto(domain.NewStore(15*time.Second, time.Minute))); err != nil || got != "" {
		t.Errorf("without a token: Authorization = %q (%v)", got, err)
	}
}

func addrs(ips ...string) fakeResolver {
	r := fakeResolver{}
	for _, ip := range ips {
		r.addrs = append(r.addrs, net.IPAddr{IP: net.ParseIP(ip)})
	}
	return r
}

func TestSenderIsTheLowestAddress(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name   string
		r      fakeResolver
		self   string
		sender bool
		peers  int
	}{
		{"lowest", addrs("10.0.0.9", "10.0.0.10"), "10.0.0.9", true, 1},
		// numeric, not string order: "10.0.0.10" < "10.0.0.9" as strings
		{"not lowest", addrs("10.0.0.9", "10.0.0.10"), "10.0.0.10", false, 1},
		{"not yet in the Service", addrs("10.0.0.20"), "10.0.0.9", true, 1},
		{"alone", addrs("10.0.0.9"), "10.0.0.9", true, 0},
		{"no endpoint", fakeResolver{err: &net.DNSError{IsNotFound: true}}, "10.0.0.9", true, 0},
		{"own address unknown", addrs("10.0.0.1", "10.0.0.2"), "", true, 2},
		{"ipv6", addrs("fd00::2", "fd00::1"), "fd00::2", false, 1},
	} {
		sender, peers, err := Sender(ctx, c.r, "kubeglance-peers", c.self)
		if err != nil || sender != c.sender || peers != c.peers {
			t.Errorf("%s: sender=%v peers=%d err=%v, want %v %d", c.name, sender, peers, err, c.sender, c.peers)
		}
	}
	if sender, peers, err := Sender(ctx, fakeResolver{}, "", "10.0.0.1"); !sender || peers != 0 || err != nil {
		t.Errorf("no peer host: %v %d %v, want the sender", sender, peers, err)
	}
	if _, _, err := Sender(ctx, fakeResolver{err: errors.New("timeout")}, "kubeglance-peers", "10.0.0.1"); err == nil {
		t.Error("a failed lookup must be an error, not a guess")
	}
}
