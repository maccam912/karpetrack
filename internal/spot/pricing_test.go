package spot

import (
	"testing"
)

// TestRoundBidPrice tests the bid price rounding function
func TestRoundBidPrice(t *testing.T) {
	tests := []struct {
		name     string
		input    float64
		expected float64
	}{
		// Zero and negative values
		{name: "zero returns step", input: 0, expected: 0.005},
		{name: "negative returns step", input: -0.01, expected: 0.005},

		// Exact multiples
		{name: "exact 0.005", input: 0.005, expected: 0.005},
		{name: "exact 0.010", input: 0.010, expected: 0.010},
		{name: "exact 0.015", input: 0.015, expected: 0.015},
		{name: "exact 0.100", input: 0.100, expected: 0.100},

		// Values needing rounding up
		{name: "0.001 rounds to 0.005", input: 0.001, expected: 0.005},
		{name: "0.004 rounds to 0.005", input: 0.004, expected: 0.005},
		{name: "0.006 rounds to 0.010", input: 0.006, expected: 0.010},
		{name: "0.011 rounds to 0.015", input: 0.011, expected: 0.015},
		{name: "0.014 rounds to 0.015", input: 0.014, expected: 0.015},
		{name: "0.016 rounds to 0.020", input: 0.016, expected: 0.020},

		// Real-world examples from the error case
		{name: "0.0110 rounds to 0.015", input: 0.0110, expected: 0.015},
		{name: "0.0111 rounds to 0.015", input: 0.0111, expected: 0.015},
		{name: "0.0149 rounds to 0.015", input: 0.0149, expected: 0.015},

		// Larger values
		{name: "0.27 stays at 0.27", input: 0.27, expected: 0.27},
		{name: "0.271 rounds to 0.275", input: 0.271, expected: 0.275},
		{name: "2.69 stays at 2.69", input: 2.69, expected: 2.69},
		{name: "2.691 rounds to 2.695", input: 2.691, expected: 2.695},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := RoundBidPrice(tt.input)
			// Use a small epsilon for floating point comparison
			epsilon := 0.0001
			diff := result - tt.expected
			if diff < -epsilon || diff > epsilon {
				t.Errorf("RoundBidPrice(%f) = %f, want %f", tt.input, result, tt.expected)
			}
		})
	}
}

// TestRoundBidPrice_AllMultiplesValid verifies that all outputs are valid multiples of 0.005
func TestRoundBidPrice_AllMultiplesValid(t *testing.T) {
	// Test a range of inputs and ensure all outputs are valid multiples
	for i := 0; i <= 1000; i++ {
		input := float64(i) * 0.001 // Test 0.000 to 1.000 in 0.001 increments
		result := RoundBidPrice(input)

		// Check that result is a multiple of 0.005
		// Due to floating point, we check if remainder is very close to 0 or very close to 0.005
		remainder := result - float64(int(result/BidPriceStep))*BidPriceStep
		if remainder > 0.0001 && remainder < BidPriceStep-0.0001 {
			t.Errorf("RoundBidPrice(%f) = %f is not a valid multiple of %f (remainder: %f)",
				input, result, BidPriceStep, remainder)
		}

		// Also verify result >= input (rounds up, not down) for positive inputs
		if input > 0 && result < input-0.0001 {
			t.Errorf("RoundBidPrice(%f) = %f rounds down instead of up", input, result)
		}
	}
}
