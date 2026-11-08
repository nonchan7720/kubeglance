//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// The tests run in file order and build on each other: the views first (the
// history fills meanwhile), then the alerts, then a rolling update, which
// needs a few minutes of history to show that none is lost.

const (
	ns       = "kubeglance"
	selector = "app.kubernetes.io/name=kubeglance"
	appNS    = "e2e" // e2e/testdata/workloads.yaml
	mib      = 1 << 20
)

var kube kubernetes.Interface

func TestMain(m *testing.M) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	over := &clientcmd.ConfigOverrides{CurrentContext: os.Getenv("E2E_CONTEXT")}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, over).ClientConfig()
	if err == nil {
		kube, err = kubernetes.NewForConfig(cfg)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e: no cluster (run `mise run e2e`):", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// eventually retries check every 5 s until it returns nil or timeout passes.
func eventually(t *testing.T, timeout time.Duration, what string, check func() error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		err := check()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: not within %s: %v", what, timeout, err)
		}
		time.Sleep(5 * time.Second)
	}
}

// api GETs a kubeglance path through the API server's service proxy (any
// Ready replica) or, with pod set, from that replica.
func api(pod, path string, params map[string]string, out any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var (
		b   []byte
		err error
	)
	if pod == "" {
		b, err = kube.CoreV1().Services(ns).ProxyGet("http", "kubeglance", "80", path, params).DoRaw(ctx)
	} else {
		b, err = kube.CoreV1().Pods(ns).ProxyGet("http", pod, "8080", path, params).DoRaw(ctx)
	}
	if err != nil {
		return fmt.Errorf("GET %s%s: %w", pod, path, err)
	}
	return json.Unmarshal(b, out)
}

// The parts of kubeglance's API the tests read.
type stats struct {
	Samples  int     `json:"samples"`
	Expected int     `json:"expected"`
	Coverage float64 `json:"coverage"`
}

type workload struct {
	Namespace string `json:"namespace"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Pods      int    `json:"pods"`
	Usage     stats  `json:"usage"`
}

func pods(t *testing.T, sel string, namespace string) []corev1.Pod {
	t.Helper()
	list, err := kube.CoreV1().Pods(namespace).List(context.Background(), metav1.ListOptions{LabelSelector: sel})
	if err != nil {
		t.Fatal(err)
	}
	return list.Items
}

func ready(p corev1.Pod) bool {
	if p.DeletionTimestamp != nil {
		return false
	}
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func TestViews(t *testing.T) {
	nodes, err := kube.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	nNodes := len(nodes.Items)

	// auto mode follows metrics-server's --metric-resolution (15s), not the
	// window it reports (20 s and 10 s on kind); the start-up estimate may
	// need a few rounds to settle
	eventually(t, 2*time.Minute, "interval", func() error {
		var status struct {
			Interval time.Duration `json:"interval"`
		}
		if err := api("", "/api/v1/status", nil, &status); err != nil {
			return err
		}
		if status.Interval != 15*time.Second {
			return fmt.Errorf("interval = %s, want metrics-server's 15s", status.Interval)
		}
		return nil
	})

	// every kind of owner resolves; the CronJob's first Job starts within a
	// minute of the install
	want := map[string]int{
		"Deployment/web": 2, "StatefulSet/db": 1, "DaemonSet/agent": nNodes, "Pod/solo": 1, "CronJob/tick": -1,
		"Deployment/receiver": 1,
	}
	eventually(t, 4*time.Minute, "workloads", func() error {
		var v struct {
			Workloads []workload `json:"workloads"`
		}
		if err := api("", "/api/v1/workloads", map[string]string{"namespace": appNS}, &v); err != nil {
			return err
		}
		got := map[string]workload{}
		for _, w := range v.Workloads {
			got[w.Kind+"/"+w.Name] = w
		}
		for key, n := range want {
			w, ok := got[key]
			switch {
			case !ok:
				return fmt.Errorf("no %s in %v", key, v.Workloads)
			case n >= 0 && w.Pods != n:
				return fmt.Errorf("%s has %d pods, want %d", key, w.Pods, n)
			case w.Usage.Samples == 0:
				return fmt.Errorf("%s has no samples yet", key)
			}
		}
		return nil
	})

	var cluster struct {
		Nodes      int   `json:"nodes"`
		ReadyNodes int   `json:"readyNodes"`
		Usage      stats `json:"usage"`
	}
	if err := api("", "/api/v1/cluster", nil, &cluster); err != nil {
		t.Fatal(err)
	}
	if cluster.Nodes != nNodes || cluster.ReadyNodes != nNodes || cluster.Usage.Samples == 0 {
		t.Errorf("cluster = %+v, want %d ready nodes with samples", cluster, nNodes)
	}

	var nv struct {
		Nodes []struct {
			Name        string `json:"name"`
			Allocatable struct {
				CPU int64 `json:"cpu"`
			} `json:"allocatable"`
			Usage stats `json:"usage"`
		} `json:"nodes"`
	}
	if err := api("", "/api/v1/nodes", nil, &nv); err != nil {
		t.Fatal(err)
	}
	if len(nv.Nodes) != nNodes {
		t.Errorf("nodes view has %d nodes, want %d", len(nv.Nodes), nNodes)
	}
	for _, n := range nv.Nodes {
		if n.Allocatable.CPU == 0 || n.Usage.Samples == 0 {
			t.Errorf("node %s: allocatable %dm, %d samples", n.Name, n.Allocatable.CPU, n.Usage.Samples)
		}
	}

	// a two-container pod is broken down per container
	web := pods(t, "app=web", appNS)
	if len(web) == 0 {
		t.Fatal("no web pods")
	}
	eventually(t, time.Minute, "containers of "+web[0].Name, func() error {
		var d struct {
			Containers []struct {
				Name  string `json:"name"`
				Usage stats  `json:"usage"`
			} `json:"containers"`
		}
		if err := api("", "/api/v1/pods/"+appNS+"/"+web[0].Name, nil, &d); err != nil {
			return err
		}
		if len(d.Containers) != 2 || d.Containers[0].Name != "app" || d.Containers[1].Name != "sidecar" {
			return fmt.Errorf("containers = %+v, want app and sidecar", d.Containers)
		}
		for _, c := range d.Containers {
			if c.Usage.Samples == 0 {
				return fmt.Errorf("container %s has no samples", c.Name)
			}
		}
		return nil
	})
}

// alert is one alert of the receiver's requests (kubeglance's json format).
type alert struct {
	Key struct {
		Rule, Namespace, Pod, Container string
	} `json:"key"`
	Status  string `json:"status"`
	Gone    bool   `json:"gone"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Verdict struct {
		Condition struct {
			Total int64 `json:"total"`
		} `json:"condition"`
	} `json:"verdict"`
}

