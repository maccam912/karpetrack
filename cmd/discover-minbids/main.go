package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"time"

	"github.com/google/uuid"
	rxtspot "github.com/rackspace-spot/spot-go-sdk/api/v1"

	"github.com/maccam912/karpetrack/internal/spot"
)

type Config struct {
	RefreshToken string
	Org          string
	Cloudspace   string
	OutputFile   string
}

func main() {
	var config Config

	flag.StringVar(&config.RefreshToken, "refresh-token", os.Getenv("RACKSPACE_SPOT_REFRESH_TOKEN"), "Rackspace Spot refresh token")
	flag.StringVar(&config.Org, "org", os.Getenv("RACKSPACE_SPOT_ORG"), "Rackspace Spot organization name")
	flag.StringVar(&config.Cloudspace, "cloudspace", os.Getenv("RACKSPACE_SPOT_CLOUDSPACE"), "Rackspace Spot cloudspace name")
	flag.StringVar(&config.OutputFile, "output", "min_bid_prices.txt", "Output file for minimum bid prices")
	flag.Parse()

	if config.RefreshToken == "" {
		fmt.Fprintln(os.Stderr, "Error: --refresh-token or RACKSPACE_SPOT_REFRESH_TOKEN is required")
		os.Exit(1)
	}
	if config.Org == "" {
		fmt.Fprintln(os.Stderr, "Error: --org or RACKSPACE_SPOT_ORG is required")
		os.Exit(1)
	}
	if config.Cloudspace == "" {
		fmt.Fprintln(os.Stderr, "Error: --cloudspace or RACKSPACE_SPOT_CLOUDSPACE is required")
		os.Exit(1)
	}

	if err := run(config); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(config Config) error {
	ctx := context.Background()

	// Create Rackspace Spot client
	httpClient := &http.Client{
		Timeout: 30 * time.Second,
	}
	spotClient, err := rxtspot.NewSpotClient(&rxtspot.Config{
		BaseURL:      "https://spot.rackspace.com",
		OAuthURL:     "https://login.spot.rackspace.com",
		RefreshToken: config.RefreshToken,
		HTTPClient:   httpClient,
	})
	if err != nil {
		return fmt.Errorf("creating Rackspace Spot client: %w", err)
	}

	// Authenticate
	if _, err := spotClient.Authenticate(ctx); err != nil {
		return fmt.Errorf("authenticating with Rackspace Spot: %w", err)
	}

	fmt.Println("Authenticated successfully!")

	// Get pricing data to discover all server classes
	pricing := spot.NewPricingProvider()
	pricingData, err := pricing.GetPricing(ctx)
	if err != nil {
		return fmt.Errorf("getting pricing data: %w", err)
	}

	// Collect all unique server classes
	serverClasses := make(map[string]bool)
	for _, region := range pricingData.Regions {
		for serverClass := range region.ServerClasses {
			serverClasses[serverClass] = true
		}
	}

	// Sort for consistent output
	sortedClasses := make([]string, 0, len(serverClasses))
	for sc := range serverClasses {
		sortedClasses = append(sortedClasses, sc)
	}
	sort.Strings(sortedClasses)

	fmt.Printf("Found %d unique server classes to probe\n", len(sortedClasses))
	fmt.Println()

	// Regex to extract minimum bid price from error message
	// Example: "BidPrice must be greater than or equal to the minimum bid price of 0.010000, but got 0.000500"
	minBidRegex := regexp.MustCompile(`minimum bid price of (\d+\.\d+)`)

	// Results map
	results := make(map[string]string)

	// Probe each server class with a very low bid
	for i, serverClass := range sortedClasses {
		fmt.Printf("[%d/%d] Probing %s... ", i+1, len(sortedClasses), serverClass)

		poolName := uuid.New().String()
		err := spotClient.CreateSpotNodePool(ctx, config.Org, rxtspot.SpotNodePool{
			Name:        poolName,
			Cloudspace:  config.Cloudspace,
			ServerClass: serverClass,
			Desired:     1,       // API requires desired > 0
			BidPrice:    "0.001", // Intentionally below most minimums (3 decimal places max)
		})

		if err != nil {
			errStr := err.Error()
			matches := minBidRegex.FindStringSubmatch(errStr)
			if len(matches) >= 2 {
				minBid := matches[1]
				fmt.Printf("min bid = $%s\n", minBid)
				results[serverClass] = minBid
			} else {
				// Different error - might be invalid server class, etc.
				fmt.Printf("error: %s\n", errStr)
				results[serverClass] = "ERROR: " + errStr
			}
		} else {
			// Unexpectedly succeeded - delete it immediately
			fmt.Printf("succeeded (unexpected!) - deleting...\n")
			_ = spotClient.DeleteSpotNodePool(ctx, config.Org, poolName)
			results[serverClass] = "0.001" // If it worked, 0.001 is valid
		}

		// Small delay to avoid rate limiting
		time.Sleep(200 * time.Millisecond)
	}

	// Write results to file
	f, err := os.Create(config.OutputFile)
	if err != nil {
		return fmt.Errorf("creating output file: %w", err)
	}
	defer f.Close()

	fmt.Fprintln(f, "# Rackspace Spot Minimum Bid Prices")
	fmt.Fprintln(f, "# Generated:", time.Now().Format(time.RFC3339))
	fmt.Fprintln(f, "# Format: server_class=min_bid_price")
	fmt.Fprintln(f)

	for _, serverClass := range sortedClasses {
		fmt.Fprintf(f, "%s=%s\n", serverClass, results[serverClass])
	}

	fmt.Println()
	fmt.Printf("Results written to %s\n", config.OutputFile)

	return nil
}
