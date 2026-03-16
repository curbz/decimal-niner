package atc

import (
	"testing"
	"time"

	"github.com/curbz/decimal-niner/internal/trafficglobal"
)

type MockAirportProvider struct {
	MockReturn string
}

func (m *MockAirportProvider) GetClosestAirport(lat, long float64) string {
	return m.MockReturn
}

func TestAddFlightPlan(t *testing.T) {
	tests := []struct {
		name                     string
		registration             string
		flightNumber             int
		simTime                  time.Time
		schedules                map[string][]trafficglobal.ScheduledFlight
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
			expectOrigin:             "KJFK",
			expectDest:               "KLAX",
			expectNoMatch:            false,
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
			expectOrigin:             "KJFK",
			expectDest:               "KLAX",
			expectNoMatch:            false,
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
			expectOrigin:             "EGLL",
			expectDest:               "LFPG",
			expectNoMatch:            false,
		},
		{
			name:                     "No matching flight plan",
			registration:             "N99999",
			flightNumber:             999,
			simTime:                  time.Date(2024, 1, 1, 10, 30, 0, 0, time.UTC),
			schedules:                map[string][]trafficglobal.ScheduledFlight{},
			strictFlightPlanMatching: true,
			expectNoMatch:            true,
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
			expectNoMatch:            true,
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
			expectNoMatch:            true,
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
			expectOrigin:             "KDFW",
			expectDest:               "KORD",
			expectNoMatch:            false,
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
			expectOrigin:             "KATL",
			expectDest:               "KMIA",
			expectNoMatch:            true,
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

func TestSetFlightPhaseClass_Detailed(t *testing.T) {
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
			prevPhase:     trafficglobal.Unknown.Index(),
			currPhase:     trafficglobal.Parked.Index(),
			origin:        "EGKK",
			dest:          "EHAM",
			closest:       "EGKK",
			expectedClass: PreflightParked,
		},
		{
			name:          "Unknown -> Parked at Destination (Postflight)",
			prevPhase:     trafficglobal.Unknown.Index(),
			currPhase:     trafficglobal.Parked.Index(),
			origin:        "EGKK",
			dest:          "EHAM",
			closest:       "EHAM",
			expectedClass: PostflightParked,
		},
		{
			name:          "Shutdown -> Parked (Standard Arrival)",
			prevPhase:     trafficglobal.Shutdown.Index(),
			currPhase:     trafficglobal.Parked.Index(),
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
