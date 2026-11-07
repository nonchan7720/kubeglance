package k8s

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	"github.com/nonchan7720/kubeglance/pkg/domain"
)

// Watcher keeps the store's pod and node metadata current with informers
// (watches, not polling). Pods are mapped to their top-level controller:
// Pod → ReplicaSet → Deployment, Pod → Job → CronJob, Pod → StatefulSet /
// DaemonSet. A pod whose owner cannot be resolved (yet) is a workload of
// its own until the owner's informer catches up.
type Watcher struct {
	store   *domain.Store
	factory informers.SharedInformerFactory
	pods    cache.SharedIndexInformer
	nodes   cache.SharedIndexInformer
	rs      cache.SharedIndexInformer
	jobs    cache.SharedIndexInformer
	log     *slog.Logger
	now     func() time.Time
}

// NewWatcher builds the informers; nothing runs until Run.
func NewWatcher(client kubernetes.Interface, store *domain.Store, log *slog.Logger) (*Watcher, error) {
	if log == nil {
		log = slog.Default()
	}
	f := informers.NewSharedInformerFactory(client, 0)
	w := &Watcher{
		store:   store,
		factory: f,
		pods:    f.Core().V1().Pods().Informer(),
		nodes:   f.Core().V1().Nodes().Informer(),
		rs:      f.Apps().V1().ReplicaSets().Informer(),
		jobs:    f.Batch().V1().Jobs().Informer(),
		log:     log,
		now:     time.Now,
	}
	if _, err := w.pods.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(o any) { w.onPod(o) },
		UpdateFunc: func(_, o any) { w.onPod(o) },
		DeleteFunc: w.onPodDelete,
	}); err != nil {
		return nil, fmt.Errorf("pod informer: %w", err)
	}
	if _, err := w.nodes.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(o any) { w.onNode(o) },
		UpdateFunc: func(_, o any) { w.onNode(o) },
		DeleteFunc: w.onNodeDelete,
	}); err != nil {
		return nil, fmt.Errorf("node informer: %w", err)
	}
	// when a ReplicaSet or Job appears, pods that pointed at it before it was
	// known get their workload resolved
	reresolve := cache.ResourceEventHandlerFuncs{AddFunc: func(o any) { w.reresolveOwned(o) }}
	if _, err := w.rs.AddEventHandler(reresolve); err != nil {
		return nil, fmt.Errorf("replicaset informer: %w", err)
	}
	if _, err := w.jobs.AddEventHandler(reresolve); err != nil {
		return nil, fmt.Errorf("job informer: %w", err)
	}
	return w, nil
}

// Run starts the informers and blocks until the caches are synced or ctx
// ends. It returns an error only when the sync fails.
func (w *Watcher) Run(ctx context.Context) error {
	w.factory.Start(ctx.Done())
	for typ, ok := range w.factory.WaitForCacheSync(ctx.Done()) {
		if !ok {
			return fmt.Errorf("informer cache for %v did not sync", typ)
		}
	}
	// The informers list in parallel, so a pod can be handled before its
	// ReplicaSet / Job is known. Now that every cache is synced, resolve all
	// pods once more so the workload mapping is complete before the first poll.
	var infos []domain.PodInfo
	for _, obj := range w.pods.GetStore().List() {
		if p, ok := obj.(*corev1.Pod); ok {
			infos = append(infos, w.podInfo(p))
		}
	}
	w.store.UpsertPods(infos)
	var nodes []domain.NodeInfo
	for _, obj := range w.nodes.GetStore().List() {
		if n, ok := obj.(*corev1.Node); ok {
			nodes = append(nodes, nodeInfo(n))
		}
	}
	w.store.UpsertNodes(nodes)
	w.log.Info("informers synced", "pods", len(infos), "nodes", len(nodes))
	return nil
}

func (w *Watcher) onPod(o any) {
	p, ok := o.(*corev1.Pod)
	if !ok {
		return
	}
	w.store.UpsertPods([]domain.PodInfo{w.podInfo(p)})
}

func (w *Watcher) onPodDelete(o any) {
	if t, ok := o.(cache.DeletedFinalStateUnknown); ok {
		o = t.Obj
	}
	if p, ok := o.(*corev1.Pod); ok {
		w.store.RemovePod(domain.PodKey{Namespace: p.Namespace, Name: p.Name}, w.now())
	}
}

func (w *Watcher) onNode(o any) {
	n, ok := o.(*corev1.Node)
	if !ok {
		return
	}
	w.store.UpsertNodes([]domain.NodeInfo{nodeInfo(n)})
}

