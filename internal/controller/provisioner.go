package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	karpetrackv1alpha1 "github.com/maccam912/karpetrack/api/v1alpha1"
	"github.com/maccam912/karpetrack/internal/scheduler"
	"github.com/maccam912/karpetrack/internal/spot"
)

// ProvisionerController watches for unschedulable pods and provisions nodes
type ProvisionerController struct {
	client.Client
	SpotClient *spot.Client
	Pricing    *spot.PricingProvider
	Optimizer  *scheduler.CostOptimizer
	Recorder   record.EventRecorder

	// BatchWindow is how long to wait for more pods before provisioning
	BatchWindow time.Duration
}

// NewProvisionerController creates a new provisioner controller
func NewProvisionerController(
	client client.Client,
	spotClient *spot.Client,
	recorder record.EventRecorder,
) *ProvisionerController {
	pricing := spotClient.GetPricing()
	return &ProvisionerController{
		Client:      client,
		SpotClient:  spotClient,
		Pricing:     pricing,
		Optimizer:   scheduler.NewCostOptimizer(pricing),
		Recorder:    recorder,
		BatchWindow: 10 * time.Second,
	}
}

// Reconcile handles pod scheduling events
func (r *ProvisionerController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Get the pod that triggered reconciliation
	var pod corev1.Pod
	if err := r.Get(ctx, req.NamespacedName, &pod); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Check if pod is unschedulable
	if !isUnschedulable(&pod) {
		return ctrl.Result{}, nil
	}

	log.Info("Found unschedulable pod", "pod", req.NamespacedName)

	// Get all unschedulable pods (batch processing)
	unschedulablePods, err := r.listUnschedulablePods(ctx)
	if err != nil {
		log.Error(err, "Failed to list unschedulable pods")
		return ctrl.Result{}, err
	}

	if len(unschedulablePods) == 0 {
		return ctrl.Result{}, nil
	}

	// Get SpotNodePools
	var nodePools karpetrackv1alpha1.SpotNodePoolList
	if err := r.List(ctx, &nodePools); err != nil {
		log.Error(err, "Failed to list SpotNodePools")
		return ctrl.Result{}, err
	}

	if len(nodePools.Items) == 0 {
		log.Info("No SpotNodePools found, cannot provision nodes")
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// Group pods by compatible NodePool
	for _, nodePool := range nodePools.Items {
		matchingPods := r.filterPodsForNodePool(unschedulablePods, &nodePool)
		if len(matchingPods) == 0 {
			continue
		}

		log.Info("Found unschedulable pods for NodePool",
			"nodePool", nodePool.Name,
			"podCount", len(matchingPods))

		// Check if we're within limits
		if !r.canProvisionMore(ctx, &nodePool) {
			log.Info("NodePool at capacity limit", "nodePool", nodePool.Name)
			continue
		}

		// Find optimal node configuration
		if err := r.provisionNodesForPods(ctx, matchingPods, &nodePool); err != nil {
			log.Error(err, "Failed to provision nodes", "nodePool", nodePool.Name)
			r.Recorder.Eventf(&nodePool, corev1.EventTypeWarning, "ProvisioningFailed",
				"Failed to provision nodes: %v", err)
			continue
		}
	}

	// Requeue to check for more pods after batch window
	return ctrl.Result{RequeueAfter: r.BatchWindow}, nil
}

// listUnschedulablePods returns all pods that cannot be scheduled
func (r *ProvisionerController) listUnschedulablePods(ctx context.Context) ([]*corev1.Pod, error) {
	var podList corev1.PodList
	if err := r.List(ctx, &podList); err != nil {
		return nil, err
	}

	var unschedulable []*corev1.Pod
	for i := range podList.Items {
		pod := &podList.Items[i]
		if isUnschedulable(pod) {
			unschedulable = append(unschedulable, pod)
		}
	}

	return unschedulable, nil
}

// isUnschedulable checks if a pod is pending due to being unschedulable
func isUnschedulable(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodPending {
		return false
	}

	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodScheduled &&
			condition.Status == corev1.ConditionFalse &&
			condition.Reason == corev1.PodReasonUnschedulable {
			return true
		}
	}

	return false
}

// filterPodsForNodePool returns pods that match the NodePool's requirements
func (r *ProvisionerController) filterPodsForNodePool(
	pods []*corev1.Pod,
	nodePool *karpetrackv1alpha1.SpotNodePool,
) []*corev1.Pod {
	var matching []*corev1.Pod

	for _, pod := range pods {
		if r.podMatchesNodePool(pod, nodePool) {
			matching = append(matching, pod)
		}
	}

	return matching
}

