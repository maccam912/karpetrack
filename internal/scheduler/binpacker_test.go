package scheduler

import (
	"fmt"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
)

// Test helpers

func makePodReqs(name string, cpuMillis int64, memMi int64) PodRequirements {
	return PodRequirements{
		Name:      name,
		Namespace: "default",
		CPU:       *resource.NewMilliQuantity(cpuMillis, resource.DecimalSI),
		Memory:    *resource.NewQuantity(memMi*1024*1024, resource.BinarySI),
	}
}

func makePodReqsWithStorage(name string, cpuMillis int64, memMi int64, storageGi int64) PodRequirements {
	reqs := makePodReqs(name, cpuMillis, memMi)
	reqs.EphemeralStorage = *resource.NewQuantity(storageGi*1024*1024*1024, resource.BinarySI)
	return reqs
}

func makeNodeCap(region, instanceType string, cpu int, memGi int, price float64) NodeCapacity {
	return NodeCapacity{
		Region:           region,
		InstanceType:     instanceType,
		CPU:              *resource.NewQuantity(int64(cpu), resource.DecimalSI),
		Memory:           *resource.NewQuantity(int64(memGi)*1024*1024*1024, resource.BinarySI),
		EphemeralStorage: *resource.NewQuantity(40*1024*1024*1024, resource.BinarySI), // Default 40GB
		PricePerHour:     price,
	}
}

func makeNodeCapWithStorage(region, instanceType string, cpu int, memGi int, storageGi int, price float64) NodeCapacity {
	nc := makeNodeCap(region, instanceType, cpu, memGi, price)
	nc.EphemeralStorage = *resource.NewQuantity(int64(storageGi)*1024*1024*1024, resource.BinarySI)
	return nc
}


func makeDaemonSetOverhead(cpuMillis int64, memMi int64) *DaemonSetOverhead {
	return &DaemonSetOverhead{
		CPU:    *resource.NewMilliQuantity(cpuMillis, resource.DecimalSI),
		Memory: *resource.NewQuantity(memMi*1024*1024, resource.BinarySI),
	}
}

func makeDaemonSetOverheadWithStorage(cpuMillis int64, memMi int64, storageGi int64) *DaemonSetOverhead {
	return &DaemonSetOverhead{
		CPU:              *resource.NewMilliQuantity(cpuMillis, resource.DecimalSI),
		Memory:           *resource.NewQuantity(memMi*1024*1024, resource.BinarySI),
		EphemeralStorage: *resource.NewQuantity(storageGi*1024*1024*1024, resource.BinarySI),
	}
}

// Basic BinPacker tests

func TestBinPacker_Pack_EmptyPods(t *testing.T) {
	bp := NewBinPacker()
	nodeTypes := []NodeCapacity{
		makeNodeCap("region-1", "small", 4, 8, 0.01),
	}

	results, err := bp.Pack(nil, nodeTypes)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if results != nil {
		t.Errorf("Expected nil results for empty pods, got %v", results)
	}
}

func TestBinPacker_Pack_EmptyNodeTypes(t *testing.T) {
	bp := NewBinPacker()
	pods := []PodRequirements{
		makePodReqs("pod-1", 1000, 1024),
	}

	_, err := bp.Pack(pods, nil)
	if err == nil {
		t.Fatal("Expected error for empty node types")
	}
}

func TestBinPacker_Pack_SinglePodSingleNode(t *testing.T) {
	bp := NewBinPacker()
	pods := []PodRequirements{
		makePodReqs("pod-1", 1000, 1024), // 1 CPU, 1Gi
	}
	nodeTypes := []NodeCapacity{
		makeNodeCap("region-1", "small", 4, 8, 0.01),
	}

	results, err := bp.Pack(pods, nodeTypes)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}
	if len(results[0].AssignedPods) != 1 {
		t.Errorf("Expected 1 assigned pod, got %d", len(results[0].AssignedPods))
	}
}

