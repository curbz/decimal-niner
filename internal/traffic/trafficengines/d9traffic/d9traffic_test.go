package d9traffic

import (
	"fmt"
	"testing"
	"time"

	"github.com/curbz/decimal-niner/internal/atc"
	"github.com/curbz/decimal-niner/internal/flightphase"
	"github.com/curbz/decimal-niner/internal/flightplan"
)

func newTestEngine(simTime time.Time) *D9TrafficEngine {
	svc := &atc.Service{}
	svc.SyncSimTime(simTime, simTime)

	return &D9TrafficEngine{
		atcService: svc,
	}
}

func assertRange(t *testing.T, name string, got, min, max int) {
	t.Helper()
	if got < min || got > max {
		t.Fatalf("%s: got %d; want between %d and %d", name, got, min, max)
	}
}

func TestDetermineInitialDeparturePhase(t *testing.T) {
	baseTime := time.Now().Truncate(time.Minute)
	scheduleTime := baseTime.Add(30 * time.Minute)
	schedule := &flightplan.ScheduledFlight{
		AircraftRegistration: "TEST123",
		IcaoOrigin:           "EGLL",
		ArrivalHour:          scheduleTime.Hour(),
		ArrivalMin:           scheduleTime.Minute(),
	}

	runway := &atc.Runway{Name: "09L"}
	runwayKey := normalizeRunwayKey(schedule.IcaoOrigin, runway)

	cases := []struct {
		name          string
		diff          int
		airportConfig map[string]ActiveRunwaySet
		runwayQueues  map[string]map[string]time.Time
		wantPhase     flightphase.FlightPhase
		wantDelay     int
		wantEstExact  bool
		wantEstMin    int
		wantEstMax    int
		wantRemExact  bool
		wantRemMin    int
		wantRemMax    int
	}{
		{
			name:       "parked_long_term_no_config",
			diff:       30,
			wantPhase:  flightphase.Parked,
			wantDelay:  0,
			wantEstMin: 780,
			wantEstMax: 1020,
			wantRemMin: 780,
			wantRemMax: 1020,
		},
		{
			name: "parked_long_term_with_queue_delay",
			diff: 30,
			airportConfig: map[string]ActiveRunwaySet{
				"EGLL": {Departure: runway},
			},
			runwayQueues: map[string]map[string]time.Time{
				runwayKey: func() map[string]time.Time {
					m := make(map[string]time.Time)
					for i := 0; i < TRAFFIC_MANAGEMENT_RUNWAY_QUEUE_THRESHOLD+1; i++ {
						m[fmt.Sprintf("ac%d", i)] = time.Time{}
					}
					return m
				}(),
			},
			wantPhase:  flightphase.Parked,
			wantDelay:  (TRAFFIC_MANAGEMENT_RUNWAY_QUEUE_THRESHOLD + 1) * TRAFFIC_MANAGEMENT_PER_AIRCRAFT_DELAY_SECONDS,
			wantEstMin: 780,
			wantEstMax: 1020,
			wantRemMin: 780,
			wantRemMax: 1020,
		},
		{
			name:       "parked_tracking_to_startup",
			diff:       20,
			wantPhase:  flightphase.Parked,
			wantEstMin: 180,
			wantEstMax: 420,
			wantRemMin: 480,
			wantRemMax: 720,
		},
		{
			name:       "startup",
			diff:       12,
			wantPhase:  flightphase.Startup,
			wantEstMin: 0,
			wantEstMax: 240,
			wantRemMin: 180,
			wantRemMax: 420,
		},
		{
			name:         "taxi_out",
			diff:         5,
			wantPhase:    flightphase.TaxiOut,
			wantEstExact: true,
			wantEstMin:   600,
			wantRemExact: true,
			wantRemMin:   600,
		},
		{
			name:         "takeoff_is_taxi_out",
			diff:         0,
			wantPhase:    flightphase.TaxiOut,
			wantEstExact: true,
			wantEstMin:   600,
			wantRemExact: true,
			wantRemMin:   600,
		},
		{
			name:       "climbout",
			diff:       -3,
			wantPhase:  flightphase.Climbout,
			wantEstMin: 80,
			wantEstMax: 160,
			wantRemMin: 200,
			wantRemMax: 280,
		},
		{
			name:       "departure",
			diff:       -10,
			wantPhase:  flightphase.Departure,
			wantEstMin: 180,
			wantEstMax: 420,
			wantRemMin: 480,
			wantRemMax: 720,
		},
		{
			name:         "cruise_default",
			diff:         -20,
			wantPhase:    flightphase.Cruise,
			wantEstMin:   1260,
			wantEstMax:   1500,
			wantRemExact: true,
			wantRemMin:   2100,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEngine(baseTime)
			e.AirportConfig = tc.airportConfig
			e.RunwayQueues = tc.runwayQueues

			phase, estimated, remaining, delay := e.determineInitialDeparturePhase(tc.diff, schedule)
			if phase != tc.wantPhase {
				t.Fatalf("phase: got %v want %v", phase, tc.wantPhase)
			}
			if delay != tc.wantDelay {
				t.Fatalf("delay: got %d want %d", delay, tc.wantDelay)
			}
			if tc.wantEstExact {
				if estimated != tc.wantEstMin {
					t.Fatalf("estimated duration: got %d want %d", estimated, tc.wantEstMin)
				}
			} else {
				assertRange(t, "estimated duration", estimated, tc.wantEstMin, tc.wantEstMax)
			}
			if tc.wantRemExact {
				if remaining != tc.wantRemMin {
					t.Fatalf("remaining duration: got %d want %d", remaining, tc.wantRemMin)
				}
			} else {
				assertRange(t, "remaining duration", remaining, tc.wantRemMin, tc.wantRemMax)
			}
		})
	}
}

