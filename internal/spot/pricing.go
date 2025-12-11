package spot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
)

const (
	// PricingEndpoint is the public S3 URL for Rackspace Spot pricing data
	PricingEndpoint = "https://ngpc-prod-public-data.s3.us-east-2.amazonaws.com/percentiles.json"

	// DefaultCacheTTL is how long to cache pricing data
	DefaultCacheTTL = 30 * time.Second
)

// PricingProvider fetches and caches spot pricing data
type PricingProvider struct {
	endpoint  string
	cacheTTL  time.Duration
	cache     *PricingData
	lastFetch time.Time
	mu        sync.RWMutex
	client    *http.Client
}

// NewPricingProvider creates a new pricing provider
func NewPricingProvider() *PricingProvider {
	return &PricingProvider{
		endpoint: PricingEndpoint,
		cacheTTL: DefaultCacheTTL,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// NewPricingProviderWithOptions creates a pricing provider with custom options
func NewPricingProviderWithOptions(endpoint string, cacheTTL time.Duration) *PricingProvider {
	p := NewPricingProvider()
	if endpoint != "" {
		p.endpoint = endpoint
	}
	if cacheTTL > 0 {
		p.cacheTTL = cacheTTL
	}
	return p
}

// GetPricing returns the current pricing data, fetching if cache is stale
func (p *PricingProvider) GetPricing(ctx context.Context) (*PricingData, error) {
	p.mu.RLock()
	if p.cache != nil && time.Since(p.lastFetch) < p.cacheTTL {
		defer p.mu.RUnlock()
		return p.cache, nil
	}
	p.mu.RUnlock()

	return p.refresh(ctx)
}

// refresh fetches fresh pricing data from the API
func (p *PricingProvider) refresh(ctx context.Context) (*PricingData, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Double-check after acquiring write lock
	if p.cache != nil && time.Since(p.lastFetch) < p.cacheTTL {
		return p.cache, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		// Return stale cache if available
		if p.cache != nil {
			return p.cache, nil
		}
		return nil, fmt.Errorf("fetching pricing: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if p.cache != nil {
			return p.cache, nil
		}
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		if p.cache != nil {
			return p.cache, nil
		}
		return nil, fmt.Errorf("reading response: %w", err)
	}

	// Log raw response for debugging
	slog.Debug("Raw pricing API response", "body", string(body), "length", len(body))

	var data PricingData
	if err := json.Unmarshal(body, &data); err != nil {
		slog.Error("Failed to parse pricing data", "error", err, "rawBody", string(body))
		if p.cache != nil {
			return p.cache, nil
		}
		return nil, fmt.Errorf("parsing pricing data: %w", err)
	}

	p.cache = &data
	p.lastFetch = time.Now()

	return &data, nil
}

// GetInstanceTypes returns all available instance types with current pricing
func (p *PricingProvider) GetInstanceTypes(ctx context.Context) ([]InstanceOption, error) {
	pricing, err := p.GetPricing(ctx)
	if err != nil {
		return nil, err
	}

	var options []InstanceOption
	for regionName, region := range pricing.Regions {
		for instanceType, serverClass := range region.ServerClasses {
			price, err := strconv.ParseFloat(serverClass.MarketPrice, 64)
			if err != nil {
				continue // Skip invalid prices
			}

			options = append(options, InstanceOption{
				Region:       regionName,
				InstanceType: instanceType,
				DisplayName:  serverClass.DisplayName,
				Category:     serverClass.Category,
				CPU:          int(serverClass.CPU),
				MemoryGB:     int(serverClass.Memory),
				PricePerHour: price,
				Generation:   region.Generation,
			})
		}
	}

	return options, nil
}

// GetCheapestInstances returns instance options sorted by price that meet requirements
func (p *PricingProvider) GetCheapestInstances(ctx context.Context, reqs Requirements) ([]InstanceOption, error) {
	allOptions, err := p.GetInstanceTypes(ctx)
	if err != nil {
		return nil, err
	}

	slog.Debug("Filtering instance options",
		"totalOptions", len(allOptions),
		"regions", reqs.Regions,
		"categories", reqs.Categories,
		"minCPU", reqs.MinCPU.String(),
		"minMemory", reqs.MinMemory.String(),
		"maxPrice", reqs.MaxPrice)

	// Filter by requirements
	var filtered []InstanceOption
	for _, opt := range allOptions {
		if !p.meetsRequirements(opt, reqs) {
			continue
		}
		filtered = append(filtered, opt)
	}

	slog.Debug("Filtered instance options",
		"filteredCount", len(filtered),
		"totalCount", len(allOptions))

	if len(filtered) > 0 {
		slog.Debug("First matching instance",
			"region", filtered[0].Region,
			"instanceType", filtered[0].InstanceType,
			"category", filtered[0].Category,
			"cpu", filtered[0].CPU,
			"memoryGB", filtered[0].MemoryGB,
			"price", filtered[0].PricePerHour)
	}

	// Sort by price (cheapest first)
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].PricePerHour < filtered[j].PricePerHour
	})

	return filtered, nil
}

