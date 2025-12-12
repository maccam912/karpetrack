package spot

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
)

// TestPricingProvider_FetchAllPricing tests fetching and parsing real pricing data from S3
func TestPricingProvider_FetchAllPricing(t *testing.T) {
	ctx := context.Background()
	provider := NewPricingProvider()

	pricing, err := provider.GetPricing(ctx)
	if err != nil {
		t.Fatalf("Failed to fetch pricing data: %v", err)
	}

	if pricing == nil {
		t.Fatal("Pricing data is nil")
	}

	if len(pricing.Regions) == 0 {
		t.Fatal("No regions found in pricing data")
	}

	// Validate structure
	for regionName, region := range pricing.Regions {
		if region.Generation == "" {
			t.Errorf("Region %s has empty generation", regionName)
		}

		if len(region.ServerClasses) == 0 {
			t.Errorf("Region %s has no server classes", regionName)
		}

		for instanceType, serverClass := range region.ServerClasses {
			if serverClass.DisplayName == "" {
				t.Errorf("Instance %s in region %s has empty display name", instanceType, regionName)
			}
			if serverClass.Category == "" {
				t.Errorf("Instance %s in region %s has empty category", instanceType, regionName)
			}
			if serverClass.CPU <= 0 {
				t.Errorf("Instance %s in region %s has invalid CPU: %d", instanceType, regionName, serverClass.CPU)
			}
			if serverClass.Memory <= 0 {
				t.Errorf("Instance %s in region %s has invalid memory: %d", instanceType, regionName, serverClass.Memory)
			}
			if serverClass.MarketPrice == "" {
				t.Errorf("Instance %s in region %s has empty market price", instanceType, regionName)
			}
		}
	}

	t.Logf("Successfully fetched pricing data with %d regions", len(pricing.Regions))
}

// TestPricingProvider_DisplayAllInstances displays all available instances with pricing info
func TestPricingProvider_DisplayAllInstances(t *testing.T) {
	ctx := context.Background()
	provider := NewPricingProvider()

	pricing, err := provider.GetPricing(ctx)
	if err != nil {
		t.Fatalf("Failed to fetch pricing data: %v", err)
	}

	// Get sorted list of regions
	regions := make([]string, 0, len(pricing.Regions))
	for region := range pricing.Regions {
		regions = append(regions, region)
	}
	sort.Strings(regions)

	var totalInstances int

	t.Log("")
	t.Log("===============================================================================")
	t.Log("RACKSPACE SPOT PRICING - All Instance Types")
	t.Logf("Minimum Bid (all instances): $%.4f/hr", MinimumBidPrice)
	t.Log("===============================================================================")

	for _, regionName := range regions {
		region := pricing.Regions[regionName]

		// Get sorted list of instance types
		instanceTypes := make([]string, 0, len(region.ServerClasses))
		for instanceType := range region.ServerClasses {
			instanceTypes = append(instanceTypes, instanceType)
		}
		sort.Strings(instanceTypes)

		t.Log("")
		t.Logf("Region: %s (Generation: %s)", regionName, region.Generation)
		t.Log("------------------------------------------------------------------------------")
		t.Logf("%-35s %-18s %5s %8s %14s %10s", "Instance Type", "Category", "CPU", "Memory", "Market Price", "Min Bid")
		t.Log("------------------------------------------------------------------------------")

		for _, instanceType := range instanceTypes {
			serverClass := region.ServerClasses[instanceType]
			t.Logf("%-35s %-18s %5d %6dGB $%12s $%8.4f",
				instanceType,
				serverClass.Category,
				serverClass.CPU,
				serverClass.Memory,
				serverClass.MarketPrice,
				MinimumBidPrice,
			)
			totalInstances++
		}
	}

	t.Log("")
	t.Log("===============================================================================")
	t.Logf("Summary: %d instance types across %d regions", totalInstances, len(regions))
	t.Log("===============================================================================")
}

