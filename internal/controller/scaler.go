package controller

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	karpetrackv1alpha1 "github.com/karpetrack/karpetrack/api/v1alpha1"
)

// ScalerController handles scale-down of empty or underutilized nodes
type ScalerController struct {
	client.Client
	Recorder record.EventRecorder

	// EmptyNodeGracePeriod is how long a node must be empty before being removed
	EmptyNodeGracePeriod time.Duration

	// CheckInterval is how often to check for scale-down opportunities
	CheckInterval time.Duration

	// MinUtilization is the minimum resource utilization to keep a node
	// Nodes below this threshold may be consolidated
	MinUtilization float64
}

// NewScalerController creates a new scaler controller
func NewScalerController(
	client client.Client,
	recorder record.EventRecorder,
) *ScalerController {
	return &ScalerController{
		Client:               client,
		Recorder:             recorder,
		EmptyNodeGracePeriod: 5 * time.Minute,
		CheckInterval:        1 * time.Minute,
		MinUtilization:       0.3, // 30% utilization threshold
	}
}

// Start runs the scale-down monitoring loop
func (r *ScalerController) Start(ctx context.Context) error {
	log := log.FromContext(ctx).WithName("scaler")
	log.Info("Starting scaler controller",
		"gracePeriod", r.EmptyNodeGracePeriod,
		"interval", r.CheckInterval)

	ticker := time.NewTicker(r.CheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info("Stopping scaler controller")
			return nil
		case <-ticker.C:
			if err := r.checkForScaleDown(ctx); err != nil {
				log.Error(err, "Error checking for scale-down")
			}
		}
	}
}

// checkForScaleDown looks for nodes that can be removed
func (r *ScalerController) checkForScaleDown(ctx context.Context) error {
	log := log.FromContext(ctx)

	// Get all running SpotNodes
	var spotNodes karpetrackv1alpha1.SpotNodeList
	if err := r.List(ctx, &spotNodes); err != nil {
		return err
	}

	for _, spotNode := range spotNodes.Items {
		if spotNode.Status.Phase != karpetrackv1alpha1.SpotNodePhaseRunning {
			continue
		}

		// Check if node should be scaled down
		shouldRemove, reason := r.shouldRemoveNode(ctx, &spotNode)
		if shouldRemove {
			log.Info("Scaling down node", "spotNode", spotNode.Name, "reason", reason)

			if err := r.removeNode(ctx, &spotNode, reason); err != nil {
				log.Error(err, "Failed to remove node", "spotNode", spotNode.Name)
				continue
			}
		}
	}

	return nil
}

// shouldRemoveNode determines if a node should be removed
func (r *ScalerController) shouldRemoveNode(
	ctx context.Context,
	spotNode *karpetrackv1alpha1.SpotNode,
) (bool, string) {
	log := log.FromContext(ctx)

	if spotNode.Status.NodeName == "" {
		return false, ""
	}

	// Get pods on this node
	var podList corev1.PodList
	if err := r.List(ctx, &podList, client.MatchingFields{"spec.nodeName": spotNode.Status.NodeName}); err != nil {
		log.Error(err, "Failed to list pods on node", "nodeName", spotNode.Status.NodeName)
		return false, ""
	}

	// Count active pods (non-daemonset, non-terminated)
	activePods := 0
	for _, pod := range podList.Items {
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}

		isDaemonSet := false
		for _, owner := range pod.OwnerReferences {
			if owner.Kind == "DaemonSet" {
				isDaemonSet = true
				break
			}
		}
		if !isDaemonSet {
			activePods++
		}
	}

	// Check if node is empty
	if activePods == 0 {
		// Check grace period via annotation
		emptyTime := r.getEmptyTimestamp(spotNode)
		if emptyTime.IsZero() {
			// First time seeing node empty - set timestamp
			r.setEmptyTimestamp(ctx, spotNode)
			return false, ""
		}

		if time.Since(emptyTime) >= r.EmptyNodeGracePeriod {
			return true, "empty"
		}
		return false, ""
	}

	// Node has pods - clear empty timestamp if set
	r.clearEmptyTimestamp(ctx, spotNode)

	// Check for consolidation (if NodePool allows it)
	// This is a simplified check - a full implementation would try to
	// reschedule pods to other nodes
	nodePool, err := r.getNodePool(ctx, spotNode.Spec.NodePoolRef)
	if err != nil {
		return false, ""
	}

	if nodePool.Spec.Disruption.ConsolidationPolicy == "WhenUnderutilized" {
		utilization := r.calculateUtilization(ctx, spotNode, podList.Items)
		if utilization < r.MinUtilization {
			// Check if pods could fit elsewhere
			if r.canConsolidate(ctx, spotNode, podList.Items) {
				return true, "underutilized"
			}
		}
	}

	return false, ""
}

