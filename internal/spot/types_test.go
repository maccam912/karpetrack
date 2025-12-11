package spot

import (
	"encoding/json"
	"testing"
)

// Sample pricing data from Rackspace Spot API (truncated for testing)
const samplePricingJSON = `{
	"regions": {
		"us-east-iad-1": {
			"generation": "v1",
			"serverclasses": {
				"gp.vs1.2xlarge-iad": {
					"20_percentile": 0.041,
					"50_percentile": 0.041,
					"80_percentile": 0.046,
					"market_price": "0.010000",
					"cpu": "16",
					"memory": "60GB",
					"display_name": "General Virtual Server.Double Extra Large",
					"category": "General Purpose",
					"description": "US East, Ashburn, VA"
				},
				"gp.vs1.medium-iad": {
					"20_percentile": 0.002,
					"50_percentile": 0.002,
					"80_percentile": 0.006,
					"market_price": "0.001000",
					"cpu": "2",
					"memory": "3.75GB",
					"display_name": "General Virtual Server.Medium",
					"category": "General Purpose",
					"description": "US East, Ashburn, VA"
				},
				"ch.vs1.large-iad": {
					"20_percentile": 0.006,
					"50_percentile": 0.011,
					"80_percentile": 0.011,
					"market_price": "0.005000",
					"cpu": "4",
					"memory": "7.5GB",
					"display_name": "Compute Virtual Server.Large",
					"category": "Compute Heavy",
					"description": "US East, Ashburn, VA"
				}
			}
		},
		"us-west-sjc-1": {
			"generation": "v2",
			"serverclasses": {
				"gp.vs2.medium-sjc": {
					"20_percentile": 0.011,
					"50_percentile": 0.011,
					"80_percentile": 0.011,
					"market_price": "0.010000",
					"cpu": "2",
					"memory": "4GB",
					"display_name": "General Virtual Server.Medium",
					"category": "General Purpose",
					"description": "US West, San Jose, CA"
				}
			}
		}
	}
}`

func TestFlexIntUnmarshal(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected int
	}{
		{"integer", `16`, 16},
		{"string integer", `"16"`, 16},
		{"string with GB suffix", `"60GB"`, 60},
		{"string with decimal and GB suffix", `"3.75GB"`, 3},
		{"string with decimal 7.5GB", `"7.5GB"`, 7},
		{"string with decimal no suffix", `"3.75"`, 3},
		{"string 4GB", `"4GB"`, 4},
		{"string 128GB", `"128GB"`, 128},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var f FlexInt
			err := json.Unmarshal([]byte(tt.input), &f)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if int(f) != tt.expected {
				t.Errorf("expected %d, got %d", tt.expected, int(f))
			}
		})
	}
}

func TestPricingDataUnmarshal(t *testing.T) {
	var data PricingData
	err := json.Unmarshal([]byte(samplePricingJSON), &data)
	if err != nil {
		t.Fatalf("failed to unmarshal pricing data: %v", err)
	}

	// Check we have the expected regions
	if len(data.Regions) != 2 {
		t.Errorf("expected 2 regions, got %d", len(data.Regions))
	}

	// Check us-east-iad-1 region
	iadRegion, ok := data.Regions["us-east-iad-1"]
	if !ok {
		t.Fatal("expected us-east-iad-1 region")
	}
	if iadRegion.Generation != "v1" {
		t.Errorf("expected generation v1, got %s", iadRegion.Generation)
	}
	if len(iadRegion.ServerClasses) != 3 {
		t.Errorf("expected 3 server classes, got %d", len(iadRegion.ServerClasses))
	}

	// Check specific server class with 60GB memory
	gpLarge, ok := iadRegion.ServerClasses["gp.vs1.2xlarge-iad"]
	if !ok {
		t.Fatal("expected gp.vs1.2xlarge-iad server class")
	}
	if int(gpLarge.CPU) != 16 {
		t.Errorf("expected CPU 16, got %d", int(gpLarge.CPU))
	}
	if int(gpLarge.Memory) != 60 {
		t.Errorf("expected Memory 60, got %d", int(gpLarge.Memory))
	}
	if gpLarge.Category != "General Purpose" {
		t.Errorf("expected category 'General Purpose', got '%s'", gpLarge.Category)
	}

	// Check server class with 3.75GB memory (decimal)
	gpMedium, ok := iadRegion.ServerClasses["gp.vs1.medium-iad"]
	if !ok {
		t.Fatal("expected gp.vs1.medium-iad server class")
	}
	if int(gpMedium.CPU) != 2 {
		t.Errorf("expected CPU 2, got %d", int(gpMedium.CPU))
	}
	if int(gpMedium.Memory) != 3 {
		t.Errorf("expected Memory 3 (truncated from 3.75), got %d", int(gpMedium.Memory))
	}

	// Check server class with 7.5GB memory
	chLarge, ok := iadRegion.ServerClasses["ch.vs1.large-iad"]
	if !ok {
		t.Fatal("expected ch.vs1.large-iad server class")
	}
	if int(chLarge.Memory) != 7 {
		t.Errorf("expected Memory 7 (truncated from 7.5), got %d", int(chLarge.Memory))
	}

	// Check v2 region
	sjcRegion, ok := data.Regions["us-west-sjc-1"]
	if !ok {
		t.Fatal("expected us-west-sjc-1 region")
	}
	if sjcRegion.Generation != "v2" {
		t.Errorf("expected generation v2, got %s", sjcRegion.Generation)
	}
}

func TestServerClassFields(t *testing.T) {
	var data PricingData
	if err := json.Unmarshal([]byte(samplePricingJSON), &data); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	sc := data.Regions["us-east-iad-1"].ServerClasses["gp.vs1.2xlarge-iad"]

	// Check all fields are properly parsed
	if sc.DisplayName != "General Virtual Server.Double Extra Large" {
		t.Errorf("unexpected display_name: %s", sc.DisplayName)
	}
	if sc.Category != "General Purpose" {
		t.Errorf("unexpected category: %s", sc.Category)
	}
	if sc.Description != "US East, Ashburn, VA" {
		t.Errorf("unexpected description: %s", sc.Description)
	}
	if sc.MarketPrice != "0.010000" {
		t.Errorf("unexpected market_price: %s", sc.MarketPrice)
	}
	if sc.Percentile20 != 0.041 {
		t.Errorf("unexpected 20_percentile: %f", sc.Percentile20)
	}
	if sc.Percentile50 != 0.041 {
		t.Errorf("unexpected 50_percentile: %f", sc.Percentile50)
	}
	if sc.Percentile80 != 0.046 {
		t.Errorf("unexpected 80_percentile: %f", sc.Percentile80)
	}
}
