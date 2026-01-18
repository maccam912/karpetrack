package spot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	rxtspot "github.com/rackspace-spot/spot-go-sdk/api/v1"
)

// spotNodePoolCreateSpec is the spec for creating a spot node pool with numeric bidPrice.
// The SDK uses string for BidPrice, but the Rackspace API expects a JSON number.
type spotNodePoolCreateSpec struct {
	ServerClass       string            `json:"serverClass"`
	Desired           int               `json:"desired"`
	BidPrice          float64           `json:"bidPrice"`
	CloudSpace        string            `json:"cloudSpace"` // Required by admission webhook
	CustomAnnotations map[string]string `json:"customAnnotations,omitempty"`
	CustomLabels      map[string]string `json:"customLabels,omitempty"`
	CustomTaints      []interface{}     `json:"customTaints,omitempty"`
	Autoscaling       struct {
		Enabled  bool  `json:"enabled"`
		MinNodes int64 `json:"minNodes"`
		MaxNodes int64 `json:"maxNodes"`
	} `json:"autoscaling"`
}

// spotNodePoolCreateMetadata matches the SDK's metadata format with labels
type spotNodePoolCreateMetadata struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
}

// spotNodePoolCreateBody is the request body for creating a spot node pool.
type spotNodePoolCreateBody struct {
	APIVersion string                     `json:"apiVersion"`
	Kind       string                     `json:"kind"`
	Metadata   spotNodePoolCreateMetadata `json:"metadata"`
	Spec       spotNodePoolCreateSpec     `json:"spec"`
}

// spotNodePoolUpdateSpec is the spec for updating a spot node pool with numeric bidPrice.
type spotNodePoolUpdateSpec struct {
	Desired           int               `json:"desired,omitempty"`
	BidPrice          float64           `json:"bidPrice,omitempty"`
	CustomAnnotations map[string]string `json:"customAnnotations,omitempty"`
	CustomLabels      map[string]string `json:"customLabels,omitempty"`
	CustomTaints      []interface{}     `json:"customTaints,omitempty"`
	Autoscaling       *struct {
		Enabled  bool  `json:"enabled"`
		MinNodes int64 `json:"minNodes"`
		MaxNodes int64 `json:"maxNodes"`
	} `json:"autoscaling,omitempty"`
}

// spotNodePoolUpdateBody is the request body for updating a spot node pool.
type spotNodePoolUpdateBody struct {
	Spec spotNodePoolUpdateSpec `json:"spec"`
}

