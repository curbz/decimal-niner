package atc

import (
	"math"
	"testing"
)

func rad(deg float64) float64 { return deg * math.Pi / 180 }

// Test dataset: 7 real-world holds (VOR-based)
func testHolds() []Hold {
    holds := []Hold{
        // Heathrow stacks
        {Name: "LAM", Region: "EG", LatRad: rad(51.646025), LonRad: rad(0.151702778)},
        {Name: "BNN", Region: "EG", LatRad: rad(51.721), LonRad: rad(-0.561)},
        {Name: "BIG", Region: "EG", LatRad: rad(51.330), LonRad: rad(0.033)},
        {Name: "OCK", Region: "EG", LatRad: rad(51.237), LonRad: rad(-0.561)},

        // Global holds
        {Name: "SFO", Region: "US", LatRad: rad(37.619), LonRad: rad(-122.374)}, // SFO VOR
        {Name: "HNL", Region: "US", LatRad: rad(21.318), LonRad: rad(-157.922)}, // Honolulu
        {Name: "SYD", Region: "AU", LatRad: rad(-33.946), LonRad: rad(151.177)}, // Sydney
    }

    // Precompute unit vectors
    for i := range holds {
        holds[i].InitUnitVector()
    }
    return holds
}

func TestNearestHold(t *testing.T) {
	s := &Service{
		Holds: testHolds(),
	}

    tests := []struct {
        name     string
        lat, lon float64
        expected string
    }{
        {"Near LAM", 51.50, 0.10, "LAM"},
        {"Near BNN", 51.80, -0.50, "BNN"},
        {"Near BIG", 51.30, 0.00, "BIG"},
        {"Near OCK", 51.20, -0.50, "OCK"},
        {"Near SFO", 37.60, -122.40, "SFO"},
        {"Near HNL", 21.30, -157.90, "HNL"},
        {"Near SYD", -33.90, 151.20, "SYD"},
    }

    for _, tc := range tests {
        t.Run(tc.name, func(t *testing.T) {
            h := s.findNearestHold(tc.lat, tc.lon)
            if h == nil {
                t.Fatalf("expected %s, got nil", tc.expected)
            }
            if h.Name != tc.expected {
                t.Fatalf("expected %s, got %s", tc.expected, h.Name)
            }
        })
    }
}
