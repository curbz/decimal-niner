package atc

import (
	"testing"

	"github.com/curbz/decimal-niner/internal/trafficglobal"
)

func TestPerformSearch(t *testing.T) {

	requiredAirports := map[string]bool{"EGLL": true, "EGKA": true, "EGNX": true, "EGHI": true}
	atcService := New("config.yaml", make(map[string][]trafficglobal.ScheduledFlight), requiredAirports)

	tests := []struct {
		label        string
		f, r         int
		la, lo, al   float64
		icao         string
		expectedICAO string
		expectedRole int
	}{
		{"Heathrow Ground (Proximity)", 0, 2, 51.4706, -0.4522, 1000.0, "", "EGLL", 2},
		{"Heathrow Ground (Freq)", 121905, 2, 51.4706, -0.4522, 1000.0, "", "EGLL", 2},
		{"Heathrow Tower (Freq)", 118505, 3, 51.4706, -0.4522, 1000.0, "", "EGLL", 3},
		{
			"Shared Freq Tie-breaker (Heathrow vs East Midlands)",
			121900, 2,
			51.4706, -0.4522, 10.0, // Standing at Heathrow
			"", "EGLL", 2, // Should pick EGLL because it's closer
		},
		{"London Center (Polygon)", 0, 6, 51.5, -0.1, 20000.0, "", "EGTT", 6},
		{"Shoreham Ground (Proximity)", 0, 2, 50.835, -0.297, 50.0, "", "EGKA", 2},
		{"Gatwick Arrival via Southampton (Polygon)", 0, 6, 50.95, -1.35, 15000.0, "", "EGTT", 6},
		{"Shanwick Oceanic (Polygon Match)", 0, 6, 45.0, -25.0, 35000.0, "", "EGGX", 6},
		{"Celtic Sea (London FIR)", 0, 6, 50.0, -8.0, 35000.0, "", "EGTT", 6},
		{"Southern Ocean Void (No Match)", 0, 6, -80.0, 60.0, 35000.0, "", "NONE", 0},
	}

	for _, tc := range tests {
		t.Run(tc.label, func(t *testing.T) {
			m := atcService.locateController(tc.label, tc.f, tc.r, tc.la, tc.lo, tc.al, tc.icao)

			// Handle the "No Match Expected" case
			if tc.expectedICAO == "NONE" {
				if m != nil {
					t.Errorf("%s: Expected nil (no coverage), but got %s (%s)", tc.label, m.Name, m.ICAO)
				}
				return // Test passed!
			}

			// Handle standard match cases
			if m == nil {
				t.Errorf("%s: Expected match for %s, got nil", tc.label, tc.expectedICAO)
				return
			}

			if m.ICAO != tc.expectedICAO {
				t.Errorf("%s: ICAO mismatch. Got %s, want %s", tc.label, m.ICAO, tc.expectedICAO)
			}
		})
	}
}

func TestPerformSearchGlobal(t *testing.T) {
	requiredAirports := map[string]bool{
		"EGLL": true, "KJFK": true, "RJTT": true,
		"NZAA": true, "FACT": true,
	}

	atcService := New("config.yaml", nil, requiredAirports)

	tests := []struct {
		label      string
		f, r       int
		la, lo, al float64
		icao       string
		expected   string
		expRole    int
	}{
		// 1. Auckland (Dateline Wrap) - Expecting ZOZ per your regions.dat
		{"Auckland Center (East of 180)", 0, 6, -37.0, 179.9, 35000.0, "", "ZOZ", 6},
		{"Auckland Tower (Proximity)", 0, 3, -37.008, 174.79, 50.0, "", "NZAA", 3},

		// 2. NYC (High Density)
		{"JFK Ground (Proximity)", 0, 2, 40.641, -73.778, 13.0, "", "KJFK", 2},
		{"JFK Departure (Role Match)", 0, 4, 40.70, -73.80, 2500.0, "", "KJFK", 4},

		// 3. Tokyo (Freq Collision Test)
		// With the 100nm limit, this will no longer match Cape Town (FACT)
		{"Tokyo Tower (Freq Match)", 118100, 3, 35.54, 139.78, 100.0, "", "RJTT", 3},

		// 4. Africa
		// Cape Town (FACT) Approach is returning Role 5 (Approach).
		{"Cape Town Approach", 0, 5, -33.97, 18.60, 5000.0, "", "FACT", 5},

		// 5. Heathrow
		// Specificity check: Requesting Role 3 ensures we get the Tower,
		// even if a Ground (Role 2) or Delivery (Role 1) point is technically closer.
		{"Below Airspace Floor (Tower Request)", 0, 3, 51.47, -0.45, 2000.0, "", "EGLL", 3},
	}

	for _, tc := range tests {
		t.Run(tc.label, func(t *testing.T) {
			m := atcService.locateController(tc.label, tc.f, tc.r, tc.la, tc.lo, tc.al, tc.icao)

			if m == nil {
				t.Fatalf("%s: Expected %s, got nil", tc.label, tc.expected)
			}

			if m.ICAO != tc.expected {
				t.Errorf("%s: ICAO mismatch. Got %s, want %s", tc.label, m.ICAO, tc.expected)
			}

			if tc.expRole != 0 && m.RoleID != tc.expRole {
				t.Errorf("%s: Role mismatch. Got %d, want %d", tc.label, m.RoleID, tc.expRole)
			}
		})
	}
}