// received is every alert the receiver got for the hog's memory, and the
// User-Agent of each request.
func received() ([]alert, []string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	b, err := kube.CoreV1().Services(appNS).ProxyGet("http", "receiver", "80", "/requests", nil).DoRaw(ctx)
	if err != nil {
		return nil, nil, err
	}
	var reqs []struct {
		Header map[string][]string `json:"header"`
		Body   struct {
			Version int     `json:"version"`
			Alerts  []alert `json:"alerts"`
		} `json:"body"`
	}
	if err := json.Unmarshal(b, &reqs); err != nil {
		return nil, nil, err
	}
	var out []alert
	var agents []string
	for _, r := range reqs {
		agents = append(agents, strings.Join(r.Header["User-Agent"], ","))
		for _, a := range r.Body.Alerts {
			if a.Key.Rule == "container-memory" && a.Key.Namespace == appNS && a.Key.Pod == "hog" && a.Key.Container == "hog" {
				out = append(out, a)
			}
		}
	}
	return out, agents, nil
}

func count(alerts []alert, status string) int {
	n := 0
	for _, a := range alerts {
		if a.Status == status {
			n++
		}
	}
	return n
}

func TestAlerts(t *testing.T) {
	ctx := context.Background()
	limit := resource.MustParse("256Mi")
	hog := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "hog", Namespace: appNS},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "hog", Image: "kubeglance-e2e-helper:e2e", ImagePullPolicy: corev1.PullNever,
			// 236 MiB plus the Go runtime is about 93 % of 256 MiB: over the
			// 90 % threshold (230.4 MiB), 17 MiB below the OOM kill. The
			// binary's own pages are file-backed and may not count in the
			// working set, so the margin is not taken from them
			Args: []string{"hog", "236"},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceMemory: limit, corev1.ResourceCPU: resource.MustParse("5m")},
				Limits:   corev1.ResourceList{corev1.ResourceMemory: limit},
			},
		}}},
	}
	if _, err := kube.CoreV1().Pods(appNS).Create(ctx, hog, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = kube.CoreV1().Pods(appNS).Delete(context.Background(), "hog", metav1.DeleteOptions{})
	})

	// at least half of the one-minute window over 90 %: a few rounds after
	// metrics-server first reports the pod
	eventually(t, 5*time.Minute, "firing alert for the hog", func() error {
		got, _, err := received()
		if err != nil {
			return err
		}
		if count(got, "firing") == 0 {
			return fmt.Errorf("no firing alert yet (%d for the hog); kubeglance sees %s", len(got), hogView())
		}
		return nil
	})
	// three more rounds: neither the other replica nor a later round sends it again
	time.Sleep(45 * time.Second)
	got, agents, err := received()
	if err != nil {
		t.Fatal(err)
	}
	if n := count(got, "firing"); n != 1 {
		t.Errorf("the firing alert arrived %d times, want once with two replicas", n)
	}
	a := got[slices.IndexFunc(got, func(a alert) bool { return a.Status == "firing" })]
	if a.URL != "https://kubeglance.e2e.example/?pod=e2e%2Fhog" || a.Verdict.Condition.Total != 256*mib || !strings.HasPrefix(a.Title, "hog: memory at ") {
		t.Errorf("firing alert = %+v", a)
	}
	for _, ua := range agents {
		if ua != "kubeglance/e2e" {
			t.Errorf("User-Agent = %q, want kubeglance/e2e", ua)
		}
	}

	// the pod goes away: resolved, as gone, once
	zero := int64(0)
	if err := kube.CoreV1().Pods(appNS).Delete(ctx, "hog", metav1.DeleteOptions{GracePeriodSeconds: &zero}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 3*time.Minute, "resolve for the deleted hog", func() error {
		got, _, err := received()
		if err != nil {
			return err
		}
		if count(got, "resolved") == 0 {
			return fmt.Errorf("no resolve yet")
		}
		return nil
	})
	time.Sleep(30 * time.Second)
	got, _, err = received()
	if err != nil {
		t.Fatal(err)
	}
	if n := count(got, "resolved"); n != 1 {
		t.Errorf("the resolve arrived %d times, want once", n)
	}
	if r := got[len(got)-1]; r.Status != "resolved" || !r.Gone {
		t.Errorf("last alert = %+v, want a resolve for a gone pod", r)
	}
}

