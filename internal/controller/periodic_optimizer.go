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

const (
	// Annotation keys for tracking optimization waves
	annotationOptimizationWave   = "karpetrack.io/optimization-wave"
	annotationPendingRemoval     = "karpetrack.io/pending-removal"
	annotationPendingRemovalTime = "karpetrack.io/pending-removal-time"
	annotationReplacementWave    = "karpetrack.io/replacement-wave"
)

// PeriodicOptimizerController performs cluster-wide optimization on a schedule
// with two phases:
// Phase 1: Create new optimal nodes (add capacity)
// Phase 2: Remove old nodes after a delay (remove old capacity)
type PeriodicOptimizerController struct {
	client.Client
	SpotClient *spot.Client
	Pricing    *spot.PricingProvider
	Optimizer  *scheduler.CostOptimizer
	Recorder   record.EventRecorder

	// OptimizationInterval is how often to run optimization checks (default: 30 minutes)
	OptimizationInterval time.Duration

	// RemovalDelay is how long to wait after adding new nodes before removing old ones (default: 20 minutes)
	RemovalDelay time.Duration

	// Threshold is the minimum savings percentage required to trigger replacement (default: 20%)
	Threshold float64

	// currentWave tracks the current optimization wave ID
	currentWave string

	// phase1CompleteTime tracks when phase 1 completed for the current wave
	phase1CompleteTime time.Time
}

// NewPeriodicOptimizerController creates a new periodic optimizer controller
func NewPeriodicOptimizerController(
	client client.Client,
	spotClient *spot.Client,
	recorder record.EventRecorder,
) *PeriodicOptimizerController {
	pricing := spotClient.GetPricing()
	return &PeriodicOptimizerController{
		Client:               client,
		SpotClient:           spotClient,
		Pricing:              pricing,
		Optimizer:            scheduler.NewCostOptimizer(pricing),
		Recorder:             recorder,
		OptimizationInterval: 30 * time.Minute,
		RemovalDelay:         20 * time.Minute,
		Threshold:            0.20,
	}
}

// Start runs the periodic optimization loop
func (r *PeriodicOptimizerController) Start(ctx context.Context) error {
	log := log.FromContext(ctx).WithName("periodic-optimizer")
	log.Info("Starting periodic optimizer",
		"interval", r.OptimizationInterval,
		"removalDelay", r.RemovalDelay,
		"threshold", r.Threshold)

	// Check every minute to handle both phase 1 and phase 2 timing
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	// Track when we last ran phase 1
	var lastPhase1 time.Time

	for {
		select {
		case <-ctx.Done():
			log.Info("Stopping periodic optimizer")
			return nil
		case now := <-ticker.C:
			// Check if we need to run phase 2 (removal of old nodes)
			if err := r.runPhase2IfNeeded(ctx, now); err != nil {
				log.Error(err, "Error in phase 2 (removal)")
			}

			// Check if it's time for a new optimization cycle (phase 1)
			if now.Sub(lastPhase1) >= r.OptimizationInterval {
				log.Info("Starting optimization cycle (phase 1)")
				if err := r.runPhase1(ctx, now); err != nil {
					log.Error(err, "Error in phase 1 (optimization)")
				} else {
					lastPhase1 = now
				}
			}
		}
	}
}

// runPhase1 performs cluster-wide optimization and creates new nodes if cheaper
func (r *PeriodicOptimizerController) runPhase1(ctx context.Context, now time.Time) error {
	log := log.FromContext(ctx)

	// Generate a wave ID for this optimization cycle
	waveID := fmt.Sprintf("wave-%d", now.Unix())
	log.Info("Phase 1: Checking for cheaper cluster configuration", "wave", waveID)

	// Get all SpotNodePools
	var nodePools karpetrackv1alpha1.SpotNodePoolList
	if err := r.List(ctx, &nodePools); err != nil {
		return fmt.Errorf("listing SpotNodePools: %w", err)
	}

	if len(nodePools.Items) == 0 {
		log.Info("No SpotNodePools found, skipping optimization")
		return nil
	}

	// Process each NodePool separately
	for _, nodePool := range nodePools.Items {
		if err := r.optimizeNodePool(ctx, &nodePool, waveID, now); err != nil {
			log.Error(err, "Failed to optimize NodePool", "nodePool", nodePool.Name)
			continue
		}
	}

	r.currentWave = waveID
	r.phase1CompleteTime = now

	return nil
}

