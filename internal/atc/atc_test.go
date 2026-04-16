package atc

import (
	"os"
	"testing"

	"github.com/curbz/decimal-niner/internal/flightphase"
	"github.com/curbz/decimal-niner/internal/logger"
)

// This runs before any tests in this package
func init() {

	logger.Log.Info("running prerequisite atc package init function")

	// Check for custom config file location
	configPath := os.Getenv("D9_CONFIG_PATH")

	if configPath == "" {
		// Use default config location.
		// Move up two levels to the root of the repo so that config.yaml and /resources is found
		configPath = "../.."
	} else {
		logger.Log.Info("loading configuration from custom location", configPath)
	}

	err := os.Chdir(configPath)
	if err != nil {
		logger.Log.Fatalf("test execution failed - error loading configuration: %v", err)
	}

}

func TestIsNorthAmerica(t *testing.T) {
	tests := []struct {
		icao string
		want bool
	}{
		// Empty string
		{"", false},
		// USA (K prefix)
		{"KJFK", true},   // New York
		{"KIND", true},   // Indianapolis
		{"KORD", true},   // Chicago
		{"KLAS", true},   // Las Vegas
		{"K", true},      // single K
		{"KABC", true},   // any K prefix
		// Canada (C prefix)
		{"CYYZ", true},   // Toronto
		{"CYUL", true},   // Montreal
		{"CYVRX", true},  // Vancouver
		{"C", true},      // single C
		// Alaska (PA prefix)
		{"PANC", true},   // Anchorage
		{"PAFB", true},   // Fairbanks
		// Hawaii (PH prefix)
		{"PHNL", true},   // Honolulu
		{"PHOG", true},   // any PH prefix
		// Mexico (M prefix)
		{"MMMX", true},   // Mexico City
		{"MMUN", true},   // Monterrey
		{"M", true},      // single M
		// Non-North American ICAO codes
		{"LFPG", false},  // Paris, France
		{"EGLL", false},  // London, UK
		{"RJTT", false},  // Tokyo, Japan
		{"UUWW", false},  // Moscow, Russia
		{"SBGR", false},  // São Paulo, Brazil
		{"FAOR", false},  // Johannesburg, South Africa
		// Other prefixes
		{"A", false},
		{"Z", false},
		{"P", false},     // P alone (not PA or PH)
		{"XB-ABC", false},// Mexico prefix in registration style but as ICAO
	}

	for _, tc := range tests {
		got := isNorthAmerica(tc.icao)
		if got != tc.want {
			t.Errorf("isNorthAmerica(%q) = %v; want %v", tc.icao, got, tc.want)
		}
	}
}

func TestIsAirborne(t *testing.T) {
	tests := []struct {
		phase int
		departIsAirborne bool
		want  bool
	}{
		{phase: flightphase.Parked.Index(), departIsAirborne: false, want: false},
		{phase: flightphase.Startup.Index(), departIsAirborne: false, want: false},
		{phase: flightphase.TaxiOut.Index(), departIsAirborne: false, want: false},
		{phase: flightphase.Depart.Index(), departIsAirborne: false, want: false},
		{phase: flightphase.Depart.Index(), departIsAirborne: true, want: true},
		{phase: flightphase.Climbout.Index(), departIsAirborne: false, want: true},
		{phase: flightphase.Cruise.Index(), departIsAirborne: false, want: true},
		{phase: flightphase.Approach.Index(), departIsAirborne: false, want: true},
		{phase: flightphase.Holding.Index(), departIsAirborne: false, want: true},
		{phase: flightphase.Final.Index(), departIsAirborne: false, want: true},
		{phase: flightphase.Braking.Index(), departIsAirborne: false, want: false},
		{phase: flightphase.TaxiIn.Index(), departIsAirborne: false, want: false},
		{phase: flightphase.Shutdown.Index(), departIsAirborne: false, want: false},
		{phase: flightphase.Unknown.Index(), departIsAirborne: false, want: false},
		{phase: 99, want: false},
	}

	for _, tc := range tests {
		got := IsAirborne(tc.phase, tc.departIsAirborne)
		if got != tc.want {
			t.Errorf("isAirborne(%d) = %v; want %v", tc.phase, got, tc.want)
		}
	}
}