// GetCheapestInstance returns the single cheapest instance that meets requirements
func (p *PricingProvider) GetCheapestInstance(ctx context.Context, reqs Requirements) (*InstanceOption, error) {
	options, err := p.GetCheapestInstances(ctx, reqs)
	if err != nil {
		return nil, err
	}

	if len(options) == 0 {
		return nil, fmt.Errorf("no instances available matching requirements")
	}

	return &options[0], nil
}

// GetPriceForInstance returns the current market price for a specific instance type/region
func (p *PricingProvider) GetPriceForInstance(ctx context.Context, region, instanceType string) (float64, error) {
	pricing, err := p.GetPricing(ctx)
	if err != nil {
		return 0, err
	}

	regionData, ok := pricing.Regions[region]
	if !ok {
		return 0, fmt.Errorf("region not found: %s", region)
	}

	serverClass, ok := regionData.ServerClasses[instanceType]
	if !ok {
		return 0, fmt.Errorf("instance type not found: %s in region %s", instanceType, region)
	}

	return strconv.ParseFloat(serverClass.MarketPrice, 64)
}

// FindCheaperAlternative finds a cheaper instance that can replace the given one
func (p *PricingProvider) FindCheaperAlternative(
	ctx context.Context,
	currentRegion, currentInstanceType string,
	minCPU, minMemoryGB int,
	savingsThreshold float64,
) (*InstanceOption, float64, error) {
	currentPrice, err := p.GetPriceForInstance(ctx, currentRegion, currentInstanceType)
	if err != nil {
		return nil, 0, err
	}

	reqs := Requirements{
		MinCPU:    resource.MustParse(fmt.Sprintf("%d", minCPU)),
		MinMemory: resource.MustParse(fmt.Sprintf("%dGi", minMemoryGB)),
	}

	options, err := p.GetCheapestInstances(ctx, reqs)
	if err != nil {
		return nil, 0, err
	}

	for _, opt := range options {
		savings := (currentPrice - opt.PricePerHour) / currentPrice
		if savings >= savingsThreshold {
			return &opt, savings, nil
		}
	}

	return nil, 0, nil // No better alternative found
}

// meetsRequirements checks if an instance option meets the given requirements
func (p *PricingProvider) meetsRequirements(opt InstanceOption, reqs Requirements) bool {
	// Check CPU
	if !reqs.MinCPU.IsZero() {
		minCPU := reqs.MinCPU.Value()
		if int64(opt.CPU) < minCPU {
			return false
		}
	}

	// Check memory
	if !reqs.MinMemory.IsZero() {
		// Convert to GB for comparison
		minMemoryGB := reqs.MinMemory.Value() / (1024 * 1024 * 1024)
		if int64(opt.MemoryGB) < minMemoryGB {
			return false
		}
	}

	// Check regions
	if len(reqs.Regions) > 0 {
		found := false
		for _, r := range reqs.Regions {
			if r == opt.Region {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	// Check categories
	if len(reqs.Categories) > 0 {
		found := false
		for _, c := range reqs.Categories {
			if strings.EqualFold(c, opt.Category) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	} else {
		// Default: exclude GPU unless explicitly requested
		if strings.EqualFold(opt.Category, "GPU") {
			return false
		}
	}

	// Check max price
	if reqs.MaxPrice > 0 && opt.PricePerHour > reqs.MaxPrice {
		return false
	}

	return true
}

// GetRegions returns all available regions
func (p *PricingProvider) GetRegions(ctx context.Context) ([]string, error) {
	pricing, err := p.GetPricing(ctx)
	if err != nil {
		return nil, err
	}

	regions := make([]string, 0, len(pricing.Regions))
	for region := range pricing.Regions {
		regions = append(regions, region)
	}
	sort.Strings(regions)
	return regions, nil
}

// GetCategories returns all available instance categories
func (p *PricingProvider) GetCategories(ctx context.Context) ([]string, error) {
	pricing, err := p.GetPricing(ctx)
	if err != nil {
		return nil, err
	}

	categorySet := make(map[string]struct{})
	for _, region := range pricing.Regions {
		for _, serverClass := range region.ServerClasses {
			categorySet[serverClass.Category] = struct{}{}
		}
	}

	categories := make([]string, 0, len(categorySet))
	for cat := range categorySet {
		categories = append(categories, cat)
	}
	sort.Strings(categories)
	return categories, nil
}
