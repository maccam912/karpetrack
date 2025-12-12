package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	karpetrackv1alpha1 "github.com/maccam912/karpetrack/api/v1alpha1"
	"github.com/maccam912/karpetrack/internal/spot"
)

const (
	// SpotNodeFinalizer is the finalizer for SpotNode resources
	SpotNodeFinalizer = "karpetrack.io/spotnode"

	// ProvisioningTimeout is how long to wait for a node to be ready
	ProvisioningTimeout = 10 * time.Minute
)

// SpotNodeController manages the lifecycle of SpotNode resources
type SpotNodeController struct {
	client.Client
	SpotClient *spot.Client
	Pricing    *spot.PricingProvider
	Recorder   record.EventRecorder
}

// NewSpotNodeController creates a new SpotNode controller
func NewSpotNodeController(
	client client.Client,
	spotClient *spot.Client,
	recorder record.EventRecorder,
) *SpotNodeController {
	return &SpotNodeController{
		Client:     client,
		SpotClient: spotClient,
		Pricing:    spotClient.GetPricing(),
		Recorder:   recorder,
	}
}

// Reconcile handles SpotNode lifecycle events
func (r *SpotNodeController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var spotNode karpetrackv1alpha1.SpotNode
	if err := r.Get(ctx, req.NamespacedName, &spotNode); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Handle deletion
	if !spotNode.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, &spotNode)
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(&spotNode, SpotNodeFinalizer) {
		controllerutil.AddFinalizer(&spotNode, SpotNodeFinalizer)
		if err := r.Update(ctx, &spotNode); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Handle based on phase
	switch spotNode.Status.Phase {
	case "", karpetrackv1alpha1.SpotNodePhasePending:
		return r.handlePending(ctx, &spotNode)

	case karpetrackv1alpha1.SpotNodePhaseProvisioning:
		return r.handleProvisioning(ctx, &spotNode)

	case karpetrackv1alpha1.SpotNodePhaseRunning:
		return r.handleRunning(ctx, &spotNode)

	case karpetrackv1alpha1.SpotNodePhaseDraining:
		return r.handleDraining(ctx, &spotNode)

	case karpetrackv1alpha1.SpotNodePhaseTerminating:
		return r.handleTerminating(ctx, &spotNode)

	default:
		log.Info("Unknown phase", "phase", spotNode.Status.Phase)
		return ctrl.Result{}, nil
	}
}

