package atc

import (
	"testing"
	"time"

	"github.com/curbz/decimal-niner/internal/flightphase"
	"github.com/curbz/decimal-niner/internal/flightplan"
	"github.com/curbz/decimal-niner/pkg/geometry"
)

type MockAirportProvider struct {
	MockReturn string
}

func TestInferFlightPlan(t *testing.T) {

	inferTests := []struct {
		name       string
		mockReturn string
		phaseClass PhaseClass
		initOrigin string
		initDest   string
		wantOrigin string
		wantDest   string
	}{
		{name: "departing sets origin", mockReturn: "EGLL", phaseClass: Departing, initOrigin: "", initDest: "", wantOrigin: "EGLL", wantDest: ""},
		{name: "arriving sets destination", mockReturn: "KJFK", phaseClass: Arriving, initOrigin: "", initDest: "", wantOrigin: "", wantDest: "KJFK"},
		{name: "does not overwrite existing origin/dest", mockReturn: "ZZZZ", phaseClass: Departing, initOrigin: "EXIST", initDest: "DEST", wantOrigin: "EXIST", wantDest: "DEST"},
	}

	for _, tt := range inferTests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Service{AirportService: &MockAirportProvider{MockReturn: tt.mockReturn}}
			ac := &Aircraft{}
			ac.Flight.Phase.Class = tt.phaseClass
			ac.Flight.Origin = tt.initOrigin
			ac.Flight.Destination = tt.initDest
			// position cannot be empty otherwise inferFlightPlan will return with no action
			ac.Flight.Position = Position{ Altitude: 1.0 }

			s.inferFlightPlan(ac)

			if ac.Flight.Origin != tt.wantOrigin {
				t.Fatalf("%s: expected origin %q, got %q", tt.name, tt.wantOrigin, ac.Flight.Origin)
			}
			if ac.Flight.Destination != tt.wantDest {
				t.Fatalf("%s: expected destination %q, got %q", tt.name, tt.wantDest, ac.Flight.Destination)
			}
		})
	}
}

func TestCalculateDistance(t *testing.T) {

	distTests := []struct {
		name string
		p1   Position
		p2   Position
		want float64
	}{
		{name: "zero distance", p1: Position{Lat: 51.0, Long: -0.1}, p2: Position{Lat: 51.0, Long: -0.1}, want: 0},
		{name: "one degree lat", p1: Position{Lat: 0.0, Long: 0.0}, p2: Position{Lat: 1.0, Long: 0.0}, want: float64(geometry.DistNM(0, 0, 1, 0))},
	}

	for _, tt := range distTests {
		t.Run(tt.name, func(t *testing.T) {
			d := calculateDistance(tt.p1, tt.p2)
			const eps = 0.0001
			if diff := d - tt.want; diff < -eps || diff > eps {
				t.Fatalf("%s: distance mismatch: got %f want %f", tt.name, d, tt.want)
			}
		})
	}
}

func (m *MockAirportProvider) GetClosestAirport(lat, long, maxRangeNm float64) string {
	return m.MockReturn
}

