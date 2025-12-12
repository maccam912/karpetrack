package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"

	rxtspot "github.com/rackspace-spot/spot-go-sdk/api/v1"

	"github.com/maccam912/karpetrack/internal/scheduler"
	"github.com/maccam912/karpetrack/internal/spot"
)

type Config struct {
	Kubeconfig   string
	Namespaces   []string
	Categories   []string
	MaxPrice     float64
	Output       string
	Apply        bool
	RefreshToken string
	Org          string
	Cloudspace   string
	DryRun       bool
}

type OutputResult struct {
	PodCount          int          `json:"podCount"`
	NamespaceCount    int          `json:"namespaceCount"`
	TotalCPUMillis    int64        `json:"totalCpuMillis"`
	TotalMemoryBytes  int64        `json:"totalMemoryBytes"`
	Nodes             []NodeOutput `json:"nodes"`
	TotalCostPerHour  float64      `json:"totalCostPerHour"`
	TotalCostPerMonth float64      `json:"totalCostPerMonth"`
}

type NodeOutput struct {
	InstanceType string  `json:"instanceType"`
	Category     string  `json:"category"`
	Region       string  `json:"region"`
	CPU          int64   `json:"cpu"`
	MemoryGB     int64   `json:"memoryGb"`
	PricePerHour float64 `json:"pricePerHour"`
	PodCount     int     `json:"podCount"`
}

// NodePoolPlan represents the desired state for a node pool
type NodePoolPlan struct {
	ServerClass string
	Count       int
	BidPrice    float64
}

