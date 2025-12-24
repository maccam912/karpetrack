package spot

import (
	"encoding/json"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
)

// DefaultEphemeralStorageGB is the fallback storage capacity for nodes (40GB)
const DefaultEphemeralStorageGB = 40

// FlexInt is an int that can be unmarshalled from either a string or int JSON value
type FlexInt int

// UnmarshalJSON implements custom JSON unmarshalling for FlexInt
func (f *FlexInt) UnmarshalJSON(data []byte) error {
	// Try int first
	var intVal int
	if err := json.Unmarshal(data, &intVal); err == nil {
		*f = FlexInt(intVal)
		return nil
	}

	// Try string
	var strVal string
	if err := json.Unmarshal(data, &strVal); err == nil {
		// Strip common suffixes like "GB"
		strVal = strings.TrimSuffix(strVal, "GB")
		strVal = strings.TrimSpace(strVal)

		// Try parsing as float first (handles "3.75", "7.5", etc.)
		if floatVal, err := strconv.ParseFloat(strVal, 64); err == nil {
			*f = FlexInt(int(floatVal))
			return nil
		}

		// Fall back to int parsing
		if parsed, err := strconv.Atoi(strVal); err == nil {
			*f = FlexInt(parsed)
			return nil
		}
	}

	return nil
}

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
	DisplayName  string  `json:"display_name"`
	Category     string  `json:"category"`
	Description  string  `json:"description"`
	CPU          FlexInt `json:"cpu"`
	Memory       FlexInt `json:"memory"`             // in GB
	Storage      FlexInt `json:"storage,omitempty"`   // in GB
	MarketPrice  string  `json:"market_price"`
	Percentile20 float64 `json:"20_percentile"`
	Percentile50 float64 `json:"50_percentile"`
	Percentile80 float64 `json:"80_percentile"`
}

// InstanceOption represents a potential instance to provision
type InstanceOption struct {
	Region           string
	InstanceType     string
	DisplayName      string
	Category         string
	CPU              int
	MemoryGB         int
	EphemeralStorage int
	PricePerHour     float64
	Generation       string
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
