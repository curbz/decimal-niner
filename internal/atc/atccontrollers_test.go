package atc

import (
	"testing"

	"github.com/curbz/decimal-niner/internal/flightplan"
)

func TestLocateController(t *testing.T) {

	requiredAirports := map[string]bool{"EGLL": true, "EGKA": true, "EGNX": true, "EGHI": true}
	atcService, _ := New("config.yaml", make(map[string][]flightplan.ScheduledFlight), requiredAirports)

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

func TestLocateControllerGlobal(t *testing.T) {
	requiredAirports := map[string]bool{
		"EGLL": true, "KJFK": true, "RJTT": true,
		"NZAA": true, "FACT": true,
	}

	atcService, _ := New("config.yaml", nil, requiredAirports)

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


// Mock Role IDs based on common ATC structures
const (
	RoleDEL  = 1
	RoleGND  = 2
	RoleTWR  = 3
	RoleAPP  = 4
	RoleCTR  = 6
)

func TestLocateControllerTierLogic(t *testing.T) {
	s := &Service{
		Airports:    make(map[string]*Airport),
		Controllers: []*Controller{},
	}

	// --- SETUP MOCK DATA ---
	
	// Tier 1: Ground (Role 2)
	egllGnd := &Controller{
		Name: "London Ground", ICAO: "EGLL", RoleID: RoleGND, 
		IsPoint: true, Lat: 51.47, Lon: -0.45, Freqs: []int{121900},
	}
	// Tier 1: Tower (Role 3)
	egllTwr := &Controller{
		Name: "London Tower", ICAO: "EGLL", RoleID: RoleTWR, 
		IsPoint: true, Lat: 51.47, Lon: -0.45, Freqs: []int{118500},
	}
	// Tier 2: Center (Role 6) - Bounding box: Lat 40-60, Lon -10 to 10
	lonCtr := &Controller{
		Name: "London Center", ICAO: "LON_CTR", RoleID: RoleCTR, 
		IsPoint: false, Freqs: []int{133700},
		Airspaces: []Airspace{
			{
				Floor: 0, Ceiling: 60000,
				MinLat: 40.0, MaxLat: 60.0, MinLon: -10.0, MaxLon: 10.0,
				Area: 5000,
				Points: [][2]float64{{40, -10}, {60, -10}, {60, 10}, {40, 10}},
			},
		},
	}

	s.Airports["EGLL"] = &Airport{Lat: 51.47, Lon: -0.45, Controllers: []*Controller{egllTwr, egllGnd}}
	s.Controllers = []*Controller{egllGnd, egllTwr, lonCtr}

	// --- TEST CASES ---
	tests := []struct {
		name       string
		tFreq      int
		tRole      int
		uLa, uLo   float64
		uAl        float64
		targetICAO string
		wantName   string
	}{
		// TIER 0: TARGET ICAO SHORTCUT
		{
			name:       "Tier 0: Shortcut Match (Tower)",
			tRole:      RoleTWR,
			uLa:        51.48, uLo: -0.44, // Within 50nm
			targetICAO: "EGLL",
			wantName:   "London Tower",
		},
		{
			name:       "Tier 0: Shortcut Fallback (Distance > 50nm)",
			tRole:      RoleTWR,
			uLa:        55.0, uLo: -5.0, // > 50nm from EGLL
			targetICAO: "EGLL",
			wantName:   "", // Should fail shortcut and find nothing in proximity
		},

		// TIER 1: POINTS (FREQ & PROXIMITY)
		{
			name:     "Tier 1: Freq Match (Tower)",
			tFreq:    118500,
			tRole:    RoleNone,
			uLa:      51.47, uLo: -0.45,
			uAl:      2000,
			wantName: "London Tower",
		},
		{
			name:     "Tier 1: Pure Proximity (No Freq, GND)",
			tFreq:    0,
			tRole:    RoleGND,
			uLa:      51.471, uLo: -0.451,
			uAl:      100,
			wantName: "London Ground",
		},
		{
			name:     "Tier 1: Altitude Gate (GND Hidden at 12k ft)",
			tFreq:    121900,
			tRole:    RoleNone,
			uLa:      51.47, uLo: -0.45,
			uAl:      12000, // Above 10,000ft gate
			wantName: "",
		},
		{
			name:     "Tier 1: Search Limit (No Freq, > 15nm)",
			tFreq:    0,
			tRole:    RoleTWR,
			uLa:      52.0, uLo: -0.45, // ~32nm away
			wantName: "", 
		},

		// TIER 2: POLYGONS (CENTER)
		{
			name:     "Tier 2: Polygon Match (Inside Center Airspace)",
			tFreq:    133700,
			tRole:    RoleNone,
			uLa:      52.0, uLo: 0.0,
			uAl:      35000,
			wantName: "London Center",
		},
		{
			name:     "Tier 2: Polygon Ceiling Check",
			tFreq:    133700,
			uLa:      52.0, uLo: 0.0,
			uAl:      70000, // Above 60k ceiling
			wantName: "",
		},

		// COMPLEX FALLBACKS
		{
			name:     "Fallback: High Alt Freq Match skips Points to check Polygons",
			tFreq:    133700,
			tRole:    RoleNone,
			uLa:      51.47, uLo: -0.45, // Directly over the Tower point
			uAl:      35000,            // But high altitude
			wantName: "London Center",
		},
	}

	// --- EXECUTION ---
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := s.locateController("test-label", tt.tFreq, tt.tRole, tt.uLa, tt.uLo, tt.uAl, tt.targetICAO)

			if tt.wantName == "" {
				if got != nil {
					t.Errorf("%s: expected nil, got %s", tt.name, got.Name)
				}
			} else {
				if got == nil {
					t.Fatalf("%s: got nil, want %s", tt.name, tt.wantName)
				}
				if got.Name != tt.wantName {
					t.Errorf("%s: got %s, want %s", tt.name, got.Name, tt.wantName)
				}
			}
		})
	}
}

func TestUnicomFallbackLogic(t *testing.T) {

	s, _ := New("config.yaml", nil, map[string]bool{"EGTF": true})
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

func TestNormaliseFreq(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want int
	}{
		{"zero", 0, 0},
		{"already scaled", 118050, 118050},
		{"missing trailing zero", 11805, 118050},
		{"short code", 118, 118000},
		{"too large then trimmed", 1180500, 118050},
		{"one becomes 100000", 1, 100000},
		{"exact lower bound", 100000, 100000},
		{"exact upper trim", 1000000, 100000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normaliseFreq(tt.in)
			if got != tt.want {
				t.Fatalf("normaliseFreq(%d) = %d; want %d", tt.in, got, tt.want)
			}
		})
	}
}
