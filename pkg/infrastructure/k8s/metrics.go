package k8s

import (
	"context"
	"fmt"
	"slices"
	"sync"
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
	est    resolutionEstimator
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
	r := convertRound(start, nodes, pods)
	// every poll refines the estimate, so following a change of
	// metrics-server's resolution costs no extra call
	r.Resolution = m.est.observe(nodes)
	return r, nil
}

// Resolution is how often metrics-server gets a new value, as far as the
// lists so far show it (resolutionEstimator); an error until two gaps
// between kubelet samples have been seen. It reads what metrics-server holds
// in memory and adds no kubelet load.
func (m *MetricsSource) Resolution(ctx context.Context) (time.Duration, error) {
	nodes, err := m.client.MetricsV1beta1().NodeMetricses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, fmt.Errorf("list node metrics: %w", err)
	}
	r := m.est.observe(nodes)
	if r == 0 {
		return 0, fmt.Errorf("not enough node metrics yet to tell metrics-server's resolution")
	}
	return r, nil
}

// resolutionSpan is how far back the estimator keeps samples: long enough
// to average a dozen gaps at 15 s, short enough to follow a change.
const resolutionSpan = 3 * time.Minute

// resolutionEstimator tells metrics-server's resolution (its
// --metric-resolution, how often a new value arrives) from what it reports.
//
// A NodeMetrics carries the timestamps of the two kubelet samples
// metrics-server holds: Timestamp and Timestamp - Window. Window is not the
// resolution: when a scrape brings no newer kubelet sample, metrics-server
// keeps the older one, and with kubelets that sample every 10 s, scrapes
// every 15 s report windows of 20 s and 10 s, never 15 s (seen on kind).
// What is the resolution is how far apart the distinct samples are on
// average: (newest - oldest) / (samples - 1) over the last resolutionSpan,
// per node, and the median over the nodes, to the second.
//
// Each list shows two consecutive samples, so polls less than twice the
// resolution apart see every sample. Slower polls miss some and
// overestimate; the collector then polls at that estimate, which is less
// than twice the resolution, and the next estimate is right.
type resolutionEstimator struct {
	mu     sync.Mutex
	points map[string][]time.Time // per node: sorted, distinct
}

// observe adds a node list and returns the resolution, 0 while no node has
// shown two gaps.
func (e *resolutionEstimator) observe(nodes *metricsv1beta1.NodeMetricsList) time.Duration {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.points == nil {
		e.points = map[string][]time.Time{}
	}
	var newest time.Time
	for _, n := range nodes.Items {
		ts := n.Timestamp.Time
		if ts.IsZero() {
			continue
		}
		pts := insert(e.points[n.Name], ts)
		if w := n.Window.Duration; w > 0 {
			pts = insert(pts, ts.Add(-w))
		}
		e.points[n.Name] = pts
		newest = later(newest, pts[len(pts)-1])
	}
	var estimates []time.Duration
	for name, pts := range e.points {
		cut := newest.Add(-resolutionSpan)
		i := 0
		for i < len(pts) && pts[i].Before(cut) {
			i++
		}
		pts = pts[i:]
		if len(pts) == 0 { // a node gone for longer than the span
			delete(e.points, name)
			continue
		}
		e.points[name] = pts
		if len(pts) >= 3 {
			estimates = append(estimates, pts[len(pts)-1].Sub(pts[0])/time.Duration(len(pts)-1))
		}
	}
	if len(estimates) == 0 {
		return 0
	}
	slices.Sort(estimates)
	return estimates[len(estimates)/2].Round(time.Second)
}

// insert adds t to the sorted pts unless it is there.
func insert(pts []time.Time, t time.Time) []time.Time {
	i, found := slices.BinarySearchFunc(pts, t, func(a, b time.Time) int { return a.Compare(b) })
	if found {
		return pts
	}
	return slices.Insert(pts, i, t)
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func convertRound(at time.Time, nodes *metricsv1beta1.NodeMetricsList, pods *metricsv1beta1.PodMetricsList) domain.Round {
	r := domain.Round{
		At:         at.Truncate(time.Second),
		Pods:       make(map[domain.PodKey]domain.Resource, len(pods.Items)),
		Nodes:      make(map[string]domain.Resource, len(nodes.Items)),
		Containers: map[domain.PodKey]map[string]domain.Resource{},
	}
	for _, n := range nodes.Items {
		r.Nodes[n.Name] = toResource(n.Usage)
	}
	for _, p := range pods.Items {
		key := domain.PodKey{Namespace: p.Namespace, Name: p.Name}
		var sum domain.Resource
		var byName map[string]domain.Resource
		if len(p.Containers) > 1 {
			// only multi-container pods: a single container's series is the pod's
			byName = make(map[string]domain.Resource, len(p.Containers))
			r.Containers[key] = byName
		}
		for _, c := range p.Containers {
			u := toResource(c.Usage)
			sum = sum.Add(u)
			if byName != nil {
				byName[c.Name] = u
			}
		}
		r.Pods[key] = sum
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