// TestPricingProvider_GetInstanceTypes tests retrieving all instance options
func TestPricingProvider_GetInstanceTypes(t *testing.T) {
	ctx := context.Background()
	provider := NewPricingProvider()

	options, err := provider.GetInstanceTypes(ctx)
	if err != nil {
		t.Fatalf("Failed to get instance types: %v", err)
	}

	if len(options) == 0 {
		t.Fatal("No instance types returned")
	}

	// Validate all options have required fields
	for _, opt := range options {
		if opt.Region == "" {
			t.Errorf("Instance %s has empty region", opt.InstanceType)
		}
		if opt.InstanceType == "" {
			t.Error("Found instance with empty instance type")
		}
		if opt.CPU <= 0 {
			t.Errorf("Instance %s has invalid CPU: %d", opt.InstanceType, opt.CPU)
		}
		if opt.MemoryGB <= 0 {
			t.Errorf("Instance %s has invalid memory: %d GB", opt.InstanceType, opt.MemoryGB)
		}
		if opt.PricePerHour <= 0 {
			t.Errorf("Instance %s has invalid price: %f", opt.InstanceType, opt.PricePerHour)
		}
	}

	t.Logf("Retrieved %d instance options", len(options))
}

// TestPricingProvider_QueryByRequirements tests filtering instances by various requirements
func TestPricingProvider_QueryByRequirements(t *testing.T) {
	ctx := context.Background()
	provider := NewPricingProvider()

	tests := []struct {
		name string
		reqs Requirements
	}{
		{
			name: "minimum 4 CPU",
			reqs: Requirements{
				MinCPU: resource.MustParse("4"),
			},
		},
		{
			name: "minimum 8GB memory",
			reqs: Requirements{
				MinMemory: resource.MustParse("8Gi"),
			},
		},
		{
			name: "4 CPU and 8GB memory",
			reqs: Requirements{
				MinCPU:    resource.MustParse("4"),
				MinMemory: resource.MustParse("8Gi"),
			},
		},
		{
			name: "General Purpose category",
			reqs: Requirements{
				Categories: []string{"General Purpose"},
			},
		},
		{
			name: "gp category alias",
			reqs: Requirements{
				Categories: []string{"gp"},
			},
		},
		{
			name: "Compute Heavy category",
			reqs: Requirements{
				Categories: []string{"ch"},
			},
		},
		{
			name: "max price $0.05/hr",
			reqs: Requirements{
				MaxPrice: 0.05,
			},
		},
		{
			name: "combined requirements",
			reqs: Requirements{
				MinCPU:     resource.MustParse("4"),
				MinMemory:  resource.MustParse("8Gi"),
				Categories: []string{"gp", "ch"},
				MaxPrice:   0.10,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			options, err := provider.GetCheapestInstances(ctx, tt.reqs)
			if err != nil {
				t.Fatalf("Failed to get cheapest instances: %v", err)
			}

			t.Logf("Found %d instances matching requirements", len(options))

			// Verify results are sorted by price
			for i := 1; i < len(options); i++ {
				if options[i].PricePerHour < options[i-1].PricePerHour {
					t.Errorf("Results not sorted by price: %f < %f",
						options[i].PricePerHour, options[i-1].PricePerHour)
				}
			}

			// Display first 5 results
			displayCount := 5
			if len(options) < displayCount {
				displayCount = len(options)
			}

			if displayCount > 0 {
				t.Log("Top results:")
				for i := 0; i < displayCount; i++ {
					opt := options[i]
					t.Logf("  %d. %s (%s) - %d CPU, %dGB, $%.4f/hr",
						i+1, opt.InstanceType, opt.Region, opt.CPU, opt.MemoryGB, opt.PricePerHour)
				}
			}
		})
	}
}

// TestPricingProvider_Regions tests retrieving all available regions
func TestPricingProvider_Regions(t *testing.T) {
	ctx := context.Background()
	provider := NewPricingProvider()

	regions, err := provider.GetRegions(ctx)
	if err != nil {
		t.Fatalf("Failed to get regions: %v", err)
	}

	if len(regions) == 0 {
		t.Fatal("No regions returned")
	}

	t.Logf("Available regions (%d):", len(regions))
	for _, region := range regions {
		t.Logf("  - %s", region)
	}
}

// TestPricingProvider_Categories tests retrieving all available categories
func TestPricingProvider_Categories(t *testing.T) {
	ctx := context.Background()
	provider := NewPricingProvider()

	categories, err := provider.GetCategories(ctx)
	if err != nil {
		t.Fatalf("Failed to get categories: %v", err)
	}

	if len(categories) == 0 {
		t.Fatal("No categories returned")
	}

	t.Logf("Available categories (%d):", len(categories))
	for _, category := range categories {
		t.Logf("  - %s", category)
	}

	// Verify category aliases work
	aliasTests := []struct {
		alias    string
		expected string
	}{
		{"gp", "General Purpose"},
		{"ch", "Compute Heavy"},
		{"mh", "Memory Heavy"},
	}

	for _, tt := range aliasTests {
		found := false
		for _, cat := range categories {
			if strings.EqualFold(cat, tt.expected) {
				found = true
				break
			}
		}
		if !found {
			t.Logf("Warning: Expected category %q (alias: %s) not found", tt.expected, tt.alias)
		}
	}
}