// podMatchesNodePool checks if a pod's requirements match the NodePool
func (r *ProvisionerController) podMatchesNodePool(
	pod *corev1.Pod,
	nodePool *karpetrackv1alpha1.SpotNodePool,
) bool {
	// Check node selector requirements
	for _, req := range nodePool.Spec.Requirements {
		if pod.Spec.NodeSelector != nil {
			if val, ok := pod.Spec.NodeSelector[req.Key]; ok {
				matched := false
				for _, v := range req.Values {
					if v == val {
						matched = true
						break
					}
				}
				if !matched {
					return false
				}
			}
		}
	}

	// Check taints/tolerations
	for _, taint := range nodePool.Spec.Taints {
		tolerated := false
		for _, toleration := range pod.Spec.Tolerations {
			if toleratesTaint(toleration, taint) {
				tolerated = true
				break
			}
		}
		if !tolerated {
			return false
		}
	}

	return true
}

// toleratesTaint checks if a toleration matches a taint
func toleratesTaint(toleration corev1.Toleration, taint corev1.Taint) bool {
	if toleration.Key == "" {
		// Empty key with Exists operator means tolerate all
		return toleration.Operator == corev1.TolerationOpExists
	}

	if toleration.Key != taint.Key {
		return false
	}

	if toleration.Operator == corev1.TolerationOpExists {
		return true
	}

	return toleration.Value == taint.Value && toleration.Effect == taint.Effect
}

// canProvisionMore checks if the NodePool can provision more nodes
func (r *ProvisionerController) canProvisionMore(
	ctx context.Context,
	nodePool *karpetrackv1alpha1.SpotNodePool,
) bool {
	// Check resource limits
	if nodePool.Spec.Limits.CPU != nil || nodePool.Spec.Limits.Memory != nil || nodePool.Spec.Limits.EphemeralStorage != nil {
		// Get current SpotNodes for this pool
		var spotNodes karpetrackv1alpha1.SpotNodeList
		if err := r.List(ctx, &spotNodes); err != nil {
			return false
		}

		var currentCPU, currentMem, currentStorage resource.Quantity
		for _, sn := range spotNodes.Items {
			if sn.Spec.NodePoolRef == nodePool.Name {
				if sn.Spec.Resources.CPU != nil {
					currentCPU.Add(*sn.Spec.Resources.CPU)
				}
				if sn.Spec.Resources.Memory != nil {
					currentMem.Add(*sn.Spec.Resources.Memory)
				}
				if sn.Spec.Resources.EphemeralStorage != nil {
					currentStorage.Add(*sn.Spec.Resources.EphemeralStorage)
				}
			}
		}

		if nodePool.Spec.Limits.CPU != nil && currentCPU.Cmp(*nodePool.Spec.Limits.CPU) >= 0 {
			return false
		}
		if nodePool.Spec.Limits.Memory != nil && currentMem.Cmp(*nodePool.Spec.Limits.Memory) >= 0 {
			return false
		}
		if nodePool.Spec.Limits.EphemeralStorage != nil && currentStorage.Cmp(*nodePool.Spec.Limits.EphemeralStorage) >= 0 {
			return false
		}
	}

	return true
}