// createSpotNodePoolRaw creates a spot node pool using raw HTTP with numeric bidPrice.
// Uses the correct SDK endpoint: /apis/ngpc.rxt.io/v1/namespaces/{orgID}/spotnodepools
func createSpotNodePoolRaw(ctx context.Context, sdk *rxtspot.RackspaceSpotClient, org, cloudspace string, pool spotNodePoolCreateBody) error {
	pool.APIVersion = "ngpc.rxt.io/v1"
	pool.Kind = "SpotNodePool"
	pool.Spec.Autoscaling.Enabled = false
	pool.Spec.CloudSpace = cloudspace

	// Set cloudspace as label in metadata (required by SDK pattern)
	if pool.Metadata.Labels == nil {
		pool.Metadata.Labels = make(map[string]string)
	}
	pool.Metadata.Labels["ngpc.rxt.io/cloudspace"] = cloudspace

	// Get org ID (namespace) - the SDK uses the org ID, not the org name
	// We need to discover the org ID first via the SDK's ListOrganizations
	orgs, err := sdk.ListOrganizations(ctx)
	if err != nil {
		return fmt.Errorf("listing organizations: %w", err)
	}

	var orgID string
	for _, o := range orgs {
		if o.Name == org {
			orgID = o.ID
			break
		}
	}
	if orgID == "" {
		return fmt.Errorf("organization '%s' not found", org)
	}

	pool.Metadata.Namespace = orgID

	body, err := json.Marshal(pool)
	if err != nil {
		return fmt.Errorf("marshaling request: %w", err)
	}

	// Use the correct SDK endpoint pattern
	url := fmt.Sprintf("%s/apis/ngpc.rxt.io/v1/namespaces/%s/spotnodepools", sdk.BaseURL, orgID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+sdk.Token)

	resp, err := sdk.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API error (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// updateSpotNodePoolRaw updates a spot node pool using raw HTTP with numeric bidPrice.
// Uses the correct SDK endpoint: /apis/ngpc.rxt.io/v1/namespaces/{orgID}/spotnodepools/{poolName}
func updateSpotNodePoolRaw(ctx context.Context, sdk *rxtspot.RackspaceSpotClient, org, poolName string, spec spotNodePoolUpdateSpec) error {
	// Get org ID (namespace) - the SDK uses the org ID, not the org name
	orgs, err := sdk.ListOrganizations(ctx)
	if err != nil {
		return fmt.Errorf("listing organizations: %w", err)
	}

	var orgID string
	for _, o := range orgs {
		if o.Name == org {
			orgID = o.ID
			break
		}
	}
	if orgID == "" {
		return fmt.Errorf("organization '%s' not found", org)
	}

	body, err := json.Marshal(spotNodePoolUpdateBody{Spec: spec})
	if err != nil {
		return fmt.Errorf("marshaling request: %w", err)
	}

	// Use the correct SDK endpoint pattern
	url := fmt.Sprintf("%s/apis/ngpc.rxt.io/v1/namespaces/%s/spotnodepools/%s", sdk.BaseURL, orgID, poolName)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Content-Type", "application/merge-patch+json")
	req.Header.Set("Authorization", "Bearer "+sdk.Token)

	resp, err := sdk.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API error (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// parseBidPrice converts a string bid price to float64.
func parseBidPrice(s string) float64 {
	var price float64
	fmt.Sscanf(s, "%f", &price)
	if price <= 0 {
		price = 0.01 // Default fallback
	}
	return price
}

// minBidRegex extracts minimum bid price from API error messages
// Example: "BidPrice must be greater than or equal to the minimum bid price of 0.040000, but got 0.010000"
var minBidRegex = regexp.MustCompile(`minimum bid price of (\d+\.\d+)`)

// parseMinBidFromError extracts the minimum bid price from a 422 error message.
// Returns the parsed price and true if found, or 0 and false if not found.
func parseMinBidFromError(err error) (float64, bool) {
	if err == nil {
		return 0, false
	}
	matches := minBidRegex.FindStringSubmatch(err.Error())
	if len(matches) >= 2 {
		if price, parseErr := strconv.ParseFloat(matches[1], 64); parseErr == nil {
			return price, true
		}
	}
	return 0, false
}

// Client wraps the Rackspace Spot SDK for node management
type Client struct {
	refreshToken string
	cloudspaceID string
	org          string
	pricing      *PricingProvider

	// Mock mode for testing without real API
	mockMode  bool
	mockNodes map[string]*Node
	mockMu    sync.RWMutex

	// Real SDK client
	sdk   *rxtspot.RackspaceSpotClient
	sdkMu sync.RWMutex
}

// ClientConfig holds configuration for the Spot client
type ClientConfig struct {
	RefreshToken string
	CloudspaceID string
	Org          string // Organization name for API calls
	MockMode     bool
}

// NewClient creates a new Rackspace Spot client
func NewClient(config ClientConfig) (*Client, error) {
	if !config.MockMode {
		if config.RefreshToken == "" {
			return nil, fmt.Errorf("refresh token is required")
		}
		if config.CloudspaceID == "" {
			return nil, fmt.Errorf("cloudspace ID is required")
		}
		if config.Org == "" {
			return nil, fmt.Errorf("organization is required")
		}
	}

	client := &Client{
		refreshToken: config.RefreshToken,
		cloudspaceID: config.CloudspaceID,
		org:          config.Org,
		pricing:      NewPricingProvider(),
		mockMode:     config.MockMode,
		mockNodes:    make(map[string]*Node),
	}

	// Initialize real SDK client
	if !config.MockMode {
		httpClient := &http.Client{Timeout: 30 * time.Second}
		sdkClient, err := rxtspot.NewSpotClient(&rxtspot.Config{
			BaseURL:      "https://spot.rackspace.com",
			OAuthURL:     "https://login.spot.rackspace.com",
			RefreshToken: config.RefreshToken,
			HTTPClient:   httpClient,
		})
		if err != nil {
			return nil, fmt.Errorf("creating SDK client: %w", err)
		}

		// Authenticate
		if _, err := sdkClient.Authenticate(context.Background()); err != nil {
			return nil, fmt.Errorf("authenticating with Rackspace Spot: %w", err)
		}

		client.sdk = sdkClient
	}

	return client, nil
}

// GetPricing returns the pricing provider
func (c *Client) GetPricing() *PricingProvider {
	return c.pricing
}

// GetCloudspaceID returns the configured cloudspace ID
func (c *Client) GetCloudspaceID() string {
	return c.cloudspaceID
}

// GetOrg returns the configured organization
func (c *Client) GetOrg() string {
	return c.org
}

// CreateNode provisions a new node on Rackspace Spot by creating/updating a node pool
func (c *Client) CreateNode(ctx context.Context, spec NodeSpec) (*Node, error) {
	if c.mockMode {
		return c.mockCreateNode(ctx, spec)
	}

	c.sdkMu.Lock()
	defer c.sdkMu.Unlock()

	// Server class in Rackspace is the instance type (e.g., "gp.vs1.medium-dfw")
	serverClass := spec.InstanceType

	// Determine initial bid price - use max(market_price, 50th_percentile)
	bidPrice := spec.BidPrice
	if bidPrice <= 0 {
		// Try to get bid price from pricing API (max of market, 50th percentile)
		if price, err := c.pricing.GetBidPriceForServerClass(ctx, serverClass); err == nil && price > 0 {
			bidPrice = price
		} else {
			// Fallback to static min bid prices only if API fails
			bidPrice = GetMinBidPrice(serverClass)
		}
	}

	// List existing pools to find one for this server class
	pools, err := c.sdk.ListSpotNodePools(ctx, c.org, c.cloudspaceID)
	if err != nil {
		return nil, fmt.Errorf("listing node pools: %w", err)
	}

	var targetPool *rxtspot.SpotNodePool
	for _, pool := range pools {
		if pool.ServerClass == serverClass &&
			pool.CustomLabels["karpetrack.io/managed"] == "true" {
			targetPool = pool
			break
		}
	}

	poolName := ""
	if targetPool != nil {
		// Update existing pool - increment desired count
		poolName = targetPool.Name
		err := c.tryUpdatePoolWithRetry(ctx, targetPool.Name, targetPool.Desired+1, bidPrice)
		if err != nil {
			return nil, fmt.Errorf("updating node pool %s: %w", targetPool.Name, err)
		}
	} else {
		// Create new pool with desired=1
		poolName = generatePoolName(serverClass)
		err := c.tryCreatePoolWithRetry(ctx, poolName, serverClass, 1, bidPrice)
		if err != nil {
			return nil, fmt.Errorf("creating node pool: %w", err)
		}
	}

	// Generate a tracking ID for this "node" request
	// This won't be the actual Rackspace node ID (we don't know it yet),
	// but a reference we can use to track the SpotNode resource
	nodeID := fmt.Sprintf("pending-%s-%d", poolName, time.Now().UnixNano())

	return &Node{
		ID:           nodeID,
		Name:         fmt.Sprintf("karpetrack-%s", poolName),
		InstanceType: serverClass,
		Region:       spec.Region,
		Status:       "pending", // Will transition to "running" when K8s node appears
		CreatedAt:    time.Now().Format(time.RFC3339),
	}, nil
}

// DeleteNode terminates a node on Rackspace Spot by scaling down or deleting a pool
func (c *Client) DeleteNode(ctx context.Context, nodeID string) error {
	if c.mockMode {
		return c.mockDeleteNode(ctx, nodeID)
	}

	c.sdkMu.Lock()
	defer c.sdkMu.Unlock()

	// Extract pool name from the nodeID
	poolName := extractPoolNameFromNodeID(nodeID)
	if poolName == "" {
		// Try to find pool by looking at managed pools
		// For now, return nil - node might already be gone
		return nil
	}

	// Get current pool state
	pools, err := c.sdk.ListSpotNodePools(ctx, c.org, c.cloudspaceID)
	if err != nil {
		return fmt.Errorf("listing pools: %w", err)
	}

	var targetPool *rxtspot.SpotNodePool
	for _, pool := range pools {
		if pool.Name == poolName {
			targetPool = pool
			break
		}
	}

	if targetPool == nil {
		return nil // Pool already deleted
	}

	if targetPool.Desired <= 1 {
		// Delete the pool entirely
		if err := c.sdk.DeleteSpotNodePool(ctx, c.org, poolName); err != nil {
			return fmt.Errorf("deleting pool %s: %w", poolName, err)
		}
	} else {
		// Decrement desired count using SDK method
		// Parse bid price from string (SDK returns string like "$0.0100")
		bidPrice := parseBidPrice(targetPool.BidPrice)
		pool := rxtspot.SpotNodePool{
			Name:     poolName,
			Desired:  targetPool.Desired - 1,
			BidPrice: fmt.Sprintf("%.4f", bidPrice),
		}
		if err := c.sdk.UpdateSpotNodePool(ctx, c.org, pool); err != nil {
			return fmt.Errorf("updating pool %s: %w", poolName, err)
		}
	}

	return nil
}

// GetNode retrieves a node by its ID
func (c *Client) GetNode(ctx context.Context, nodeID string) (*Node, error) {
	if c.mockMode {
		return c.mockGetNode(ctx, nodeID)
	}

	// For pending nodes, we can't query Rackspace directly
	// The controller should rely on watching Kubernetes nodes
	if strings.HasPrefix(nodeID, "pending-") {
		return &Node{
			ID:     nodeID,
			Status: "pending",
		}, nil
	}

	// For actual node IDs, assume running
	// The controller monitors K8s nodes for actual status
	return &Node{
		ID:     nodeID,
		Status: "running",
	}, nil
}

// ListNodes returns all nodes in the cloudspace
func (c *Client) ListNodes(ctx context.Context) ([]*Node, error) {
	if c.mockMode {
		return c.mockListNodes(ctx)
	}

	c.sdkMu.RLock()
	defer c.sdkMu.RUnlock()

	// List pools and derive node counts
	pools, err := c.sdk.ListSpotNodePools(ctx, c.org, c.cloudspaceID)
	if err != nil {
		return nil, fmt.Errorf("listing pools: %w", err)
	}

	var nodes []*Node
	for _, pool := range pools {
		if pool.CustomLabels["karpetrack.io/managed"] != "true" {
			continue
		}

		// Create virtual node entries for the pool's capacity
		for i := 0; i < pool.Desired; i++ {
			nodes = append(nodes, &Node{
				ID:           fmt.Sprintf("%s-node-%d", pool.Name, i),
				Name:         fmt.Sprintf("%s-node-%d", pool.Name, i),
				InstanceType: pool.ServerClass,
				Status:       "running", // Assume running for pools
			})
		}
	}

	return nodes, nil
}

// UpdateNodePool updates a node pool's configuration
func (c *Client) UpdateNodePool(ctx context.Context, poolID string, spec NodePoolSpec) error {
	if c.mockMode {
		// Mock: no-op for now
		return nil
	}

	c.sdkMu.Lock()
	defer c.sdkMu.Unlock()

	return c.tryUpdatePoolWithRetry(ctx, poolID, spec.DesiredCount, spec.BidPrice)
}

// ScaleNodePool scales a node pool to the desired count
func (c *Client) ScaleNodePool(ctx context.Context, poolID string, desiredCount int) error {
	if c.mockMode {
		return nil
	}

	c.sdkMu.Lock()
	defer c.sdkMu.Unlock()

	// Get current pool to preserve bid price
	pools, err := c.sdk.ListSpotNodePools(ctx, c.org, c.cloudspaceID)
	if err != nil {
		return fmt.Errorf("listing pools: %w", err)
	}

	var bidPriceStr string
	for _, pool := range pools {
		if pool.Name == poolID {
			bidPriceStr = pool.BidPrice
			break
		}
	}

	if bidPriceStr == "" {
		bidPriceStr = "0.01" // Default fallback
	}

	bidPrice := parseBidPrice(bidPriceStr)

	return c.tryUpdatePoolWithRetry(ctx, poolID, desiredCount, bidPrice)
}

// ListManagedPools returns all pools managed by karpetrack
func (c *Client) ListManagedPools(ctx context.Context) ([]*rxtspot.SpotNodePool, error) {
	if c.mockMode {
		return nil, nil
	}

	c.sdkMu.RLock()
	defer c.sdkMu.RUnlock()

	pools, err := c.sdk.ListSpotNodePools(ctx, c.org, c.cloudspaceID)
	if err != nil {
		return nil, fmt.Errorf("listing pools: %w", err)
	}

	var managed []*rxtspot.SpotNodePool
	for _, pool := range pools {
		if pool.CustomLabels["karpetrack.io/managed"] == "true" {
			managed = append(managed, pool)
		}
	}

	return managed, nil
}

// --- Retry helpers for bid price validation ---

// tryCreatePoolWithRetry attempts to create a pool, and if the API rejects the bid price
// with a 422 error containing the actual minimum, retries with the discovered minimum.
func (c *Client) tryCreatePoolWithRetry(ctx context.Context, poolName, serverClass string, desired int, initialBid float64) error {
	// Use raw HTTP with numeric bidPrice (API requires number, not string)
	// Round to 3 decimal places (API maximum)
	roundedBid := RoundBidPrice(initialBid)

	createBody := spotNodePoolCreateBody{}
	createBody.Metadata.Name = poolName
	createBody.Spec = spotNodePoolCreateSpec{
		ServerClass: serverClass,
		Desired:     desired,
		BidPrice:    roundedBid,
		CustomLabels: map[string]string{
			"karpetrack.io/managed":      "true",
			"karpetrack.io/server-class": sanitizeLabel(serverClass),
		},
	}

	err := createSpotNodePoolRaw(ctx, c.sdk, c.org, c.cloudspaceID, createBody)
	if err == nil {
		return nil
	}

	// Check if error contains minimum bid price info
	if minBid, found := parseMinBidFromError(err); found {
		slog.Info("Bid price rejected, retrying with API-specified minimum",
			"serverClass", serverClass,
			"initialBid", initialBid,
			"requiredMinBid", minBid)
		createBody.Spec.BidPrice = RoundBidPrice(minBid)
		return createSpotNodePoolRaw(ctx, c.sdk, c.org, c.cloudspaceID, createBody)
	}

	return err
}

// tryUpdatePoolWithRetry attempts to update a pool, and if the API rejects the bid price
// with a 422 error containing the actual minimum, retries with the discovered minimum.
func (c *Client) tryUpdatePoolWithRetry(ctx context.Context, poolName string, desired int, initialBid float64) error {
	// Use raw HTTP with numeric bidPrice (API requires number, not string)
	// Round to 3 decimal places (API maximum)
	updateSpec := spotNodePoolUpdateSpec{
		Desired:  desired,
		BidPrice: RoundBidPrice(initialBid),
	}

	err := updateSpotNodePoolRaw(ctx, c.sdk, c.org, poolName, updateSpec)
	if err == nil {
		return nil
	}

	// Check if error contains minimum bid price info
	if minBid, found := parseMinBidFromError(err); found {
		slog.Info("Bid price rejected on update, retrying with API-specified minimum",
			"poolName", poolName,
			"initialBid", initialBid,
			"requiredMinBid", minBid)
		updateSpec.BidPrice = RoundBidPrice(minBid)
		return updateSpotNodePoolRaw(ctx, c.sdk, c.org, poolName, updateSpec)
	}

	return err
}

// --- Helper functions ---

func generatePoolName(serverClass string) string {
	return fmt.Sprintf("karpetrack-%s", sanitizeLabel(serverClass))
}

func sanitizeLabel(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, ".", "-")
	s = strings.ReplaceAll(s, "_", "-")
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

func extractPoolNameFromNodeID(nodeID string) string {
	// Format: pending-karpetrack-{sanitized-server-class}-{timestamp}
	if strings.HasPrefix(nodeID, "pending-") {
		// Remove "pending-" prefix and extract pool name
		rest := strings.TrimPrefix(nodeID, "pending-")
		// Find the last dash followed by digits (timestamp)
		lastDashIdx := strings.LastIndex(rest, "-")
		if lastDashIdx > 0 {
			return rest[:lastDashIdx]
		}
		return rest
	}
	// If it's already a pool name format
	if strings.HasPrefix(nodeID, "karpetrack-") {
		return nodeID
	}
	return ""
}

// --- Mock implementations for testing ---

func (c *Client) mockCreateNode(ctx context.Context, spec NodeSpec) (*Node, error) {
	c.mockMu.Lock()
	defer c.mockMu.Unlock()

	nodeID := fmt.Sprintf("mock-node-%d", time.Now().UnixNano())
	node := &Node{
		ID:           nodeID,
		Name:         fmt.Sprintf("karpetrack-%s", nodeID[:12]),
		InstanceType: spec.InstanceType,
		Region:       spec.Region,
		Status:       "running",
		CreatedAt:    time.Now().Format(time.RFC3339),
		IPAddress:    fmt.Sprintf("10.0.%d.%d", len(c.mockNodes)/256, len(c.mockNodes)%256),
	}

	c.mockNodes[nodeID] = node
	return node, nil
}

func (c *Client) mockDeleteNode(ctx context.Context, nodeID string) error {
	c.mockMu.Lock()
	defer c.mockMu.Unlock()

	if _, exists := c.mockNodes[nodeID]; !exists {
		return fmt.Errorf("node not found: %s", nodeID)
	}

	delete(c.mockNodes, nodeID)
	return nil
}

func (c *Client) mockGetNode(ctx context.Context, nodeID string) (*Node, error) {
	c.mockMu.RLock()
	defer c.mockMu.RUnlock()

	node, exists := c.mockNodes[nodeID]
	if !exists {
		return nil, fmt.Errorf("node not found: %s", nodeID)
	}

	return node, nil
}

func (c *Client) mockListNodes(ctx context.Context) ([]*Node, error) {
	c.mockMu.RLock()
	defer c.mockMu.RUnlock()

	nodes := make([]*Node, 0, len(c.mockNodes))
	for _, node := range c.mockNodes {
		nodes = append(nodes, node)
	}

	return nodes, nil
}