// getEmptyTimestamp returns when the node became empty
func (r *ScalerController) getEmptyTimestamp(spotNode *karpetrackv1alpha1.SpotNode) time.Time {
	if spotNode.Annotations == nil {
		return time.Time{}
	}

	ts, ok := spotNode.Annotations["karpetrack.io/empty-since"]
	if !ok {
		return time.Time{}
	}

	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return time.Time{}
	}

	return t
}

// setEmptyTimestamp marks when the node became empty
func (r *ScalerController) setEmptyTimestamp(ctx context.Context, spotNode *karpetrackv1alpha1.SpotNode) {
	patch := client.MergeFrom(spotNode.DeepCopy())
	if spotNode.Annotations == nil {
		spotNode.Annotations = make(map[string]string)
	}
	spotNode.Annotations["karpetrack.io/empty-since"] = time.Now().Format(time.RFC3339)
	if err := r.Patch(ctx, spotNode, patch); err != nil {
		log.FromContext(ctx).Error(err, "Failed to set empty timestamp")
	}
}

// clearEmptyTimestamp removes the empty timestamp annotation
func (r *ScalerController) clearEmptyTimestamp(ctx context.Context, spotNode *karpetrackv1alpha1.SpotNode) {
	if spotNode.Annotations == nil {
		return
	}
	if _, ok := spotNode.Annotations["karpetrack.io/empty-since"]; !ok {
		return
	}

	patch := client.MergeFrom(spotNode.DeepCopy())
	delete(spotNode.Annotations, "karpetrack.io/empty-since")
	if err := r.Patch(ctx, spotNode, patch); err != nil {
		log.FromContext(ctx).Error(err, "Failed to clear empty timestamp")
	}
}

// getNodePool retrieves the NodePool for a SpotNode
func (r *ScalerController) getNodePool(ctx context.Context, name string) (*karpetrackv1alpha1.SpotNodePool, error) {
	var nodePool karpetrackv1alpha1.SpotNodePool
	if err := r.Get(ctx, client.ObjectKey{Name: name}, &nodePool); err != nil {
		return nil, err
	}
	return &nodePool, nil
}

// calculateUtilization calculates the resource utilization of a node
func (r *ScalerController) calculateUtilization(
	ctx context.Context,
	spotNode *karpetrackv1alpha1.SpotNode,
	pods []corev1.Pod,
) float64 {
	if spotNode.Spec.Resources.CPU == nil || spotNode.Spec.Resources.Memory == nil {
		return 1.0 // Assume fully utilized if we can't calculate
	}

	var cpuRequests, memRequests int64
	for _, pod := range pods {
		for _, container := range pod.Spec.Containers {
			if cpu := container.Resources.Requests.Cpu(); cpu != nil {
				cpuRequests += cpu.MilliValue()
			}
			if mem := container.Resources.Requests.Memory(); mem != nil {
				memRequests += mem.Value()
			}
		}
	}

	cpuCapacity := spotNode.Spec.Resources.CPU.MilliValue()
	memCapacity := spotNode.Spec.Resources.Memory.Value()

	cpuUtil := float64(cpuRequests) / float64(cpuCapacity)
	memUtil := float64(memRequests) / float64(memCapacity)

	// Return average utilization
	return (cpuUtil + memUtil) / 2
}

