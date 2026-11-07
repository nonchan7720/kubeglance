// Package k8s adapts the Kubernetes API to kubeglance: the metrics.k8s.io
// poll that feeds the store, and the informers that keep pod and node
// metadata (owner, node, requests, limits, allocatable) current.
package k8s

import (
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"
)

// Clients are the two clientsets kubeglance needs.
type Clients struct {
	Core    kubernetes.Interface
	Metrics metricsclient.Interface
}

// NewClients connects in-cluster when possible, else through kubeconfig
// (the path, else $KUBECONFIG, else ~/.kube/config).
func NewClients(kubeconfig string) (*Clients, error) {
	cfg, err := restConfig(kubeconfig)
	if err != nil {
		return nil, err
	}
	cfg.UserAgent = "kubeglance"
	// two list calls per 15 s plus a few watches: the defaults (5 qps) are
	// plenty, but a burst at start-up (initial lists) should not be throttled
	cfg.QPS, cfg.Burst = 20, 50
	core, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("core client: %w", err)
	}
	metrics, err := metricsclient.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("metrics client: %w", err)
	}
	return &Clients{Core: core, Metrics: metrics}, nil
}

func restConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig == "" {
		if cfg, err := rest.InClusterConfig(); err == nil {
			return cfg, nil
		}
		kubeconfig = os.Getenv("KUBECONFIG")
	}
	if kubeconfig == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("not in cluster and no kubeconfig: %w", err)
		}
		kubeconfig = filepath.Join(home, ".kube", "config")
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("kubeconfig %s: %w", kubeconfig, err)
	}
	return cfg, nil
}
