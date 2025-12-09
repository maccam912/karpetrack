package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	karpetrackv1alpha1 "github.com/maccam912/karpetrack/api/v1alpha1"
	"github.com/maccam912/karpetrack/internal/scheduler"
	"github.com/maccam912/karpetrack/internal/spot"
)

// OptimizerController monitors spot prices and optimizes node placement
type OptimizerController struct {
	client.Client
	SpotClient *spot.Client
	Pricing    *spot.PricingProvider
	Optimizer  *scheduler.CostOptimizer
	Recorder   record.EventRecorder

	// Threshold is the minimum savings percentage required to trigger replacement
	// e.g., 0.20 means 20% savings required
	Threshold float64

	// CheckInterval is how often to check for optimization opportunities
	CheckInterval time.Duration
}

// NewOptimizerController creates a new optimizer controller
func NewOptimizerController(
	client client.Client,
	spotClient *spot.Client,
	recorder record.EventRecorder,
) *OptimizerController {
	pricing := spotClient.GetPricing()
	return &OptimizerController{
		Client:        client,
		SpotClient:    spotClient,
		Pricing:       pricing,
		Optimizer:     scheduler.NewCostOptimizer(pricing),
		Recorder:      recorder,
		Threshold:     0.20, // 20% savings threshold
		CheckInterval: 30 * time.Second,
	}
}

// Start runs the price monitoring loop
func (r *OptimizerController) Start(ctx context.Context) error {
	log := log.FromContext(ctx).WithName("optimizer")
	log.Info("Starting price optimizer", "threshold", r.Threshold, "interval", r.CheckInterval)

	ticker := time.NewTicker(r.CheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info("Stopping price optimizer")
			return nil
		case <-ticker.C:
			if err := r.checkForOptimizations(ctx); err != nil {
				log.Error(err, "Error checking for optimizations")
			}
		}
	}
}

// checkForOptimizations looks for nodes that can be replaced with cheaper alternatives
func (r *OptimizerController) checkForOptimizations(ctx context.Context) error {
	log := log.FromContext(ctx)

	// Get all running SpotNodes
	var spotNodes karpetrackv1alpha1.SpotNodeList
	if err := r.List(ctx, &spotNodes); err != nil {
		return fmt.Errorf("listing SpotNodes: %w", err)
	}

	for _, spotNode := range spotNodes.Items {
		if spotNode.Status.Phase != karpetrackv1alpha1.SpotNodePhaseRunning {
			continue
		}

		// Check for cheaper alternative
		shouldReplace, alternative, savings, err := r.shouldReplaceNode(ctx, &spotNode)
		if err != nil {
			log.Error(err, "Error checking node for replacement", "spotNode", spotNode.Name)
			continue
		}

		if shouldReplace && alternative != nil {
			log.Info("Found cheaper alternative",
				"spotNode", spotNode.Name,
				"currentRegion", spotNode.Spec.Region,
				"currentType", spotNode.Spec.InstanceType,
				"currentPrice", spotNode.Status.PricePerHour,
				"newRegion", alternative.Region,
				"newType", alternative.InstanceType,
				"newPrice", alternative.PricePerHour,
				"savings", fmt.Sprintf("%.1f%%", savings*100))

			if err := r.replaceNode(ctx, &spotNode, alternative); err != nil {
				log.Error(err, "Failed to replace node", "spotNode", spotNode.Name)
				continue
			}
		}
	}

	return nil
}

