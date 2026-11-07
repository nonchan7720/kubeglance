package services

import (
	"cmp"
	"slices"
	"time"

	"github.com/nonchan7720/kubeglance/pkg/domain"
)

// Query answers the four levels (cluster, nodes, workloads, pods) over a
// window ending now. Every view carries the same Stats so the UI renders one
// row shape everywhere.
type Query struct {
	store *domain.Store
	now   func() time.Time
}

// NewQuery returns a Query over the store.
func NewQuery(store *domain.Store) *Query {
	return &Query{store: store, now: time.Now}
}

// WindowInfo is the window a view was computed over.
type WindowInfo struct {
	From     time.Time     `json:"from"`
	To       time.Time     `json:"to"`
	Duration time.Duration `json:"duration"`
	Step     time.Duration `json:"step"`
}

func (q *Query) window(d time.Duration) (domain.Window, WindowInfo) {
	if d <= 0 {
		d = 30 * time.Minute
	}
	if d > q.store.Retention() {
		d = q.store.Retention()
	}
	to := q.now()
	// the timeline, so a window spanning a change of metrics-server's
	// resolution expects each part's samples at its own step
	w := domain.Window{From: to.Add(-d), To: to, Step: q.store.Interval(), Timeline: q.store.Timeline()}
	return w, WindowInfo{From: w.From, To: w.To, Duration: d, Step: w.Step}
}

// ClusterView answers "how much headroom is left?".
type ClusterView struct {
	Window      WindowInfo      `json:"window"`
	Nodes       int             `json:"nodes"`
	ReadyNodes  int             `json:"readyNodes"`
	Pods        int             `json:"pods"`
	Workloads   int             `json:"workloads"`
	Capacity    domain.Resource `json:"capacity"`
	Allocatable domain.Resource `json:"allocatable"`
	Requests    domain.Resource `json:"requests"` // sum of live pods' requests
	Limits      domain.Resource `json:"limits"`
	Usage       domain.Stats    `json:"usage"` // sum of node usage over time
}

// Cluster computes the cluster view.
func (q *Query) Cluster(d time.Duration) ClusterView {
	w, info := q.window(d)
	v := ClusterView{Window: info}
	var nodeSeries [][]domain.Sample
	for _, n := range q.store.Nodes() {
		if q.store.NodeDeleted(n.Name) {
			continue
		}
		v.Nodes++
		if n.Ready {
			v.ReadyNodes++
		}
		v.Capacity = v.Capacity.Add(n.Capacity)
		v.Allocatable = v.Allocatable.Add(n.Allocatable)
		nodeSeries = append(nodeSeries, q.store.NodeSamples(n.Name, w.From))
	}
	workloads := map[domain.WorkloadKey]struct{}{}
	for _, p := range q.store.Pods() {
		if q.store.PodDeleted(p.Key) {
			continue
		}
		v.Pods++
		v.Requests = v.Requests.Add(p.Requests)
		v.Limits = v.Limits.Add(p.Limits)
		workloads[workloadOf(p)] = struct{}{}
	}
	v.Workloads = len(workloads)
	v.Usage = domain.ComputeStats(domain.SumByInstant(nodeSeries...), w)
	return v
}

// NodeView is one node: its own usage and what is scheduled on it.
type NodeView struct {
	domain.NodeInfo
	Pods        int             `json:"pods"`
	Requests    domain.Resource `json:"requests"`
	Limits      domain.Resource `json:"limits"`
	Usage       domain.Stats    `json:"usage"`
	CPURatio    float64         `json:"cpuRatio"`    // usage now / allocatable
	MemoryRatio float64         `json:"memoryRatio"` // usage now / allocatable
}

// NodesView answers "is the load spread evenly?".
type NodesView struct {
	Window    WindowInfo `json:"window"`
	Nodes     []NodeView `json:"nodes"`
	Imbalance struct {
		CPU    domain.ImbalanceStats `json:"cpu"`
		Memory domain.ImbalanceStats `json:"memory"`
	} `json:"imbalance"`
}

