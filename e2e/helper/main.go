// Command helper is the one image the end-to-end tests run besides
// kubeglance, so they pull nothing else: a pod that only sleeps, a memory
// hog that holds a fixed amount of memory, and a webhook receiver that keeps
// every request it gets.
//
//	helper sleep [duration]   (forever without one)
//	helper hog MiB
//	helper receive   (POST anything: kept; GET /requests: all of them as JSON)
package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if len(os.Args) < 2 {
		log.Error("usage: helper sleep [duration] | hog MiB | receive")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "sleep":
		if len(os.Args) == 3 {
			d, err := time.ParseDuration(os.Args[2])
			if err != nil {
				log.Error("usage: helper sleep [duration]", "err", err)
				os.Exit(2)
			}
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, d)
			defer cancel()
		}
		<-ctx.Done()
	case "hog":
		mib, err := strconv.Atoi(os.Args[len(os.Args)-1])
		if err != nil || len(os.Args) != 3 {
			log.Error("usage: helper hog MiB")
			os.Exit(2)
		}
		hog(ctx, mib, log)
	case "receive":
		if err := receive(ctx, log); err != nil {
			log.Error("receiver stopped", "err", err)
			os.Exit(1)
		}
	default:
		log.Error("unknown command", "command", os.Args[1])
		os.Exit(2)
	}
}

// hog allocates mib MiB, writes every page so it is resident (and counted
// in the container's working set), and holds it until it is stopped.
func hog(ctx context.Context, mib int, log *slog.Logger) {
	buf := make([]byte, mib<<20)
	for i := 0; i < len(buf); i += 4096 {
		buf[i] = 1
	}
	log.Info("holding memory", "MiB", mib)
	<-ctx.Done()
	log.Info("released", "MiB", len(buf)>>20)
}

// request is one request the receiver got.
type request struct {
	At     time.Time       `json:"at"`
	Path   string          `json:"path"`
	Header http.Header     `json:"header"`
	Body   json.RawMessage `json:"body"`
}

func receive(ctx context.Context, log *slog.Logger) error {
	var (
		mu  sync.Mutex
		got = []request{}
	)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /requests", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(got)
	})
	mux.HandleFunc("POST /", func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil || !json.Valid(b) {
			http.Error(w, "want a JSON body", http.StatusBadRequest)
			return
		}
		mu.Lock()
		got = append(got, request{At: time.Now().UTC(), Path: r.URL.Path, Header: r.Header, Body: b})
		mu.Unlock()
		log.Info("received", "path", r.URL.Path, "bytes", len(b))
		w.WriteHeader(http.StatusNoContent)
	})
	srv := &http.Server{Addr: ":8080", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		// the parent context is done: shut down within a fresh deadline
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	log.Info("receiving", "addr", srv.Addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
