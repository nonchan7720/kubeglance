// Package handler is the HTTP surface of kubeglance: the JSON API the UI
// reads, the embedded UI itself, the health endpoints, and the internal
// snapshot endpoint peers copy history from.
package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/nonchan7720/kubeglance/pkg/domain"
	"github.com/nonchan7720/kubeglance/pkg/interfaces/handler/ui"
	"github.com/nonchan7720/kubeglance/pkg/services"
)

// Deps is what the handler needs.
type Deps struct {
	Store     *domain.Store
	Query     *services.Query
	Collector *services.Collector
	Version   string
	Log       *slog.Logger
}

// New returns the router.
func New(d Deps) http.Handler {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	h := &handler{Deps: d}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/cluster", h.cluster)
	mux.HandleFunc("GET /api/v1/nodes", h.nodes)
	mux.HandleFunc("GET /api/v1/workloads", h.workloads)
	mux.HandleFunc("GET /api/v1/pods", h.pods)
	mux.HandleFunc("GET /api/v1/status", h.status)
	mux.HandleFunc("GET /internal/snapshot", h.snapshot)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /readyz", h.ready)
	mux.Handle("GET /", ui.Handler())
	return h.wrap(mux)
}

type handler struct{ Deps }

func (h *handler) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/internal/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// window parses ?window=30m (default 30m). Anything unparsable is 30m.
func window(r *http.Request) time.Duration {
	d, err := time.ParseDuration(r.URL.Query().Get("window"))
	if err != nil || d <= 0 {
		return 30 * time.Minute
	}
	return d
}

func (h *handler) writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		h.Log.Debug("write response", "err", err)
	}
}

func (h *handler) cluster(w http.ResponseWriter, r *http.Request) {
	h.writeJSON(w, h.Query.Cluster(window(r)))
}

func (h *handler) nodes(w http.ResponseWriter, r *http.Request) {
	h.writeJSON(w, h.Query.Nodes(window(r)))
}

func (h *handler) workloads(w http.ResponseWriter, r *http.Request) {
	h.writeJSON(w, h.Query.Workloads(window(r), r.URL.Query().Get("namespace")))
}

func (h *handler) pods(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := services.PodFilter{Namespace: q.Get("namespace"), Node: q.Get("node")}
	if wl := q.Get("workload"); wl != "" {
		parts := strings.SplitN(wl, "/", 3)
		if len(parts) != 3 {
			http.Error(w, "workload must be namespace/Kind/name", http.StatusBadRequest)
			return
		}
		f.Workload = &domain.WorkloadKey{Namespace: parts[0], Kind: parts[1], Name: parts[2]}
	}
	h.writeJSON(w, h.Query.Pods(window(r), f))
}

// Status is what the UI shows in its header.
type Status struct {
	Version   string          `json:"version"`
	Interval  time.Duration   `json:"interval"`
	Retention time.Duration   `json:"retention"`
	Collector services.Health `json:"collector"`
}

func (h *handler) status(w http.ResponseWriter, _ *http.Request) {
	h.writeJSON(w, Status{
		Version:   h.Version,
		Interval:  h.Store.Interval(),
		Retention: h.Store.Retention(),
		Collector: h.Collector.Health(),
	})
}

func (h *handler) snapshot(w http.ResponseWriter, _ *http.Request) {
	h.writeJSON(w, h.Store.Snapshot())
}

// ready is true once at least one round has been recorded, so a replica
// joins the Service only with data to show. During a rolling update the
// new replica has already copied the peer's history by then.
func (h *handler) ready(w http.ResponseWriter, _ *http.Request) {
	if h.Collector.Health().Rounds == 0 {
		http.Error(w, "no metrics collected yet", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}