// Nodes computes the node view, sorted by CPU ratio descending.
func (q *Query) Nodes(d time.Duration) NodesView {
	w, info := q.window(d)
	v := NodesView{Window: info, Nodes: []NodeView{}}
	byNode := map[string]*NodeView{}
	for _, n := range q.store.Nodes() {
		if q.store.NodeDeleted(n.Name) {
			continue
		}
		nv := &NodeView{NodeInfo: n}
		nv.Usage = domain.ComputeStats(q.store.NodeSamples(n.Name, w.From), w)
		nv.CPURatio = domain.Ratio(nv.Usage.Now.CPU, n.Allocatable.CPU)
		nv.MemoryRatio = domain.Ratio(nv.Usage.Now.Memory, n.Allocatable.Memory)
		byNode[n.Name] = nv
	}
	for _, p := range q.store.Pods() {
		if q.store.PodDeleted(p.Key) {
			continue
		}
		if nv, ok := byNode[p.Node]; ok {
			nv.Pods++
			nv.Requests = nv.Requests.Add(p.Requests)
			nv.Limits = nv.Limits.Add(p.Limits)
		}
	}
	var cpu, mem []float64
	for _, nv := range byNode {
		v.Nodes = append(v.Nodes, *nv)
		cpu = append(cpu, nv.CPURatio)
		mem = append(mem, nv.MemoryRatio)
	}
	slices.SortFunc(v.Nodes, func(a, b NodeView) int {
		return cmp.Or(cmp.Compare(b.CPURatio, a.CPURatio), cmp.Compare(a.Name, b.Name))
	})
	v.Imbalance.CPU = domain.Imbalance(cpu)
	v.Imbalance.Memory = domain.Imbalance(mem)
	return v
}

// WorkloadView is one Deployment / StatefulSet / DaemonSet / ...: the sum of
// its pods, and the per-pod average that scaling decisions need.
type WorkloadView struct {
	domain.WorkloadKey
	Pods     int             `json:"pods"`
	Requests domain.Resource `json:"requests"`
	Limits   domain.Resource `json:"limits"`
	Usage    domain.Stats    `json:"usage"`  // sum over pods, over time
	PerPod   domain.Resource `json:"perPod"` // Usage.Now / Pods
	// RequestRatio is usage now / requests (0 when no request is set): above 1
	// the workload is under-requested; well below, over-requested.
	RequestRatio struct {
		CPU    float64 `json:"cpu"`
		Memory float64 `json:"memory"`
	} `json:"requestRatio"`
}

// WorkloadsView answers "which service is under load, should it scale?".
type WorkloadsView struct {
	Window    WindowInfo     `json:"window"`
	Workloads []WorkloadView `json:"workloads"`
}

