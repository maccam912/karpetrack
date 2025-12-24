package scheduler

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// DefaultEphemeralStorage is the fallback storage capacity for nodes (40GB)
const DefaultEphemeralStorage = "40Gi"

// PodRequirements represents the resource requirements for a pod
type PodRequirements struct {
	Name      string
	Namespace string
	CPU       resource.Quantity
	Memory    resource.Quantity
	GPU       resource.Quantity

	// Scheduling constraints
	NodeSelector map[string]string
	Tolerations  []corev1.Toleration
	Affinity     *corev1.Affinity
}

// GetPodRequirements extracts resource requirements from a pod
func GetPodRequirements(pod *corev1.Pod) PodRequirements {
	reqs := PodRequirements{
		Name:         pod.Name,
		Namespace:    pod.Namespace,
		NodeSelector: pod.Spec.NodeSelector,
		Tolerations:  pod.Spec.Tolerations,
		Affinity:     pod.Spec.Affinity,
	}

	// Sum up all container resource requests
	for _, container := range pod.Spec.Containers {
		if cpu := container.Resources.Requests.Cpu(); cpu != nil {
			reqs.CPU.Add(*cpu)
		}
		if mem := container.Resources.Requests.Memory(); mem != nil {
			reqs.Memory.Add(*mem)
		}
		// Check for GPU requests
		if gpu, ok := container.Resources.Requests["nvidia.com/gpu"]; ok {
			reqs.GPU.Add(gpu)
		}
	}

	// Include init container requirements (take max, not sum)
	for _, container := range pod.Spec.InitContainers {
		if cpu := container.Resources.Requests.Cpu(); cpu != nil && cpu.Cmp(reqs.CPU) > 0 {
			reqs.CPU = *cpu
		}
		if mem := container.Resources.Requests.Memory(); mem != nil && mem.Cmp(reqs.Memory) > 0 {
			reqs.Memory = *mem
		}
	}

	return reqs
}

// AggregateRequirements combines requirements from multiple pods
type AggregateRequirements struct {
	TotalCPU    resource.Quantity
	TotalMemory resource.Quantity
	TotalGPU    resource.Quantity
	PodCount    int
	Pods        []PodRequirements
}

// AggregatePodRequirements combines requirements from multiple pods
func AggregatePodRequirements(pods []*corev1.Pod) AggregateRequirements {
	agg := AggregateRequirements{
		Pods: make([]PodRequirements, 0, len(pods)),
	}

	for _, pod := range pods {
		reqs := GetPodRequirements(pod)
		agg.Pods = append(agg.Pods, reqs)
		agg.TotalCPU.Add(reqs.CPU)
		agg.TotalMemory.Add(reqs.Memory)
		agg.TotalGPU.Add(reqs.GPU)
		agg.PodCount++
	}

	return agg
}

// NodeCapacity represents the capacity of a node type
type NodeCapacity struct {
	Region       string
	InstanceType string
	Category     string
	CPU          resource.Quantity
	Memory       resource.Quantity
	GPU          resource.Quantity
	PricePerHour float64
}

// CanFit checks if a pod's requirements can fit on this node type
func (nc NodeCapacity) CanFit(reqs PodRequirements) bool {
	if nc.CPU.Cmp(reqs.CPU) < 0 {
		return false
	}
	if nc.Memory.Cmp(reqs.Memory) < 0 {
		return false
	}
	if !reqs.GPU.IsZero() && nc.GPU.Cmp(reqs.GPU) < 0 {
		return false
	}
	return true
}

// CanFitMultiple checks how many pods of given requirements can fit
func (nc NodeCapacity) CanFitMultiple(pods []PodRequirements) int {
	remaining := NodeCapacity{
		CPU:    nc.CPU.DeepCopy(),
		Memory: nc.Memory.DeepCopy(),
		GPU:    nc.GPU.DeepCopy(),
	}

	count := 0
	for _, pod := range pods {
		if remaining.CPU.Cmp(pod.CPU) >= 0 &&
			remaining.Memory.Cmp(pod.Memory) >= 0 &&
			(pod.GPU.IsZero() || remaining.GPU.Cmp(pod.GPU) >= 0) {
			remaining.CPU.Sub(pod.CPU)
			remaining.Memory.Sub(pod.Memory)
			if !pod.GPU.IsZero() {
				remaining.GPU.Sub(pod.GPU)
			}
			count++
		}
	}

	return count
}

// Utilization calculates the utilization ratio after fitting pods
func (nc NodeCapacity) Utilization(pods []PodRequirements) float64 {
	var usedCPU, usedMem resource.Quantity
	for _, pod := range pods {
		usedCPU.Add(pod.CPU)
		usedMem.Add(pod.Memory)
	}

	cpuUtil := float64(usedCPU.MilliValue()) / float64(nc.CPU.MilliValue())
	memUtil := float64(usedMem.Value()) / float64(nc.Memory.Value())

	// Return average utilization
	return (cpuUtil + memUtil) / 2
}