func TestDetermineInitialArrivalPhase(t *testing.T) {
	baseTime := time.Now().Truncate(time.Minute)
	scheduleTime := baseTime.Add(30 * time.Minute)
	schedule := &flightplan.ScheduledFlight{
		AircraftRegistration: "TEST123",
		ArrivalHour:          scheduleTime.Hour(),
		ArrivalMin:           scheduleTime.Minute(),
	}

	cases := []struct {
		name         string
		diff         int
		wantPhase    flightphase.FlightPhase
		wantEstExact bool
		wantEstMin   int
		wantEstMax   int
		wantRemExact bool
		wantRemMin   int
		wantRemMax   int
	}{
		{
			name:       "arrival_phase",
			diff:       10,
			wantPhase:  flightphase.Approach,
			wantEstMin: 60,
			wantEstMax: 300,
			wantRemMin: 360,
			wantRemMax: 600,
		},
		{
			name:       "approach_phase",
			diff:       5,
			wantPhase:  flightphase.Approach,
			wantEstMin: 180,
			wantEstMax: 300,
			wantRemMin: 300,
			wantRemMax: 420,
		},
		{
			name:         "final_phase_promoted_to_approach",
			diff:         1,
			wantPhase:    flightphase.Approach,
			wantEstExact: true,
			wantEstMin:   360,
			wantRemExact: true,
			wantRemMin:   360,
		},
		{
			name:         "braking_promoted_to_approach",
			diff:         0,
			wantPhase:    flightphase.Approach,
			wantEstExact: true,
			wantEstMin:   360,
			wantRemExact: true,
			wantRemMin:   360,
		},
		{
			name:         "taxi_in",
			diff:         -1,
			wantPhase:    flightphase.TaxiIn,
			wantEstExact: true,
			wantEstMin:   600,
			wantRemExact: true,
			wantRemMin:   600,
		},
		{
			name:         "shutdown",
			diff:         -5,
			wantPhase:    flightphase.Shutdown,
			wantEstMin:   300,
			wantEstMax:   540,
			wantRemExact: true,
			wantRemMin:   600,
		},
		{
			name:         "parked",
			diff:         -13,
			wantPhase:    flightphase.Parked,
			wantEstMin:   0,
			wantEstMax:   240,
			wantRemExact: true,
			wantRemMin:   180,
		},
		{
			name:         "cruise_default",
			diff:         20,
			wantPhase:    flightphase.Cruise,
			wantEstExact: true,
			wantEstMin:   2100,
			wantRemMin:   1980,
			wantRemMax:   2220,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEngine(baseTime)

			phase, remaining, estimated := e.determineInitialArrivalPhase(tc.diff, schedule)
			if phase != tc.wantPhase {
				t.Fatalf("phase: got %v want %v", phase, tc.wantPhase)
			}
			if tc.wantEstExact {
				if estimated != tc.wantEstMin {
					t.Fatalf("estimated duration: got %d want %d", estimated, tc.wantEstMin)
				}
			} else {
				assertRange(t, "estimated duration", estimated, tc.wantEstMin, tc.wantEstMax)
			}
			if tc.wantRemExact {
				if remaining != tc.wantRemMin {
					t.Fatalf("remaining duration: got %d want %d", remaining, tc.wantRemMin)
				}
			} else {
				assertRange(t, "remaining duration", remaining, tc.wantRemMin, tc.wantRemMax)
			}
		})
	}
}
