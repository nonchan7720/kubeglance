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

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func sec(s float64) time.Time { return t0.Add(time.Duration(s * float64(time.Second))) }

// scraped is what metrics-server reports for a node after the scrapes at
// scrapeEvery up to now, when the kubelet's own samples are taken every
// sampleEvery: each scrape gets the kubelet's newest sample, and Window is
// the gap back to the previous one metrics-server kept (metrics-server
// pkg/storage: Window = last.Timestamp - prev.Timestamp). The previous point
// is replaced only when a scrape brings a newer sample.
//
// Kubelet samples are taken at fractions of a second (0.1 s to 0.9 s past
// the grid, varying), and the API serves Timestamp in whole seconds
// (metav1.Time) but Window to the nanosecond, so Timestamp - Window is not
// exactly a Timestamp an earlier list showed.
func scraped(name string, now float64, scrapeEvery, sampleEvery float64) metricsv1beta1.NodeMetrics {
	var last, prev float64 = -1, -1
	for s := 0.0; s <= now; s += scrapeEvery {
		k := float64(int(s / sampleEvery))
		ts := k*sampleEvery + 0.1 + float64(int(k)%9)/10
		if ts > s {
			ts -= sampleEvery // not taken yet at this scrape
		}
		if ts > last {
			prev, last = last, ts
		}
	}
	n := metricsv1beta1.NodeMetrics{ObjectMeta: metav1.ObjectMeta{Name: name}, Timestamp: metav1.NewTime(sec(last).Truncate(time.Second))}
	if prev >= 0 {
		n.Window = metav1.Duration{Duration: sec(last).Sub(sec(prev))}
	}
	return n
}

func list(nodes ...metricsv1beta1.NodeMetrics) *metricsv1beta1.NodeMetricsList {
	return &metricsv1beta1.NodeMetricsList{Items: nodes}
}

func TestWindowIsNotTheResolution(t *testing.T) {
	// the premise of the estimator, as seen on kind: scrapes every 15 s of
	// kubelet samples taken every 10 s report windows of about 20 s and
	// 10 s (the first scrape has no window yet: 0)
	got := map[time.Duration]bool{}
	for now := 15.0; now <= 120; now += 15 {
		got[scraped("n1", now, 15, 10).Window.Round(time.Second)] = true
	}
	for w := range got {
		if w >= 14*time.Second && w <= 16*time.Second {
			t.Errorf("window %v seen: the premise is that windows are not the resolution", w)
		}
	}
	if !got[20*time.Second] {
		t.Errorf("windows seen = %v, want one of 20s", got)
	}
}

func TestResolutionIsTheSpacingOfTheSamplesSeen(t *testing.T) {
	for _, c := range []struct {
		name                 string
		pollEvery, scrape, k float64
		want                 time.Duration
	}{
		{"polled at the resolution", 15, 15, 10, 15 * time.Second},
		{"polled slower than the resolution", 20, 15, 10, 15 * time.Second},
		{"kubelet samples every second", 15, 15, 1, 15 * time.Second},
		{"10 s resolution", 10, 10, 1, 10 * time.Second},
		// polled slower than twice the resolution, some samples are never
		// seen and it overestimates; the collector then polls faster, and
		// the next estimate is right (see the next case)
		{"30 s polls of a 10 s resolution", 30, 10, 1, 15 * time.Second},
		{"then 15 s polls", 15, 10, 1, 10 * time.Second},
	} {
		var e resolutionEstimator
		var got time.Duration
		for now := c.pollEvery; now <= 180; now += c.pollEvery {
			got = e.observe(list(scraped("n1", now, c.scrape, c.k), scraped("n2", now, c.scrape, c.k)))
		}
		if got != c.want {
			t.Errorf("%s: resolution = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestResolutionNeedsTwoGaps(t *testing.T) {
	// the kubelet samples of scraped(15, 10) are at 10.2 s, 20.4 s, 40.5 s:
	// the list at 15 s shows one, the list at 30 s two (one gap), and only
	// the list at 45 s a second gap: (40 - 10) / 2
	var e resolutionEstimator
	for _, now := range []float64{15, 30} {
		if got := e.observe(list(scraped("n1", now, 15, 10))); got != 0 {
			t.Errorf("after the list at %v s: %v, want 0 (unknown)", now, got)
		}
	}
	if got := e.observe(list(scraped("n1", 45, 15, 10))); got != 15*time.Second {
		t.Errorf("after the list at 45 s: %v, want 15s", got)
	}
}

func TestResolutionForgetsOldSamples(t *testing.T) {
	// 15 s for three minutes, then 10 s: once the 15 s samples are older
	// than the span, the estimate is the new resolution
	var e resolutionEstimator
	for now := 15.0; now <= 180; now += 15 {
		e.observe(list(scraped("n1", now, 15, 1)))
	}
	var got time.Duration
	for now := 190.0; now <= 400; now += 10 {
		n := scraped("n1", now-180, 10, 1) // the 10 s schedule, shifted to start at 180 s
		n.Timestamp = metav1.NewTime(n.Timestamp.Add(180 * time.Second))
		got = e.observe(list(n))
	}
	if got != 10*time.Second {
		t.Errorf("after the change: %v, want 10s", got)
	}
}

func TestOneSampleSeenTwiceIsOnePoint(t *testing.T) {
	// the sample at 10.6 s: Timestamp 10 s in one list, 30 s - 19.8 s =
	// 10.2 s in the next (the later sample is at 30.4 s)
	var e resolutionEstimator
	e.observe(list(metricsv1beta1.NodeMetrics{ObjectMeta: metav1.ObjectMeta{Name: "n1"}, Timestamp: metav1.NewTime(sec(10)),
		Window: metav1.Duration{Duration: 10600 * time.Millisecond}}))
	e.observe(list(metricsv1beta1.NodeMetrics{ObjectMeta: metav1.ObjectMeta{Name: "n1"}, Timestamp: metav1.NewTime(sec(30)),
		Window: metav1.Duration{Duration: 19800 * time.Millisecond}}))
	if got := len(e.points["n1"]); got != 3 {
		t.Errorf("points = %v, want 3 (0, 10, 30)", e.points["n1"])
	}
}

func TestResolutionIsTheMedianOverNodes(t *testing.T) {
	// n3's kubelet missed scrapes: its samples are 30 s apart
	var e resolutionEstimator
	var got time.Duration
	for now := 15.0; now <= 120; now += 15 {
		got = e.observe(list(scraped("n1", now, 15, 1), scraped("n2", now, 15, 1), scraped("n3", now, 30, 1)))
	}
	if got != 15*time.Second {
		t.Errorf("resolution = %v, want 15s", got)
	}
}

func TestPollReportsTheResolution(t *testing.T) {
	cs := metricsfake.NewSimpleClientset()
	var now float64
	cs.PrependReactor("list", "nodes", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, list(scraped("n1", now, 15, 10)), nil
	})
	cs.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, &metricsv1beta1.PodMetricsList{}, nil
	})
	src := NewMetricsSource(cs)
	// start-up: not known while the lists show one gap (see
	// TestResolutionNeedsTwoGaps)
	for _, now = range []float64{15, 30} {
		if _, err := src.Resolution(context.Background()); err == nil {
			t.Errorf("resolution after the list at %v s: want an error (not known yet)", now)
		}
	}
	now = 45
	r, err := src.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.Resolution != 15*time.Second {
		t.Errorf("round resolution = %v, want 15s (the list at start-up counts too)", r.Resolution)
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