// handlePending starts provisioning a new node
func (r *SpotNodeController) handlePending(
	ctx context.Context,
	spotNode *karpetrackv1alpha1.SpotNode,
) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.Info("Starting node provisioning", "spotNode", spotNode.Name)

	// Create node via Rackspace Spot API
	nodeSpec := spot.NodeSpec{
		InstanceType: spotNode.Spec.InstanceType,
		Region:       spotNode.Spec.Region,
		NodePoolName: spotNode.Spec.NodePoolRef,
		Labels: map[string]string{
			"karpetrack.io/spotnode": spotNode.Name,
		},
	}

	if spotNode.Spec.BidPrice != "" {
		var price float64
		fmt.Sscanf(spotNode.Spec.BidPrice, "%f", &price)
		nodeSpec.BidPrice = price
	}

	node, err := r.SpotClient.CreateNode(ctx, nodeSpec)
	if err != nil {
		log.Error(err, "Failed to create node via Spot API")
		r.Recorder.Eventf(spotNode, corev1.EventTypeWarning, "ProvisioningFailed",
			"Failed to create node: %v", err)

		// Update status with error condition
		r.setCondition(spotNode, metav1.Condition{
			Type:    "Ready",
			Status:  metav1.ConditionFalse,
			Reason:  "ProvisioningFailed",
			Message: err.Error(),
		})
		if err := r.Status().Update(ctx, spotNode); err != nil {
			return ctrl.Result{}, err
		}

		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// Update status
	spotNode.Status.ProviderID = node.ID
	spotNode.Status.Phase = karpetrackv1alpha1.SpotNodePhaseProvisioning
	now := metav1.Now()
	spotNode.Status.ProvisionedAt = &now

	// Get current price
	if price, err := r.Pricing.GetPriceForInstance(ctx, spotNode.Spec.Region, spotNode.Spec.InstanceType); err == nil {
		spotNode.Status.PricePerHour = fmt.Sprintf("%.6f", price)
	}

	r.setCondition(spotNode, metav1.Condition{
		Type:    "Ready",
		Status:  metav1.ConditionFalse,
		Reason:  "Provisioning",
		Message: "Node is being provisioned",
	})

	if err := r.Status().Update(ctx, spotNode); err != nil {
		return ctrl.Result{}, err
	}

	r.Recorder.Eventf(spotNode, corev1.EventTypeNormal, "Provisioning",
		"Node provisioning started (provider ID: %s)", node.ID)

	// Requeue to check provisioning status
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// handleProvisioning checks if the node is ready
func (r *SpotNodeController) handleProvisioning(
	ctx context.Context,
	spotNode *karpetrackv1alpha1.SpotNode,
) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Check if we've exceeded provisioning timeout
	if spotNode.Status.ProvisionedAt != nil {
		elapsed := time.Since(spotNode.Status.ProvisionedAt.Time)
		if elapsed > ProvisioningTimeout {
			log.Info("Provisioning timeout exceeded", "elapsed", elapsed)
			r.Recorder.Eventf(spotNode, corev1.EventTypeWarning, "ProvisioningTimeout",
				"Node provisioning timed out after %v", elapsed)

			// Mark for cleanup
			spotNode.Status.Phase = karpetrackv1alpha1.SpotNodePhaseTerminating
			if err := r.Status().Update(ctx, spotNode); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{Requeue: true}, nil
		}
	}

	// With the pool-based model, we need to find unclaimed Kubernetes nodes
	// that match our server class. Rackspace Spot creates nodes asynchronously
	// and they register with Kubernetes with labels we can match.

	// Look for Kubernetes nodes that:
	// 1. Are managed by karpetrack (have the karpetrack.io/managed label)
	// 2. Match our server class
	// 3. Are not yet claimed by another SpotNode resource
	var nodeList corev1.NodeList
	if err := r.List(ctx, &nodeList); err != nil {
		return ctrl.Result{}, err
	}

	var claimedNode *corev1.Node
	for i := range nodeList.Items {
		k8sNode := &nodeList.Items[i]

		// Check if this node is from a karpetrack-managed pool
		// Nodes from Rackspace Spot pools get labels from the pool's CustomLabels
		if k8sNode.Labels["karpetrack.io/managed"] != "true" {
			continue
		}

		// Check if node matches our server class (via sanitized label)
		nodeServerClass := k8sNode.Labels["karpetrack.io/server-class"]
		expectedServerClass := sanitizeServerClass(spotNode.Spec.InstanceType)
		if nodeServerClass != expectedServerClass {
			continue
		}

		// Check if this node is already claimed by another SpotNode
		if isClaimed, _ := r.isNodeClaimed(ctx, k8sNode.Name); isClaimed {
			continue
		}

		// Check if node is ready
		nodeReady := false
		for _, condition := range k8sNode.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
				nodeReady = true
				break
			}
		}

		if nodeReady {
			claimedNode = k8sNode
			break
		}
	}

	if claimedNode == nil {
		log.Info("Waiting for Kubernetes node to appear",
			"serverClass", spotNode.Spec.InstanceType)
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// Found and claiming node
	log.Info("Found ready node, claiming", "nodeName", claimedNode.Name)

	spotNode.Status.NodeName = claimedNode.Name
	spotNode.Status.Phase = karpetrackv1alpha1.SpotNodePhaseRunning
	now := metav1.Now()
	spotNode.Status.LastPriceCheck = &now

	r.setCondition(spotNode, metav1.Condition{
		Type:    "Ready",
		Status:  metav1.ConditionTrue,
		Reason:  "NodeReady",
		Message: "Node is running and ready",
	})

	if err := r.Status().Update(ctx, spotNode); err != nil {
		return ctrl.Result{}, err
	}

	r.Recorder.Eventf(spotNode, corev1.EventTypeNormal, "NodeReady",
		"Node %s is ready", claimedNode.Name)

	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

// isNodeClaimed checks if a Kubernetes node is already claimed by a SpotNode
func (r *SpotNodeController) isNodeClaimed(ctx context.Context, nodeName string) (bool, error) {
	var spotNodes karpetrackv1alpha1.SpotNodeList
	if err := r.List(ctx, &spotNodes); err != nil {
		return false, err
	}

	for _, sn := range spotNodes.Items {
		if sn.Status.NodeName == nodeName {
			return true, nil
		}
	}

	return false, nil
}

// sanitizeServerClass converts a server class name to the label format
func sanitizeServerClass(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, ".", "-")
	s = strings.ReplaceAll(s, "_", "-")
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

// handleRunning monitors a running node
func (r *SpotNodeController) handleRunning(
	ctx context.Context,
	spotNode *karpetrackv1alpha1.SpotNode,
) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Verify Kubernetes node still exists
	if spotNode.Status.NodeName != "" {
		var k8sNode corev1.Node
		err := r.Get(ctx, client.ObjectKey{Name: spotNode.Status.NodeName}, &k8sNode)
		if err != nil {
			if apierrors.IsNotFound(err) {
				log.Info("Kubernetes node disappeared", "nodeName", spotNode.Status.NodeName)
				spotNode.Status.Phase = karpetrackv1alpha1.SpotNodePhaseTerminating
				if err := r.Status().Update(ctx, spotNode); err != nil {
					return ctrl.Result{}, err
				}
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, err
		}

		// Count pods on this node
		var podList corev1.PodList
		if err := r.List(ctx, &podList, client.MatchingFields{"spec.nodeName": spotNode.Status.NodeName}); err == nil {
			spotNode.Status.AllocatedPods = int32(len(podList.Items))
		}
	}

	// Update current price
	price, err := r.Pricing.GetPriceForInstance(ctx, spotNode.Spec.Region, spotNode.Spec.InstanceType)
	if err == nil {
		spotNode.Status.PricePerHour = fmt.Sprintf("%.6f", price)
		now := metav1.Now()
		spotNode.Status.LastPriceCheck = &now
	}

	if err := r.Status().Update(ctx, spotNode); err != nil {
		return ctrl.Result{}, err
	}

	// Requeue for periodic health check
	return ctrl.Result{RequeueAfter: 1 * time.Minute}, nil
}

// handleDraining waits for node to be drained
func (r *SpotNodeController) handleDraining(
	ctx context.Context,
	spotNode *karpetrackv1alpha1.SpotNode,
) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	if spotNode.Status.NodeName == "" {
		// No node to drain
		spotNode.Status.Phase = karpetrackv1alpha1.SpotNodePhaseTerminating
		if err := r.Status().Update(ctx, spotNode); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Check if node still has pods (excluding daemonsets)
	var podList corev1.PodList
	if err := r.List(ctx, &podList, client.MatchingFields{"spec.nodeName": spotNode.Status.NodeName}); err != nil {
		return ctrl.Result{}, err
	}

	activePods := 0
	for _, pod := range podList.Items {
		// Skip completed pods
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		// Skip pods with controller owner that is a DaemonSet
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

	if activePods > 0 {
		log.Info("Node still has active pods", "nodeName", spotNode.Status.NodeName, "pods", activePods)
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	log.Info("Node drained, proceeding to termination", "nodeName", spotNode.Status.NodeName)

	spotNode.Status.Phase = karpetrackv1alpha1.SpotNodePhaseTerminating
	if err := r.Status().Update(ctx, spotNode); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{Requeue: true}, nil
}

// handleTerminating cleans up the node
func (r *SpotNodeController) handleTerminating(
	ctx context.Context,
	spotNode *karpetrackv1alpha1.SpotNode,
) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Delete the node from Rackspace Spot
	if spotNode.Status.ProviderID != "" {
		if err := r.SpotClient.DeleteNode(ctx, spotNode.Status.ProviderID); err != nil {
			log.Error(err, "Failed to delete node from Spot API")
			// Continue anyway - the node might already be gone
		} else {
			log.Info("Deleted node from Spot API", "providerID", spotNode.Status.ProviderID)
		}
	}

	// Delete the Kubernetes node if it exists
	if spotNode.Status.NodeName != "" {
		var k8sNode corev1.Node
		if err := r.Get(ctx, client.ObjectKey{Name: spotNode.Status.NodeName}, &k8sNode); err == nil {
			if err := r.Delete(ctx, &k8sNode); err != nil && !apierrors.IsNotFound(err) {
				log.Error(err, "Failed to delete Kubernetes node")
			} else {
				log.Info("Deleted Kubernetes node", "nodeName", spotNode.Status.NodeName)
			}
		}
	}

	r.Recorder.Eventf(spotNode, corev1.EventTypeNormal, "NodeTerminated",
		"Node terminated successfully")

	return ctrl.Result{}, nil
}

// handleDeletion handles SpotNode deletion
func (r *SpotNodeController) handleDeletion(
	ctx context.Context,
	spotNode *karpetrackv1alpha1.SpotNode,
) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(spotNode, SpotNodeFinalizer) {
		return ctrl.Result{}, nil
	}

	log.Info("Handling SpotNode deletion", "spotNode", spotNode.Name)

	// If running, need to drain first
	if spotNode.Status.Phase == karpetrackv1alpha1.SpotNodePhaseRunning {
		spotNode.Status.Phase = karpetrackv1alpha1.SpotNodePhaseDraining
		if err := r.Status().Update(ctx, spotNode); err != nil {
			return ctrl.Result{}, err
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

		return ctrl.Result{Requeue: true}, nil
	}

	// If draining, wait for it to complete
	if spotNode.Status.Phase == karpetrackv1alpha1.SpotNodePhaseDraining {
		result, err := r.handleDraining(ctx, spotNode)
		if spotNode.Status.Phase == karpetrackv1alpha1.SpotNodePhaseDraining {
			return result, err
		}
	}

	// Cleanup
	if spotNode.Status.Phase == karpetrackv1alpha1.SpotNodePhaseTerminating ||
		spotNode.Status.Phase == karpetrackv1alpha1.SpotNodePhaseDraining {
		if _, err := r.handleTerminating(ctx, spotNode); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Remove finalizer
	controllerutil.RemoveFinalizer(spotNode, SpotNodeFinalizer)
	if err := r.Update(ctx, spotNode); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// setCondition sets a condition on the SpotNode
func (r *SpotNodeController) setCondition(spotNode *karpetrackv1alpha1.SpotNode, condition metav1.Condition) {
	condition.LastTransitionTime = metav1.Now()

	// Find and update existing condition
	for i, c := range spotNode.Status.Conditions {
		if c.Type == condition.Type {
			if c.Status != condition.Status {
				spotNode.Status.Conditions[i] = condition
			}
			return
		}
	}

	// Add new condition
	spotNode.Status.Conditions = append(spotNode.Status.Conditions, condition)
}

// SetupWithManager sets up the controller with the Manager
func (r *SpotNodeController) SetupWithManager(mgr ctrl.Manager) error {
	// Index pods by nodeName for efficient lookup
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &corev1.Pod{}, "spec.nodeName", func(obj client.Object) []string {
		pod := obj.(*corev1.Pod)
		if pod.Spec.NodeName == "" {
			return nil
		}
		return []string{pod.Spec.NodeName}
	}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&karpetrackv1alpha1.SpotNode{}).
		Named("spotnode").
		Complete(r)
}