// provisionNodesForPods creates SpotNode resources for the optimal configuration
func (r *ProvisionerController) provisionNodesForPods(
	ctx context.Context,
	pods []*corev1.Pod,
	nodePool *karpetrackv1alpha1.SpotNodePool,
) error {
	log := log.FromContext(ctx)

	// Convert pods to requirements
	podReqs := make([]scheduler.PodRequirements, len(pods))
	for i, pod := range pods {
		podReqs[i] = scheduler.GetPodRequirements(pod)
	}

	// Build constraints from NodePool spec
	constraints := scheduler.OptimizationConstraints{
		Regions:    nodePool.Spec.Regions,
		Categories: nodePool.Spec.Categories,
	}

	if nodePool.Spec.MaxPrice != "" {
		if price, err := parsePrice(nodePool.Spec.MaxPrice); err == nil {
			constraints.MaxPricePerNode = price
		}
	}

	// Find optimal configuration
	result, err := r.Optimizer.FindOptimalConfiguration(ctx, podReqs, constraints)
	if err != nil {
		return fmt.Errorf("finding optimal configuration: %w", err)
	}

	log.Info("Found optimal configuration",
		"nodeCount", len(result.Nodes),
		"totalCost", result.TotalCost)

	// Create SpotNode resources
	for _, nodeRec := range result.Nodes {
		spotNode := &karpetrackv1alpha1.SpotNode{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: fmt.Sprintf("karpetrack-%s-", nodePool.Name),
				Labels: map[string]string{
					"karpetrack.io/nodepool": nodePool.Name,
					"karpetrack.io/region":   nodeRec.Region,
					"karpetrack.io/category": sanitizeLabelValue(nodeRec.Category),
				},
			},
			Spec: karpetrackv1alpha1.SpotNodeSpec{
				NodePoolRef:  nodePool.Name,
				InstanceType: nodeRec.InstanceType,
				Region:       nodeRec.Region,
				Resources: karpetrackv1alpha1.SpotNodeResources{
					CPU:              &nodeRec.CPU,
					Memory:           &nodeRec.Memory,
					EphemeralStorage: &nodeRec.Storage,
				},
				BidPrice: fmt.Sprintf("%.6f", nodeRec.PricePerHour*1.1), // Bid 10% above market
			},
		}

		if err := r.Create(ctx, spotNode); err != nil {
			return fmt.Errorf("creating SpotNode: %w", err)
		}

		log.Info("Created SpotNode",
			"name", spotNode.Name,
			"instanceType", nodeRec.InstanceType,
			"region", nodeRec.Region,
			"price", nodeRec.PricePerHour)

		r.Recorder.Eventf(nodePool, corev1.EventTypeNormal, "NodeProvisioning",
			"Creating node %s (type: %s, region: %s, price: $%.4f/hr)",
			spotNode.Name, nodeRec.InstanceType, nodeRec.Region, nodeRec.PricePerHour)
	}

	// Update NodePool status
	if err := r.updateNodePoolStatus(ctx, nodePool); err != nil {
		log.Error(err, "Failed to update NodePool status")
	}

	return nil
}

// updateNodePoolStatus updates the NodePool's status with current node count
func (r *ProvisionerController) updateNodePoolStatus(
	ctx context.Context,
	nodePool *karpetrackv1alpha1.SpotNodePool,
) error {
	var spotNodes karpetrackv1alpha1.SpotNodeList
	if err := r.List(ctx, &spotNodes); err != nil {
		return err
	}

	var count int32
	var totalCPU, totalMem, totalStorage resource.Quantity
	for _, sn := range spotNodes.Items {
		if sn.Spec.NodePoolRef == nodePool.Name {
			count++
			if sn.Spec.Resources.CPU != nil {
				totalCPU.Add(*sn.Spec.Resources.CPU)
			}
			if sn.Spec.Resources.Memory != nil {
				totalMem.Add(*sn.Spec.Resources.Memory)
			}
			if sn.Spec.Resources.EphemeralStorage != nil {
				totalStorage.Add(*sn.Spec.Resources.EphemeralStorage)
			}
		}
	}

	patch := client.MergeFrom(nodePool.DeepCopy())
	nodePool.Status.NodeCount = count
	nodePool.Status.Resources.CPU = &totalCPU
	nodePool.Status.Resources.Memory = &totalMem
	nodePool.Status.Resources.EphemeralStorage = &totalStorage

	return r.Status().Patch(ctx, nodePool, patch)
}

// parsePrice parses a price string to float64
func parsePrice(s string) (float64, error) {
	var price float64
	_, err := fmt.Sscanf(s, "%f", &price)
	return price, err
}

// SetupWithManager sets up the controller with the Manager
func (r *ProvisionerController) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		Named("provisioner").
		Complete(r)
}

// PodFilter returns true for pods that should trigger reconciliation
func PodFilter(obj client.Object) bool {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return false
	}
	return isUnschedulable(pod)
}

// sanitizeLabelValue converts a string to a valid Kubernetes label value.
// Label values must be 63 characters or less and must consist of alphanumeric
// characters, '-', '_', or '.', and must start and end with an alphanumeric.
func sanitizeLabelValue(s string) string {
	// Convert to lowercase and replace spaces with hyphens
	result := strings.ToLower(s)
	result = strings.ReplaceAll(result, " ", "-")

	// Replace any other invalid characters with hyphens
	var sanitized strings.Builder
	for i, c := range result {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' {
			sanitized.WriteRune(c)
		} else if i > 0 {
			sanitized.WriteRune('-')
		}
	}
	result = sanitized.String()

	// Trim leading/trailing non-alphanumeric characters
	result = strings.Trim(result, "-_.")

	// Truncate to 63 characters if needed
	if len(result) > 63 {
		result = result[:63]
		result = strings.TrimRight(result, "-_.")
	}

	return result
}