// Workloads computes the workload view, optionally limited to a namespace
// (empty = all), sorted by CPU usage now descending.
func (q *Query) Workloads(d time.Duration, namespace string) WorkloadsView {
	w, info := q.window(d)
	v := WorkloadsView{Window: info, Workloads: []WorkloadView{}}
	type acc struct {
		view   WorkloadView
		series [][]domain.Sample
	}
	byKey := map[domain.WorkloadKey]*acc{}
	for _, p := range q.store.Pods() {
		if namespace != "" && p.Key.Namespace != namespace {
			continue
		}
		key := workloadOf(p)
		a, ok := byKey[key]
		if !ok {
			a = &acc{view: WorkloadView{WorkloadKey: key}}
			byKey[key] = a
		}
		// a deleted pod still contributes its history to the sum, so a
		// rollout does not make the workload's past vanish
		a.series = append(a.series, q.store.PodSamples(p.Key, w.From))
		if q.store.PodDeleted(p.Key) {
			continue
		}
		a.view.Pods++
		a.view.Requests = a.view.Requests.Add(p.Requests)
		a.view.Limits = a.view.Limits.Add(p.Limits)
	}
	for _, a := range byKey {
		a.view.Usage = domain.ComputeStats(domain.SumByInstant(a.series...), w)
		if a.view.Pods > 0 {
			a.view.PerPod = domain.Resource{
				CPU:    a.view.Usage.Now.CPU / int64(a.view.Pods),
				Memory: a.view.Usage.Now.Memory / int64(a.view.Pods),
			}
		}
		a.view.RequestRatio.CPU = domain.Ratio(a.view.Usage.Now.CPU, a.view.Requests.CPU)
		a.view.RequestRatio.Memory = domain.Ratio(a.view.Usage.Now.Memory, a.view.Requests.Memory)
		if a.view.Pods == 0 && a.view.Usage.Samples == 0 {
			continue // fully gone and no history in the window
		}
		v.Workloads = append(v.Workloads, a.view)
	}
	slices.SortFunc(v.Workloads, func(a, b WorkloadView) int {
		return cmp.Or(cmp.Compare(b.Usage.Now.CPU, a.Usage.Now.CPU), cmp.Compare(a.String(), b.String()))
	})
	return v
}

// PodView is one pod.
type PodView struct {
	domain.PodInfo
	Deleted      bool         `json:"deleted"`
	Usage        domain.Stats `json:"usage"`
	LimitRatio   ratio        `json:"limitRatio"`   // usage now / limits (0 when unlimited)
	RequestRatio ratio        `json:"requestRatio"` // usage now / requests
}

type ratio struct {
	CPU    float64 `json:"cpu"`
	Memory float64 `json:"memory"`
}

// PodsView answers "which single pod is hot?".
type PodsView struct {
	Window WindowInfo `json:"window"`
	Pods   []PodView  `json:"pods"`
}

// PodFilter narrows the pod view. Empty fields match everything.
type PodFilter struct {
	Namespace string
	Node      string
	Workload  *domain.WorkloadKey
}

// Pods computes the pod view, sorted by CPU usage now descending. Deleted
// pods are included while they are in history, flagged Deleted.
func (q *Query) Pods(d time.Duration, f PodFilter) PodsView {
	w, info := q.window(d)
	v := PodsView{Window: info, Pods: []PodView{}}
	for _, p := range q.store.Pods() {
		if f.Namespace != "" && p.Key.Namespace != f.Namespace {
			continue
		}
		if f.Node != "" && p.Node != f.Node {
			continue
		}
		if f.Workload != nil && workloadOf(p) != *f.Workload {
			continue
		}
		pv := PodView{PodInfo: p, Deleted: q.store.PodDeleted(p.Key)}
		pv.Usage = domain.ComputeStats(q.store.PodSamples(p.Key, w.From), w)
		if pv.Deleted && pv.Usage.Samples == 0 {
			continue
		}
		pv.LimitRatio = ratio{domain.Ratio(pv.Usage.Now.CPU, p.Limits.CPU), domain.Ratio(pv.Usage.Now.Memory, p.Limits.Memory)}
		pv.RequestRatio = ratio{domain.Ratio(pv.Usage.Now.CPU, p.Requests.CPU), domain.Ratio(pv.Usage.Now.Memory, p.Requests.Memory)}
		v.Pods = append(v.Pods, pv)
	}
	slices.SortFunc(v.Pods, func(a, b PodView) int {
		return cmp.Or(cmp.Compare(b.Usage.Now.CPU, a.Usage.Now.CPU), cmp.Compare(a.Key.String(), b.Key.String()))
	})
	return v
}

// workloadOf is the pod's workload; a pod without a resolved controller is a
// workload of its own.
func workloadOf(p domain.PodInfo) domain.WorkloadKey {
	if p.Workload.Kind == "" {
		return domain.WorkloadKey{Namespace: p.Key.Namespace, Kind: domain.KindPod, Name: p.Key.Name}
	}
	return p.Workload
}
