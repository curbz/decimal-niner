package pcl

import (
	"fmt"
	"testing"
)

func TestPCL(t *testing.T) {
	// Mock Application State
	altitude := 4000.0
	approach := "ILS"
	speed := 260.0

	// Dynamic Context with Providers using new $ and @ prefixes
	ctx := PCLContext{
		// Raw Data Providers ($)
		"$ALTITUDE":      func(args ...string) interface{} { return altitude },
		"$APPROACH_TYPE": func(args ...string) interface{} { return approach },
		"$SPEED":         func(args ...string) interface{} { return speed },
		"$CALLSIGN":      func(args ...string) interface{} { return "Speedbird 123" },

		// Formatting Macros (@)
		"@ALTITUDE": func(args ...string) interface{} {
			if altitude >= 18000 {
				return fmt.Sprintf("Flight Level %0.0f", altitude/100)
			}
			return fmt.Sprintf("%0.0f feet", altitude)
		},
		"@VALEDICTION": func(args ...string) interface{} {
			// Tests parameter extraction: {@VALEDICTION(5)}
			if len(args) > 0 && args[0] == "5" {
				return "Good day"
			}
			return "Goodbye"
		},
	}

	tests := []struct {
		name     string
		phrase   string
		expected string
	}{
		{
			"Simple Variable Replacement",
			"Contact tower, $CALLSIGN",
			"Contact tower, Speedbird 123",
		},
		{
			"Braced Variable Replacement",
			"You are at {$ALTITUDE} units",
			"You are at 4000 units",
		},
		{
			"Formatted Macro",
			"Climb and maintain @ALTITUDE",
			"Climb and maintain 4000 feet",
		},
		{
			"Parameterized Macro",
			"Contact center on 133.7, {@VALEDICTION(5)}",
			"Contact center on 133.7, Good day",
		},
		{
			"SAY Shortcut with Backticks",
			"The computer says {SAY `don't descend yet`}",
			"The computer says don't descend yet",
		},
		{
			"Complex WHEN with Contractions",
			"{WHEN $SPEED GT 250 SAY `don't exceed 250 knots` OTHERWISE SAY `it's pilot's discretion`}",
			"don't exceed 250 knots",
		},
		{
			"Boolean AND Logic",
			"{WHEN $ALTITUDE LT 10000 AND $SPEED GT 250 SAY `reduce speed`} $CALLSIGN",
			"reduce speed Speedbird 123",
		},
		{
			"Nested Logic",
			"maintain course, {WHEN $ALTITUDE GT 5000 SAY `descend to` OTHERWISE {WHEN $ALTITUDE GT 3000 SAY `maintain` OTHERWISE SAY `climb to` text}} $ALTITUDE",
			"maintain course, maintain 4000",
		},
		{
			"Nested Logic and variable inside SAY",
			"maintain course, {WHEN $ALTITUDE GT 5000 SAY `descend to` OTHERWISE {WHEN $ALTITUDE EQ 35000 SAY `maintain current altitude` OTHERWISE SAY `climb to $ALTITUDE`}}",
			"maintain course, climb to 4000",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Reset vars for specific tests
			if tc.name == "Nested Logic" {
				altitude = 4000.0
			}

			res, err := ProcessPhrase(tc.phrase, ctx)
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
			
			if res != tc.expected {
				t.Errorf("Result mismatch.\nGot:  %q\nWant: %q", res, tc.expected)
			}
		})
	}
}

func TestPCL_Comparators(t *testing.T) {
	tests := []struct {
		name     string
		left     interface{}
		op       string
		right    interface{}
		expected bool
	}{
		// --- EQUAL (EQ) ---
		{"EQ Numeric True", 250.0, "EQ", "250", true},
		{"EQ Numeric False", 250.0, "EQ", 240.0, false},
		{"EQ String True", "EGLL", "EQ", "EGLL", true},
		{"EQ String False", "EGLL", "EQ", "EGKK", false},

		// --- NOT EQUAL (NE) ---
		{"NE Numeric True", 5000, "NE", 3000, true},
		{"NE Numeric False", 120.5, "NE", "120.5", false},
		{"NE String True", "VOR", "NE", "ILS", true},
		{"NE String False", "GND", "NE", "GND", false},

		// --- LESS THAN (LT) ---
		{"LT Numeric True", 4000, "LT", 5000, true},
		{"LT Numeric Boundary", 4000, "LT", 4000, false},
		{"LT Numeric False", 6000, "LT", 5000, false},
		{"LT String Fallback", "B", "LT", "A", false}, // Lexicographical string comparison fallback

		// --- LESS THAN OR EQUAL (LE) ---
		{"LE Numeric True", 3000, "LE", 4000, true},
		{"LE Numeric Boundary", 4000, "LE", 4000, true},
		{"LE Numeric False", 5000, "LE", 4000, false},

		// --- GREATER THAN (GT) ---
		{"GT Numeric True", 260, "GT", 250, true},
		{"GT Numeric Boundary", 250, "GT", 250, false},
		{"GT Numeric False", 240, "GT", 250, false},

		// --- GREATER THAN OR EQUAL (GE) ---
		{"GE Numeric True", 12000, "GE", 10000, true},
		{"GE Numeric Boundary", 10000, "GE", 10000, true},
		{"GE Numeric False", 9000, "GE", 10000, false},

		// --- MIXED TYPE / STRING FALLBACK EDGE CASES ---
		{"Mixed Numeric and Word EQ", 250, "EQ", "heading", false},
		{"Mixed Numeric and Word NE", 250, "NE", "heading", true},
		{"Boolean True String Match", true, "EQ", "true", true},
		{"Boolean False String Match", false, "EQ", "false", true},
		{"Invalid Operator Fallback to String Match", "hello", "UNKNOWN_OP", "hello", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := evaluateComparison(tt.left, tt.op, tt.right)
			if result != tt.expected {
				t.Errorf("evaluateComparison(%v, %q, %v) = %v; want %v",
					tt.left, tt.op, tt.right, result, tt.expected)
			}
		})
	}
}
