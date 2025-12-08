package spot

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Client wraps the Rackspace Spot SDK for node management
type Client struct {
	refreshToken string
	cloudspaceID string
	pricing      *PricingProvider

	// Mock mode for testing without real API
	mockMode bool
	mockNodes map[string]*Node
	mockMu    sync.RWMutex

	// TODO: Add spot-go-sdk client when implementing real API calls
	// sdk *spotsdk.SpotClient
}

// ClientConfig holds configuration for the Spot client
type ClientConfig struct {
	RefreshToken string
	CloudspaceID string
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
	}

	client := &Client{
		refreshToken: config.RefreshToken,
		cloudspaceID: config.CloudspaceID,
		pricing:      NewPricingProvider(),
		mockMode:     config.MockMode,
		mockNodes:    make(map[string]*Node),
	}

	// TODO: Initialize real SDK client
	// if !config.MockMode {
	//     sdkClient, err := spotsdk.NewSpotClient(&spotsdk.Config{
	//         RefreshToken: config.RefreshToken,
	//     })
	//     if err != nil {
	//         return nil, fmt.Errorf("initializing SDK: %w", err)
	//     }
	//     if err := sdkClient.Authenticate(context.Background()); err != nil {
	//         return nil, fmt.Errorf("authenticating: %w", err)
	//     }
	//     client.sdk = sdkClient
	// }

	return client, nil
}

// GetPricing returns the pricing provider
func (c *Client) GetPricing() *PricingProvider {
	return c.pricing
}

// CreateNode provisions a new node on Rackspace Spot
func (c *Client) CreateNode(ctx context.Context, spec NodeSpec) (*Node, error) {
	if c.mockMode {
		return c.mockCreateNode(ctx, spec)
	}

	// TODO: Implement real API call using spot-go-sdk
	// The SDK should support:
	// 1. Creating/updating a node pool with the desired server class
	// 2. Scaling up the node pool to add a new node
	// 3. Returning the new node's information

	return nil, fmt.Errorf("real API not yet implemented - set MockMode=true for testing")
}

// DeleteNode terminates a node on Rackspace Spot
func (c *Client) DeleteNode(ctx context.Context, nodeID string) error {
	if c.mockMode {
		return c.mockDeleteNode(ctx, nodeID)
	}

	// TODO: Implement real API call
	// 1. Find the node in the cloudspace
	// 2. Remove it from the node pool or scale down
	// 3. Wait for termination confirmation

	return fmt.Errorf("real API not yet implemented")
}

// GetNode retrieves a node by its ID
func (c *Client) GetNode(ctx context.Context, nodeID string) (*Node, error) {
	if c.mockMode {
		return c.mockGetNode(ctx, nodeID)
	}

	// TODO: Implement real API call

	return nil, fmt.Errorf("real API not yet implemented")
}

// ListNodes returns all nodes in the cloudspace
func (c *Client) ListNodes(ctx context.Context) ([]*Node, error) {
	if c.mockMode {
		return c.mockListNodes(ctx)
	}

	// TODO: Implement real API call

	return nil, fmt.Errorf("real API not yet implemented")
}

// UpdateNodePool updates a node pool's configuration
func (c *Client) UpdateNodePool(ctx context.Context, poolID string, spec NodePoolSpec) error {
	if c.mockMode {
		// Mock: no-op for now
		return nil
	}

	// TODO: Implement real API call
	// 1. Find the node pool
	// 2. Update server class, bid price, scaling settings
	// 3. Apply changes

	return fmt.Errorf("real API not yet implemented")
}

// ScaleNodePool scales a node pool to the desired count
func (c *Client) ScaleNodePool(ctx context.Context, poolID string, desiredCount int) error {
	if c.mockMode {
		return nil
	}

	// TODO: Implement real API call

	return fmt.Errorf("real API not yet implemented")
}

// GetCloudspaceID returns the configured cloudspace ID
func (c *Client) GetCloudspaceID() string {
	return c.cloudspaceID
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
