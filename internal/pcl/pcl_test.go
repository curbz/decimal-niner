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