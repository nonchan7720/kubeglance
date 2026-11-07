package k8s

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"

	"github.com/nonchan7720/kubeglance/pkg/domain"
)

// MetricsSource polls metrics.k8s.io. One poll is two list calls (nodes,
// pods in all namespaces) against values metrics-server already holds in
// memory; it adds no load to kubelets.
type MetricsSource struct {
	client metricsclient.Interface
}

// NewMetricsSource returns a source over the metrics clientset.
func NewMetricsSource(c metricsclient.Interface) *MetricsSource {
	return &MetricsSource{client: c}
}

// Poll implements services.MetricsSource. The round's timestamp is when the
// poll started, so a slow list call cannot push it into the next slot; the
// store records it at its slot (domain.Store.Slot), which every entity of the
// round and every replica polling in that slot share.
func (m *MetricsSource) Poll(ctx context.Context) (domain.Round, error) {
	start := time.Now()
	nodes, err := m.client.MetricsV1beta1().NodeMetricses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return domain.Round{}, fmt.Errorf("list node metrics: %w", err)
	}
	pods, err := m.client.MetricsV1beta1().PodMetricses("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return domain.Round{}, fmt.Errorf("list pod metrics: %w", err)
	}
	return convertRound(start, nodes, pods), nil
}

func convertRound(at time.Time, nodes *metricsv1beta1.NodeMetricsList, pods *metricsv1beta1.PodMetricsList) domain.Round {
	r := domain.Round{
		At:    at.Truncate(time.Second),
		Pods:  make(map[domain.PodKey]domain.Resource, len(pods.Items)),
		Nodes: make(map[string]domain.Resource, len(nodes.Items)),
	}
	for _, n := range nodes.Items {
		r.Nodes[n.Name] = toResource(n.Usage)
	}
	for _, p := range pods.Items {
		var sum domain.Resource
		for _, c := range p.Containers {
			sum = sum.Add(toResource(c.Usage))
		}
		r.Pods[domain.PodKey{Namespace: p.Namespace, Name: p.Name}] = sum
	}
	return r
}

// toResource converts a Kubernetes resource list to millicores / bytes.
func toResource(l corev1.ResourceList) domain.Resource {
	var out domain.Resource
	if q, ok := l[corev1.ResourceCPU]; ok {
		out.CPU = q.MilliValue()
	}
	if q, ok := l[corev1.ResourceMemory]; ok {
		out.Memory = q.Value()
	}
	return out
}
