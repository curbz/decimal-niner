package atc

import (
	"testing"

	"github.com/curbz/decimal-niner/internal/flightphase"
)

// Test dataset: 7 real-world holds (VOR-based)
func testHolds() map[string]*Hold {
	holds := map[string]*Hold{
		// Heathrow stacks
		"LAM_EG": {Ident: "LAM", Region: "EG", Lat: 51.646025, Lon: 0.151702778},
		"BNN_EG": {Ident: "BNN", Region: "EG", Lat: 51.721, Lon: -0.561},
		"BIG_EG": {Ident: "BIG", Region: "EG", Lat: 51.330, Lon: 0.03},
		"OCK_EG": {Ident: "OCK", Region: "EG", Lat: 51.237, Lon: -0.561},

		// Global holds
		"SFO_US": {Ident: "SFO", Region: "US", Lat: 37.619, Lon: -122.374}, // SFO VOR
		"HNL_US": {Ident: "HNL", Region: "US", Lat: 21.318, Lon: -157.922}, // Honolulu
		"SYD_AU": {Ident: "SYD", Region: "AU", Lat: -33.946, Lon: 151.177}, // Sydney
	}

	// Precompute unit vectors
	for _, h := range holds {
		h.InitUnitVector()
	}
	return holds
}

func TestAssignHold(t *testing.T) {
	s := &Service{
		Holds: testHolds(),
	}

	tests := []struct {
		name     string
		lat, lon float64
		expected string
	}{
		{"Near LAM", 51.50, 0.10, "LAM_EG"},
		{"Near BNN", 51.80, -0.50, "BNN_EG"},
		{"Near BIG", 51.30, 0.00, "BIG_EG"},
		{"Near OCK", 51.20, -0.50, "OCK_EG"},
		{"Near SFO", 37.60, -122.40, "SFO_US"},
		{"Near HNL", 21.30, -157.90, "HNL_US"},
		{"Near SYD", -33.90, 151.20, "SYD_AU"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ac := &Aircraft{
				Flight: Flight{
					Origin:      "EGLL",
					Destination: "EGLL",
					Position:    Position{Lat: tc.lat, Long: tc.lon},
				},
			}
			s.AssignHold(ac, "", true)
			if ac.Flight.Holding == nil || ac.Flight.Holding.AssignedHoldIdent == "" {
				t.Fatalf("expected %s, got nil", tc.expected)
			} else {
				if ac.Flight.Holding.AssignedHoldIdent != tc.expected {
					t.Fatalf("expected %s, got %s", tc.expected, ac.Flight.Holding.AssignedHoldIdent)
				}
			}
		})
	}
}