func main() {
	var config Config
	var namespacesFlag string
	var categoriesFlag string

	// Determine default kubeconfig path
	defaultKubeconfig := ""
	if home := homedir.HomeDir(); home != "" {
		defaultKubeconfig = filepath.Join(home, ".kube", "config")
	}

	flag.StringVar(&config.Kubeconfig, "kubeconfig", defaultKubeconfig, "Path to kubeconfig file")
	flag.StringVar(&namespacesFlag, "namespace", "", "Filter to specific namespace(s), comma-separated (default: all)")
	flag.StringVar(&categoriesFlag, "categories", "gp,ch,mh", "Allowed instance categories, comma-separated")
	flag.Float64Var(&config.MaxPrice, "max-price", 0, "Maximum price per node per hour (0 = no limit)")
	flag.StringVar(&config.Output, "output", "table", "Output format: table or json")

	// Apply flags
	flag.BoolVar(&config.Apply, "apply", false, "Apply the optimal configuration by creating/updating node pools")
	flag.BoolVar(&config.DryRun, "dry-run", false, "Show what would be applied without making changes")
	flag.StringVar(&config.RefreshToken, "refresh-token", os.Getenv("RACKSPACE_SPOT_REFRESH_TOKEN"), "Rackspace Spot refresh token")
	flag.StringVar(&config.Org, "org", os.Getenv("RACKSPACE_SPOT_ORG"), "Rackspace Spot organization name")
	flag.StringVar(&config.Cloudspace, "cloudspace", os.Getenv("RACKSPACE_SPOT_CLOUDSPACE"), "Rackspace Spot cloudspace name")
	flag.Parse()

	// Parse comma-separated values
	if namespacesFlag != "" {
		config.Namespaces = strings.Split(namespacesFlag, ",")
	}
	if categoriesFlag != "" {
		config.Categories = strings.Split(categoriesFlag, ",")
	}

	if err := run(config); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(config Config) error {
	ctx := context.Background()

	// Build Kubernetes client
	kubeConfig, err := clientcmd.BuildConfigFromFlags("", config.Kubeconfig)
	if err != nil {
		return fmt.Errorf("building kubeconfig: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		return fmt.Errorf("creating kubernetes client: %w", err)
	}

	// Fetch pods
	pods, namespaces, err := fetchPods(ctx, clientset, config.Namespaces)
	if err != nil {
		return fmt.Errorf("fetching pods: %w", err)
	}

	if len(pods) == 0 {
		fmt.Println("No running pods found.")
		return nil
	}

	// Extract requirements from pods
	podReqs := make([]scheduler.PodRequirements, 0, len(pods))
	for _, pod := range pods {
		podReqs = append(podReqs, scheduler.GetPodRequirements(pod))
	}

	// Create pricing provider and cost optimizer
	pricing := spot.NewPricingProvider()
	optimizer := scheduler.NewCostOptimizer(pricing)

	// Set up constraints
	constraints := scheduler.OptimizationConstraints{
		Categories:      config.Categories,
		MaxPricePerNode: config.MaxPrice,
	}

	// Find optimal configuration
	result, err := optimizer.FindOptimalConfiguration(ctx, podReqs, constraints)
	if err != nil {
		return fmt.Errorf("finding optimal configuration: %w", err)
	}

	// Prepare output
	output := OutputResult{
		PodCount:          len(pods),
		NamespaceCount:    len(namespaces),
		TotalCPUMillis:    result.TotalCPU.MilliValue(),
		TotalMemoryBytes:  result.TotalMemory.Value(),
		Nodes:             make([]NodeOutput, 0, len(result.Nodes)),
		TotalCostPerHour:  result.TotalCost,
		TotalCostPerMonth: result.TotalCost * 24 * 30,
	}

	for _, node := range result.Nodes {
		output.Nodes = append(output.Nodes, NodeOutput{
			InstanceType: node.InstanceType,
			Category:     node.Category,
			Region:       node.Region,
			CPU:          node.CPU.Value(),
			MemoryGB:     node.Memory.Value() / (1024 * 1024 * 1024),
			PricePerHour: node.PricePerHour,
			PodCount:     node.PodCount,
		})
	}

	// Print output
	if config.Output == "json" {
		if err := printJSON(output); err != nil {
			return err
		}
	} else {
		if err := printTable(output); err != nil {
			return err
		}
	}

	// Apply if requested
	if config.Apply || config.DryRun {
		return applyConfiguration(ctx, config, result)
	}

	return nil
}

func applyConfiguration(ctx context.Context, config Config, result *scheduler.OptimizationResult) error {
	// Validate required configuration
	if config.RefreshToken == "" {
		return fmt.Errorf("--refresh-token or RACKSPACE_SPOT_REFRESH_TOKEN is required for apply")
	}
	if config.Org == "" {
		return fmt.Errorf("--org or RACKSPACE_SPOT_ORG is required for apply")
	}
	if config.Cloudspace == "" {
		return fmt.Errorf("--cloudspace or RACKSPACE_SPOT_CLOUDSPACE is required for apply")
	}

	// Create Rackspace Spot client
	spotClient, err := rxtspot.NewSpotClient(&rxtspot.Config{
		RefreshToken: config.RefreshToken,
	})
	if err != nil {
		return fmt.Errorf("creating Rackspace Spot client: %w", err)
	}

	// Authenticate
	if _, err := spotClient.Authenticate(ctx); err != nil {
		return fmt.Errorf("authenticating with Rackspace Spot: %w", err)
	}

	// Build the desired node pool plan from optimization result
	desiredPools := buildNodePoolPlan(result)

	// Get existing node pools
	existingPools, err := spotClient.ListSpotNodePools(ctx, config.Org, config.Cloudspace)
	if err != nil {
		return fmt.Errorf("listing existing node pools: %w", err)
	}

	// Build a map of existing pools by server class
	existingByClass := make(map[string]*rxtspot.SpotNodePool)
	for _, pool := range existingPools {
		existingByClass[pool.ServerClass] = pool
	}

	fmt.Println()
	fmt.Println("═══════════════════════════════════════════════════════════════")
	if config.DryRun {
		fmt.Println("                      DRY RUN - No changes will be made")
	} else {
		fmt.Println("                      APPLYING CONFIGURATION")
	}
	fmt.Println("═══════════════════════════════════════════════════════════════")
	fmt.Println()

	// Track which pools we've handled
	handledPools := make(map[string]bool)

	// Create or update desired pools
	for serverClass, plan := range desiredPools {
		handledPools[serverClass] = true

		existing, exists := existingByClass[serverClass]
		if exists {
			// Update existing pool if count differs
			if existing.Desired != plan.Count {
				fmt.Printf("📝 UPDATE: %s (desired: %d → %d)\n", serverClass, existing.Desired, plan.Count)
				if !config.DryRun {
					err := spotClient.UpdateSpotNodePool(ctx, config.Org, rxtspot.SpotNodePool{
						Name:       existing.Name,
						Cloudspace: config.Cloudspace,
						Desired:    plan.Count,
						BidPrice:   fmt.Sprintf("$%.4f", plan.BidPrice),
					})
					if err != nil {
						return fmt.Errorf("updating node pool %s: %w", existing.Name, err)
					}
					fmt.Printf("   ✓ Updated successfully\n")
				}
			} else {
				fmt.Printf("✓ OK: %s (desired: %d)\n", serverClass, plan.Count)
			}
		} else {
			// Create new pool
			poolName := fmt.Sprintf("karpetrack-%s", sanitizeName(serverClass))
			fmt.Printf("➕ CREATE: %s (name: %s, desired: %d, bid: $%.4f)\n",
				serverClass, poolName, plan.Count, plan.BidPrice)
			if !config.DryRun {
				err := spotClient.CreateSpotNodePool(ctx, config.Org, rxtspot.SpotNodePool{
					Name:        poolName,
					Cloudspace:  config.Cloudspace,
					ServerClass: serverClass,
					Desired:     plan.Count,
					BidPrice:    fmt.Sprintf("$%.4f", plan.BidPrice),
				})
				if err != nil {
					return fmt.Errorf("creating node pool %s: %w", poolName, err)
				}
				fmt.Printf("   ✓ Created successfully\n")
			}
		}
	}

	// Scale down or delete unused pools that were managed by karpetrack
	for serverClass, pool := range existingByClass {
		if !handledPools[serverClass] && strings.HasPrefix(pool.Name, "karpetrack-") {
			fmt.Printf("➖ DELETE: %s (name: %s)\n", serverClass, pool.Name)
			if !config.DryRun {
				err := spotClient.DeleteSpotNodePool(ctx, config.Org, pool.Name)
				if err != nil {
					return fmt.Errorf("deleting node pool %s: %w", pool.Name, err)
				}
				fmt.Printf("   ✓ Deleted successfully\n")
			}
		}
	}

	fmt.Println()
	if config.DryRun {
		fmt.Println("Dry run complete. Use --apply to make changes.")
	} else {
		fmt.Println("Configuration applied successfully!")
	}

	return nil
}

// buildNodePoolPlan converts optimization results to a map of server class -> count
func buildNodePoolPlan(result *scheduler.OptimizationResult) map[string]NodePoolPlan {
	plan := make(map[string]NodePoolPlan)

	for _, node := range result.Nodes {
		existing := plan[node.InstanceType]
		existing.ServerClass = node.InstanceType
		existing.Count++
		existing.BidPrice = node.PricePerHour * 1.1 // Bid 10% above market price
		plan[node.InstanceType] = existing
	}

	return plan
}

// sanitizeName converts a server class name to a valid Kubernetes name
func sanitizeName(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, ".", "-")
	s = strings.ReplaceAll(s, "_", "-")
	// Truncate if too long
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

func fetchPods(ctx context.Context, clientset *kubernetes.Clientset, namespaces []string) ([]*corev1.Pod, map[string]bool, error) {
	var pods []*corev1.Pod
	namespaceSet := make(map[string]bool)

	if len(namespaces) == 0 {
		// Fetch from all namespaces
		podList, err := clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{
			FieldSelector: "status.phase=Running",
		})
		if err != nil {
			return nil, nil, err
		}
		for i := range podList.Items {
			pod := &podList.Items[i]
			pods = append(pods, pod)
			namespaceSet[pod.Namespace] = true
		}
	} else {
		for _, ns := range namespaces {
			podList, err := clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
				FieldSelector: "status.phase=Running",
			})
			if err != nil {
				return nil, nil, err
			}
			for i := range podList.Items {
				pod := &podList.Items[i]
				pods = append(pods, pod)
				namespaceSet[pod.Namespace] = true
			}
		}
	}

	return pods, namespaceSet, nil
}