func (w *Watcher) onNodeDelete(o any) {
	if t, ok := o.(cache.DeletedFinalStateUnknown); ok {
		o = t.Obj
	}
	if n, ok := o.(*corev1.Node); ok {
		w.store.RemoveNode(n.Name, w.now())
	}
}

// reresolveOwned re-upserts the pods owned by a controller that just became
// known, so their workload is resolved.
func (w *Watcher) reresolveOwned(o any) {
	meta, ok := o.(metav1.Object)
	if !ok {
		return
	}
	for _, obj := range w.pods.GetStore().List() {
		p, ok := obj.(*corev1.Pod)
		if !ok || p.Namespace != meta.GetNamespace() {
			continue
		}
		for _, ref := range p.OwnerReferences {
			if ref.UID == meta.GetUID() {
				w.onPod(p)
				break
			}
		}
	}
}

func (w *Watcher) podInfo(p *corev1.Pod) domain.PodInfo {
	info := domain.PodInfo{
		Key:      domain.PodKey{Namespace: p.Namespace, Name: p.Name},
		Node:     p.Spec.NodeName,
		Workload: w.resolveWorkload(p),
		Phase:    string(p.Status.Phase),
	}
	limitless := false
	for _, c := range p.Spec.Containers {
		info.Requests = info.Requests.Add(toResource(c.Resources.Requests))
		lim := toResource(c.Resources.Limits)
		if lim.CPU == 0 || lim.Memory == 0 {
			limitless = true
		}
		info.Limits = info.Limits.Add(lim)
	}
	if limitless {
		// a pod is only as limited as its least limited container
		info.Limits = domain.Resource{}
	}
	return info
}

// resolveWorkload follows owner references to the top-level controller.
func (w *Watcher) resolveWorkload(p *corev1.Pod) domain.WorkloadKey {
	ref := controllerRef(p.OwnerReferences)
	if ref == nil {
		return domain.WorkloadKey{Namespace: p.Namespace, Kind: domain.KindPod, Name: p.Name}
	}
	switch ref.Kind {
	case domain.KindReplicaSet:
		if obj, ok, _ := w.rs.GetStore().GetByKey(p.Namespace + "/" + ref.Name); ok {
			if rs, ok := obj.(*appsv1.ReplicaSet); ok {
				if up := controllerRef(rs.OwnerReferences); up != nil && up.Kind == domain.KindDeployment {
					return domain.WorkloadKey{Namespace: p.Namespace, Kind: domain.KindDeployment, Name: up.Name}
				}
			}
		}
		return domain.WorkloadKey{Namespace: p.Namespace, Kind: domain.KindReplicaSet, Name: ref.Name}
	case domain.KindJob:
		if obj, ok, _ := w.jobs.GetStore().GetByKey(p.Namespace + "/" + ref.Name); ok {
			if job, ok := obj.(*batchv1.Job); ok {
				if up := controllerRef(job.OwnerReferences); up != nil && up.Kind == domain.KindCronJob {
					return domain.WorkloadKey{Namespace: p.Namespace, Kind: domain.KindCronJob, Name: up.Name}
				}
			}
		}
		return domain.WorkloadKey{Namespace: p.Namespace, Kind: domain.KindJob, Name: ref.Name}
	case domain.KindStatefulSet, domain.KindDaemonSet:
		return domain.WorkloadKey{Namespace: p.Namespace, Kind: ref.Kind, Name: ref.Name}
	case "Node":
		// a static pod (mirror pod) is owned by its node: its own workload
		return domain.WorkloadKey{Namespace: p.Namespace, Kind: domain.KindPod, Name: p.Name}
	default:
		// an operator's custom controller: group by it as it is
		return domain.WorkloadKey{Namespace: p.Namespace, Kind: ref.Kind, Name: ref.Name}
	}
}

func controllerRef(refs []metav1.OwnerReference) *metav1.OwnerReference {
	for i := range refs {
		if refs[i].Controller != nil && *refs[i].Controller {
			return &refs[i]
		}
	}
	return nil
}

func nodeInfo(n *corev1.Node) domain.NodeInfo {
	info := domain.NodeInfo{
		Name:        n.Name,
		Allocatable: toResource(n.Status.Allocatable),
		Capacity:    toResource(n.Status.Capacity),
	}
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			info.Ready = c.Status == corev1.ConditionTrue
		}
	}
	for k := range n.Labels {
		if role, ok := strings.CutPrefix(k, "node-role.kubernetes.io/"); ok && role != "" {
			info.Roles = append(info.Roles, role)
		}
	}
	return info
}
