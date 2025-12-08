package spot

import (
	"k8s.io/apimachinery/pkg/api/resource"
)

// PricingData represents the complete pricing data from Rackspace Spot
type PricingData struct {
	Regions map[string]RegionData `json:"regions"`
}

// RegionData contains pricing for a specific region
type RegionData struct {
	Generation    string                 `json:"generation"`
	ServerClasses map[string]ServerClass `json:"serverclasses"`
}

// ServerClass represents a single instance type with its pricing
type ServerClass struct {
	DisplayName  string `json:"display_name"`
	Category     string `json:"category"`
	Description  string `json:"description"`
	CPU          int    `json:"cpu"`
	Memory       int    `json:"memory"` // in GB
	MarketPrice  string `json:"market_price"`
	Percentile20 string `json:"20_percentile"`
	Percentile50 string `json:"50_percentile"`
	Percentile80 string `json:"80_percentile"`
}

// InstanceOption represents a potential instance to provision
type InstanceOption struct {
	Region       string
	InstanceType string
	DisplayName  string
	Category     string
	CPU          int
	MemoryGB     int
	PricePerHour float64
	Generation   string
}

// NodeSpec defines parameters for creating a new node
type NodeSpec struct {
	InstanceType string
	Region       string
	NodePoolName string
	Labels       map[string]string
	Taints       []Taint
	BidPrice     float64
}

// Taint represents a Kubernetes taint
type Taint struct {
	Key    string
	Value  string
	Effect string
}

// Node represents a provisioned node from Rackspace Spot
type Node struct {
	ID           string
	Name         string
	InstanceType string
	Region       string
	Status       string
	CreatedAt    string
	IPAddress    string
}

// NodePoolSpec defines parameters for a Rackspace Spot node pool
type NodePoolSpec struct {
	Name          string
	ServerClass   string
	BidPrice      float64
	DesiredCount  int
	MinCount      int
	MaxCount      int
	AutoscaleType string
}

// Requirements defines resource requirements for node selection
type Requirements struct {
	// Minimum CPU cores required
	MinCPU resource.Quantity

	// Minimum memory required
	MinMemory resource.Quantity

	// Allowed regions (empty = all)
	Regions []string

	// Allowed categories (empty = all except gpu)
	Categories []string

	// Maximum price per hour
	MaxPrice float64
}