// canConsolidate checks if pods from this node could fit on other nodes
func (r *ScalerController) canConsolidate(
	ctx context.Context,
	spotNode *karpetrackv1alpha1.SpotNode,
	pods []corev1.Pod,
) bool {
	// Get other running SpotNodes in the same pool
	var allNodes karpetrackv1alpha1.SpotNodeList
	if err := r.List(ctx, &allNodes); err != nil {
		return false
	}

	// Calculate total resources needed by pods on this node
	var cpuNeeded, memNeeded int64
	for _, pod := range pods {
		for _, container := range pod.Spec.Containers {
			if cpu := container.Resources.Requests.Cpu(); cpu != nil {
				cpuNeeded += cpu.MilliValue()
			}
			if mem := container.Resources.Requests.Memory(); mem != nil {
				memNeeded += mem.Value()
			}
		}
	}

	// Check if other nodes have capacity
	for _, otherNode := range allNodes.Items {
		if otherNode.Name == spotNode.Name {
			continue
		}
		if otherNode.Status.Phase != karpetrackv1alpha1.SpotNodePhaseRunning {
			continue
		}
		if otherNode.Spec.NodePoolRef != spotNode.Spec.NodePoolRef {
			continue
		}

		// Get available capacity on other node
		otherPods, err := r.getPodsOnNode(ctx, otherNode.Status.NodeName)
		if err != nil {
			continue
		}

		var otherCPUUsed, otherMemUsed int64
		for _, pod := range otherPods {
			for _, container := range pod.Spec.Containers {
				if cpu := container.Resources.Requests.Cpu(); cpu != nil {
					otherCPUUsed += cpu.MilliValue()
				}
				if mem := container.Resources.Requests.Memory(); mem != nil {
					otherMemUsed += mem.Value()
				}
			}
		}

		if otherNode.Spec.Resources.CPU != nil && otherNode.Spec.Resources.Memory != nil {
			availableCPU := otherNode.Spec.Resources.CPU.MilliValue() - otherCPUUsed
			availableMem := otherNode.Spec.Resources.Memory.Value() - otherMemUsed

			if availableCPU >= cpuNeeded && availableMem >= memNeeded {
				return true // Found a node that can accommodate these pods
			}
		}
	}

	return false
}

// getPodsOnNode returns pods running on a specific node
func (r *ScalerController) getPodsOnNode(ctx context.Context, nodeName string) ([]corev1.Pod, error) {
	var podList corev1.PodList
	if err := r.List(ctx, &podList, client.MatchingFields{"spec.nodeName": nodeName}); err != nil {
		return nil, err
	}
	return podList.Items, nil
}

// removeNode initiates removal of a node
func (r *ScalerController) removeNode(
	ctx context.Context,
	spotNode *karpetrackv1alpha1.SpotNode,
	reason string,
) error {
	log := log.FromContext(ctx)

	// Update status to draining
	patch := client.MergeFrom(spotNode.DeepCopy())
	spotNode.Status.Phase = karpetrackv1alpha1.SpotNodePhaseDraining

	if spotNode.Annotations == nil {
		spotNode.Annotations = make(map[string]string)
	}
	spotNode.Annotations["karpetrack.io/removal-reason"] = reason

	if err := r.Status().Patch(ctx, spotNode, patch); err != nil {
		return err
	}

	// Cordon the node
	if spotNode.Status.NodeName != "" {
		var k8sNode corev1.Node
		if err := r.Get(ctx, client.ObjectKey{Name: spotNode.Status.NodeName}, &k8sNode); err == nil {
			if !k8sNode.Spec.Unschedulable {
				k8sNode.Spec.Unschedulable = true
				if err := r.Update(ctx, &k8sNode); err != nil {
					log.Error(err, "Failed to cordon node")
				}
			}
		}
	}

	r.Recorder.Eventf(spotNode, corev1.EventTypeNormal, "ScalingDown",
		"Node marked for removal: %s", reason)

	return nil
}

// SetupWithManager sets up the scaler as a runnable
func (r *ScalerController) SetupWithManager(mgr ctrl.Manager) error {
	return mgr.Add(manager.RunnableFunc(r.Start))
}