func TestAssignHoldPriority(t *testing.T) {
	// Setup service with global holds
	s := &Service{Holds: testHolds(), Airports: map[string]*Airport{}}

	// Seed dummy airports for safe synthetic route calculations
	s.Airports["EGLL"] = &Airport{ICAO: "EGLL", Lat: 51.470, Lon: -0.454, Runways: map[string]*Runway{}, Holds: []*Hold{}}
	s.Airports["EGAA"] = &Airport{ICAO: "EGAA", Lat: 54.657, Lon: -6.215, Runways: map[string]*Runway{}, Holds: []*Hold{}}
	s.Airports["EGKK"] = &Airport{ICAO: "EGKK", Lat: 51.148, Lon: -0.190, Runways: map[string]*Runway{}, Holds: []*Hold{}}
	s.Airports["EMPTY"] = &Airport{ICAO: "EMPTY", Lat: 37.619, Lon: -122.374, Runways: map[string]*Runway{}, Holds: []*Hold{}}

	// Helper to create airport hold
	makeHold := func(name, region string, lat, lon float64) *Hold {
		h := &Hold{Ident: name, Region: region, Lat: lat, Lon: lon}
		h.InitUnitVector()
		return h
	}

	// 1) Airport holds preferred over global
	ap := s.Airports["EGAA"]
	ap.Holds = []*Hold{makeHold("LOCAL", "EG", 51.50, -0.10)}

	ac := &Aircraft{
		Flight: Flight{
			Origin:      "EGLL",
			Destination: "EGAA",
			Position:    Position{Lat: 51.50, Long: -0.10},
		},
	}
	s.AssignHold(ac, "EGAA", true)
	if ac.Flight.Holding == nil || ac.Flight.Holding.AssignedHoldIdent == "" || ac.Flight.Holding.AssignedHoldIdent != "LOCAL_EG" {
		t.Fatalf("airport hold not preferred, got %v", ac.Flight.Holding)
	}

	// 2) Go-around should return the runway MAFix if present
	ap2 := s.Airports["EGLL"]
	ap2.Runways = map[string]*Runway{"27R": {MAFix: "MA1"}}
	ap2.Holds = []*Hold{makeHold("MA1", "EG", 51.64, 0.15), makeHold("OTHER", "EG", 51.65, 0.16)}

	ac2 := &Aircraft{
		Flight: Flight{
			Origin:             "EGKK",
			Destination:        "EGLL",
			Position:           Position{Lat: 51.64, Long: 0.16},
			AssignedRunwayName: "27R",
			Phase:              flightphase.Phase{Current: flightphase.GoAround.Index()},
		},
	}
	s.AssignHold(ac2, "EGLL", true)
	if ac2.Flight.Holding == nil || ac2.Flight.Holding.AssignedHoldIdent == "" || ac2.Flight.Holding.AssignedHoldIdent != "MA1_EG" {
		t.Fatalf("go-around MAFix not returned, got %v", ac2.Flight.Holding)
	}

	// 3) Go-around with MAFix not in airport holds should fallback to nearest airport hold
	ap3 := s.Airports["EGKK"]
	ap3.Runways = map[string]*Runway{"09": {MAFix: "MISSING"}}
	ap3.Holds = []*Hold{makeHold("A1", "EG", 51.20, -0.50), makeHold("A2", "EG", 51.25, -0.55)}

	ac3 := &Aircraft{
		Flight: Flight{
			Origin:             "EGLL",
			Destination:        "EGKK",
			Position:           Position{Lat: 51.21, Long: -0.51},
			AssignedRunwayName: "09",
			Phase:              flightphase.Phase{Current: flightphase.GoAround.Index()},
		},
	}
	s.AssignHold(ac3, "EGKK", true)
	if ac3.Flight.Holding == nil || ac3.Flight.Holding.AssignedHoldIdent == "" || (ac3.Flight.Holding.AssignedHoldIdent != "A1_EG" && ac3.Flight.Holding.AssignedHoldIdent != "A2_EG") {
		t.Fatalf("expected nearest airport hold fallback, got %v", ac3.Flight.Holding)
	}

	// 4) Airport exists but has no holds -> global fallback
	ac4 := &Aircraft{
		Flight: Flight{
			Origin:      "EGLL",
			Destination: "EMPTY",
			Position:    Position{Lat: 37.60, Long: -122.40},
		},
	}
	s.AssignHold(ac4, "EMPTY", true)
	if ac4.Flight.Holding == nil || ac4.Flight.Holding.AssignedHoldIdent == "" || ac4.Flight.Holding.AssignedHoldIdent != "SFO_US" {
		t.Fatalf("expected global fallback to SFO_US, got %v", ac4.Flight.Holding)
	}
}

// TestHoldLookupAfterAssignment verifies that a hold can be retrieved using GetHoldByIdent
// after it has been assigned to an aircraft. This is a regression test for the issue where
// AssignedHoldIdent was stored as just the Ident instead of the full Ident_Region key.
func TestHoldLookupAfterAssignment(t *testing.T) {
	s := &Service{
		Holds: testHolds(),
		Airports: map[string]*Airport{
			"EGLL": {
				ICAO:    "EGLL",
				Lat:     51.470,
				Lon:     -0.454,
				Runways: map[string]*Runway{},
				Holds:   []*Hold{},
			},
		},
	}

	ac := &Aircraft{
		Flight: Flight{
			Origin:      "EGLL",
			Destination: "EGLL",
			Position:    Position{Lat: 51.50, Long: 0.10},
		},
	}

	// Assign a hold to the aircraft
	s.AssignHold(ac, "", true)

	// Verify that a hold was assigned
	if ac.Flight.Holding == nil || ac.Flight.Holding.AssignedHoldIdent == "" {
		t.Fatalf("no hold assigned")
	}

	// Verify that we can look up the hold using the assigned identifier
	hold := s.GetHoldByIdent(ac.Flight.Holding.AssignedHoldIdent)
	if hold == nil {
		t.Fatalf("hold %s not found - lookup failed", ac.Flight.Holding.AssignedHoldIdent)
	}

	// Verify the looked-up hold has expected properties
	if hold.Ident == "" || hold.Region == "" {
		t.Fatalf("hold has missing Ident or Region: %+v", hold)
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
