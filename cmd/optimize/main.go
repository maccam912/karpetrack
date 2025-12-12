package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"

	"github.com/maccam912/karpetrack/internal/scheduler"
	"github.com/maccam912/karpetrack/internal/spot"
)

type Config struct {
	Kubeconfig string
	Namespaces []string
	Categories []string
	MaxPrice   float64
	Output     string
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
		return printJSON(output)
	}
	return printTable(output)
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
	fmt.Printf("Total estimated cost: $%.4f/hour ($%.2f/month)\n", output.TotalCostPerHour, output.TotalCostPerMonth)
	fmt.Println()

	return nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}