func TestBinPacker_Pack_MultiplePodsOneNode(t *testing.T) {
	bp := NewBinPacker()
	pods := []PodRequirements{
		makePodReqs("pod-1", 1000, 1024), // 1 CPU, 1Gi
		makePodReqs("pod-2", 1000, 1024), // 1 CPU, 1Gi
		makePodReqs("pod-3", 1000, 1024), // 1 CPU, 1Gi
	}
	nodeTypes := []NodeCapacity{
		makeNodeCap("region-1", "medium", 8, 16, 0.02),
	}

	results, err := bp.Pack(pods, nodeTypes)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}
	if len(results[0].AssignedPods) != 3 {
		t.Errorf("Expected 3 assigned pods, got %d", len(results[0].AssignedPods))
	}
}

func TestBinPacker_Pack_MultipleNodes(t *testing.T) {
	bp := NewBinPacker()
	// Each pod needs 3 CPU, 4Gi - won't fit more than 2 on a 8 CPU node
	pods := []PodRequirements{
		makePodReqs("pod-1", 3000, 4096),
		makePodReqs("pod-2", 3000, 4096),
		makePodReqs("pod-3", 3000, 4096),
	}
	nodeTypes := []NodeCapacity{
		makeNodeCap("region-1", "medium", 8, 16, 0.02),
	}

	results, err := bp.Pack(pods, nodeTypes)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if len(results) < 2 {
		t.Fatalf("Expected at least 2 nodes, got %d", len(results))
	}

	// Verify all pods are assigned
	totalPods := 0
	for _, r := range results {
		totalPods += len(r.AssignedPods)
	}
	if totalPods != 3 {
		t.Errorf("Expected 3 total assigned pods, got %d", totalPods)
	}
}

func TestBinPacker_Pack_SelectsCheapestNode(t *testing.T) {
	bp := NewBinPacker()
	pods := []PodRequirements{
		makePodReqs("pod-1", 1000, 1024),
	}
	// Both nodes can fit the pod, but small is cheaper
	nodeTypes := []NodeCapacity{
		makeNodeCap("region-1", "large", 16, 32, 0.10),
		makeNodeCap("region-1", "small", 4, 8, 0.01),
	}

	results, err := bp.Pack(pods, nodeTypes)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	// The packer should select the more cost-efficient option
	// Cost efficiency = resources per dollar
	// small: (4 + 8) / 0.01 = 1200
	// large: (16 + 32) / 0.10 = 480
	// So small should be selected
	t.Logf("Selected node: %s at $%.4f/hr", results[0].Node.InstanceType, results[0].Node.PricePerHour)
}

func TestBinPacker_Pack_PodTooLarge(t *testing.T) {
	bp := NewBinPacker()
	// Pod requires more resources than any available node
	pods := []PodRequirements{
		makePodReqs("large-pod", 100000, 1024*1024), // 100 CPU, 1Ti
	}
	nodeTypes := []NodeCapacity{
		makeNodeCap("region-1", "small", 4, 8, 0.01),
		makeNodeCap("region-1", "medium", 8, 16, 0.02),
	}

	_, err := bp.Pack(pods, nodeTypes)
	if err == nil {
		t.Fatal("Expected error for pod too large to fit")
	}
	t.Logf("Got expected error: %v", err)
}

// Daemonset overhead tests

func TestBinPacker_WithOverhead_PodsFit(t *testing.T) {
	// Node: 8 CPU, 16Gi
	// Overhead: 1 CPU, 2Gi
	// Available: 7 CPU, 14Gi
	// Pod: 7 CPU, 14Gi -> should fit
	overhead := makeDaemonSetOverhead(1000, 2*1024) // 1 CPU, 2Gi
	bp := NewBinPackerWithOverhead(overhead)

	pods := []PodRequirements{
		makePodReqs("pod-1", 7000, 14*1024), // 7 CPU, 14Gi
	}
	nodeTypes := []NodeCapacity{
		makeNodeCap("region-1", "medium", 8, 16, 0.02),
	}

	results, err := bp.Pack(pods, nodeTypes)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}
	if len(results[0].AssignedPods) != 1 {
		t.Errorf("Expected pod to fit, got %d assigned", len(results[0].AssignedPods))
	}
	t.Logf("Pod successfully placed with overhead accounting")
}

