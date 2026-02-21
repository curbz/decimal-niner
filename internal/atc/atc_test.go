package atc

import (
	
	"os"
	"testing"
	"time"

	"github.com/curbz/decimal-niner/internal/trafficglobal"
)

func init() {
	// This runs before any tests in this package
	// TODO: how can we make this work for custom config locations?
	// We move up two levels to the root of the repo so that config.yaml and /resources is found
	_ = os.Chdir("../../")
}

func TestPerformSearch(t *testing.T) {

    requiredAirports := map[string]bool{"EGLL": true, "EGKA": true}
    atcService := New("config.yaml", make(map[string][]trafficglobal.ScheduledFlight), requiredAirports)

    tests := []struct {
        label      string
        f, r       int
        la, lo, al float64
        icao       string
        expectedICAO string
        expectedRole int
    }{
        {"Heathrow Ground (Proximity)", 0, 2, 51.4706, -0.4522, 1000.0, "", "EGLL", 2},
        {"Heathrow Ground (Freq)", 121905, 2, 51.4706, -0.4522, 1000.0, "", "EGLL", 2},
        {"Heathrow Tower (Freq)", 118505, 3, 51.4706, -0.4522, 1000.0, "", "EGLL", 3},
        {"London Center (Polygon)", 0, 6, 51.5, -0.1, 20000.0, "", "EGTT", 6},
        {"Shoreham Ground (Proximity)", 0, 2, 50.835, -0.297, 50.0, "", "EGKA", 2},
        {"Gatwick Arrival via Southampton (Polygon)", 0, 6, 50.95, -1.35, 15000.0, "", "EGTT", 6},
		{"Shanwick Oceanic (Polygon Match)", 0, 6, 45.0, -25.0, 35000.0, "", "EGGX", 6},
		{"Celtic Sea (London FIR)", 0, 6, 50.0, -8.0, 35000.0, "", "EGTT", 6},
		{"Southern Ocean Void (No Match)", 0, 6, -80.0, 60.0, 35000.0, "", "NONE", 0},
    }

    for _, tc := range tests {
		t.Run(tc.label, func(t *testing.T) {
			m := atcService.LocateController(tc.label, tc.f, tc.r, tc.la, tc.lo, tc.al, tc.icao)
			
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
            m := atcService.LocateController(tc.label, tc.f, tc.r, tc.la, tc.lo, tc.al, tc.icao)
            
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
        {"Any Role Sentinel (-1)", 0, RoleAny, "EGLL", "EGLL", 1},

        // --- TIER 2: FREQUENCY OVERRIDE ---
        // Tune London City Tower freq (118.07) while sitting at Heathrow
        {"Frequency Override", 118070, RoleAny, "", "EGLC", 3},

        // --- TIER 3: POLYGON VS POINT ---
        // At 35,000ft, ICAO shortcut should still work for the airport, 
        // but a Role 6 request with no ICAO should hit the Center polygon.
        {"High Altitude Center", 0, 6, "", "EGTT", 6},
    }

    for _, tc := range tests {
        t.Run(tc.label, func(t *testing.T) {
            // We use a fixed altitude for these logic tests
            alt := 2000.0
            if tc.label == "High Altitude Center" { alt = 35000.0 }

            m := s.LocateController(tc.label, tc.tFreq, tc.tRole, lat, lon, alt, tc.targetICAO)
            
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
    // Let's use EGTF since we know it has a Role 3 in your data
    s := New("config.yaml", nil, map[string]bool{"EGTF": true})
    lat, lon := 51.35, -0.56

    // 1. Search for a role we are SURE isn't there (e.g., Role 6 - Center)
    // Even if it's Fairoaks, it shouldn't have a 'Center' point record.
    m := s.LocateController("Fairoaks Center Point", 0, 6, lat, lon, 1000, "EGTF")
    if m != nil && m.IsPoint {
        t.Errorf("Expected nil for Fairoaks Center Point, got Role %d", m.RoleID)
    }

    // 2. Search for the role we now know exists (Role 3)
    m = s.LocateController("Fairoaks Tower", 0, 3, lat, lon, 1000, "EGTF")
    if m == nil || m.RoleID != 3 {
        t.Errorf("Expected Tower (Role 3) for Fairoaks, got %v", m)
    }
}

func TestAddFlightPlan(t *testing.T) {
	tests := []struct {
		name          string
		registration  string
		flightNumber  int
		simTime       time.Time
		schedules     map[string][]trafficglobal.ScheduledFlight
		strictFlightPlanMatching bool
		expectOrigin  string
		expectDest    string
		expectNoMatch bool
	}{
		{
			name:         "Match today's flight within time window",
			registration: "N12345",
			flightNumber: 101,
			simTime:      time.Date(2024, 1, 1, 10, 30, 0, 0, time.UTC), // this date in 2024 resolves to a Monday at 10:30
			schedules: map[string][]trafficglobal.ScheduledFlight{
				"N12345_101_0": {
					{
						IcaoOrigin:         "KJFK",
						IcaoDest:           "KLAX",
						DepatureHour:       10,
						DepartureMin:       0,
						DepartureDayOfWeek: 0, // Monday
						ArrivalHour:        13,
						ArrivalMin:         0,
						ArrivalDayOfWeek:   0,
					},
				},
			},
			strictFlightPlanMatching: true,
			expectOrigin:  "KJFK",
			expectDest:    "KLAX",
			expectNoMatch: false,
		},
		{
			name:         "Match today's flight in extended arrival time window",
			registration: "N12346",
			flightNumber: 101,
			simTime:      time.Date(2024, 1, 1, 13, 15, 0, 0, time.UTC), // this date in 2024 resolves to a Monday at 13:15
			schedules: map[string][]trafficglobal.ScheduledFlight{
				"N12346_101_0": {
					{
						IcaoOrigin:         "KJFK",
						IcaoDest:           "KLAX",
						DepatureHour:       10,
						DepartureMin:       0,
						DepartureDayOfWeek: 0,
						ArrivalHour:        13,  // actual scheduled arrival is 13:15
						ArrivalMin:         0,
						ArrivalDayOfWeek:   0,
					},
				},
			},
			strictFlightPlanMatching: true,
			expectOrigin:  "KJFK",
			expectDest:    "KLAX",
			expectNoMatch: false,
		},
		{
			name:         "Match today's flight in extended departure time window",
			registration: "N12347",
			flightNumber: 101,
			simTime:      time.Date(2024, 1, 1, 9, 45, 0, 0, time.UTC), // this date in 2024 resolves to a Monday at 09:45
			schedules: map[string][]trafficglobal.ScheduledFlight{
				"N12347_101_0": {
					{
						IcaoOrigin:         "KJFK",
						IcaoDest:           "KLAX",
						DepatureHour:       10, // actual scheduled departure is 10:00 am
						DepartureMin:       0,
						DepartureDayOfWeek: 0,
						ArrivalHour:        13,
						ArrivalMin:         0,
						ArrivalDayOfWeek:   0,
					},
				},
			},
			strictFlightPlanMatching: true,
			expectOrigin:  "KJFK",
			expectDest:    "KLAX",
			expectNoMatch: false,
		},
		{
			name:         "Match yesterday's flight arriving today",
			registration: "N54321",
			flightNumber: 202,
			simTime:      time.Date(2026, 1, 27, 6, 0, 0, 0, time.UTC), // this date in 2024 resolves to a Tuesday at 08:00
			schedules: map[string][]trafficglobal.ScheduledFlight{
				"N54321_202_0": { // Monday
					{
						IcaoOrigin:         "EGLL",
						IcaoDest:           "LFPG",
						DepatureHour:       22,
						DepartureMin:       0,
						DepartureDayOfWeek: 0, // Monday
						ArrivalHour:        7,
						ArrivalMin:         30,
						ArrivalDayOfWeek:   1, // Tuesday
					},
				},
			},
			strictFlightPlanMatching: true,
			expectOrigin:  "EGLL",
			expectDest:    "LFPG",
			expectNoMatch: false,
		},
		{
			name:          "No matching flight plan",
			registration:  "N99999",
			flightNumber:  999,
			simTime:       time.Date(2024, 1, 1, 10, 30, 0, 0, time.UTC),
			schedules:     map[string][]trafficglobal.ScheduledFlight{},
			strictFlightPlanMatching: true,
			expectNoMatch: true,
		},
		{
			name:         "Time is earlier than flight departure time",
			registration: "N11111",
			flightNumber: 111,
			simTime:      time.Date(2026, 1, 27, 5, 0, 0, 0, time.UTC), // 5am Tuesday, this is before 10:00 departure and outside max extended search window
			schedules: map[string][]trafficglobal.ScheduledFlight{
				"N11111_111_1": {
					{
						IcaoOrigin:         "KATL",
						IcaoDest:           "KMIA",
						DepatureHour:       10,
						DepartureMin:       0,
						DepartureDayOfWeek: 1,
						ArrivalHour:        12,
						ArrivalMin:         0,
						ArrivalDayOfWeek:   1,
					},
				},
			},
			strictFlightPlanMatching: true,
			expectNoMatch: true,
		},
		{
			name:         "Flight arrival time has passed",
			registration: "N22222",
			flightNumber: 222,
			simTime:      time.Date(2024, 1, 27, 18, 0, 0, 0, time.UTC), // 6pm Tuesday is after 13:00 arrival and outside max extended search window
			schedules: map[string][]trafficglobal.ScheduledFlight{
				"N22222_222_1": {
					{
						IcaoOrigin:         "KDFW",
						IcaoDest:           "KORD",
						DepatureHour:       10,
						DepartureMin:       0,
						DepartureDayOfWeek: 1,
						ArrivalHour:        13,
						ArrivalMin:         0,
						ArrivalDayOfWeek:   1,
					},
				},
			},
			strictFlightPlanMatching: true,
			expectNoMatch: true,
		},
		{
			name:         "Flight arrival time has passed but strict matching is disabled",
			registration: "N33333",
			flightNumber: 333,
			simTime:      time.Date(2024, 1, 27, 18, 0, 0, 0, time.UTC), // 6pm Tuesday is after 13:00 arrival and outside max extended search window
			schedules: map[string][]trafficglobal.ScheduledFlight{
				"N33333_333_1": {
					{
						IcaoOrigin:         "KDFW",
						IcaoDest:           "KORD",
						DepatureHour:       10,
						DepartureMin:       0,
						DepartureDayOfWeek: 1,
						ArrivalHour:        13,
						ArrivalMin:         0,
						ArrivalDayOfWeek:   1,
					},
				},
			},
			strictFlightPlanMatching: false,
			expectOrigin:"KDFW",
			expectDest: "KORD",
			expectNoMatch: false,
		},
		{
			name:         "Flight departure is on a different day but strict matching is disabled",
			registration: "N44444",
			flightNumber: 444,
			simTime:      time.Date(2026, 1, 31, 6, 0, 0, 0, time.UTC), // 6am Saturday, this is before 10:00 departure and on a different departure day of week
			schedules: map[string][]trafficglobal.ScheduledFlight{
				"N44444_444_1": {
					{
						IcaoOrigin:         "KATL",
						IcaoDest:           "KMIA",
						DepatureHour:       10,
						DepartureMin:       0,
						DepartureDayOfWeek: 1,
						ArrivalHour:        12,
						ArrivalMin:         0,
						ArrivalDayOfWeek:   1,
					},
				},
			},
			strictFlightPlanMatching: true,
			expectOrigin: "KATL",
			expectDest: "KMIA",
			expectNoMatch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			atcService := New("config.yaml", tt.schedules, make(map[string]bool))

			ac := &Aircraft{
				Registration: tt.registration,
				Flight: Flight{
					Number: tt.flightNumber,
				},
			}
 
			atcService.Config.ATC.StrictFlightPlanMatch = tt.strictFlightPlanMatching
			atcService.AddFlightPlan(ac, tt.simTime)

			if tt.expectNoMatch {
				if ac.Flight.Origin != "" || ac.Flight.Destination != "" {
					t.Errorf("expected no match, got Origin=%s Destination=%s", ac.Flight.Origin, ac.Flight.Destination)
				}
			} else {
				if ac.Flight.Origin != tt.expectOrigin {
					t.Errorf("expected Origin=%s, got %s", tt.expectOrigin, ac.Flight.Origin)
				}
				if ac.Flight.Destination != tt.expectDest {
					t.Errorf("expected Destination=%s, got %s", tt.expectDest, ac.Flight.Destination)
				}
			}
		})
	}
}
