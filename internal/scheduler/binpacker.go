package scheduler

import (
	"fmt"
	"sort"

	"k8s.io/apimachinery/pkg/api/resource"
)

// PackingResult represents a node configuration with assigned pods
type PackingResult struct {
	Node         NodeCapacity
	AssignedPods []PodRequirements
	TotalCost    float64
	Utilization  float64
}

// BinPacker implements bin packing algorithms for pod-to-node assignment
type BinPacker struct {
	// MinUtilization is the minimum acceptable utilization for a node
	MinUtilization float64

	// DaemonSetOverhead is the per-node resource overhead from DaemonSet pods.
	// When set, this overhead is subtracted from each node's capacity before
	// fitting regular pods, ensuring nodes aren't oversubscribed.
	DaemonSetOverhead *DaemonSetOverhead
}

// NewBinPacker creates a new bin packer
func NewBinPacker() *BinPacker {
	return &BinPacker{
		MinUtilization: 0.5, // 50% minimum utilization
	}
}

// NewBinPackerWithOverhead creates a new bin packer with daemonset overhead
func NewBinPackerWithOverhead(overhead *DaemonSetOverhead) *BinPacker {
	return &BinPacker{
		MinUtilization:    0.5,
		DaemonSetOverhead: overhead,
	}
}

// Pack finds the optimal node configuration for the given pods
// Uses First-Fit Decreasing with cost optimization
func (bp *BinPacker) Pack(pods []PodRequirements, nodeTypes []NodeCapacity) ([]PackingResult, error) {
	if len(pods) == 0 {
		return nil, nil
	}

	if len(nodeTypes) == 0 {
		return nil, fmt.Errorf("no node types available")
	}

	// Sort pods by resource requirements (descending) - largest pods first
	sortedPods := make([]PodRequirements, len(pods))
	copy(sortedPods, pods)
	sort.Slice(sortedPods, func(i, j int) bool {
		// Primary sort by CPU, secondary by memory
		cpuCmp := sortedPods[i].CPU.Cmp(sortedPods[j].CPU)
		if cpuCmp != 0 {
			return cpuCmp > 0
		}
		return sortedPods[i].Memory.Cmp(sortedPods[j].Memory) > 0
	})

	// Sort node types by cost efficiency (resources per dollar)
	sortedNodes := make([]NodeCapacity, len(nodeTypes))
	copy(sortedNodes, nodeTypes)
	sort.Slice(sortedNodes, func(i, j int) bool {
		// Calculate cost efficiency: (CPU + Memory/1Gi) / price
		effI := (float64(sortedNodes[i].CPU.MilliValue())/1000 +
			float64(sortedNodes[i].Memory.Value())/(1024*1024*1024)) /
			sortedNodes[i].PricePerHour
		effJ := (float64(sortedNodes[j].CPU.MilliValue())/1000 +
			float64(sortedNodes[j].Memory.Value())/(1024*1024*1024)) /
			sortedNodes[j].PricePerHour
		return effI > effJ
	})

	// Try multiple strategies and pick the cheapest
	strategies := []func([]PodRequirements, []NodeCapacity) []PackingResult{
		bp.packFirstFitDecreasing,
		bp.packBestFit,
		bp.packSingleLargeNode,
	}

	var bestResult []PackingResult
	var bestCost float64 = -1

	for _, strategy := range strategies {
		result := strategy(sortedPods, sortedNodes)
		if result == nil {
			continue
		}

		totalCost := calculateTotalCost(result)
		if bestCost < 0 || totalCost < bestCost {
			bestResult = result
			bestCost = totalCost
		}
	}

	if bestResult == nil {
		return nil, fmt.Errorf("unable to pack pods into available node types")
	}

	return bestResult, nil
}

// packFirstFitDecreasing uses FFD algorithm
func (bp *BinPacker) packFirstFitDecreasing(pods []PodRequirements, nodeTypes []NodeCapacity) []PackingResult {
	var results []PackingResult
	unassigned := make([]PodRequirements, len(pods))
	copy(unassigned, pods)

	for len(unassigned) > 0 {
		// Find the best node type for remaining pods
		bestNode, bestPods := bp.findBestNodeForPods(unassigned, nodeTypes)
		if bestNode == nil {
			return nil // Can't fit remaining pods
		}

		result := PackingResult{
			Node:         *bestNode,
			AssignedPods: bestPods,
			TotalCost:    bestNode.PricePerHour,
			Utilization:  bestNode.Utilization(bestPods),
		}
		results = append(results, result)

		// Remove assigned pods from unassigned list
		unassigned = removePods(unassigned, bestPods)
	}

	return results
}

// packBestFit tries to maximize utilization per node
func (bp *BinPacker) packBestFit(pods []PodRequirements, nodeTypes []NodeCapacity) []PackingResult {
	var results []PackingResult
	unassigned := make([]PodRequirements, len(pods))
	copy(unassigned, pods)

	for len(unassigned) > 0 {
		var bestNode *NodeCapacity
		var bestPods []PodRequirements
		var bestUtil float64

		for _, nodeType := range nodeTypes {
			fittingPods := bp.fitPodsToNode(unassigned, nodeType)
			if len(fittingPods) == 0 {
				continue
			}

			util := nodeType.Utilization(fittingPods)
			// Prefer higher utilization
			if bestNode == nil || util > bestUtil {
				bestNode = &nodeType
				bestPods = fittingPods
				bestUtil = util
			}
		}

		if bestNode == nil {
			return nil
		}

		result := PackingResult{
			Node:         *bestNode,
			AssignedPods: bestPods,
			TotalCost:    bestNode.PricePerHour,
			Utilization:  bestUtil,
		}
		results = append(results, result)

		unassigned = removePods(unassigned, bestPods)
	}

	return results
}