// TestPricingProvider_MinimumBid verifies the minimum bid constant
func TestPricingProvider_MinimumBid(t *testing.T) {
	// Verify constant is set correctly
	if MinimumBidPrice != 0.001 {
		t.Errorf("Expected MinimumBidPrice to be 0.001, got %f", MinimumBidPrice)
	}

	// Verify all instances have market price >= minimum bid
	ctx := context.Background()
	provider := NewPricingProvider()

	options, err := provider.GetInstanceTypes(ctx)
	if err != nil {
		t.Fatalf("Failed to get instance types: %v", err)
	}

	for _, opt := range options {
		if opt.PricePerHour < MinimumBidPrice {
			t.Errorf("Instance %s in %s has price %f below minimum bid %f",
				opt.InstanceType, opt.Region, opt.PricePerHour, MinimumBidPrice)
		}
	}

	t.Logf("Verified all %d instances have price >= minimum bid ($%.4f/hr)", len(options), MinimumBidPrice)
}

// TestPricingProvider_GetCheapestInstance tests getting the single cheapest instance
func TestPricingProvider_GetCheapestInstance(t *testing.T) {
	ctx := context.Background()
	provider := NewPricingProvider()

	reqs := Requirements{
		MinCPU:    resource.MustParse("2"),
		MinMemory: resource.MustParse("4Gi"),
	}

	cheapest, err := provider.GetCheapestInstance(ctx, reqs)
	if err != nil {
		t.Fatalf("Failed to get cheapest instance: %v", err)
	}

	if cheapest == nil {
		t.Fatal("No cheapest instance returned")
	}

	t.Logf("Cheapest instance meeting requirements (2 CPU, 4GB):")
	t.Logf("  Instance: %s", cheapest.InstanceType)
	t.Logf("  Region: %s", cheapest.Region)
	t.Logf("  Category: %s", cheapest.Category)
	t.Logf("  CPU: %d", cheapest.CPU)
	t.Logf("  Memory: %d GB", cheapest.MemoryGB)
	t.Logf("  Market Price: $%.4f/hr", cheapest.PricePerHour)
	t.Logf("  Minimum Bid: $%.4f/hr", MinimumBidPrice)
}

// TestPricingProvider_PercentilePrices tests that percentile data is available
func TestPricingProvider_PercentilePrices(t *testing.T) {
	ctx := context.Background()
	provider := NewPricingProvider()

	pricing, err := provider.GetPricing(ctx)
	if err != nil {
		t.Fatalf("Failed to fetch pricing data: %v", err)
	}

	var withPercentiles, withoutPercentiles int

	for regionName, region := range pricing.Regions {
		for instanceType, serverClass := range region.ServerClasses {
			hasPercentiles := serverClass.Percentile20 > 0 ||
				serverClass.Percentile50 > 0 ||
				serverClass.Percentile80 > 0

			if hasPercentiles {
				withPercentiles++
				// Log a few examples
				if withPercentiles <= 3 {
					t.Logf("Example with percentiles: %s (%s)", instanceType, regionName)
					t.Logf("  Market Price: %s", serverClass.MarketPrice)
					t.Logf("  20th percentile: %.4f", serverClass.Percentile20)
					t.Logf("  50th percentile: %.4f", serverClass.Percentile50)
					t.Logf("  80th percentile: %.4f", serverClass.Percentile80)
				}
			} else {
				withoutPercentiles++
			}
		}
	}

	t.Logf("Instances with percentile data: %d", withPercentiles)
	t.Logf("Instances without percentile data: %d", withoutPercentiles)
}

