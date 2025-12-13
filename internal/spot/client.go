package spot

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	rxtspot "github.com/rackspace-spot/spot-go-sdk/api/v1"
)

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

	// Determine bid price - use live market price, not stale static prices
	bidPrice := spec.BidPrice
	if bidPrice <= 0 {
		// Try to get live market price from pricing API
		// Server class format includes region: "gp.vs1.medium-dfw"
		if marketPrice, err := c.pricing.GetPriceForServerClass(ctx, serverClass); err == nil && marketPrice > 0 {
			bidPrice = marketPrice * 1.1 // 10% above live market price
		} else {
			// Fallback to static min bid prices only if API fails
			bidPrice = GetMinBidPrice(serverClass) * 1.1
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
		err := c.sdk.UpdateSpotNodePool(ctx, c.org, rxtspot.SpotNodePool{
			Name:       targetPool.Name,
			Cloudspace: c.cloudspaceID,
			Desired:    targetPool.Desired + 1,
			BidPrice:   fmt.Sprintf("%.3f", bidPrice),
		})
		if err != nil {
			return nil, fmt.Errorf("updating node pool %s: %w", targetPool.Name, err)
		}
	} else {
		// Create new pool with desired=1
		poolName = generatePoolName(serverClass)
		err := c.sdk.CreateSpotNodePool(ctx, c.org, rxtspot.SpotNodePool{
			Name:        poolName,
			Cloudspace:  c.cloudspaceID,
			ServerClass: serverClass,
			Desired:     1,
			BidPrice:    fmt.Sprintf("%.3f", bidPrice),
			CustomLabels: map[string]string{
				"karpetrack.io/managed":      "true",
				"karpetrack.io/server-class": sanitizeLabel(serverClass),
			},
		})
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
		// Decrement desired count
		if err := c.sdk.UpdateSpotNodePool(ctx, c.org, rxtspot.SpotNodePool{
			Name:       poolName,
			Cloudspace: c.cloudspaceID,
			Desired:    targetPool.Desired - 1,
			BidPrice:   targetPool.BidPrice,
		}); err != nil {
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

	return c.sdk.UpdateSpotNodePool(ctx, c.org, rxtspot.SpotNodePool{
		Name:       poolID,
		Cloudspace: c.cloudspaceID,
		Desired:    spec.DesiredCount,
		BidPrice:   fmt.Sprintf("%.3f", spec.BidPrice),
	})
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

	var bidPrice string
	for _, pool := range pools {
		if pool.Name == poolID {
			bidPrice = pool.BidPrice
			break
		}
	}

	if bidPrice == "" {
		bidPrice = "0.01" // Default fallback
	}

	return c.sdk.UpdateSpotNodePool(ctx, c.org, rxtspot.SpotNodePool{
		Name:       poolID,
		Cloudspace: c.cloudspaceID,
		Desired:    desiredCount,
		BidPrice:   bidPrice,
	})
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