// packSingleLargeNode tries to fit all pods on one large node
func (bp *BinPacker) packSingleLargeNode(pods []PodRequirements, nodeTypes []NodeCapacity) []PackingResult {
	// Calculate total requirements
	var totalCPU, totalMem, totalStorage resource.Quantity
	for _, pod := range pods {
		totalCPU.Add(pod.CPU)
		totalMem.Add(pod.Memory)
		totalStorage.Add(pod.EphemeralStorage)
	}

	// Add DaemonSet overhead to required capacity
	if bp.DaemonSetOverhead != nil {
		totalCPU.Add(bp.DaemonSetOverhead.CPU)
		totalMem.Add(bp.DaemonSetOverhead.Memory)
		totalStorage.Add(bp.DaemonSetOverhead.EphemeralStorage)
	}

	// Find smallest node that fits all pods (including overhead)
	for _, nodeType := range nodeTypes {
		if nodeType.CPU.Cmp(totalCPU) >= 0 &&
			nodeType.Memory.Cmp(totalMem) >= 0 &&
			nodeType.EphemeralStorage.Cmp(totalStorage) >= 0 {
			return []PackingResult{{
				Node:         nodeType,
				AssignedPods: pods,
				TotalCost:    nodeType.PricePerHour,
				Utilization:  nodeType.Utilization(pods),
			}}
		}
	}

	return nil
}

// findBestNodeForPods finds the most cost-effective node for as many pods as possible
func (bp *BinPacker) findBestNodeForPods(pods []PodRequirements, nodeTypes []NodeCapacity) (*NodeCapacity, []PodRequirements) {
	var bestNode *NodeCapacity
	var bestPods []PodRequirements
	var bestEfficiency float64 // pods per dollar

	for _, nodeType := range nodeTypes {
		fittingPods := bp.fitPodsToNode(pods, nodeType)
		if len(fittingPods) == 0 {
			continue
		}

		efficiency := float64(len(fittingPods)) / nodeType.PricePerHour
		if bestNode == nil || efficiency > bestEfficiency {
			nodeCopy := nodeType
			bestNode = &nodeCopy
			bestPods = fittingPods
			bestEfficiency = efficiency
		}
	}

	return bestNode, bestPods
}

// fitPodsToNode returns as many pods as can fit on the given node type
func (bp *BinPacker) fitPodsToNode(pods []PodRequirements, node NodeCapacity) []PodRequirements {
	remaining := NodeCapacity{
		CPU:              node.CPU.DeepCopy(),
		Memory:           node.Memory.DeepCopy(),
		GPU:              node.GPU.DeepCopy(),
		EphemeralStorage: node.EphemeralStorage.DeepCopy(),
	}

	// Subtract DaemonSet overhead from available capacity first
	if bp.DaemonSetOverhead != nil {
		remaining.CPU.Sub(bp.DaemonSetOverhead.CPU)
		remaining.Memory.Sub(bp.DaemonSetOverhead.Memory)
		remaining.EphemeralStorage.Sub(bp.DaemonSetOverhead.EphemeralStorage)

		// If overhead exceeds node capacity, no pods can fit
		if remaining.CPU.Sign() < 0 || remaining.Memory.Sign() < 0 || remaining.EphemeralStorage.Sign() < 0 {
			return nil
		}
	}

	var fitting []PodRequirements
	for _, pod := range pods {
		if remaining.CPU.Cmp(pod.CPU) >= 0 &&
			remaining.Memory.Cmp(pod.Memory) >= 0 &&
			remaining.EphemeralStorage.Cmp(pod.EphemeralStorage) >= 0 &&
			(pod.GPU.IsZero() || remaining.GPU.Cmp(pod.GPU) >= 0) {
			fitting = append(fitting, pod)
			remaining.CPU.Sub(pod.CPU)
			remaining.Memory.Sub(pod.Memory)
			remaining.EphemeralStorage.Sub(pod.EphemeralStorage)
			if !pod.GPU.IsZero() {
				remaining.GPU.Sub(pod.GPU)
			}
		}
	}

	return fitting
}

// removePods removes assigned pods from the unassigned list
func removePods(unassigned, assigned []PodRequirements) []PodRequirements {
	assignedSet := make(map[string]struct{})
	for _, pod := range assigned {
		key := fmt.Sprintf("%s/%s", pod.Namespace, pod.Name)
		assignedSet[key] = struct{}{}
	}

	var remaining []PodRequirements
	for _, pod := range unassigned {
		key := fmt.Sprintf("%s/%s", pod.Namespace, pod.Name)
		if _, ok := assignedSet[key]; !ok {
			remaining = append(remaining, pod)
		}
	}

	return remaining
}

// calculateTotalCost sums the cost of all nodes in the result
func calculateTotalCost(results []PackingResult) float64 {
	var total float64
	for _, r := range results {
		total += r.TotalCost
	}
	return total
}