// shouldReplaceNode checks if a node should be replaced with a cheaper alternative
func (r *OptimizerController) shouldReplaceNode(
	ctx context.Context,
	spotNode *karpetrackv1alpha1.SpotNode,
) (bool, *spot.InstanceOption, float64, error) {
	// Verify current instance pricing is available
	_, err := r.Pricing.GetPriceForInstance(ctx, spotNode.Spec.Region, spotNode.Spec.InstanceType)
	if err != nil {
		return false, nil, 0, err
	}

	// Get CPU and memory requirements
	var cpuValue int
	var memValue int
	if spotNode.Spec.Resources.CPU != nil {
		cpuValue = int(spotNode.Spec.Resources.CPU.Value())
	}
	if spotNode.Spec.Resources.Memory != nil {
		// Convert to GB
		memValue = int(spotNode.Spec.Resources.Memory.Value() / (1024 * 1024 * 1024))
	}

	// Get the NodePool to check constraints
	var nodePool karpetrackv1alpha1.SpotNodePool
	if err := r.Get(ctx, client.ObjectKey{Name: spotNode.Spec.NodePoolRef}, &nodePool); err != nil {
		return false, nil, 0, err
	}

	// Find cheaper alternative
	alternative, savings, err := r.Pricing.FindCheaperAlternative(
		ctx,
		spotNode.Spec.Region,
		spotNode.Spec.InstanceType,
		cpuValue,
		memValue,
		r.Threshold,
	)
	if err != nil {
		return false, nil, 0, err
	}

	if alternative == nil {
		return false, nil, 0, nil // No cheaper alternative found
	}

	// Verify alternative is in allowed regions/categories
	if len(nodePool.Spec.Regions) > 0 {
		found := false
		for _, region := range nodePool.Spec.Regions {
			if region == alternative.Region {
				found = true
				break
			}
		}
		if !found {
			return false, nil, 0, nil
		}
	}

	if len(nodePool.Spec.Categories) > 0 {
		found := false
		for _, cat := range nodePool.Spec.Categories {
			if cat == alternative.Category {
				found = true
				break
			}
		}
		if !found {
			return false, nil, 0, nil
		}
	}

	return true, alternative, savings, nil
}

// replaceNode initiates replacement of a node with a cheaper alternative
func (r *OptimizerController) replaceNode(
	ctx context.Context,
	oldNode *karpetrackv1alpha1.SpotNode,
	alternative *spot.InstanceOption,
) error {
	log := log.FromContext(ctx)

	// Create a new SpotNode for the replacement
	newNode := &karpetrackv1alpha1.SpotNode{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: fmt.Sprintf("karpetrack-%s-", oldNode.Spec.NodePoolRef),
			Labels: map[string]string{
				"karpetrack.io/nodepool": oldNode.Spec.NodePoolRef,
				"karpetrack.io/region":   alternative.Region,
				"karpetrack.io/category": alternative.Category,
				"karpetrack.io/replaces": oldNode.Name,
				"karpetrack.io/reason":   "price-optimization",
			},
		},
		Spec: karpetrackv1alpha1.SpotNodeSpec{
			NodePoolRef:  oldNode.Spec.NodePoolRef,
			InstanceType: alternative.InstanceType,
			Region:       alternative.Region,
			Resources:    oldNode.Spec.Resources, // Keep same resource requirements
			BidPrice:     fmt.Sprintf("%.6f", alternative.PricePerHour*1.1),
		},
	}

	if err := r.Create(ctx, newNode); err != nil {
		return fmt.Errorf("creating replacement node: %w", err)
	}

	log.Info("Created replacement node", "newNode", newNode.Name, "replaces", oldNode.Name)

	// Record event on old node
	r.Recorder.Eventf(oldNode, corev1.EventTypeNormal, "Replacing",
		"Replacing with cheaper node %s (region: %s, type: %s)",
		newNode.Name, alternative.Region, alternative.InstanceType)

	// Mark old node for draining
	patch := client.MergeFrom(oldNode.DeepCopy())
	oldNode.Status.Phase = karpetrackv1alpha1.SpotNodePhaseDraining

	// Add annotation linking to replacement
	if oldNode.Annotations == nil {
		oldNode.Annotations = make(map[string]string)
	}
	oldNode.Annotations["karpetrack.io/replaced-by"] = newNode.Name

	if err := r.Status().Patch(ctx, oldNode, patch); err != nil {
		return fmt.Errorf("updating old node status: %w", err)
	}

	// Cordon the old Kubernetes node
	if oldNode.Status.NodeName != "" {
		var k8sNode corev1.Node
		if err := r.Get(ctx, client.ObjectKey{Name: oldNode.Status.NodeName}, &k8sNode); err == nil {
			if !k8sNode.Spec.Unschedulable {
				k8sNode.Spec.Unschedulable = true
				if err := r.Update(ctx, &k8sNode); err != nil {
					log.Error(err, "Failed to cordon node", "nodeName", oldNode.Status.NodeName)
				} else {
					log.Info("Cordoned node for replacement", "nodeName", oldNode.Status.NodeName)
				}
			}
		}
	}

	r.Recorder.Eventf(oldNode, corev1.EventTypeNormal, "Draining",
		"Node marked for draining - replacement %s is provisioning", newNode.Name)

	return nil
}

// SetupWithManager sets up the optimizer as a runnable
func (r *OptimizerController) SetupWithManager(mgr ctrl.Manager) error {
	return mgr.Add(manager.RunnableFunc(r.Start))
}