func TestBinPacker_WithOverhead_PodsNoFit(t *testing.T) {
	// Node: 4 CPU, 8Gi
	// Overhead: 1 CPU, 2Gi
	// Available: 3 CPU, 6Gi
	// Pod: 4 CPU, 8Gi -> should NOT fit
	overhead := makeDaemonSetOverhead(1000, 2*1024)
	bp := NewBinPackerWithOverhead(overhead)

	pods := []PodRequirements{
		makePodReqs("pod-1", 4000, 8*1024), // 4 CPU, 8Gi
	}
	nodeTypes := []NodeCapacity{
		makeNodeCap("region-1", "small", 4, 8, 0.01),
	}

	_, err := bp.Pack(pods, nodeTypes)
	if err == nil {
		t.Fatal("Expected error when pod doesn't fit after overhead")
	}
	t.Logf("Got expected error: %v", err)
}

func TestBinPacker_WithOverhead_NeedsLargerNode(t *testing.T) {
	// Small node: 4 CPU, 8Gi
	// Large node: 8 CPU, 16Gi
	// Overhead: 1 CPU, 2Gi
	// Pod: 4 CPU, 8Gi
	// Without overhead: fits on small
	// With overhead: needs large (4+1=5 CPU > 4, needs 8 CPU node)
	overhead := makeDaemonSetOverhead(1000, 2*1024)
	bp := NewBinPackerWithOverhead(overhead)

	pods := []PodRequirements{
		makePodReqs("pod-1", 4000, 8*1024),
	}
	nodeTypes := []NodeCapacity{
		makeNodeCap("region-1", "small", 4, 8, 0.01),
		makeNodeCap("region-1", "medium", 8, 16, 0.02),
	}

	results, err := bp.Pack(pods, nodeTypes)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	// Should select medium node since small doesn't fit with overhead
	if results[0].Node.InstanceType != "medium" {
		t.Errorf("Expected medium node, got %s", results[0].Node.InstanceType)
	}
	t.Logf("Selected node: %s (overhead forced larger node)", results[0].Node.InstanceType)
}

func TestBinPacker_WithOverhead_MultipleNodes(t *testing.T) {
	// Node: 8 CPU, 16Gi
	// Overhead: 1 CPU, 2Gi per node
	// Available per node: 7 CPU, 14Gi
	// Pods: 4 x (2 CPU, 4Gi) = 8 CPU, 16Gi total
	// With overhead: can fit 3 pods per node (6 CPU < 7 CPU), need 2 nodes
	overhead := makeDaemonSetOverhead(1000, 2*1024)
	bp := NewBinPackerWithOverhead(overhead)

	pods := []PodRequirements{
		makePodReqs("pod-1", 2000, 4*1024),
		makePodReqs("pod-2", 2000, 4*1024),
		makePodReqs("pod-3", 2000, 4*1024),
		makePodReqs("pod-4", 2000, 4*1024),
	}
	nodeTypes := []NodeCapacity{
		makeNodeCap("region-1", "medium", 8, 16, 0.02),
	}

	results, err := bp.Pack(pods, nodeTypes)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Should need at least 2 nodes
	if len(results) < 2 {
		t.Errorf("Expected at least 2 nodes with overhead, got %d", len(results))
	}

	totalPods := 0
	for i, r := range results {
		totalPods += len(r.AssignedPods)
		t.Logf("Node %d: %d pods assigned", i+1, len(r.AssignedPods))
	}
	if totalPods != 4 {
		t.Errorf("Expected 4 total pods, got %d", totalPods)
	}
}