// hogView is kubeglance's view of the hog's memory, for a failure message.
func hogView() string {
	var v struct {
		Pod struct {
			Limits struct {
				Memory int64 `json:"memory"`
			} `json:"limits"`
			Usage struct {
				Now      struct{ Memory int64 } `json:"now"`
				Median   struct{ Memory int64 } `json:"median"`
				Samples  int                    `json:"samples"`
				Expected int                    `json:"expected"`
			} `json:"usage"`
		} `json:"pod"`
	}
	if err := api("", "/api/v1/pods/"+appNS+"/hog", map[string]string{"window": "1m"}, &v); err != nil {
		return err.Error()
	}
	u := v.Pod.Usage
	return fmt.Sprintf("memory now %.1f MiB, median %.1f MiB of a %d MiB limit, %d/%d samples over 1m",
		float64(u.Now.Memory)/mib, float64(u.Median.Memory)/mib, v.Pod.Limits.Memory/mib, u.Samples, u.Expected)
}

// coverage is the share of the last three minutes a replica holds samples
// for, summed over the nodes.
func coverage(pod string) (float64, error) {
	var v struct {
		Usage stats `json:"usage"`
	}
	if err := api(pod, "/api/v1/cluster", map[string]string{"window": "3m"}, &v); err != nil {
		return 0, err
	}
	return v.Usage.Coverage, nil
}

func TestRollingUpdateKeepsTheHistory(t *testing.T) {
	ctx := context.Background()
	// both replicas must hold more than the window before the update, or
	// a full window afterwards proves nothing
	eventually(t, 5*time.Minute, "replicas older than the window", func() error {
		for _, p := range pods(t, selector, ns) {
			if age := time.Since(p.CreationTimestamp.Time); !ready(p) || age < 4*time.Minute {
				return fmt.Errorf("%s: ready %v, age %s", p.Name, ready(p), age.Round(time.Second))
			}
		}
		return nil
	})
	old := pods(t, selector, ns)
	for _, p := range old {
		c, err := coverage(p.Name)
		if err != nil {
			t.Fatal(err)
		}
		if c < 0.9 {
			t.Fatalf("before the update %s covers %.2f of the window, want ≥ 0.9", p.Name, c)
		}
	}

	patch := fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{"e2e/restartedAt":%q}}}}}`, time.Now().Format(time.RFC3339))
	if _, err := kube.AppsV1().Deployments(ns).Patch(ctx, "kubeglance", types.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		t.Fatal(err)
	}
	isOld := func(name string) bool {
		return slices.ContainsFunc(old, func(p corev1.Pod) bool { return p.Name == name })
	}
	var fresh []corev1.Pod
	eventually(t, 6*time.Minute, "rollout", func() error {
		fresh = fresh[:0]
		for _, p := range pods(t, selector, ns) {
			if isOld(p.Name) {
				return fmt.Errorf("old replica %s still there", p.Name)
			}
			if !ready(p) {
				return fmt.Errorf("%s not ready", p.Name)
			}
			fresh = append(fresh, p)
		}
		if len(fresh) != 2 {
			return fmt.Errorf("%d ready replicas, want 2", len(fresh))
		}
		return nil
	})

	for _, p := range fresh {
		age := time.Since(p.CreationTimestamp.Time)
		if age >= 3*time.Minute {
			t.Fatalf("%s is %s old: older than the window, the check would prove nothing", p.Name, age)
		}
		// without the copy a replica this young would cover at most
		// age / 3 min of the window
		c, err := coverage(p.Name)
		if err != nil {
			t.Fatal(err)
		}
		if c < 0.9 {
			t.Errorf("%s (%s old) covers %.2f of the last 3 minutes, want ≥ 0.9: history was lost", p.Name, age.Round(time.Second), c)
		}
		logs, err := kube.CoreV1().Pods(ns).GetLogs(p.Name, &corev1.PodLogOptions{}).DoRaw(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(logs), "history copied from peer") {
			t.Errorf("%s did not log a peer copy", p.Name)
		}
	}
}
