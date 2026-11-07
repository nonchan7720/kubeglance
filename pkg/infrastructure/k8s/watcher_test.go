package k8s

import (
	"context"
	"log/slog"
	"slices"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"

	"github.com/nonchan7720/kubeglance/pkg/domain"
)

func ctrl(kind, name string, uid types.UID) metav1.OwnerReference {
	t := true
	return metav1.OwnerReference{Kind: kind, Name: name, UID: uid, Controller: &t}
}

func q(s string) resource.Quantity { return resource.MustParse(s) }

func TestWatcherResolvesOwnersAndSums(t *testing.T) {
	client := fake.NewClientset(
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "web-abc", UID: "rs1",
			OwnerReferences: []metav1.OwnerReference{ctrl("Deployment", "web", "d1")}}},
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "backup-123", UID: "j1",
			OwnerReferences: []metav1.OwnerReference{ctrl("CronJob", "backup", "cj1")}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "web-abc-x", OwnerReferences: []metav1.OwnerReference{ctrl("ReplicaSet", "web-abc", "rs1")}},
			Spec: corev1.PodSpec{NodeName: "n1", Containers: []corev1.Container{
				{Name: "app", Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: q("100m"), corev1.ResourceMemory: q("64Mi")},
					Limits:   corev1.ResourceList{corev1.ResourceCPU: q("200m"), corev1.ResourceMemory: q("128Mi")}}},
				{Name: "proxy", Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: q("50m")},
					Limits:   corev1.ResourceList{corev1.ResourceCPU: q("100m"), corev1.ResourceMemory: q("128Mi")}}},
			}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "backup-123-y", OwnerReferences: []metav1.OwnerReference{ctrl("Job", "backup-123", "j1")}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "db-0", OwnerReferences: []metav1.OwnerReference{ctrl("StatefulSet", "db", "s1")}},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: q("1")}}}}}}, // no limits
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "etcd-n1", OwnerReferences: []metav1.OwnerReference{ctrl("Node", "n1", "n1")}}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1", Labels: map[string]string{"node-role.kubernetes.io/control-plane": "true"}},
			Status: corev1.NodeStatus{
				Allocatable: corev1.ResourceList{corev1.ResourceCPU: q("4"), corev1.ResourceMemory: q("8Gi")},
				Capacity:    corev1.ResourceList{corev1.ResourceCPU: q("4"), corev1.ResourceMemory: q("8Gi")},
				Conditions:  []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
			}},
	)
	store := domain.NewStore(15*time.Second, time.Minute)
	w, err := NewWatcher(client, store, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	// Run re-resolves every pod after the sync, so the mapping is complete
	// here; the event handlers may still fire afterwards, with the same result.
	web, ok := store.Pod(domain.PodKey{Namespace: "app", Name: "web-abc-x"})
	if !ok {
		t.Fatal("web pod missing")
	}
	if web.Workload != (domain.WorkloadKey{Namespace: "app", Kind: domain.KindDeployment, Name: "web"}) {
		t.Errorf("web workload = %v", web.Workload)
	}
	if web.Node != "n1" || web.Requests.CPU != 150 || web.Requests.Memory != 64<<20 {
		t.Errorf("web requests = %+v", web)
	}
	if web.Limits.CPU != 300 || web.Limits.Memory != 256<<20 {
		t.Errorf("web limits = %+v", web.Limits)
	}
	wantC := []domain.ContainerInfo{
		{Name: "app", Requests: domain.Resource{CPU: 100, Memory: 64 << 20}, Limits: domain.Resource{CPU: 200, Memory: 128 << 20}},
		{Name: "proxy", Requests: domain.Resource{CPU: 50}, Limits: domain.Resource{CPU: 100, Memory: 128 << 20}},
	}
	if !slices.Equal(web.Containers, wantC) {
		t.Errorf("web containers = %+v, want %+v", web.Containers, wantC)
	}

	bk, _ := store.Pod(domain.PodKey{Namespace: "app", Name: "backup-123-y"})
	if bk.Workload.Kind != domain.KindCronJob || bk.Workload.Name != "backup" {
		t.Errorf("job pod workload = %v", bk.Workload)
	}
	db, _ := store.Pod(domain.PodKey{Namespace: "app", Name: "db-0"})
	if db.Workload.Kind != domain.KindStatefulSet || !db.Limits.IsZero() {
		t.Errorf("db = %+v", db)
	}
	etcd, _ := store.Pod(domain.PodKey{Namespace: "kube-system", Name: "etcd-n1"})
	if etcd.Workload.Kind != domain.KindPod {
		t.Errorf("static pod workload = %v", etcd.Workload)
	}

	n1, ok := store.Node("n1")
	if !ok || !n1.Ready || n1.Allocatable.CPU != 4000 || n1.Allocatable.Memory != 8<<30 {
		t.Errorf("node = %+v", n1)
	}
	if len(n1.Roles) != 1 || n1.Roles[0] != "control-plane" {
		t.Errorf("roles = %v", n1.Roles)
	}
}

func TestConvertRoundSumsContainers(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 500, time.UTC)
	r := convertRound(at,
		&metricsv1beta1.NodeMetricsList{Items: []metricsv1beta1.NodeMetrics{{
			ObjectMeta: metav1.ObjectMeta{Name: "n1"},
			Usage:      corev1.ResourceList{corev1.ResourceCPU: q("1500m"), corev1.ResourceMemory: q("2Gi")}}}},
		&metricsv1beta1.PodMetricsList{Items: []metricsv1beta1.PodMetrics{{
			ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "p"},
			Containers: []metricsv1beta1.ContainerMetrics{
				{Name: "app", Usage: corev1.ResourceList{corev1.ResourceCPU: q("10m"), corev1.ResourceMemory: q("10Mi")}},
				{Name: "proxy", Usage: corev1.ResourceList{corev1.ResourceCPU: q("20m"), corev1.ResourceMemory: q("20Mi")}},
			}}, {
			ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "single"},
			Containers: []metricsv1beta1.ContainerMetrics{
				{Name: "only", Usage: corev1.ResourceList{corev1.ResourceCPU: q("5m")}},
			}}}},
	)
	if r.At.Nanosecond() != 0 {
		t.Errorf("round time not truncated: %v", r.At)
	}
	if r.Nodes["n1"].CPU != 1500 || r.Nodes["n1"].Memory != 2<<30 {
		t.Errorf("node usage = %+v", r.Nodes["n1"])
	}
	p := r.Pods[domain.PodKey{Namespace: "app", Name: "p"}]
	if p.CPU != 30 || p.Memory != 30<<20 {
		t.Errorf("pod usage = %+v", p)
	}
	c := r.Containers[domain.PodKey{Namespace: "app", Name: "p"}]
	if len(c) != 2 || c["app"].CPU != 10 || c["proxy"].Memory != 20<<20 {
		t.Errorf("containers = %+v", c)
	}
	if _, ok := r.Containers[domain.PodKey{Namespace: "app", Name: "single"}]; ok {
		t.Error("a single-container pod got a container breakdown (its pod series is the container's)")
	}
}
