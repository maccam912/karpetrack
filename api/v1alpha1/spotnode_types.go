package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SpotNodePhase represents the lifecycle phase of a SpotNode
type SpotNodePhase string

const (
	// SpotNodePhasePending indicates the node is waiting to be provisioned
	SpotNodePhasePending SpotNodePhase = "Pending"

	// SpotNodePhaseProvisioning indicates the node is being created
	SpotNodePhaseProvisioning SpotNodePhase = "Provisioning"

	// SpotNodePhaseRunning indicates the node is running and ready
	SpotNodePhaseRunning SpotNodePhase = "Running"

	// SpotNodePhaseDraining indicates the node is being drained before termination
	SpotNodePhaseDraining SpotNodePhase = "Draining"

	// SpotNodePhaseTerminating indicates the node is being terminated
	SpotNodePhaseTerminating SpotNodePhase = "Terminating"
)

// SpotNodeSpec defines the desired state of SpotNode
type SpotNodeSpec struct {
	// NodePoolRef references the SpotNodePool that created this node
	// +kubebuilder:validation:Required
	NodePoolRef string `json:"nodePoolRef"`

	// InstanceType is the Rackspace Spot instance type (e.g., gp.0.0, ch.1.0)
	// +kubebuilder:validation:Required
	InstanceType string `json:"instanceType"`

	// Region is the Rackspace Spot region (e.g., us-east-iad-1)
	// +kubebuilder:validation:Required
	Region string `json:"region"`

	// Resources defines the allocated resources for this node
	// +optional
	Resources SpotNodeResources `json:"resources,omitempty"`

	// BidPrice is the price bid for this spot instance
	// +optional
	BidPrice string `json:"bidPrice,omitempty"`
}

// SpotNodeResources defines the resources for a spot node
type SpotNodeResources struct {
	// CPU cores
	// +optional
	CPU *resource.Quantity `json:"cpu,omitempty"`

	// Memory
	// +optional
	Memory *resource.Quantity `json:"memory,omitempty"`

	// EphemeralStorage
	// +optional
	EphemeralStorage *resource.Quantity `json:"ephemeralStorage,omitempty"`

	// GPU count (if applicable)
	// +optional
	GPU *resource.Quantity `json:"gpu,omitempty"`
}

// SpotNodeStatus defines the observed state of SpotNode
type SpotNodeStatus struct {
	// ProviderID is the unique identifier from Rackspace Spot
	// +optional
	ProviderID string `json:"providerID,omitempty"`

	// NodeName is the name of the corresponding Kubernetes node
	// +optional
	NodeName string `json:"nodeName,omitempty"`

	// Phase represents the current lifecycle phase
	// +optional
	Phase SpotNodePhase `json:"phase,omitempty"`

	// PricePerHour is the current market price for this instance type
	// +optional
	PricePerHour string `json:"pricePerHour,omitempty"`

	// ProvisionedAt is when the node was provisioned
	// +optional
	ProvisionedAt *metav1.Time `json:"provisionedAt,omitempty"`

	// LastPriceCheck is when pricing was last verified
	// +optional
	LastPriceCheck *metav1.Time `json:"lastPriceCheck,omitempty"`

	// Conditions represent the current state of the node
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// AllocatedPods is the number of pods scheduled on this node
	// +optional
	AllocatedPods int32 `json:"allocatedPods,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=sn
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Node",type="string",JSONPath=".status.nodeName"
// +kubebuilder:printcolumn:name="Instance",type="string",JSONPath=".spec.instanceType"
// +kubebuilder:printcolumn:name="Region",type="string",JSONPath=".spec.region"
// +kubebuilder:printcolumn:name="Price",type="string",JSONPath=".status.pricePerHour"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// SpotNode is the Schema for the spotnodes API
type SpotNode struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SpotNodeSpec   `json:"spec,omitempty"`
	Status SpotNodeStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SpotNodeList contains a list of SpotNode
type SpotNodeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SpotNode `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SpotNode{}, &SpotNodeList{})
}