// optimizeNodePool checks if a specific NodePool can be optimized
func (r *PeriodicOptimizerController) optimizeNodePool(
	ctx context.Context,
	nodePool *karpetrackv1alpha1.SpotNodePool,
	waveID string,
	now time.Time,
) error {
	log := log.FromContext(ctx)

	// Get all running SpotNodes for this pool
	var allSpotNodes karpetrackv1alpha1.SpotNodeList
	if err := r.List(ctx, &allSpotNodes); err != nil {
		return fmt.Errorf("listing SpotNodes: %w", err)
	}

	var currentNodes []karpetrackv1alpha1.SpotNode
	for _, sn := range allSpotNodes.Items {
		if sn.Spec.NodePoolRef == nodePool.Name &&
			sn.Status.Phase == karpetrackv1alpha1.SpotNodePhaseRunning {
			// Skip nodes that are already pending removal from a previous wave
			if sn.Annotations != nil && sn.Annotations[annotationPendingRemoval] == "true" {
				continue
			}
			currentNodes = append(currentNodes, sn)
		}
	}

	if len(currentNodes) == 0 {
		log.V(1).Info("No running nodes in pool", "nodePool", nodePool.Name)
		return nil
	}

	// Get all pods currently running on these nodes
	podReqs, err := r.getPodsOnNodes(ctx, currentNodes)
	if err != nil {
		return fmt.Errorf("getting pods on nodes: %w", err)
	}

	if len(podReqs) == 0 {
		log.V(1).Info("No pods on nodes in pool", "nodePool", nodePool.Name)
		return nil
	}

	// Calculate current cost
	var currentCost float64
	currentNodeRecs := make([]scheduler.NodeRecommendation, len(currentNodes))
	for i, node := range currentNodes {
		price, err := parsePrice(node.Status.PricePerHour)
		if err != nil {
			log.V(1).Info("Failed to parse price for node", "node", node.Name, "price", node.Status.PricePerHour)
			price = 0
		}
		currentCost += price
		currentNodeRecs[i] = scheduler.NodeRecommendation{
			Region:       node.Spec.Region,
			InstanceType: node.Spec.InstanceType,
			PricePerHour: price,
		}
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
	optimal, savings, err := r.Optimizer.FindCheaperConfiguration(
		ctx,
		currentNodeRecs,
		podReqs,
		constraints,
		r.Threshold,
	)
	if err != nil {
		return fmt.Errorf("finding cheaper configuration: %w", err)
	}

	if optimal == nil {
		log.Info("No cheaper configuration found",
			"nodePool", nodePool.Name,
			"currentCost", fmt.Sprintf("$%.4f/hr", currentCost),
			"savings", fmt.Sprintf("%.1f%%", savings*100))
		return nil
	}

	log.Info("Found cheaper configuration!",
		"nodePool", nodePool.Name,
		"currentCost", fmt.Sprintf("$%.4f/hr", currentCost),
		"newCost", fmt.Sprintf("$%.4f/hr", optimal.TotalCost),
		"savings", fmt.Sprintf("%.1f%%", savings*100),
		"currentNodes", len(currentNodes),
		"newNodes", len(optimal.Nodes))

	// Phase 1: Create new nodes first
	for _, nodeRec := range optimal.Nodes {
		newNode := &karpetrackv1alpha1.SpotNode{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: fmt.Sprintf("karpetrack-%s-", nodePool.Name),
				Labels: map[string]string{
					"karpetrack.io/nodepool": nodePool.Name,
					"karpetrack.io/region":   nodeRec.Region,
					"karpetrack.io/category": sanitizeLabelValue(nodeRec.Category),
					"karpetrack.io/reason":   "periodic-optimization",
				},
				Annotations: map[string]string{
					annotationOptimizationWave: waveID,
				},
			},
			Spec: karpetrackv1alpha1.SpotNodeSpec{
				NodePoolRef:  nodePool.Name,
				InstanceType: nodeRec.InstanceType,
				Region:       nodeRec.Region,
				Resources: karpetrackv1alpha1.SpotNodeResources{
					CPU:    &nodeRec.CPU,
					Memory: &nodeRec.Memory,
				},
				BidPrice: fmt.Sprintf("%.6f", nodeRec.PricePerHour*1.1),
			},
		}

		if err := r.Create(ctx, newNode); err != nil {
			return fmt.Errorf("creating replacement node: %w", err)
		}

		log.Info("Created replacement node (phase 1)",
			"node", newNode.Name,
			"instanceType", nodeRec.InstanceType,
			"region", nodeRec.Region,
			"price", fmt.Sprintf("$%.4f/hr", nodeRec.PricePerHour),
			"wave", waveID)
	}

	// Mark old nodes as pending removal (but don't drain yet)
	removalTime := now.Add(r.RemovalDelay).Format(time.RFC3339)
	for _, oldNode := range currentNodes {
		nodeCopy := oldNode.DeepCopy()
		if nodeCopy.Annotations == nil {
			nodeCopy.Annotations = make(map[string]string)
		}
		nodeCopy.Annotations[annotationPendingRemoval] = "true"
		nodeCopy.Annotations[annotationPendingRemovalTime] = removalTime
		nodeCopy.Annotations[annotationReplacementWave] = waveID

		if err := r.Update(ctx, nodeCopy); err != nil {
			log.Error(err, "Failed to mark node for pending removal", "node", oldNode.Name)
			continue
		}

		log.Info("Marked node for pending removal (phase 1)",
			"node", oldNode.Name,
			"removalTime", removalTime,
			"wave", waveID)

		r.Recorder.Eventf(&oldNode, corev1.EventTypeNormal, "PendingReplacement",
			"Node marked for replacement in wave %s, removal scheduled for %s", waveID, removalTime)
	}

	r.Recorder.Eventf(nodePool, corev1.EventTypeNormal, "OptimizationStarted",
		"Optimization wave %s: replacing %d nodes with %d cheaper nodes (saving %.1f%%)",
		waveID, len(currentNodes), len(optimal.Nodes), savings*100)

	return nil
}

