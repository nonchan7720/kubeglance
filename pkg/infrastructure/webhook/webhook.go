// Package webhook delivers alerts over HTTP: kubeglance's own JSON (every
// alert of an evaluation in one request) or, for the pushshell notification
// backend, one push notification per alert.
package webhook

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nonchan7720/kubeglance/pkg/domain"
)

// Formats of the request body.
const (
	FormatJSON      = "json"
	FormatPushshell = "pushshell"
)

// LoginID is the pushshell login ID kubeglance notifies; the UI registers
// the phone under it when it runs inside a pushshell app.
const LoginID = "kubeglance"

// maxSeparate is how many pushshell notifications one evaluation sends
// separately; more become one summary, so a node going down does not buzz
// the phone once per pod.
const maxSeparate = 3

// Settings describes the receiver.
type Settings struct {
	URL    string
	Format string // FormatJSON (default) or FormatPushshell
	// Headers are "Name: value" lines added to every request (an API key),
	// read from a mounted Secret; blank lines and # comments are skipped.
	Headers string
	// PublicURL is where the phone reaches kubeglance; with it every alert
	// links to its pod or node. Empty: no links.
	PublicURL string
	Language  string        // "en" (default) or "ja"
	Window    time.Duration // the rules' window, for the text
	UserAgent string
}

// Receiver is one webhook endpoint alerts are sent to.
type Receiver struct {
	cfg     Settings
	headers http.Header
	public  string
	msg     messages
	http    *http.Client
}

// New validates the configuration.
func New(cfg Settings) (*Receiver, error) {
	if u, err := url.Parse(cfg.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("webhook URL %s: want an absolute http(s) URL", Redact(cfg.URL))
	}
	switch cfg.Format {
	case "":
		cfg.Format = FormatJSON
	case FormatJSON, FormatPushshell:
	default:
		return nil, fmt.Errorf("webhook format %q: want %s or %s", cfg.Format, FormatJSON, FormatPushshell)
	}
	switch cfg.Language {
	case "":
		cfg.Language = "en"
	case "en", "ja":
	default:
		return nil, fmt.Errorf("alert language %q: want en or ja", cfg.Language)
	}
	public := strings.TrimRight(cfg.PublicURL, "/")
	if public != "" {
		if u, err := url.Parse(public); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("public URL %q: want an absolute http(s) URL", cfg.PublicURL)
		}
	}
	h, err := parseHeaders(cfg.Headers)
	if err != nil {
		return nil, err
	}
	return &Receiver{
		cfg: cfg, headers: h, public: public,
		msg:  messages{lang: cfg.Language, window: cfg.Window},
		http: &http.Client{Timeout: 10 * time.Second},
	}, nil
}

// parseHeaders reads "Name: value" lines. The values are secrets: an error
// names the line, never its content.
func parseHeaders(s string) (http.Header, error) {
	h := http.Header{}
	for i, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		name = strings.TrimSpace(name)
		if !ok || name == "" || strings.ContainsAny(name, " \t") {
			return nil, fmt.Errorf("webhook headers, line %d: want \"Name: value\"", i+1)
		}
		h.Add(name, strings.TrimSpace(value))
	}
	return h, nil
}

// Redact is the URL without what may be a secret (user info, path, query):
// a Slack-style webhook URL is itself the credential.
func Redact(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(invalid)"
	}
	return u.Scheme + "://" + u.Host
}

// Notify implements services.Notifier.
func (c *Receiver) Notify(ctx context.Context, alerts []domain.Alert) (int, error) {
	if len(alerts) == 0 {
		return 0, nil
	}
	if c.cfg.Format == FormatJSON {
		if err := c.post(ctx, c.jsonPayload(alerts)); err != nil {
			return 0, err
		}
		return len(alerts), nil
	}
	if len(alerts) > maxSeparate {
		if err := c.post(ctx, c.summary(alerts)); err != nil {
			return 0, err
		}
		return len(alerts), nil
	}
	for i, a := range alerts {
		if err := c.post(ctx, c.pushshell(a)); err != nil {
			return i, err
		}
	}
	return len(alerts), nil
}

type jsonAlert struct {
	domain.Alert
	WindowSeconds int    `json:"windowSeconds"`
	Title         string `json:"title"`
	Body          string `json:"body"`
	URL           string `json:"url,omitempty"`
}

func (c *Receiver) jsonPayload(alerts []domain.Alert) any {
	out := make([]jsonAlert, len(alerts))
	for i, a := range alerts {
		title, body := c.msg.text(a)
		out[i] = jsonAlert{Alert: a, WindowSeconds: int(c.cfg.Window / time.Second), Title: title, Body: body, URL: c.link(a.Key)}
	}
	return struct {
		Version int         `json:"version"`
		Alerts  []jsonAlert `json:"alerts"`
	}{1, out}
}

// pushshellNotification is the subset of pushshell's SendNotificationRequest
// kubeglance uses.
type pushshellNotification struct {
	LoginIDs    []string          `json:"loginIds"`
	Title       string            `json:"title"`
	Body        string            `json:"body,omitempty"`
	URL         string            `json:"url,omitempty"`
	CollapseKey string            `json:"collapseKey,omitempty"`
	ThreadID    string            `json:"threadId,omitempty"`
	Data        map[string]string `json:"data,omitempty"`
}

func (c *Receiver) pushshell(a domain.Alert) pushshellNotification {
	title, body := c.msg.text(a)
	return pushshellNotification{
		LoginIDs: []string{LoginID}, Title: title, Body: body, URL: c.link(a.Key),
		// the resolve replaces the firing on the phone; pushshell allows 64
		CollapseKey: "kg-" + hash(a.Key.String()),
		ThreadID:    "kubeglance",
		Data:        map[string]string{"alert": a.Key.String(), "status": string(a.Status)},
	}
}

func (c *Receiver) summary(alerts []domain.Alert) pushshellNotification {
	const lines = 8
	var b strings.Builder
	for i, a := range alerts {
		if i == lines {
			b.WriteString(c.msg.more(len(alerts) - lines))
			break
		}
		if i > 0 {
			b.WriteByte('\n')
		}
		title, _ := c.msg.text(a)
		b.WriteString(title)
	}
	link := ""
	if c.public != "" {
		link = c.public + "/"
	}
	return pushshellNotification{
		LoginIDs: []string{LoginID}, Title: c.msg.summaryTitle(len(alerts)), Body: b.String(), URL: link,
		ThreadID: "kubeglance", Data: map[string]string{"alerts": fmt.Sprint(len(alerts))},
	}
}

func hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:12])
}

// link opens the alert's pod, or the pods on its node, in the UI.
func (c *Receiver) link(k domain.AlertKey) string {
	if c.public == "" {
		return ""
	}
	q := url.Values{}
	if k.Node != "" {
		q.Set("node", k.Node)
	} else {
		q.Set("pod", k.Namespace+"/"+k.Pod)
	}
	return c.public + "/?" + q.Encode()
}

func (c *Receiver) post(ctx context.Context, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.URL, bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("webhook %s: %w", Redact(c.cfg.URL), err)
	}
	maps.Copy(req.Header, c.headers)
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.UserAgent != "" {
		req.Header.Set("User-Agent", c.cfg.UserAgent)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ue, ok := errors.AsType[*url.Error](err); ok {
			ue.URL = Redact(ue.URL)
		}
		return err
	}
	defer resp.Body.Close() //nolint:errcheck // nothing to do with a close error on a read
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10)) // reuse the connection
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("webhook %s: %s: %s", Redact(c.cfg.URL), resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}
