package scheduler

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DaemonSetOverhead represents the total per-node resource overhead from DaemonSets.
// When bin packing pods, this overhead should be subtracted from each node's available
// capacity to ensure regular pods don't oversubscribe nodes.
type DaemonSetOverhead struct {
	CPU              resource.Quantity
	Memory           resource.Quantity
	EphemeralStorage resource.Quantity
}

// Add adds another overhead to this one
func (o *DaemonSetOverhead) Add(other *DaemonSetOverhead) {
	if other == nil {
		return
	}
	o.CPU.Add(other.CPU)
	o.Memory.Add(other.Memory)
	o.EphemeralStorage.Add(other.EphemeralStorage)
}

// IsZero returns true if both CPU and Memory overhead are zero
func (o *DaemonSetOverhead) IsZero() bool {
	return o == nil || (o.CPU.IsZero() && o.Memory.IsZero() && o.EphemeralStorage.IsZero())
}

// String returns a human-readable representation of the overhead
func (o *DaemonSetOverhead) String() string {
	if o == nil {
		return "DaemonSetOverhead{nil}"
	}
	return fmt.Sprintf("DaemonSetOverhead{CPU: %s, Memory: %s, Storage: %s}",
		o.CPU.String(), o.Memory.String(), o.EphemeralStorage.String())
}

// GetDaemonSetOverhead queries the cluster for all DaemonSets and calculates
// the total per-node resource overhead. This represents the resources that will
// be consumed on every node by DaemonSet pods, and should be subtracted from
// node capacity when bin packing regular workloads.
func GetDaemonSetOverhead(ctx context.Context, k8sClient client.Client) (*DaemonSetOverhead, error) {
	var daemonSets appsv1.DaemonSetList
	if err := k8sClient.List(ctx, &daemonSets); err != nil {
		return nil, fmt.Errorf("listing daemonsets: %w", err)
	}

	overhead := &DaemonSetOverhead{}

	for _, ds := range daemonSets.Items {
		dsOverhead := getDaemonSetPodOverhead(&ds)
		overhead.Add(dsOverhead)
	}

	return overhead, nil
}

// GetDaemonSetOverheadForNamespace queries the cluster for DaemonSets in a specific
// namespace and calculates the per-node resource overhead.
func GetDaemonSetOverheadForNamespace(ctx context.Context, k8sClient client.Client, namespace string) (*DaemonSetOverhead, error) {
	var daemonSets appsv1.DaemonSetList
	if err := k8sClient.List(ctx, &daemonSets, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("listing daemonsets in namespace %s: %w", namespace, err)
	}

	overhead := &DaemonSetOverhead{}

	for _, ds := range daemonSets.Items {
		dsOverhead := getDaemonSetPodOverhead(&ds)
		overhead.Add(dsOverhead)
	}

	return overhead, nil
}

// getDaemonSetPodOverhead calculates the resource overhead for a single DaemonSet
func getDaemonSetPodOverhead(ds *appsv1.DaemonSet) *DaemonSetOverhead {
	overhead := &DaemonSetOverhead{}

	// Sum up all container resource requests
	for _, container := range ds.Spec.Template.Spec.Containers {
		if cpu := container.Resources.Requests.Cpu(); cpu != nil {
			overhead.CPU.Add(*cpu)
		}
		if mem := container.Resources.Requests.Memory(); mem != nil {
			overhead.Memory.Add(*mem)
		}
		if storage := container.Resources.Requests.StorageEphemeral(); storage != nil {
			overhead.EphemeralStorage.Add(*storage)
		}
	}

	// For init containers, take the max (they run sequentially before main containers)
	// Unlike regular pods where init containers can require more resources than the
	// final running state, for DaemonSets we assume steady-state resource usage
	// which is determined by the regular containers
	var maxInitCPU, maxInitMem, maxInitStorage resource.Quantity
	for _, container := range ds.Spec.Template.Spec.InitContainers {
		if cpu := container.Resources.Requests.Cpu(); cpu != nil && cpu.Cmp(maxInitCPU) > 0 {
			maxInitCPU = *cpu
		}
		if mem := container.Resources.Requests.Memory(); mem != nil && mem.Cmp(maxInitMem) > 0 {
			maxInitMem = *mem
		}
		if storage := container.Resources.Requests.StorageEphemeral(); storage != nil && storage.Cmp(maxInitStorage) > 0 {
			maxInitStorage = *storage
		}
	}

	// Only add init container overhead if it exceeds regular container requirements
	// This matches Kubernetes scheduler behavior
	if maxInitCPU.Cmp(overhead.CPU) > 0 {
		overhead.CPU = maxInitCPU
	}
	if maxInitMem.Cmp(overhead.Memory) > 0 {
		overhead.Memory = maxInitMem
	}
	if maxInitStorage.Cmp(overhead.EphemeralStorage) > 0 {
		overhead.EphemeralStorage = maxInitStorage
	}

	return overhead
}

// GetDaemonSetOverheadFromPods calculates the total overhead from a list of
// PodRequirements. This is useful for testing without cluster access or when
// you already have pod requirements extracted.
func GetDaemonSetOverheadFromPods(pods []PodRequirements) *DaemonSetOverhead {
	overhead := &DaemonSetOverhead{}

	for _, pod := range pods {
		overhead.CPU.Add(pod.CPU)
		overhead.Memory.Add(pod.Memory)
		overhead.EphemeralStorage.Add(pod.EphemeralStorage)
	}

	return overhead
}

// NewDaemonSetOverhead creates a DaemonSetOverhead with the specified CPU and memory
func NewDaemonSetOverhead(cpu, memory resource.Quantity) *DaemonSetOverhead {
	return &DaemonSetOverhead{
		CPU:    cpu,
		Memory: memory,
	}
}

// NewDaemonSetOverheadWithStorage creates a DaemonSetOverhead with the specified CPU, memory and storage
func NewDaemonSetOverheadWithStorage(cpu, memory, storage resource.Quantity) *DaemonSetOverhead {
	return &DaemonSetOverhead{
		CPU:              cpu,
		Memory:           memory,
		EphemeralStorage: storage,
	}
}

// NewDaemonSetOverheadFromMillis creates a DaemonSetOverhead from millicores and bytes
func NewDaemonSetOverheadFromMillis(cpuMillis int64, memoryBytes int64) *DaemonSetOverhead {
	return &DaemonSetOverhead{
		CPU:    *resource.NewMilliQuantity(cpuMillis, resource.DecimalSI),
		Memory: *resource.NewQuantity(memoryBytes, resource.BinarySI),
	}
}

// CommonDaemonSetOverhead returns a typical DaemonSet overhead for common Kubernetes
// cluster components. This can be used as a reasonable default when cluster access
// is not available. Includes estimates for:
// - kube-proxy: 100m CPU, 128Mi memory
// - CNI (e.g., Calico): 250m CPU, 256Mi memory
// - Node exporter: 100m CPU, 64Mi memory
// - Logging agent (e.g., Fluent Bit): 200m CPU, 256Mi memory
// Total: ~650m CPU, ~704Mi memory
func CommonDaemonSetOverhead() *DaemonSetOverhead {
	return &DaemonSetOverhead{
		CPU:    resource.MustParse("650m"),
		Memory: resource.MustParse("704Mi"),
	}
}
