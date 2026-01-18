// Debug tool to test Rackspace Spot API authentication and permissions
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"
	rxtspot "github.com/rackspace-spot/spot-go-sdk/api/v1"
)

func main() {
	var (
		refreshToken = flag.String("refresh-token", os.Getenv("RACKSPACE_SPOT_REFRESH_TOKEN"), "Rackspace Spot refresh token")
		org          = flag.String("org", os.Getenv("RACKSPACE_SPOT_ORG"), "Organization name")
		cloudspace   = flag.String("cloudspace", os.Getenv("RACKSPACE_SPOT_CLOUDSPACE"), "Cloudspace ID")
	)
	flag.Parse()

	if *refreshToken == "" {
		fmt.Fprintln(os.Stderr, "Error: --refresh-token or RACKSPACE_SPOT_REFRESH_TOKEN is required")
		os.Exit(1)
	}
	if *org == "" {
		fmt.Fprintln(os.Stderr, "Error: --org or RACKSPACE_SPOT_ORG is required")
		os.Exit(1)
	}
	if *cloudspace == "" {
		fmt.Fprintln(os.Stderr, "Error: --cloudspace or RACKSPACE_SPOT_CLOUDSPACE is required")
		os.Exit(1)
	}

	ctx := context.Background()

	fmt.Println("═══════════════════════════════════════════════════════════════")
	fmt.Println("             RACKSPACE SPOT API DEBUG TOOL")
	fmt.Println("═══════════════════════════════════════════════════════════════")
	fmt.Println()
	fmt.Printf("Organization: %s\n", *org)
	fmt.Printf("Cloudspace:   %s\n", *cloudspace)
	fmt.Println()

	// Create SDK client
	httpClient := &http.Client{Timeout: 60 * time.Second}
	client, err := rxtspot.NewSpotClient(&rxtspot.Config{
		BaseURL:      "https://spot.rackspace.com",
		OAuthURL:     "https://login.spot.rackspace.com",
		RefreshToken: *refreshToken,
		HTTPClient:   httpClient,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating SDK client: %v\n", err)
		os.Exit(1)
	}

	// Step 1: Authenticate
	fmt.Println("Step 1: Authenticating...")
	_, err = client.Authenticate(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Authentication failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✓ Authentication successful\n")
	fmt.Printf("  Token length: %d characters\n", len(client.Token))
	fmt.Printf("  Token prefix: %s...\n", client.Token[:min(50, len(client.Token))])
	fmt.Println()

	// Step 2: List existing node pools (GET request - should work if we have read access)
	fmt.Println("Step 2: Listing spot node pools (testing READ access)...")
	pools, err := client.ListSpotNodePools(ctx, *org, *cloudspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to list node pools: %v\n", err)
		fmt.Println("   This suggests a permission issue with READ access.")
	} else {
		fmt.Printf("✓ Successfully listed %d node pools\n", len(pools))
		for i, pool := range pools {
			fmt.Printf("   %d. %s (server-class: %s, desired: %d, status: %s)\n",
				i+1, pool.Name, pool.ServerClass, pool.Desired, pool.Status)
		}
	}
	fmt.Println()

	// Step 3: Try to create a test node pool (POST request - this is what's failing)
	fmt.Println("Step 3: Testing CREATE access (the failing operation)...")

	// First, let's check what the API returns for the org endpoint
	fmt.Println("   3a. Checking org endpoint...")
	checkOrgEndpoint(ctx, client, *org)

	// Try to create with the raw HTTP approach
	fmt.Println("   3b. Attempting to create a test node pool with RAW HTTP...")
	err = testCreateNodePool(ctx, client, *org, *cloudspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Create node pool (raw HTTP) failed: %v\n", err)
	} else {
		fmt.Println("✓ Create node pool (raw HTTP) succeeded!")
	}

	// Try using the SDK's method
	fmt.Println("   3c. Attempting to create with SDK method...")
	err = testCreateViaSDK(ctx, client, *org, *cloudspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Create node pool failed: %v\n", err)

		// Additional debugging
		fmt.Println()
		fmt.Println("   Debugging suggestions:")
		fmt.Println("   1. The 403 error typically means your OAuth token doesn't have")
		fmt.Println("      the 'spotnodepools:write' scope or equivalent permission.")
		fmt.Println()
		fmt.Println("   2. Check if your organization has billing enabled.")
		fmt.Println("      SpotNodePools require billing to be set up.")
		fmt.Println()
		fmt.Println("   3. Verify your refresh token was generated from an account with")
		fmt.Println("      admin or owner rights in the Rackspace Spot console.")
		fmt.Println()
		fmt.Println("   4. Try regenerating the refresh token from:")
		fmt.Println("      Rackspace Spot Console > API Access > Terraform > Get New Token")
	} else {
		fmt.Println("✓ Create node pool (SDK method) succeeded!")
	}
	fmt.Println()

	// Step 4: Check what scopes/permissions we have
	fmt.Println("Step 4: Analyzing token information...")
	analyzeToken(client.Token)
}

func checkOrgEndpoint(ctx context.Context, client *rxtspot.RackspaceSpotClient, org string) {
	// Try to get org info
	url := fmt.Sprintf("%s/v1/orgs/%s", client.BaseURL, org)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		fmt.Printf("      ❌ Failed to create request: %v\n", err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+client.Token)

	resp, err := client.HTTPClient.Do(req)
	if err != nil {
		fmt.Printf("      ❌ Request failed: %v\n", err)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	fmt.Printf("      GET %s -> HTTP %d\n", url, resp.StatusCode)
	if resp.StatusCode >= 400 {
		fmt.Printf("      Response: %s\n", string(body))
	} else {
		// Pretty print JSON
		var prettyJSON bytes.Buffer
		if json.Indent(&prettyJSON, body, "      ", "  ") == nil {
			fmt.Printf("      Response (truncated):\n%s\n", truncate(prettyJSON.String(), 500))
		}
	}
}

func testCreateNodePool(ctx context.Context, client *rxtspot.RackspaceSpotClient, org, cloudspace string) error {
	// Create a minimal test pool spec
	createBody := map[string]interface{}{
		"apiVersion": "ngpc.rxt.io/v1",
		"kind":       "SpotNodePool",
		"metadata": map[string]interface{}{
			"name": "debug-test-pool-delete-me",
		},
		"spec": map[string]interface{}{
			"serverClass": "gp.vs1.medium-ord", // Small instance for testing
			"desired":     0,                    // 0 desired so we don't actually spin anything up
			"bidPrice":    0.01,
			"autoscaling": map[string]interface{}{
				"enabled":  false,
				"minNodes": 0,
				"maxNodes": 0,
			},
		},
	}

	body, err := json.Marshal(createBody)
	if err != nil {
		return fmt.Errorf("marshaling request: %w", err)
	}

	url := fmt.Sprintf("%s/v1/orgs/%s/cloudspaces/%s/spotnodepools", client.BaseURL, org, cloudspace)
	fmt.Printf("      URL: %s\n", url)
	fmt.Printf("      Body: %s\n", string(body))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+client.Token)

	resp, err := client.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	fmt.Printf("      Response: HTTP %d\n", resp.StatusCode)
	fmt.Printf("      Body: %s\n", string(respBody))

	if resp.StatusCode >= 400 {
		return fmt.Errorf("API error (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	// Clean up - delete the test pool
	fmt.Println("      Cleaning up test pool...")
	deleteURL := fmt.Sprintf("%s/v1/orgs/%s/spotnodepools/debug-test-pool-delete-me", client.BaseURL, org)
	delReq, _ := http.NewRequestWithContext(ctx, http.MethodDelete, deleteURL, nil)
	delReq.Header.Set("Authorization", "Bearer "+client.Token)
	delResp, _ := client.HTTPClient.Do(delReq)
	if delResp != nil {
		delResp.Body.Close()
		fmt.Printf("      Delete response: HTTP %d\n", delResp.StatusCode)
	}

	return nil
}

func testCreateViaSDK(ctx context.Context, client *rxtspot.RackspaceSpotClient, org, cloudspace string) error {
	// Use the SDK's CreateSpotNodePool method which takes org, not cloudspace
	// The SDK method signature is: CreateSpotNodePool(ctx, org, pool)
	// BUT the SpotNodePool struct must include the Cloudspace field!

	poolName := uuid.New().String()
	pool := rxtspot.SpotNodePool{
		Name:        poolName,
		Cloudspace:  cloudspace, // This field is required by the admission webhook!
		ServerClass: "gp.vs1.medium-ord",
		Desired:     1, // Must be >= 1 per admission webhook; will delete immediately after
		BidPrice:    "0.01",
	}

	fmt.Printf("      SDK method: CreateSpotNodePool(org=%q, pool=%+v)\n", org, pool)

	err := client.CreateSpotNodePool(ctx, org, pool)
	if err != nil {
		fmt.Printf("      SDK error: %v\n", err)
		return err
	}

	fmt.Println("      SDK create succeeded!")

	// Clean up
	fmt.Println("      Cleaning up SDK test pool...")
	if delErr := client.DeleteSpotNodePool(ctx, org, pool.Name); delErr != nil {
		fmt.Printf("      Warning: cleanup failed: %v\n", delErr)
	} else {
		fmt.Println("      Cleanup succeeded")
	}

	return nil
}

func analyzeToken(token string) {
	// JWT tokens have 3 parts separated by dots
	parts := bytes.Split([]byte(token), []byte("."))
	if len(parts) != 3 {
		fmt.Println("   Token is not a valid JWT (expected 3 parts)")
		return
	}

	fmt.Println("   Token appears to be a valid JWT")
	fmt.Printf("   Header length:  %d bytes\n", len(parts[0]))
	fmt.Printf("   Payload length: %d bytes\n", len(parts[1]))
	fmt.Printf("   Signature length: %d bytes\n", len(parts[2]))

	// Note: We're not decoding the JWT payload because that would require
	// base64 decoding and the token might contain sensitive info.
	// The key thing to check is that we have a properly formatted token.
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