func TestLocateControllerLogicTiers(t *testing.T) {
	requiredAirports := map[string]bool{"EGLL": true, "EGLC": true}
	s := New("config.yaml", nil, requiredAirports)

	// Setup coordinates: Heathrow (EGLL)
	lat, lon := 51.47, -0.45

	tests := []struct {
		label      string
		tFreq      int
		tRole      int
		targetICAO string
		expected   string
		expRole    int
	}{
		// --- TIER 0: ICAO SHORTCUT ---
		// Even if we are at Heathrow, if we ask for EGLC (London City), we should get it.
		{"ICAO Shortcut (City Airport)", 0, 3, "EGLC", "EGLC", 3},

		// --- TIER 1: ROLE FILTERING ---
		// Ask for Ground (2) specifically at Heathrow
		{"Specific Role (Ground)", 0, 2, "EGLL", "EGLL", 2},
		// Ask for Tower (3) specifically at Heathrow
		{"Specific Role (Tower)", 0, 3, "EGLL", "EGLL", 3},

		// --- TIER 1.5: THE "ANY" SENTINEL ---
		// Using -1 should return the closest facility (likely Ground or Delivery at these coords)
		{"Any Role Sentinel (-1)", 0, RoleNone, "EGLL", "EGLL", 1},

		// --- TIER 2: FREQUENCY OVERRIDE ---
		// Tune London City Tower freq (118.07) while sitting at Heathrow
		{"Frequency Override", 118070, RoleNone, "", "EGLC", 3},

		// --- TIER 3: POLYGON VS POINT ---
		// At 35,000ft, ICAO shortcut should still work for the airport,
		// but a Role 6 request with no ICAO should hit the Center polygon.
		{"High Altitude Center", 0, 6, "", "EGTT", 6},
	}

	for _, tc := range tests {
		t.Run(tc.label, func(t *testing.T) {
			// We use a fixed altitude for these logic tests
			alt := 2000.0
			if tc.label == "High Altitude Center" {
				alt = 35000.0
			}

			m := s.locateController(tc.label, tc.tFreq, tc.tRole, lat, lon, alt, tc.targetICAO)

			if m == nil {
				t.Fatalf("%s: Got nil, want %s", tc.label, tc.expected)
			}
			if m.ICAO != tc.expected {
				t.Errorf("%s: ICAO mismatch. Got %s, want %s", tc.label, m.ICAO, tc.expected)
			}
			if tc.expRole != -1 && m.RoleID != tc.expRole {
				t.Errorf("%s: Role mismatch. Got %d, want %d", tc.label, m.RoleID, tc.expRole)
			}
		})
	}
}

func TestUnicomFallbackLogic(t *testing.T) {

	s := New("config.yaml", nil, map[string]bool{"EGTF": true})
	lat, lon := 51.35, -0.56

	// 1. Search for a role we are SURE isn't there (e.g., Role 6 - Center)
	// Even if it's Fairoaks, it shouldn't have a 'Center' point record.
	m := s.locateController("Fairoaks Center Point", 0, 6, lat, lon, 1000, "EGTF")
	if m != nil && m.ICAO == "EGTF" {
		t.Errorf("Expected nil for Fairoaks Center Point, got Role %d", m.RoleID)
	}

	// 2. Search for the role we now know exists (Role 3)
	m = s.locateController("Fairoaks Tower", 0, 3, lat, lon, 1000, "EGTF")
	if m == nil || m.RoleID != 3 || m.ICAO != "EGTF" {
		t.Errorf("Expected Tower (Role 3) for Fairoaks, got %v", m)
	}
}