func TestAddFlightPlan(t *testing.T) {
	tests := []struct {
		name                     string
		registration             string
		flightNumber             int
		simTime                  time.Time
		schedules                map[string][]flightplan.ScheduledFlight
		strictFlightPlanMatching bool
		expectOrigin             string
		expectDest               string
		expectNoMatch            bool
	}{
		{
			name:         "Match today's flight within time window",
			registration: "N12345",
			flightNumber: 101,
			simTime:      time.Date(2024, 1, 1, 10, 30, 0, 0, time.UTC), // this date in 2024 resolves to a Monday at 10:30
			schedules: map[string][]flightplan.ScheduledFlight{
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
			expectOrigin:             "KJFK",
			expectDest:               "KLAX",
			expectNoMatch:            false,
		},
		{
			name:         "Match today's flight in extended arrival time window",
			registration: "N12346",
			flightNumber: 101,
			simTime:      time.Date(2024, 1, 1, 13, 15, 0, 0, time.UTC), // this date in 2024 resolves to a Monday at 13:15
			schedules: map[string][]flightplan.ScheduledFlight{
				"N12346_101_0": {
					{
						IcaoOrigin:         "KJFK",
						IcaoDest:           "KLAX",
						DepatureHour:       10,
						DepartureMin:       0,
						DepartureDayOfWeek: 0,
						ArrivalHour:        13, // actual scheduled arrival is 13:15
						ArrivalMin:         0,
						ArrivalDayOfWeek:   0,
					},
				},
			},
			strictFlightPlanMatching: true,
			expectOrigin:             "KJFK",
			expectDest:               "KLAX",
			expectNoMatch:            false,
		},
		{
			name:         "Match today's flight in extended departure time window",
			registration: "N12347",
			flightNumber: 101,
			simTime:      time.Date(2024, 1, 1, 9, 45, 0, 0, time.UTC), // this date in 2024 resolves to a Monday at 09:45
			schedules: map[string][]flightplan.ScheduledFlight{
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
			expectOrigin:             "KJFK",
			expectDest:               "KLAX",
			expectNoMatch:            false,
		},
		{
			name:         "Match yesterday's flight arriving today",
			registration: "N54321",
			flightNumber: 202,
			simTime:      time.Date(2026, 1, 27, 6, 0, 0, 0, time.UTC), // this date in 2024 resolves to a Tuesday at 08:00
			schedules: map[string][]flightplan.ScheduledFlight{
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
			expectOrigin:             "EGLL",
			expectDest:               "LFPG",
			expectNoMatch:            false,
		},
		{
			name:                     "No matching flight plan",
			registration:             "N99999",
			flightNumber:             999,
			simTime:                  time.Date(2024, 1, 1, 10, 30, 0, 0, time.UTC),
			schedules:                map[string][]flightplan.ScheduledFlight{},
			strictFlightPlanMatching: true,
			expectNoMatch:            true,
		},
		{
			name:         "Time is earlier than flight departure time",
			registration: "N11111",
			flightNumber: 111,
			simTime:      time.Date(2026, 1, 27, 5, 0, 0, 0, time.UTC), // 5am Tuesday, this is before 10:00 departure and outside max extended search window
			schedules: map[string][]flightplan.ScheduledFlight{
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
			expectNoMatch:            true,
		},
		{
			name:         "Flight arrival time has passed",
			registration: "N22222",
			flightNumber: 222,
			simTime:      time.Date(2024, 1, 27, 18, 0, 0, 0, time.UTC), // 6pm Tuesday is after 13:00 arrival and outside max extended search window
			schedules: map[string][]flightplan.ScheduledFlight{
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
			expectNoMatch:            true,
		},
		{
			name:         "Flight arrival time has passed but strict matching is disabled",
			registration: "N33333",
			flightNumber: 333,
			simTime:      time.Date(2024, 1, 27, 18, 0, 0, 0, time.UTC), // 6pm Tuesday is after 13:00 arrival and outside max extended search window
			schedules: map[string][]flightplan.ScheduledFlight{
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
			expectOrigin:             "KDFW",
			expectDest:               "KORD",
			expectNoMatch:            false,
		},
		{
			name:         "Flight departure is on a different day but strict matching is disabled",
			registration: "N44444",
			flightNumber: 444,
			simTime:      time.Date(2026, 1, 31, 6, 0, 0, 0, time.UTC), // 6am Saturday, this is before 10:00 departure and on a different departure day of week
			schedules: map[string][]flightplan.ScheduledFlight{
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
			expectOrigin:             "KATL",
			expectDest:               "KMIA",
			expectNoMatch:            true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			atcService, _ := New("config.yaml", tt.schedules, make(map[string]bool))

			ac := &Aircraft{
				Registration: tt.registration,
				Flight: Flight{
					Number: tt.flightNumber,
				},
			}

			atcService.Config.ATC.StrictFlightPlanMatch = tt.strictFlightPlanMatching
			planFound := atcService.AddFlightPlan(ac, tt.simTime)

			if tt.expectNoMatch {
				if planFound {
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

func TestSetFlightPhaseClass(t *testing.T) {
	mockAirports := &MockAirportProvider{}
	s := &Service{AirportService: mockAirports}

	tests := []struct {
		name          string
		prevPhase     int
		currPhase     int
		origin        string
		dest          string
		closest       string
		expectedClass PhaseClass
	}{
		{
			name:          "Unknown -> Parked at Origin (Preflight)",
			prevPhase:     flightphase.Unknown.Index(),
			currPhase:     flightphase.Parked.Index(),
			origin:        "EGKK",
			dest:          "EHAM",
			closest:       "EGKK",
			expectedClass: PreflightParked,
		},
		{
			name:          "Unknown -> Parked at Destination (Postflight)",
			prevPhase:     flightphase.Unknown.Index(),
			currPhase:     flightphase.Parked.Index(),
			origin:        "EGKK",
			dest:          "EHAM",
			closest:       "EHAM",
			expectedClass: PostflightParked,
		},
		{
			name:          "Shutdown -> Parked (Standard Arrival)",
			prevPhase:     flightphase.Shutdown.Index(),
			currPhase:     flightphase.Parked.Index(),
			expectedClass: PostflightParked,
		},
		{
			name:          "Sticky Guard (No change if already classified)",
			prevPhase:     1,
			currPhase:     1,
			origin:        "EGKK",
			dest:          "EHAM",
			closest:       "EGKK",
			expectedClass: PreflightParked, // Should stay what it was
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set the mock to return the 'closest' airport for this specific test case
			mockAirports.MockReturn = tt.closest

			ac := &Aircraft{
				Flight: Flight{
					Origin:      tt.origin,
					Destination: tt.dest,
					Phase: Phase{
						Previous: tt.prevPhase,
						Current:  tt.currPhase,
						Class:    tt.expectedClass, // Pre-set for sticky test
					},
				},
			}

			// For non-sticky tests, ensure class starts at Unknown
			if tt.name != "Sticky Guard (No change if already classified)" {
				ac.Flight.Phase.Class = Unknown
			}

			s.setFlightPhaseClass(ac)

			if ac.Flight.Phase.Class != tt.expectedClass {
				t.Errorf("%s: expected %v, got %v", tt.name, tt.expectedClass, ac.Flight.Phase.Class)
			}
		})
	}
}

func TestCheckForCruiseSectorChange(t *testing.T) {
	tests := []struct {
		name                 string
		setup                func() (*Service, *Aircraft)
		wantLastCheckedDelta bool // whether LastCheckedPosition should be updated to Position
	}{
		{
			name: "not cruise - no change",
			setup: func() (*Service, *Aircraft) {
				s := &Service{}
				ac := &Aircraft{}
				ac.Flight.Phase.Current = flightphase.Parked.Index()
				ac.Flight.LastCheckedPosition = Position{Lat: 0, Long: 0}
				ac.Flight.Position = Position{Lat: 51.0, Long: -0.1}
				return s, ac
			},
			wantLastCheckedDelta: false,
		},
		{
			name: "cruise initial checkpoint sets last checked",
			setup: func() (*Service, *Aircraft) {
				s := &Service{}
				ac := &Aircraft{}
				ac.Flight.Phase.Current = flightphase.Cruise.Index()
				ac.Flight.LastCheckedPosition = Position{Lat: 0, Long: 0}
				ac.Flight.Position = Position{Lat: 51.5, Long: -0.2}
				return s, ac
			},
			wantLastCheckedDelta: true,
		},
		{
			name: "cruise small movement - no update",
			setup: func() (*Service, *Aircraft) {
				s := &Service{}
				ac := &Aircraft{}
				ac.Flight.Phase.Current = flightphase.Cruise.Index()
				ac.Flight.LastCheckedPosition = Position{Lat: 51.0000, Long: -0.1000}
				ac.Flight.Position = Position{Lat: 51.00005, Long: -0.10005} // ~ few meters
				ac.Flight.Comms.Controller = &Controller{}
				ac.Flight.Comms.CruiseHandoff = NoHandoff
				return s, ac
			},
			wantLastCheckedDelta: false,
		},
		{
			name: "cruise large movement - update last checked",
			setup: func() (*Service, *Aircraft) {
				s := &Service{}
				ac := &Aircraft{}
				ac.Flight.Phase.Current = flightphase.Cruise.Index()
				ac.Flight.LastCheckedPosition = Position{Lat: 51.0, Long: -0.1}
				ac.Flight.Position = Position{Lat: 51.2, Long: -0.1} // ~0.2 deg lat ~= 12 NM
				ac.Flight.Comms.Controller = &Controller{}
				ac.Flight.Comms.CruiseHandoff = NoHandoff
				return s, ac
			},
			wantLastCheckedDelta: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, ac := tc.setup()
			before := ac.Flight.LastCheckedPosition
			s.CheckForCruiseSectorChange(ac)
			after := ac.Flight.LastCheckedPosition
			if tc.wantLastCheckedDelta {
				if before.Lat == after.Lat && before.Long == after.Long {
					t.Fatalf("expected LastCheckedPosition to change but it did not: before=%v after=%v", before, after)
				}
			} else {
				if before.Lat != after.Lat || before.Long != after.Long {
					t.Fatalf("expected LastCheckedPosition to remain same but it changed: before=%v after=%v", before, after)
				}
			}

		})
	}
}

// mockAirportProvider used to return a deterministic closest airport
type mockAirportProviderForTrans struct{ ret string }

func (m *mockAirportProviderForTrans) GetClosestAirport(lat, long, maxRangeNm float64) string {
	return m.ret
}

func TestGetTransistionAltitude(t *testing.T) {
	tests := []struct {
		name           string
		airports       map[string]*Airport
		airportService *mockAirportProviderForTrans
		controllerICAO string
		positionLat    float64
		positionLong   float64
		want           int
	}{
		{
			name: "controller ICAO with TransAlt",
			airports: map[string]*Airport{
				"KAAA": {ICAO: "KAAA", TransAlt: 7000},
			},
			airportService: &mockAirportProviderForTrans{ret: ""},
			controllerICAO: "KAAA",
			want:           7000,
		},
		{
			name: "nearest airport fallback",
			airports: map[string]*Airport{
				"EGLL": {ICAO: "EGLL", TransAlt: 6000},
			},
			airportService: &mockAirportProviderForTrans{ret: "EGLL"},
			controllerICAO: "EGTT",
			want:           6000,
		},
		{
			name:           "regional default when nearest ICAO starts with E",
			airports:       map[string]*Airport{},
			airportService: &mockAirportProviderForTrans{ret: "EHXX"},
			controllerICAO: "EGTT",
			want:           6000,
		},
		{
			name:           "global default when nothing found",
			airports:       map[string]*Airport{},
			airportService: &mockAirportProviderForTrans{ret: ""},
			controllerICAO: "EGTT",
			want:           18000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Service{Airports: tt.airports, AirportService: tt.airportService}
			ac := &Aircraft{Flight: Flight{Comms: Comms{Controller: &Controller{ICAO: tt.controllerICAO}}, Position: Position{Lat: tt.positionLat, Long: tt.positionLong}}}
			got := s.getTransistionAltitude(ac)
			if got != tt.want {
				t.Fatalf("%s: got %d want %d", tt.name, got, tt.want)
			}
		})
	}
}
