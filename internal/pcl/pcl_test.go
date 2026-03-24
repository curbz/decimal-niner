package pcl

import (
	"testing"
)

func TestPCL(t *testing.T) {
	// Mock Application State
	altitude := 4000.0
	approach := "ILS"
	speed := 260.0

	// Dynamic Context with Providers
	ctx := PCLContext{
		"ALTITUDE": func() interface{} { return altitude },
		"APPROACH_TYPE": func() interface{} { return approach },
		"SPEED": func() interface{} { return speed },
		"CALLSIGN": func() interface{} { return "Speedbird 123" },
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
			"Basic WHEN Statement (True)",
			"{WHEN $APPROACH_TYPE EQ 'ILS' SAY 'intercept the ILS and'} slow to 180 knots",
			"intercept the ILS and slow to 180 knots",
		},
		{
			"Basic WHEN Statement (False)",
			"{WHEN $APPROACH_TYPE EQ 'VISUAL' SAY 'report field in sight'} cleared to land",
			"cleared to land",
		},
		{
			"Boolean AND Logic",
			"{WHEN $ALTITUDE LT 10000 AND $SPEED GT 250 SAY 'reduce speed to 250'} $CALLSIGN",
			"reduce speed to 250 Speedbird 123",
		},
		{
			"Nested Logic (Climb/Descend/Maintain)",
			"maintain course, {WHEN $ALTITUDE GT 5000 SAY 'descend to' OTHERWISE {WHEN $ALTITUDE GT 3000 SAY 'maintain' OTHERWISE SAY 'climb to'}} $ALTITUDE",
			"maintain course, maintain 4000",
		},
		{
			"Deeply Nested Logic (Targeting climb)",
			"{WHEN $ALTITUDE GT 8000 SAY 'descend' OTHERWISE {WHEN $ALTITUDE GT 6000 SAY 'maintain' OTHERWISE SAY 'climb'}} to 7000",
			"climb to 7000",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Reset vars for specific tests if needed
			if tc.name == "Deeply Nested Logic (Targeting climb)" {
				altitude = 2000.0
			} else {
				altitude = 4000.0
			}

			res, err := ProcessPhrase(tc.phrase, ctx)
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
			if res != tc.expected {
				t.Errorf("Result mismatch.\nGot:  %s\nWant: %s", res, tc.expected)
			}
		})
	}
}