func TestBinPacker_ZeroOverhead(t *testing.T) {
	// Verify backward compatibility: nil overhead = current behavior
	bpNil := NewBinPacker()
	bpZero := NewBinPackerWithOverhead(&DaemonSetOverhead{})

	pods := []PodRequirements{
		makePodReqs("pod-1", 2000, 4*1024),
		makePodReqs("pod-2", 2000, 4*1024),
	}
	nodeTypes := []NodeCapacity{
		makeNodeCap("region-1", "medium", 8, 16, 0.02),
	}

	resultsNil, errNil := bpNil.Pack(pods, nodeTypes)
	resultsZero, errZero := bpZero.Pack(pods, nodeTypes)

	if errNil != nil || errZero != nil {
		t.Fatalf("Unexpected errors: nil=%v, zero=%v", errNil, errZero)
	}

	if len(resultsNil) != len(resultsZero) {
		t.Errorf("Different number of nodes: nil=%d, zero=%d", len(resultsNil), len(resultsZero))
	}

	// Both should place all pods on 1 node
	if len(resultsNil) != 1 || len(resultsNil[0].AssignedPods) != 2 {
		t.Errorf("Expected 1 node with 2 pods, got %d nodes", len(resultsNil))
	}
}

func TestBinPacker_OverheadExceedsNodeCapacity(t *testing.T) {
	// Node: 4 CPU, 8Gi
	// Overhead: 8 CPU, 16Gi (more than node capacity!)
	// Should not be able to fit any pods
	overhead := makeDaemonSetOverhead(8000, 16*1024)
	bp := NewBinPackerWithOverhead(overhead)

	pods := []PodRequirements{
		makePodReqs("pod-1", 100, 100), // Tiny pod
	}
	nodeTypes := []NodeCapacity{
		makeNodeCap("region-1", "small", 4, 8, 0.01),
	}

	_, err := bp.Pack(pods, nodeTypes)
	if err == nil {
		t.Fatal("Expected error when overhead exceeds all node capacities")
	}
	t.Logf("Got expected error: %v", err)
}

// fitPodsToNode tests

func TestFitPodsToNode_Basic(t *testing.T) {
	bp := NewBinPacker()
	node := makeNodeCap("region-1", "medium", 8, 16, 0.02)
	pods := []PodRequirements{
		makePodReqs("pod-1", 2000, 4*1024),
		makePodReqs("pod-2", 2000, 4*1024),
		makePodReqs("pod-3", 2000, 4*1024),
		makePodReqs("pod-4", 2000, 4*1024),
		makePodReqs("pod-5", 2000, 4*1024), // Won't fit (total 10 CPU > 8)
	}

	fitting := bp.fitPodsToNode(pods, node)

	// Should fit 4 pods (8 CPU)
	if len(fitting) != 4 {
		t.Errorf("Expected 4 pods to fit, got %d", len(fitting))
	}
}

func TestFitPodsToNode_WithOverhead(t *testing.T) {
	overhead := makeDaemonSetOverhead(2000, 4*1024) // 2 CPU, 4Gi
	bp := NewBinPackerWithOverhead(overhead)

	node := makeNodeCap("region-1", "medium", 8, 16, 0.02)
	pods := []PodRequirements{
		makePodReqs("pod-1", 2000, 4*1024),
		makePodReqs("pod-2", 2000, 4*1024),
		makePodReqs("pod-3", 2000, 4*1024),
		makePodReqs("pod-4", 2000, 4*1024), // Won't fit (8 CPU pods + 2 overhead > 8 CPU node)
	}

	fitting := bp.fitPodsToNode(pods, node)

	// Node: 8 CPU, 16Gi
	// Overhead: 2 CPU, 4Gi
	// Available: 6 CPU, 12Gi
	// Pods: 2 CPU, 4Gi each -> 3 pods fit (6 CPU, 12Gi)
	if len(fitting) != 3 {
		t.Errorf("Expected 3 pods to fit with overhead, got %d", len(fitting))
	}
}

// Utilization tests