// TestPricingProvider_PriceSummary provides a summary of pricing across all instances
func TestPricingProvider_PriceSummary(t *testing.T) {
	ctx := context.Background()
	provider := NewPricingProvider()

	options, err := provider.GetInstanceTypes(ctx)
	if err != nil {
		t.Fatalf("Failed to get instance types: %v", err)
	}

	if len(options) == 0 {
		t.Fatal("No instance types returned")
	}

	// Find min, max, average prices
	var minPrice, maxPrice, totalPrice float64
	minPrice = options[0].PricePerHour
	maxPrice = options[0].PricePerHour

	for _, opt := range options {
		if opt.PricePerHour < minPrice {
			minPrice = opt.PricePerHour
		}
		if opt.PricePerHour > maxPrice {
			maxPrice = opt.PricePerHour
		}
		totalPrice += opt.PricePerHour
	}

	avgPrice := totalPrice / float64(len(options))

	t.Log("")
	t.Log("===============================================================================")
	t.Log("PRICE SUMMARY")
	t.Log("===============================================================================")
	t.Logf("Total instances: %d", len(options))
	t.Logf("Minimum price: $%.4f/hr", minPrice)
	t.Logf("Maximum price: $%.4f/hr", maxPrice)
	t.Logf("Average price: $%.4f/hr", avgPrice)
	t.Logf("Universal minimum bid: $%.4f/hr", MinimumBidPrice)
	t.Log("===============================================================================")

	// Group by category
	categoryPrices := make(map[string][]float64)
	for _, opt := range options {
		categoryPrices[opt.Category] = append(categoryPrices[opt.Category], opt.PricePerHour)
	}

	t.Log("")
	t.Log("Price by Category:")
	categories := make([]string, 0, len(categoryPrices))
	for cat := range categoryPrices {
		categories = append(categories, cat)
	}
	sort.Strings(categories)

	for _, cat := range categories {
		prices := categoryPrices[cat]
		sort.Float64s(prices)

		var sum float64
		for _, p := range prices {
			sum += p
		}

		t.Logf("  %s (%d instances):", cat, len(prices))
		t.Logf("    Min: $%.4f/hr, Max: $%.4f/hr, Avg: $%.4f/hr",
			prices[0], prices[len(prices)-1], sum/float64(len(prices)))
	}
}

// TestPricingProvider_InstancesByRegion shows instance count per region
func TestPricingProvider_InstancesByRegion(t *testing.T) {
	ctx := context.Background()
	provider := NewPricingProvider()

	pricing, err := provider.GetPricing(ctx)
	if err != nil {
		t.Fatalf("Failed to fetch pricing data: %v", err)
	}

	regions := make([]string, 0, len(pricing.Regions))
	for region := range pricing.Regions {
		regions = append(regions, region)
	}
	sort.Strings(regions)

	t.Log("")
	t.Log("Instances by Region:")
	t.Log("------------------------------------------------------------------------------")
	t.Logf("%-30s %12s %12s", "Region", "Generation", "Instances")
	t.Log("------------------------------------------------------------------------------")

	var totalInstances int
	for _, regionName := range regions {
		region := pricing.Regions[regionName]
		count := len(region.ServerClasses)
		totalInstances += count
		t.Logf("%-30s %12s %12d", regionName, region.Generation, count)
	}

	t.Log("------------------------------------------------------------------------------")
	t.Logf("%-30s %12s %12d", "TOTAL", "", totalInstances)
}

// BenchmarkPricingProvider_GetPricing benchmarks the pricing fetch (with cache)
func BenchmarkPricingProvider_GetPricing(b *testing.B) {
	ctx := context.Background()
	provider := NewPricingProvider()

	// Warm up cache
	_, err := provider.GetPricing(ctx)
	if err != nil {
		b.Fatalf("Failed to warm up cache: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := provider.GetPricing(ctx)
		if err != nil {
			b.Fatalf("Failed to get pricing: %v", err)
		}
	}
}

// BenchmarkPricingProvider_GetCheapestInstances benchmarks the filtering
func BenchmarkPricingProvider_GetCheapestInstances(b *testing.B) {
	ctx := context.Background()
	provider := NewPricingProvider()

	// Warm up cache
	_, err := provider.GetPricing(ctx)
	if err != nil {
		b.Fatalf("Failed to warm up cache: %v", err)
	}

	reqs := Requirements{
		MinCPU:    resource.MustParse("4"),
		MinMemory: resource.MustParse("8Gi"),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := provider.GetCheapestInstances(ctx, reqs)
		if err != nil {
			b.Fatalf("Failed to get cheapest instances: %v", err)
		}
	}
}

// ExamplePricingProvider_GetCheapestInstance demonstrates finding the cheapest instance
func ExamplePricingProvider_GetCheapestInstance() {
	ctx := context.Background()
	provider := NewPricingProvider()

	reqs := Requirements{
		MinCPU:     resource.MustParse("4"),
		MinMemory:  resource.MustParse("8Gi"),
		Categories: []string{"gp"},
	}

	cheapest, err := provider.GetCheapestInstance(ctx, reqs)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	fmt.Printf("Cheapest: %s at $%.4f/hr\n", cheapest.InstanceType, cheapest.PricePerHour)
}
