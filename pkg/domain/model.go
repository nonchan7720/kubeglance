package domain

import "time"

// PodKey identifies a pod.
type PodKey struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

func (k PodKey) String() string { return k.Namespace + "/" + k.Name }

// Workload kinds. A pod whose owner chain ends in something kubeglance does
// not group by (a bare pod, a static pod) belongs to a workload of kind
// KindPod named after itself, so every pod has exactly one workload.
const (
	KindDeployment  = "Deployment"
	KindStatefulSet = "StatefulSet"
	KindDaemonSet   = "DaemonSet"
	KindJob         = "Job"
	KindCronJob     = "CronJob"
	KindReplicaSet  = "ReplicaSet" // a ReplicaSet without a Deployment
	KindPod         = "Pod"
)

// WorkloadKey identifies the top-level controller of a set of pods.
type WorkloadKey struct {
	Namespace string `json:"namespace"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
}

func (k WorkloadKey) String() string { return k.Namespace + "/" + k.Kind + "/" + k.Name }

// PodInfo is what kubeglance knows about a pod besides its usage.
type PodInfo struct {
	Key      PodKey      `json:"key"`
	Node     string      `json:"node"`
	Workload WorkloadKey `json:"workload"`
	Requests Resource    `json:"requests"` // sum over containers
	Limits   Resource    `json:"limits"`   // sum over containers; 0 = at least one container has none
	Phase    string      `json:"phase"`
}

// NodeInfo is what kubeglance knows about a node besides its usage.
type NodeInfo struct {
	Name        string   `json:"name"`
	Allocatable Resource `json:"allocatable"`
	Capacity    Resource `json:"capacity"`
	Ready       bool     `json:"ready"`
	Roles       []string `json:"roles,omitempty"`
}

// Round is the result of one poll of metrics-server: every pod's and every
// node's usage at one instant. Pods or nodes missing from a round simply get
// no sample for it.
type Round struct {
	At    time.Time
	Pods  map[PodKey]Resource
	Nodes map[string]Resource
}
