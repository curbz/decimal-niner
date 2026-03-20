package atc

import (
	"os"
	"testing"

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

func TestGetCountryFromRegistration(t *testing.T) {
	tests := []struct {
		reg  string
		want string
	}{
		{"", ""},
		{"G-ABCD", "EG"}, // 1-char prefix fallback
		{"G", "EG"},      // single-char
		{"GBR123", "EG"}, // two-letter not in map -> fallback to 1-char
		{"XB-ABC", "MM"}, // two-letter match
		{"XB", "MM"},     // exact two-letter
		{"EI-XYZ", "EI"}, // two-letter exact
		{"N12345", "K"},  // 1-char N -> K (USA)
		{"D-ABCD", "ED"}, // Germany
		{"F123", "LF"},   // France
		{"VH-OAA", "YW"}, // Australia (two-letter)
		{"C-FABC", "CY"}, // Canada
		{"g-ABCD", ""},   // case-sensitive: lowercase not mapped
	}

	for _, tc := range tests {
		got := GetCountryFromRegistration(tc.reg)
		if got != tc.want {
			t.Errorf("GetCountryFromRegistration(%q) = %q; want %q", tc.reg, got, tc.want)
		}
	}
}