func printJSON(output OutputResult) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(output)
}

func printTable(output OutputResult) error {
	fmt.Println()
	fmt.Println("╔═══════════════════════════════════════════════════════════════╗")
	fmt.Println("║           Karpetrack Instance Optimizer                       ║")
	fmt.Println("╚═══════════════════════════════════════════════════════════════╝")
	fmt.Println()

	totalCPU := float64(output.TotalCPUMillis) / 1000
	totalMemGB := float64(output.TotalMemoryBytes) / (1024 * 1024 * 1024)

	fmt.Printf("Analyzed %d pods across %d namespaces\n", output.PodCount, output.NamespaceCount)
	fmt.Printf("Total resource requirements: %.1f CPU, %.1f GB memory\n", totalCPU, totalMemGB)
	fmt.Println()

	if len(output.Nodes) == 0 {
		fmt.Println("No optimal configuration found.")
		return nil
	}

	fmt.Println("Optimal Instance Configuration:")
	fmt.Println("┌──────────────────────┬──────────────────┬──────────┬───────────────────┬──────────────┬──────────┐")
	fmt.Println("│ Instance Type        │ Category         │ Region   │ Resources         │ Price/Hour   │ Pods     │")
	fmt.Println("├──────────────────────┼──────────────────┼──────────┼───────────────────┼──────────────┼──────────┤")

	for _, node := range output.Nodes {
		resources := fmt.Sprintf("%d CPU / %d GB", node.CPU, node.MemoryGB)
		fmt.Printf("│ %-20s │ %-16s │ %-8s │ %-17s │ $%-10.4f │ %-8d │\n",
			truncate(node.InstanceType, 20),
			truncate(node.Category, 16),
			truncate(node.Region, 8),
			resources,
			node.PricePerHour,
			node.PodCount,
		)
	}

	fmt.Println("└──────────────────────┴──────────────────┴──────────┴───────────────────┴──────────────┴──────────┘")
	fmt.Println()

	// Show summary grouped by instance type
	summary := summarizeNodes(output.Nodes)
	fmt.Println("Node Pool Summary:")
	for serverClass, count := range summary {
		fmt.Printf("  • %s: %d node(s)\n", serverClass, count)
	}
	fmt.Println()

	fmt.Printf("Total estimated cost: $%.4f/hour ($%.2f/month)\n", output.TotalCostPerHour, output.TotalCostPerMonth)
	fmt.Println()

	return nil
}

func summarizeNodes(nodes []NodeOutput) map[string]int {
	summary := make(map[string]int)
	for _, node := range nodes {
		summary[node.InstanceType]++
	}
	return summary
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

// Unused import guard
var _ = sort.Strings
