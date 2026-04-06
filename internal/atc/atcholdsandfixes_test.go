package atc

import (
	"math"
	"testing"

	"github.com/curbz/decimal-niner/internal/flightphase"
)

func rad(deg float64) float64 { return deg * math.Pi / 180 }

// Test dataset: 7 real-world holds (VOR-based)
func testHolds() map[string]*Hold {
	holds := map[string]*Hold{
		// Heathrow stacks
		"LAM": {Ident: "LAM", Region: "EG", LatRad: rad(51.646025), LonRad: rad(0.151702778)},
		"BNN": {Ident: "BNN", Region: "EG", LatRad: rad(51.721), LonRad: rad(-0.561)},
		"BIG": {Ident: "BIG", Region: "EG", LatRad: rad(51.330), LonRad: rad(0.033)},
		"OCK": {Ident: "OCK", Region: "EG", LatRad: rad(51.237), LonRad: rad(-0.561)},

		// Global holds
		"SFO": {Ident: "SFO", Region: "US", LatRad: rad(37.619), LonRad: rad(-122.374)}, // SFO VOR
		"HNL": {Ident: "HNL", Region: "US", LatRad: rad(21.318), LonRad: rad(-157.922)}, // Honolulu
		"SYD": {Ident: "SYD", Region: "AU", LatRad: rad(-33.946), LonRad: rad(151.177)}, // Sydney
	}

	// Precompute unit vectors
	for _, h := range holds {
		h.InitUnitVector()
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
			ac := &Aircraft{Flight: Flight{Position: Position{Lat: tc.lat, Long: tc.lon}}}
			h := s.findNearestHold(ac, "")
			if h == nil {
				t.Fatalf("expected %s, got nil", tc.expected)
			}
			if h.Ident != tc.expected {
				t.Fatalf("expected %s, got %s", tc.expected, h.Ident)
			}
		})
	}
}

func TestFindNearestHoldPriority(t *testing.T) {
	// Setup service with global holds
	s := &Service{Holds: testHolds(), Airports: map[string]*Airport{}}

	// Helper to create airport hold
	makeHold := func(name string, lat, lon float64) *Hold {
		h := &Hold{Ident: name, LatRad: rad(lat), LonRad: rad(lon)}
		h.InitUnitVector()
		return h
	}

	// 1) Airport holds preferred over global
	ap := &Airport{ICAO: "EGAA", Name: "Test", Runways: map[string]*Runway{}, Holds: []*Hold{makeHold("LOCAL", 51.50, -0.10)}}
	s.Airports["EGAA"] = ap
	ac := &Aircraft{Flight: Flight{Position: Position{Lat: 51.50, Long: -0.10}}}
	h := s.findNearestHold(ac, "EGAA")
	if h == nil || h.Ident != "LOCAL" {
		t.Fatalf("airport hold not preferred, got %v", h)
	}

	// 2) Go-around should return the runway MAFix if present
	// Create holds: MA1 (target) and OTHER (closer)
	ap2 := &Airport{ICAO: "EGLL", Name: "GA", Runways: map[string]*Runway{"27R": {MAFix: "MA1"}}, Holds: []*Hold{makeHold("MA1", 51.64, 0.15), makeHold("OTHER", 51.65, 0.16)}}
	s.Airports["EGLL"] = ap2
	ac2 := &Aircraft{Flight: Flight{Position: Position{Lat: 51.64, Long: 0.16}, AssignedRunway: "27R", Phase: flightphase.Phase{Current: flightphase.GoAround.Index()}}}
	h2 := s.findNearestHold(ac2, "EGLL")
	if h2 == nil || h2.Ident != "MA1" {
		t.Fatalf("go-around MAFix not returned, got %v", h2)
	}

	// 3) Go-around with MAFix not in airport holds should fallback to nearest airport hold
	ap3 := &Airport{ICAO: "EGKK", Name: "NoMA", Runways: map[string]*Runway{"09": {MAFix: "MISSING"}}, Holds: []*Hold{makeHold("A1", 51.20, -0.50), makeHold("A2", 51.25, -0.55)}}
	s.Airports["EGKK"] = ap3
	ac3 := &Aircraft{Flight: Flight{Position: Position{Lat: 51.21, Long: -0.51}, AssignedRunway: "09", Phase: flightphase.Phase{Current: flightphase.GoAround.Index()}}}
	h3 := s.findNearestHold(ac3, "EGKK")
	if h3 == nil || (h3.Ident != "A1" && h3.Ident != "A2") {
		t.Fatalf("expected nearest airport hold fallback, got %v", h3)
	}

	// 4) Airport exists but has no holds -> global fallback
	s.Airports["EMPTY"] = &Airport{ICAO: "EMPTY", Name: "Empty", Runways: map[string]*Runway{}, Holds: []*Hold{}}
	ac4 := &Aircraft{Flight: Flight{Position: Position{Lat: 37.60, Long: -122.40}}}
	h4 := s.findNearestHold(ac4, "EMPTY")
	if h4 == nil || h4.Ident != "SFO" {
		t.Fatalf("expected global fallback to SFO, got %v", h4)
	}
}

func TestCleanFixName(t *testing.T) {
	tests := []struct {
		in  string
		out string
	}{
		{"RDU (COUNTRY)", "RDU"},
		{"KJFK", "KJFK"},
		{"SOME NAME VOR/DME", "SOME NAME"},
		{"ALPHA NDB", "ALPHA"},
		{"BRAVO VOR", "BRAVO"},
		{"CHARLIE FARLIE DME-ILS", "CHARLIE FARLIE"},
		{"DELTA DME", "DELTA"},
		{"NEW VORI VORTAC", "NEW VORI"},
		{"", ""},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got := cleanFixName(tc.in)
			if got != tc.out {
				t.Errorf("cleanFixName(%q) = %q; want %q", tc.in, got, tc.out)
			}
		})
	}
}
