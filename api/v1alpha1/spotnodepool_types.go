package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SpotNodePoolSpec defines the desired state of SpotNodePool
type SpotNodePoolSpec struct {
	// Regions to consider for provisioning (empty = all regions)
	// +optional
	Regions []string `json:"regions,omitempty"`

	// Categories of instance types to consider: gp, ch, mh, gpu
	// Empty means all categories except gpu
	// +optional
	Categories []string `json:"categories,omitempty"`

	// MaxPrice is the maximum hourly price willing to pay per node (0 = no limit)
	// +optional
	MaxPrice string `json:"maxPrice,omitempty"`

	// Requirements are node selector requirements that nodes must satisfy
	// +optional
	Requirements []corev1.NodeSelectorRequirement `json:"requirements,omitempty"`

	// Limits define maximum resources this pool can provision
	// +optional
	Limits Resources `json:"limits,omitempty"`

	// Disruption defines how nodes can be disrupted for optimization
	// +optional
	Disruption DisruptionSpec `json:"disruption,omitempty"`

	// Taints to apply to nodes provisioned by this pool
	// +optional
	Taints []corev1.Taint `json:"taints,omitempty"`

	// Labels to apply to nodes provisioned by this pool
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
}

// Resources defines resource limits
type Resources struct {
	// CPU is the maximum total CPU cores
	// +optional
	CPU *resource.Quantity `json:"cpu,omitempty"`

	// Memory is the maximum total memory
	// +optional
	Memory *resource.Quantity `json:"memory,omitempty"`

	// EphemeralStorage is the maximum total ephemeral storage
	// +optional
	EphemeralStorage *resource.Quantity `json:"ephemeralStorage,omitempty"`
}

// DisruptionSpec defines disruption behavior
type DisruptionSpec struct {
	// ConsolidationPolicy determines when to consolidate nodes
	// Valid values: WhenEmpty, WhenUnderutilized
	// +kubebuilder:validation:Enum=WhenEmpty;WhenUnderutilized
	// +optional
	ConsolidationPolicy string `json:"consolidationPolicy,omitempty"`

	// ConsolidateAfter is the duration to wait before consolidating
	// +optional
	ConsolidateAfter string `json:"consolidateAfter,omitempty"`

	// ExpireAfter is the duration after which nodes should be replaced
	// +optional
	ExpireAfter string `json:"expireAfter,omitempty"`
}

// SpotNodePoolStatus defines the observed state of SpotNodePool
type SpotNodePoolStatus struct {
	// Resources currently provisioned by this pool
	// +optional
	Resources Resources `json:"resources,omitempty"`

	// NodeCount is the number of nodes currently managed by this pool
	// +optional
	NodeCount int32 `json:"nodeCount,omitempty"`

	// Conditions represent the current state of the pool
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=snp
// +kubebuilder:printcolumn:name="Nodes",type="integer",JSONPath=".status.nodeCount"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// SpotNodePool is the Schema for the spotnodepools API
type SpotNodePool struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SpotNodePoolSpec   `json:"spec,omitempty"`
	Status SpotNodePoolStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SpotNodePoolList contains a list of SpotNodePool
type SpotNodePoolList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SpotNodePool `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SpotNodePool{}, &SpotNodePoolList{})
}