// runPhase2IfNeeded checks for nodes pending removal and drains them after the delay
func (r *PeriodicOptimizerController) runPhase2IfNeeded(ctx context.Context, now time.Time) error {
	log := log.FromContext(ctx)

	// Get all SpotNodes with pending removal annotation
	var spotNodes karpetrackv1alpha1.SpotNodeList
	if err := r.List(ctx, &spotNodes); err != nil {
		return fmt.Errorf("listing SpotNodes: %w", err)
	}

	for _, node := range spotNodes.Items {
		if node.Annotations == nil {
			continue
		}

		if node.Annotations[annotationPendingRemoval] != "true" {
			continue
		}

		// Skip if already draining or beyond
		if node.Status.Phase == karpetrackv1alpha1.SpotNodePhaseDraining ||
			node.Status.Phase == karpetrackv1alpha1.SpotNodePhaseTerminating {
			continue
		}

		// Check if it's time to remove
		removalTimeStr := node.Annotations[annotationPendingRemovalTime]
		if removalTimeStr == "" {
			continue
		}

		removalTime, err := time.Parse(time.RFC3339, removalTimeStr)
		if err != nil {
			log.Error(err, "Failed to parse removal time", "node", node.Name)
			continue
		}

		if now.Before(removalTime) {
			// Not time yet
			continue
		}

		// Check if replacement nodes from this wave are running
		waveID := node.Annotations[annotationReplacementWave]
		if waveID != "" {
			replacementsReady, err := r.areReplacementNodesReady(ctx, waveID)
			if err != nil {
				log.Error(err, "Failed to check replacement nodes", "wave", waveID)
				continue
			}

			if !replacementsReady {
				log.Info("Waiting for replacement nodes to be ready before removing old node",
					"node", node.Name,
					"wave", waveID)
				continue
			}
		}

		// Phase 2: Start draining the old node
		log.Info("Phase 2: Starting drain of old node",
			"node", node.Name,
			"wave", waveID)

		patch := client.MergeFrom(node.DeepCopy())
		node.Status.Phase = karpetrackv1alpha1.SpotNodePhaseDraining

		if err := r.Status().Patch(ctx, &node, patch); err != nil {
			log.Error(err, "Failed to set node to draining", "node", node.Name)
			continue
		}

		// Cordon the Kubernetes node
		if node.Status.NodeName != "" {
			var k8sNode corev1.Node
			if err := r.Get(ctx, client.ObjectKey{Name: node.Status.NodeName}, &k8sNode); err == nil {
				if !k8sNode.Spec.Unschedulable {
					k8sNode.Spec.Unschedulable = true
					if err := r.Update(ctx, &k8sNode); err != nil {
						log.Error(err, "Failed to cordon node", "nodeName", node.Status.NodeName)
					} else {
						log.Info("Cordoned node for replacement", "nodeName", node.Status.NodeName)
					}
				}
			}
		}

		r.Recorder.Eventf(&node, corev1.EventTypeNormal, "DrainingStarted",
			"Phase 2: Node draining started after removal delay")
	}

	return nil
}

