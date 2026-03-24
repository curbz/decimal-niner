package atc

import "testing"

func TestGetTransitionLevel(t *testing.T) {
	tests := []struct {
		name          string
		transitionAlt int
		currBaro      float64
		want          int
	}{
		{"standard_equal", 6000, 101325.0, 70},
		{"standard_above", 6000, 101400.0, 70},
		{"low_pressure", 6000, 101000.0, 80},
		{"zero_alt_standard", 0, 101325.0, 10},
		{"small_alt_low_pressure", 50, 101000.0, 20},
		{"hundred_low_pressure", 100, 101000.0, 21},
		{"odd_alt_standard", 3500, 101325.0, 45},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getTransitionLevel(tt.transitionAlt, tt.currBaro)
			if got != tt.want {
				t.Fatalf("getTransitionLevel(%d, %f) = %d; want %d", tt.transitionAlt, tt.currBaro, got, tt.want)
			}
		})
	}
}
