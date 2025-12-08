package scheduler

import (
	"context"
	"fmt"
	"sort"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/karpetrack/karpetrack/internal/spot"
)

// CostOptimizer finds the cheapest node configurations for pending pods
type CostOptimizer struct {
	pricing   *spot.PricingProvider
	binPacker *BinPacker
}

// NewCostOptimizer creates a new cost optimizer
func NewCostOptimizer(pricing *spot.PricingProvider) *CostOptimizer {
	return &CostOptimizer{
		pricing:   pricing,
		binPacker: NewBinPacker(),
	}
}

// OptimizationConstraints defines constraints for node selection
type OptimizationConstraints struct {
	// Allowed regions (empty = all)
	Regions []string

	// Allowed categories (empty = all except gpu)
	Categories []string

	// Maximum price per hour per node
	MaxPricePerNode float64

	// Maximum total hourly cost
	MaxTotalCost float64
}

// OptimizationResult contains the recommended node configuration
type OptimizationResult struct {
	Nodes       []NodeRecommendation
	TotalCost   float64
	TotalCPU    resource.Quantity
	TotalMemory resource.Quantity
}

// NodeRecommendation represents a single node to provision
type NodeRecommendation struct {
	Region       string
	InstanceType string
	Category     string
	CPU          resource.Quantity
	Memory       resource.Quantity
	PricePerHour float64
	PodCount     int
	Pods         []string // Pod names assigned to this node
}

// FindOptimalConfiguration finds the cheapest node configuration for pending pods
func (co *CostOptimizer) FindOptimalConfiguration(
	ctx context.Context,
	pods []PodRequirements,
	constraints OptimizationConstraints,
) (*OptimizationResult, error) {
	if len(pods) == 0 {
		return &OptimizationResult{}, nil
	}

	// Get available instance types with current pricing
	nodeTypes, err := co.getAvailableNodeTypes(ctx, constraints)
	if err != nil {
		return nil, fmt.Errorf("getting node types: %w", err)
	}

	if len(nodeTypes) == 0 {
		return nil, fmt.Errorf("no node types available matching constraints")
	}

	// Run bin packing to find optimal configuration
	packingResults, err := co.binPacker.Pack(pods, nodeTypes)
	if err != nil {
		return nil, fmt.Errorf("bin packing: %w", err)
	}

	// Convert to optimization result
	result := &OptimizationResult{
		Nodes: make([]NodeRecommendation, 0, len(packingResults)),
	}

	for _, pr := range packingResults {
		podNames := make([]string, 0, len(pr.AssignedPods))
		for _, pod := range pr.AssignedPods {
			podNames = append(podNames, fmt.Sprintf("%s/%s", pod.Namespace, pod.Name))
		}

		node := NodeRecommendation{
			Region:       pr.Node.Region,
			InstanceType: pr.Node.InstanceType,
			Category:     pr.Node.Category,
			CPU:          pr.Node.CPU,
			Memory:       pr.Node.Memory,
			PricePerHour: pr.Node.PricePerHour,
			PodCount:     len(pr.AssignedPods),
			Pods:         podNames,
		}

		result.Nodes = append(result.Nodes, node)
		result.TotalCost += pr.Node.PricePerHour
		result.TotalCPU.Add(pr.Node.CPU)
		result.TotalMemory.Add(pr.Node.Memory)
	}

	// Validate against max total cost constraint
	if constraints.MaxTotalCost > 0 && result.TotalCost > constraints.MaxTotalCost {
		return nil, fmt.Errorf("optimal configuration exceeds max total cost: %.4f > %.4f",
			result.TotalCost, constraints.MaxTotalCost)
	}

	return result, nil
}

// getAvailableNodeTypes fetches and filters available instance types
func (co *CostOptimizer) getAvailableNodeTypes(
	ctx context.Context,
	constraints OptimizationConstraints,
) ([]NodeCapacity, error) {
	reqs := spot.Requirements{
		Regions:    constraints.Regions,
		Categories: constraints.Categories,
		MaxPrice:   constraints.MaxPricePerNode,
	}

	options, err := co.pricing.GetCheapestInstances(ctx, reqs)
	if err != nil {
		return nil, err
	}

	nodeTypes := make([]NodeCapacity, 0, len(options))
	for _, opt := range options {
		nodeTypes = append(nodeTypes, NodeCapacity{
			Region:       opt.Region,
			InstanceType: opt.InstanceType,
			Category:     opt.Category,
			CPU:          resource.MustParse(fmt.Sprintf("%d", opt.CPU)),
			Memory:       resource.MustParse(fmt.Sprintf("%dGi", opt.MemoryGB)),
			PricePerHour: opt.PricePerHour,
		})
	}

	return nodeTypes, nil
}

// FindCheaperConfiguration checks if a cheaper configuration exists for current nodes
func (co *CostOptimizer) FindCheaperConfiguration(
	ctx context.Context,
	currentNodes []NodeRecommendation,
	pods []PodRequirements,
	constraints OptimizationConstraints,
	savingsThreshold float64, // e.g., 0.20 for 20% savings
) (*OptimizationResult, float64, error) {
	// Calculate current total cost
	var currentCost float64
	for _, node := range currentNodes {
		currentCost += node.PricePerHour
	}

	// Find optimal configuration with current constraints
	optimal, err := co.FindOptimalConfiguration(ctx, pods, constraints)
	if err != nil {
		return nil, 0, err
	}

	// Calculate savings
	savings := (currentCost - optimal.TotalCost) / currentCost

	if savings >= savingsThreshold {
		return optimal, savings, nil
	}

	return nil, savings, nil // No significant savings available
}

// RankNodesByEfficiency returns nodes sorted by cost efficiency for given requirements
func (co *CostOptimizer) RankNodesByEfficiency(
	ctx context.Context,
	minCPU, minMemoryGB int,
	constraints OptimizationConstraints,
) ([]spot.InstanceOption, error) {
	reqs := spot.Requirements{
		MinCPU:     resource.MustParse(fmt.Sprintf("%d", minCPU)),
		MinMemory:  resource.MustParse(fmt.Sprintf("%dGi", minMemoryGB)),
		Regions:    constraints.Regions,
		Categories: constraints.Categories,
		MaxPrice:   constraints.MaxPricePerNode,
	}

	options, err := co.pricing.GetCheapestInstances(ctx, reqs)
	if err != nil {
		return nil, err
	}

	// Sort by efficiency (resources per dollar)
	sort.Slice(options, func(i, j int) bool {
		effI := (float64(options[i].CPU) + float64(options[i].MemoryGB)/4) / options[i].PricePerHour
		effJ := (float64(options[j].CPU) + float64(options[j].MemoryGB)/4) / options[j].PricePerHour
		return effI > effJ
	})

	return options, nil
}

// EstimateCost estimates the hourly cost for a set of pods
func (co *CostOptimizer) EstimateCost(
	ctx context.Context,
	pods []PodRequirements,
	constraints OptimizationConstraints,
) (float64, error) {
	result, err := co.FindOptimalConfiguration(ctx, pods, constraints)
	if err != nil {
		return 0, err
	}
	return result.TotalCost, nil
}
