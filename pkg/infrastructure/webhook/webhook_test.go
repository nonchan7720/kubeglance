package webhook

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nonchan7720/kubeglance/pkg/domain"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// receiver records every request; failAfter > 0 answers 503 from that
// request on.
type receiver struct {
	mu        sync.Mutex
	bodies    []string
	headers   []http.Header
	failAfter int
}

func (r *receiver) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		defer r.mu.Unlock()
		r.bodies = append(r.bodies, string(b))
		r.headers = append(r.headers, req.Header.Clone())
		if r.failAfter > 0 && len(r.bodies) >= r.failAfter {
			http.Error(w, "busy", http.StatusServiceUnavailable)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// memAlert is container-memory firing on app/api-1 (container "app"): the
// window's median 950Mi, p95 1000Mi, now 975Mi of a 1Gi limit.
func memAlert() domain.Alert {
	const mi = 1 << 20
	return domain.Alert{
		Key:    domain.AlertKey{Rule: domain.RuleContainerMemory, Namespace: "app", Pod: "api-1", Container: "app"},
		Status: domain.AlertFiring,
		Since:  t0,
		At:     t0.Add(10 * time.Minute),
		Verdict: domain.Verdict{
			Firing:    true,
			Above:     30,
			Condition: domain.Condition{Resource: domain.ResourceMemory, Total: 1024 * mi, Threshold: 0.9},
			Stats: domain.Stats{
				Now: domain.Resource{Memory: 975 * mi}, Median: domain.Resource{Memory: 950 * mi},
				P95: domain.Resource{Memory: 1000 * mi}, Samples: 41, Expected: 41, Coverage: 1,
			},
		},
	}
}

func nodeAlert() domain.Alert {
	return domain.Alert{
		Key:    domain.AlertKey{Rule: domain.RuleNodeCPU, Node: "n1"},
		Status: domain.AlertFiring,
		Since:  t0,
		At:     t0,
		Verdict: domain.Verdict{
			Firing:    true,
			Condition: domain.Condition{Resource: domain.ResourceCPU, Total: 4000, Threshold: 0.9},
			Stats: domain.Stats{
				Now: domain.Resource{CPU: 3900}, Median: domain.Resource{CPU: 3800}, P95: domain.Resource{CPU: 4000},
				Samples: 30, Expected: 40, Coverage: 0.75,
			},
		},
	}
}

func newClient(t *testing.T, cfg Settings) *Receiver {
	t.Helper()
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestJSONSendsEveryAlertInOneRequest(t *testing.T) {
	r := &receiver{}
	srv := r.serve(t)
	c := newClient(t, Settings{
		URL: srv.URL, Format: FormatJSON, Window: 10 * time.Minute, PublicURL: "https://kubeglance.example/",
		Headers: "Authorization: Bearer s3cret\n# a comment\n\nX-Extra: 1", UserAgent: "kubeglance/test",
	})
	n, err := c.Notify(context.Background(), []domain.Alert{memAlert(), nodeAlert()})
	if err != nil || n != 2 || len(r.bodies) != 1 {
		t.Fatalf("sent %d (%v), %d requests; want 2 in one", n, err, len(r.bodies))
	}
	h := r.headers[0]
	if h.Get("Authorization") != "Bearer s3cret" || h.Get("X-Extra") != "1" || h.Get("Content-Type") != "application/json" || h.Get("User-Agent") != "kubeglance/test" {
		t.Errorf("headers = %v", h)
	}
	var got struct {
		Version int `json:"version"`
		Alerts  []struct {
			Key           domain.AlertKey `json:"key"`
			Status        string          `json:"status"`
			Verdict       domain.Verdict  `json:"verdict"`
			WindowSeconds int             `json:"windowSeconds"`
			Title         string          `json:"title"`
			Body          string          `json:"body"`
			URL           string          `json:"url"`
		} `json:"alerts"`
	}
	if err := json.Unmarshal([]byte(r.bodies[0]), &got); err != nil {
		t.Fatal(err)
	}
	if got.Version != 1 || len(got.Alerts) != 2 {
		t.Fatalf("payload = %+v", got)
	}
	a := got.Alerts[0]
	if a.Key.Pod != "api-1" || a.Status != "firing" || a.WindowSeconds != 600 || a.Verdict.Stats.Median.Memory != 950<<20 {
		t.Errorf("alert = %+v", a)
	}
	if a.URL != "https://kubeglance.example/?pod=app%2Fapi-1" || got.Alerts[1].URL != "https://kubeglance.example/?node=n1" {
		t.Errorf("urls = %q, %q", a.URL, got.Alerts[1].URL)
	}
	if a.Title == "" || a.Body == "" {
		t.Error("no text for the alert")
	}
}

func TestMessagesEnglish(t *testing.T) {
	m := messages{lang: "en", window: 10 * time.Minute}
	title, body := m.text(memAlert())
	// median 950Mi of 1024Mi = 92.77 % -> 93 %
	if title != "api-1: memory at 93% of limit" {
		t.Errorf("title = %q", title)
	}
	if body != "app/api-1 · app\nover 10m: median 93% · p95 98% · now 95% of 1.0Gi" {
		t.Errorf("body = %q", body)
	}
	title, body = m.text(nodeAlert())
	if title != "node n1: CPU at 95% of allocatable" {
		t.Errorf("node title = %q", title)
	}
	if body != "node n1\nover 10m: median 95% · p95 100% · now 98% of 4.00 cores\n25% of the window has no data" {
		t.Errorf("node body = %q", body)
	}

	res := memAlert()
	res.Status = domain.AlertResolved
	res.At = t0.Add(24 * time.Minute)
	res.Verdict.Stats.Now.Memory = 600 << 20
	if title, body = m.text(res); title != "api-1: memory back under 90% of limit" || body != "app/api-1 · app\nnow 59% · fired for 24m" {
		t.Errorf("resolved = %q / %q", title, body)
	}
	res.Gone = true
	res.At = t0.Add(90 * time.Minute)
	if title, body = m.text(res); title != "api-1: resolved, the pod is gone" || body != "app/api-1 · app\nfired for 1h30m" {
		t.Errorf("gone = %q / %q", title, body)
	}
}

func TestMessagesJapanese(t *testing.T) {
	m := messages{lang: "ja", window: 10 * time.Minute}
	title, body := m.text(memAlert())
	if title != "api-1: メモリが limit の 93%" {
		t.Errorf("title = %q", title)
	}
	if body != "app/api-1 · app\n10分間: 中央値 93% · p95 98% · 現在 95%（1.0Gi 中）" {
		t.Errorf("body = %q", body)
	}
	if _, body = m.text(nodeAlert()); !strings.HasSuffix(body, "\nウィンドウの 25% はデータなし") {
		t.Errorf("node body = %q", body)
	}
	res := memAlert()
	res.Status = domain.AlertResolved
	res.At = t0.Add(24 * time.Minute)
	res.Verdict.Stats.Now.Memory = 600 << 20
	if title, body = m.text(res); title != "api-1: メモリが limit の 90% 未満に回復" || body != "app/api-1 · app\n現在 59% · 24分間 発火" {
		t.Errorf("resolved = %q / %q", title, body)
	}
	res.Gone = true
	if title, _ = m.text(res); title != "api-1: 解消（Pod が消滅）" {
		t.Errorf("gone = %q", title)
	}
}

func TestPushshellSendsOneNotificationPerAlert(t *testing.T) {
	r := &receiver{}
	srv := r.serve(t)
	c := newClient(t, Settings{URL: srv.URL, Format: FormatPushshell, Window: 10 * time.Minute, PublicURL: "https://kg.example", Headers: "X-API-Key: k"})
	n, err := c.Notify(context.Background(), []domain.Alert{memAlert(), nodeAlert()})
	if err != nil || n != 2 || len(r.bodies) != 2 {
		t.Fatalf("sent %d (%v) in %d requests, want 2 in 2", n, err, len(r.bodies))
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(r.bodies[0]), &got); err != nil {
		t.Fatal(err)
	}
	if ids, _ := got["loginIds"].([]any); len(ids) != 1 || ids[0] != LoginID {
		t.Errorf("loginIds = %v", got["loginIds"])
	}
	if got["title"] != "api-1: memory at 93% of limit" || got["url"] != "https://kg.example/?pod=app%2Fapi-1" || got["threadId"] != "kubeglance" {
		t.Errorf("notification = %v", got)
	}
	key, _ := got["collapseKey"].(string)
	if len(key) == 0 || len(key) > 64 {
		t.Errorf("collapseKey = %q, want 1-64 characters (pushshell's limit)", key)
	}
	if data, _ := got["data"].(map[string]any); data["alert"] != "container-memory/app/api-1/app" || data["status"] != "firing" {
		t.Errorf("data = %v", got["data"])
	}
	if r.headers[0].Get("X-API-Key") != "k" {
		t.Errorf("headers = %v", r.headers[0])
	}

	// the resolve replaces the firing on the phone: same collapse key
	res := memAlert()
	res.Status = domain.AlertResolved
	if _, err := c.Notify(context.Background(), []domain.Alert{res}); err != nil {
		t.Fatal(err)
	}
	var again map[string]any
	_ = json.Unmarshal([]byte(r.bodies[2]), &again)
	if again["collapseKey"] != key {
		t.Errorf("resolve collapseKey = %v, want %q", again["collapseKey"], key)
	}
}

func TestPushshellSummarizesABurst(t *testing.T) {
	r := &receiver{}
	srv := r.serve(t)
	c := newClient(t, Settings{URL: srv.URL, Format: FormatPushshell, Window: 10 * time.Minute, PublicURL: "https://kg.example"})
	burst := []domain.Alert{memAlert(), memAlert(), memAlert(), nodeAlert()}
	n, err := c.Notify(context.Background(), burst)
	if err != nil || n != 4 || len(r.bodies) != 1 {
		t.Fatalf("sent %d (%v) in %d requests, want 4 in one summary", n, err, len(r.bodies))
	}
	var got map[string]any
	_ = json.Unmarshal([]byte(r.bodies[0]), &got)
	if got["title"] != "kubeglance: 4 alerts" || got["url"] != "https://kg.example/" {
		t.Errorf("summary = %v", got)
	}
	if body, _ := got["body"].(string); !strings.Contains(body, "api-1: memory at 93% of limit") || !strings.Contains(body, "node n1: CPU at 95% of allocatable") {
		t.Errorf("summary body = %q", body)
	}
}

func TestPushshellStopsAtTheFirstFailure(t *testing.T) {
	r := &receiver{failAfter: 2}
	srv := r.serve(t)
	c := newClient(t, Settings{URL: srv.URL, Format: FormatPushshell, Window: 10 * time.Minute})
	n, err := c.Notify(context.Background(), []domain.Alert{memAlert(), nodeAlert()})
	if err == nil || n != 1 {
		t.Errorf("sent %d (%v), want 1 and the 503", n, err)
	}
	if !strings.Contains(err.Error(), "503") || !strings.Contains(err.Error(), "busy") {
		t.Errorf("err = %v, want the status and the receiver's message", err)
	}
	var got map[string]any
	_ = json.Unmarshal([]byte(r.bodies[0]), &got)
	if _, ok := got["url"]; ok {
		t.Errorf("url without --public-url: %v", got["url"])
	}
}

func TestJSONFailureDeliversNothing(t *testing.T) {
	r := &receiver{failAfter: 1}
	srv := r.serve(t)
	c := newClient(t, Settings{URL: srv.URL, Format: FormatJSON, Window: time.Minute})
	if n, err := c.Notify(context.Background(), []domain.Alert{memAlert()}); err == nil || n != 0 {
		t.Errorf("sent %d (%v), want 0 and an error", n, err)
	}
}

func TestNewValidates(t *testing.T) {
	for name, cfg := range map[string]Settings{
		"relative url":      {URL: "/hook", Format: FormatJSON},
		"other scheme":      {URL: "ftp://x/hook", Format: FormatJSON},
		"unknown format":    {URL: "https://x/hook", Format: "slack"},
		"unknown language":  {URL: "https://x/hook", Format: FormatJSON, Language: "fr"},
		"header no colon":   {URL: "https://x/hook", Format: FormatJSON, Headers: "Authorization Bearer x"},
		"header empty name": {URL: "https://x/hook", Format: FormatJSON, Headers: ": x"},
		"public url":        {URL: "https://x/hook", Format: FormatJSON, PublicURL: "kubeglance.local"},
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if _, err := New(Settings{URL: "http://x/hook"}); err != nil {
		t.Errorf("defaults: %v", err)
	}
}

func TestErrorsDoNotLeakTheURL(t *testing.T) {
	// a Slack-style webhook URL is itself the secret
	c := newClient(t, Settings{URL: "http://127.0.0.1:1/hooks/T000/s3cret", Format: FormatJSON})
	_, err := c.Notify(context.Background(), []domain.Alert{memAlert()})
	if err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Errorf("err = %v, want an error without the URL's path", err)
	}
}

func TestRedact(t *testing.T) {
	if got := Redact("https://user:pw@hooks.example.com/T000/s3cret?x=1"); got != "https://hooks.example.com" {
		t.Errorf("Redact = %q", got)
	}
	if got := Redact("::"); got != "(invalid)" {
		t.Errorf("Redact(invalid) = %q", got)
	}
}