func TestBinPacker_StorageConstraint(t *testing.T) {
	bp := NewBinPacker()

	// Node: 4 CPU, 8Gi, 40Gi Storage
	nodeType := makeNodeCapWithStorage("region-1", "medium", 4, 8, 40, 0.02)

	// Pod: 1 CPU, 2Gi, 50Gi Storage (Exceeds node storage)
	pod := makePodReqsWithStorage("large-storage-pod", 1000, 2048, 50)

	fitting := bp.fitPodsToNode([]PodRequirements{pod}, nodeType)

	if len(fitting) != 0 {
		t.Errorf("Expected pod to NOT fit due to storage constraint, but it did")
	}

	// Pod: 1 CPU, 2Gi, 30Gi Storage (Fits)
	pod2 := makePodReqsWithStorage("small-storage-pod", 1000, 2048, 30)
	fitting2 := bp.fitPodsToNode([]PodRequirements{pod2}, nodeType)

	if len(fitting2) != 1 {
		t.Errorf("Expected pod to fit, but it didn't")
	}
}

func TestNodeCapacity_Utilization(t *testing.T) {
	node := makeNodeCapWithStorage("region-1", "medium", 8, 16, 40, 0.02)

	tests := []struct {
		name         string
		pods         []PodRequirements
		expectedUtil float64
	}{
		{
			name:         "no pods",
			pods:         nil,
			expectedUtil: 0.0,
		},
		{
			name: "half utilization",
			pods: []PodRequirements{
				makePodReqsWithStorage("pod-1", 4000, 8*1024, 20), // 4 CPU, 8Gi, 20Gi = 50%
			},
			expectedUtil: 0.5,
		},
		{
			name: "full utilization",
			pods: []PodRequirements{
				makePodReqsWithStorage("pod-1", 8000, 16*1024, 40), // 8 CPU, 16Gi, 40Gi = 100%
			},
			expectedUtil: 1.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			util := node.Utilization(tt.pods)
			// Allow small floating point tolerance
			if util < tt.expectedUtil-0.01 || util > tt.expectedUtil+0.01 {
				t.Errorf("Expected utilization ~%.2f, got %.2f", tt.expectedUtil, util)
			}
		})
	}
}

// Benchmark

func BenchmarkBinPacker_Pack(b *testing.B) {
	bp := NewBinPacker()

	// Create 50 pods with varying sizes
	pods := make([]PodRequirements, 50)
	for i := 0; i < 50; i++ {
		cpuMillis := int64(500 + (i%8)*500) // 500m to 4000m
		memMi := int64(512 + (i%8)*512)     // 512Mi to 4Gi
		pods[i] = makePodReqs("pod", cpuMillis, memMi)
		pods[i].Name = fmt.Sprintf("pod-%d", i)
	}

	// Create 10 node types
	nodeTypes := make([]NodeCapacity, 10)
	for i := 0; i < 10; i++ {
		cpu := (i + 1) * 4 // 4 to 40 CPU
		mem := (i + 1) * 8 // 8 to 80 Gi
		price := float64(i+1) * 0.01
		nodeTypes[i] = makeNodeCap("region-1", fmt.Sprintf("type-%d", i), cpu, mem, price)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := bp.Pack(pods, nodeTypes)
		if err != nil {
			b.Fatalf("Pack failed: %v", err)
		}
	}
}

func BenchmarkBinPacker_PackWithOverhead(b *testing.B) {
	overhead := makeDaemonSetOverhead(1000, 2*1024)
	bp := NewBinPackerWithOverhead(overhead)

	pods := make([]PodRequirements, 50)
	for i := 0; i < 50; i++ {
		cpuMillis := int64(500 + (i%8)*500)
		memMi := int64(512 + (i%8)*512)
		pods[i] = makePodReqs("pod", cpuMillis, memMi)
		pods[i].Name = fmt.Sprintf("pod-%d", i)
	}

	nodeTypes := make([]NodeCapacity, 10)
	for i := 0; i < 10; i++ {
		cpu := (i + 1) * 4
		mem := (i + 1) * 8
		price := float64(i+1) * 0.01
		nodeTypes[i] = makeNodeCap("region-1", fmt.Sprintf("type-%d", i), cpu, mem, price)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := bp.Pack(pods, nodeTypes)
		if err != nil {
			b.Fatalf("Pack failed: %v", err)
		}
	}
}
