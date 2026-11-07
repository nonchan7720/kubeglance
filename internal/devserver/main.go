// Command devserver serves the kubeglance UI with synthetic data, for UI work
// without a cluster. Not part of the release binary.
package main

import (
	"context"
	"flag"
	"log/slog"
	"math"
	"math/rand/v2"
	"net/http"
	"os"
	"time"

	"github.com/nonchan7720/kubeglance/pkg/domain"
	"github.com/nonchan7720/kubeglance/pkg/interfaces/handler"
	"github.com/nonchan7720/kubeglance/pkg/services"
)

type synth struct{ t time.Time }

var pods = []domain.PodInfo{
	{Key: domain.PodKey{Namespace: "app", Name: "web-7d9f-abcde"}, Node: "k3s-1", Workload: domain.WorkloadKey{Namespace: "app", Kind: "Deployment", Name: "web"}, Requests: domain.Resource{CPU: 100, Memory: 128 << 20}, Limits: domain.Resource{CPU: 500, Memory: 256 << 20}, Phase: "Running"},
	{Key: domain.PodKey{Namespace: "app", Name: "web-7d9f-fghij"}, Node: "k3s-2", Workload: domain.WorkloadKey{Namespace: "app", Kind: "Deployment", Name: "web"}, Requests: domain.Resource{CPU: 100, Memory: 128 << 20}, Limits: domain.Resource{CPU: 500, Memory: 256 << 20}, Phase: "Running"},
	{Key: domain.PodKey{Namespace: "app", Name: "postgres-0"}, Node: "k3s-1", Workload: domain.WorkloadKey{Namespace: "app", Kind: "StatefulSet", Name: "postgres"}, Requests: domain.Resource{CPU: 500, Memory: 1 << 30}, Phase: "Running"},
	{Key: domain.PodKey{Namespace: "argocd", Name: "argocd-repo-server-5c-xyz"}, Node: "k3s-3", Workload: domain.WorkloadKey{Namespace: "argocd", Kind: "Deployment", Name: "argocd-repo-server"}, Requests: domain.Resource{CPU: 250, Memory: 256 << 20}, Limits: domain.Resource{CPU: 1000, Memory: 1 << 30}, Phase: "Running"},
	{Key: domain.PodKey{Namespace: "kube-system", Name: "traefik-abc"}, Node: "k3s-2", Workload: domain.WorkloadKey{Namespace: "kube-system", Kind: "Deployment", Name: "traefik"}, Requests: domain.Resource{CPU: 50, Memory: 64 << 20}, Limits: domain.Resource{CPU: 200, Memory: 128 << 20}, Phase: "Running"},
	{Key: domain.PodKey{Namespace: "kube-system", Name: "metrics-server-xyz"}, Node: "k3s-3", Workload: domain.WorkloadKey{Namespace: "kube-system", Kind: "Deployment", Name: "metrics-server"}, Requests: domain.Resource{CPU: 100, Memory: 70 << 20}, Phase: "Running"},
}

func (s *synth) Poll(context.Context) (domain.Round, error) {
	s.t = s.t.Add(15 * time.Second)
	r := domain.Round{At: s.t, Pods: map[domain.PodKey]domain.Resource{}, Nodes: map[string]domain.Resource{}}
	wave := 0.5 + 0.5*math.Sin(float64(s.t.Unix())/300)
	for i, p := range pods {
		base := float64(p.Requests.CPU) * (0.4 + wave*float64(i%3))
		r.Pods[p.Key] = domain.Resource{CPU: int64(base + rand.Float64()*30), Memory: int64(float64(p.Requests.Memory)*0.8) + rand.Int64N(32<<20)} //nolint:gosec // synthetic data
	}
	r.Nodes["k3s-1"] = domain.Resource{CPU: int64(900 + wave*1800), Memory: 5<<30 + rand.Int64N(1<<29)}          //nolint:gosec
	r.Nodes["k3s-2"] = domain.Resource{CPU: int64(400 + rand.Float64()*100), Memory: 2<<30 + rand.Int64N(1<<28)} //nolint:gosec
	r.Nodes["k3s-3"] = domain.Resource{CPU: int64(300 + rand.Float64()*50), Memory: 3<<30 + rand.Int64N(1<<28)}  //nolint:gosec
	return r, nil
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "listen address")
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	store := domain.NewStore(15*time.Second, 24*time.Hour)
	store.UpsertNodes([]domain.NodeInfo{
		{Name: "k3s-1", Ready: true, Roles: []string{"control-plane"}, Allocatable: domain.Resource{CPU: 4000, Memory: 8 << 30}, Capacity: domain.Resource{CPU: 4000, Memory: 8 << 30}},
		{Name: "k3s-2", Ready: true, Allocatable: domain.Resource{CPU: 4000, Memory: 8 << 30}, Capacity: domain.Resource{CPU: 4000, Memory: 8 << 30}},
		{Name: "k3s-3", Ready: true, Allocatable: domain.Resource{CPU: 2000, Memory: 4 << 30}, Capacity: domain.Resource{CPU: 2000, Memory: 4 << 30}},
	})
	store.UpsertPods(pods)
	src := &synth{t: time.Now().Add(-2 * time.Hour)}
	col := services.NewCollector(store, src, log)
	for range 480 { // two hours of history
		col.PollOnce(context.Background())
	}
	go col.Run(context.Background())
	log.Info("devserver", "addr", *addr)
	srv := &http.Server{Addr: *addr, Handler: handler.New(handler.Deps{Store: store, Query: services.NewQuery(store), Collector: col, Version: "dev", Log: log}), ReadHeaderTimeout: 5 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		log.Error("devserver", "err", err)
		os.Exit(1)
	}
}
