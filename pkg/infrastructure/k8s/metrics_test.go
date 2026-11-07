package k8s

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsfake "k8s.io/metrics/pkg/client/clientset/versioned/fake"
)

func nodeWithWindow(name string, w time.Duration) metricsv1beta1.NodeMetrics {
	return metricsv1beta1.NodeMetrics{ObjectMeta: metav1.ObjectMeta{Name: name}, Window: metav1.Duration{Duration: w}}
}

// nodeMetrics is a metrics clientset whose node list returns nodes. The
// fake's object tracker files NodeMetrics under a guessed resource name, not
// "nodes" as the API serves them, so the list is answered by a reactor.
func nodeMetrics(nodes ...metricsv1beta1.NodeMetrics) *MetricsSource {
	cs := metricsfake.NewSimpleClientset()
	cs.PrependReactor("list", "nodes", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, &metricsv1beta1.NodeMetricsList{Items: nodes}, nil
	})
	return NewMetricsSource(cs)
}

func TestResolutionIsTheMedianNodeWindowInWholeSeconds(t *testing.T) {
	// metrics-server reports Window = last scrape - previous scrape, which is
	// its --metric-resolution plus the kubelets' jitter; n3 missed a scrape
	src := nodeMetrics(
		nodeWithWindow("n1", 15020*time.Millisecond),
		nodeWithWindow("n2", 14970*time.Millisecond),
		nodeWithWindow("n3", 30010*time.Millisecond),
	)
	got, err := src.Resolution(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != 15*time.Second {
		t.Errorf("resolution = %v, want 15s", got)
	}
}

func TestEveryRoundReportsTheResolution(t *testing.T) {
	r := convertRound(time.Now(),
		&metricsv1beta1.NodeMetricsList{Items: []metricsv1beta1.NodeMetrics{
			nodeWithWindow("n1", 10010*time.Millisecond), nodeWithWindow("n2", 9990*time.Millisecond),
		}},
		&metricsv1beta1.PodMetricsList{})
	if r.Resolution != 10*time.Second {
		t.Errorf("round resolution = %v, want 10s", r.Resolution)
	}
	if r := convertRound(time.Now(), &metricsv1beta1.NodeMetricsList{}, &metricsv1beta1.PodMetricsList{}); r.Resolution != 0 {
		t.Errorf("no nodes: resolution = %v, want 0 (unknown)", r.Resolution)
	}
}

func TestResolutionWithoutNodeMetricsIsAnError(t *testing.T) {
	for name, src := range map[string]*MetricsSource{
		"no nodes":    nodeMetrics(),
		"zero window": nodeMetrics(nodeWithWindow("n1", 0)),
	} {
		if _, err := src.Resolution(context.Background()); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