// areReplacementNodesReady checks if all nodes from a wave are running
func (r *PeriodicOptimizerController) areReplacementNodesReady(ctx context.Context, waveID string) (bool, error) {
	var spotNodes karpetrackv1alpha1.SpotNodeList
	if err := r.List(ctx, &spotNodes); err != nil {
		return false, err
	}

	var waveNodes []karpetrackv1alpha1.SpotNode
	for _, node := range spotNodes.Items {
		if node.Annotations != nil && node.Annotations[annotationOptimizationWave] == waveID {
			waveNodes = append(waveNodes, node)
		}
	}

	if len(waveNodes) == 0 {
		// No replacement nodes found for this wave - might have been cleaned up
		return true, nil
	}

	for _, node := range waveNodes {
		if node.Status.Phase != karpetrackv1alpha1.SpotNodePhaseRunning {
			return false, nil
		}
	}

	return true, nil
}

// getPodsOnNodes returns pod requirements for all pods running on the given nodes
func (r *PeriodicOptimizerController) getPodsOnNodes(
	ctx context.Context,
	nodes []karpetrackv1alpha1.SpotNode,
) ([]scheduler.PodRequirements, error) {
	// Build a set of node names
	nodeNames := make(map[string]bool)
	for _, node := range nodes {
		if node.Status.NodeName != "" {
			nodeNames[node.Status.NodeName] = true
		}
	}

	if len(nodeNames) == 0 {
		return nil, nil
	}

	// List all pods
	var podList corev1.PodList
	if err := r.List(ctx, &podList); err != nil {
		return nil, err
	}

	var podReqs []scheduler.PodRequirements
	for i := range podList.Items {
		pod := &podList.Items[i]

		// Skip pods not on our nodes
		if !nodeNames[pod.Spec.NodeName] {
			continue
		}

		// Skip completed/failed pods
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}

		// Skip daemonset pods (they'll run on any node)
		if isDaemonSetPod(pod) {
			continue
		}

		podReqs = append(podReqs, scheduler.GetPodRequirements(pod))
	}

	return podReqs, nil
}

// isDaemonSetPod checks if a pod is owned by a DaemonSet
func isDaemonSetPod(pod *corev1.Pod) bool {
	for _, owner := range pod.OwnerReferences {
		if owner.Kind == "DaemonSet" {
			return true
		}
	}
	return false
}

// SetupWithManager sets up the periodic optimizer as a runnable
func (r *PeriodicOptimizerController) SetupWithManager(mgr ctrl.Manager) error {
	return mgr.Add(manager.RunnableFunc(r.Start))
}
