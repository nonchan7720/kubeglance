// Package peer finds the other replicas of kubeglance through the headless
// Service and fetches their history, so a starting replica serves with the
// full window from its first second.
package peer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/nonchan7720/kubeglance/pkg/domain"
)

// SnapshotPath is where every replica serves its history.
const SnapshotPath = "/internal/snapshot"

// Client fetches a snapshot from one replica.
type Client struct {
	url   string
	token string
	http  *http.Client
}

// NewClient returns a client for the replica at host:port. A non-empty
// token (--auth-token-file, shared by the replicas) is sent as a Bearer
// token, since every replica requires it on /internal/snapshot.
func NewClient(host string, port int, token string) *Client {
	return &Client{
		url:   "http://" + net.JoinHostPort(host, strconv.Itoa(port)) + SnapshotPath,
		token: token,
		http:  &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *Client) String() string { return c.url }

// Fetch implements services.PeerSource.
func (c *Client) Fetch(ctx context.Context) (domain.Snapshot, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return domain.Snapshot{}, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return domain.Snapshot{}, err
	}
	defer resp.Body.Close() //nolint:errcheck // nothing to do with a close error on a read
	if resp.StatusCode != http.StatusOK {
		return domain.Snapshot{}, fmt.Errorf("%s: %s", c.url, resp.Status)
	}
	var snap domain.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		return domain.Snapshot{}, fmt.Errorf("%s: decode: %w", c.url, err)
	}
	return snap, nil
}

// Resolver looks up the addresses behind a host name (the headless Service).
type Resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// Discover resolves the headless Service and returns a client per address
// other than self (this pod's IP). An empty host returns nothing: a single
// replica install has no peers. Resolution failure returns an error only when
// the host is set; a headless Service with no other ready endpoint resolves
// to nothing, which is not an error.
func Discover(ctx context.Context, r Resolver, host string, port int, self, token string) ([]*Client, error) {
	if host == "" {
		return nil, nil
	}
	addrs, err := r.LookupIPAddr(ctx, host)
	if err != nil {
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("resolve %s: %w", host, err)
	}
	var peers []*Client
	for _, a := range addrs {
		ip := a.IP.String()
		if ip == self {
			continue
		}
		peers = append(peers, NewClient(ip, port, token))
	}
	return peers, nil
}
