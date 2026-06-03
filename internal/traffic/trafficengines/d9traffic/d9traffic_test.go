package d9traffic

import (
	"fmt"
	"testing"
	"time"

	"github.com/curbz/decimal-niner/internal/atc"
	"github.com/curbz/decimal-niner/internal/flightphase"
	"github.com/curbz/decimal-niner/internal/flightplan"
	"github.com/curbz/decimal-niner/internal/traffic"
)

func newTestEngine(simTime time.Time) *D9TrafficEngine {
	svc := &atc.Service{}
	svc.SyncSimTime(simTime, simTime)

	return &D9TrafficEngine{
        CommonTrafficEngine: traffic.CommonTrafficEngine{
            AtcService: svc,
        },
	}
}

func assertRange(t *testing.T, name string, got, min, max int) {
	t.Helper()
	if got < min || got > max {
		t.Fatalf("%s: got %d; want between %d and %d", name, got, min, max)
	}
}

func buildDepartureSchedule(baseTime time.Time, diff, durationMin int) *flightplan.ScheduledFlight {
	departureTime := baseTime.Add(time.Duration(diff) * time.Minute)
	arrivalTime := departureTime.Add(time.Duration(durationMin) * time.Minute)
	return &flightplan.ScheduledFlight{
		AircraftRegistration: "TEST123",
		IcaoOrigin:           "EGLL",
		DepartureHour:        departureTime.Hour(),
		DepartureMin:         departureTime.Minute(),
		ArrivalHour:          arrivalTime.Hour(),
		ArrivalMin:           arrivalTime.Minute(),
	}
}

func TestDetermineInitialDeparturePhase(t *testing.T) {
	baseTime := time.Now().Truncate(time.Minute)
	runway := &atc.Runway{Name: "09L"}
	scheduleTemplate := buildDepartureSchedule(baseTime, 30, 30)
	runwayKey := normalizeRunwayKey(scheduleTemplate.IcaoOrigin, runway)

	cases := []struct {
		name          string
		diff          int
		airportConfig map[string]ActiveRunwaySet
		runwayQueues  map[string]map[string]time.Time
		wantPhase     flightphase.FlightPhase
		wantDelay     int
		wantRemExact  bool
		wantRemMin    int
		wantRemMax    int
		wantDurExact  bool
		wantDurMin    int
		wantDurMax    int
	}{
		{
			name:       "parked_long_term_no_config",
			diff:       30,
			wantPhase:  flightphase.Parked,
			wantDelay:  0,
			wantRemMin: 780,
			wantRemMax: 1020,
			wantDurMin: 780,
			wantDurMax: 1020,
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
			wantRemMin: 780,
			wantRemMax: 1020,
			wantDurMin: 780,
			wantDurMax: 1020,
		},
		{
			name:       "parked_tracking_to_startup",
			diff:       20,
			wantPhase:  flightphase.Parked,
			wantRemMin: 180,
			wantRemMax: 420,
			wantDurMin: 480,
			wantDurMax: 720,
		},
		{
			name:       "startup",
			diff:       12,
			wantPhase:  flightphase.Startup,
			wantRemMin: 0,
			wantRemMax: 240,
			wantDurMin: 180,
			wantDurMax: 420,
		},
		{
			name:         "taxi_out",
			diff:         5,
			wantPhase:    flightphase.TaxiOut,
			wantRemExact: true,
			wantRemMin:   300,
			wantDurExact: true,
			wantDurMin:   600,
		},
		{
			name:         "takeoff_is_taxi_out",
			diff:         0,
			wantPhase:    flightphase.TaxiOut,
			wantRemExact: true,
			wantRemMin:   600,
			wantDurExact: true,
			wantDurMin:   600,
		},
		{
			name:       "climbout",
			diff:       -3,
			wantPhase:  flightphase.Climbout,
			wantRemMin: 80,
			wantRemMax: 160,
			wantDurMin: 200,
			wantDurMax: 280,
		},
		{
			name:       "departure",
			diff:       -10,
			wantPhase:  flightphase.Departure,
			wantRemMin: 180,
			wantRemMax: 420,
			wantDurMin: 480,
			wantDurMax: 720,
		},
		{
			name:         "cruise_default",
			diff:         -20,
			wantPhase:    flightphase.Cruise,
			wantRemExact: false,
			wantRemMin:   180,
			wantRemMax:   420,
			wantDurExact: true,
			wantDurMin:   600,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEngine(baseTime)
			e.AirportConfig = tc.airportConfig
			e.RunwayQueues = tc.runwayQueues
			schedule := buildDepartureSchedule(baseTime, tc.diff, 30)

			phase, remaining, totalDur, delay := e.determineInitialDeparturePhase(tc.diff, schedule)
			if phase != tc.wantPhase {
				t.Fatalf("phase: got %v want %v", phase, tc.wantPhase)
			}
			if delay != tc.wantDelay {
				t.Fatalf("delay: got %d want %d", delay, tc.wantDelay)
			}
			if tc.wantRemExact {
				if remaining != tc.wantRemMin {
					t.Fatalf("estimated duration: got %d want %d", remaining, tc.wantRemMin)
				}
			} else {
				assertRange(t, "estimated duration", remaining, tc.wantRemMin, tc.wantRemMax)
			}
			if tc.wantDurExact {
				if totalDur != tc.wantDurMin {
					t.Fatalf("remaining duration: got %d want %d", totalDur, tc.wantDurMin)
				}
			} else {
				assertRange(t, "remaining duration", totalDur, tc.wantDurMin, tc.wantDurMax)
			}
		})
	}
}

func buildArrivalSchedule(baseTime time.Time, diff, durationMin int) *flightplan.ScheduledFlight {
	arrivalTime := baseTime.Add(time.Duration(diff) * time.Minute)
	departureTime := arrivalTime.Add(-time.Duration(durationMin) * time.Minute)
	return &flightplan.ScheduledFlight{
		AircraftRegistration: "TEST123",
		DepartureHour:        departureTime.Hour(),
		DepartureMin:         departureTime.Minute(),
		ArrivalHour:          arrivalTime.Hour(),
		ArrivalMin:           arrivalTime.Minute(),
	}
}

func TestDetermineInitialArrivalPhase(t *testing.T) {
	baseTime := time.Now().Truncate(time.Minute)

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
			name:      "arrival_phase",
			diff:      10,
			wantPhase: flightphase.Arrival,
			// estimated total approach window (AMINUS_ARRIVAL_MINS - AMINUS_APPROACH_MINS) * 60 => 540 +/- jitter
			wantEstMin: 480,
			wantEstMax: 600,
			// remaining until approach (minsToSchedArr - AMINUS_APPROACH_MINS) * 60 => ~240 +/- jitter
			wantRemMin: 180,
			wantRemMax: 300,
		},
		{
			name:      "approach_phase",
			diff:      5,
			wantPhase: flightphase.Approach,
			// estimated approach window (AMINUS_APPROACH_MINS - AMINUS_FINAL_MINS) * 60 => 240 +/- jitter
			wantEstMin: 210,
			wantEstMax: 270,
			// remaining until final (minsToSchedArr - AMINUS_FINAL_MINS) * 60 => ~180 +/- jitter
			wantRemMin: 150,
			wantRemMax: 210,
		},
		{
			name:         "final_phase_promoted_to_approach",
			diff:         1,
			wantPhase:    flightphase.Approach,
			wantEstExact: true,
			wantEstMin:   240,
			wantRemExact: true,
			wantRemMin:   60,
		},
		{
			name:         "braking_promoted_to_approach",
			diff:         0,
			wantPhase:    flightphase.TaxiIn,
			wantEstExact: true,
			wantEstMin:   600,
			wantRemExact: true,
			wantRemMin:   600,
		},
		{
			name:         "taxi_in",
			diff:         -1,
			wantPhase:    flightphase.TaxiIn,
			wantEstExact: true,
			wantEstMin:   60,
			wantRemExact: true,
			wantRemMin:   60,
		},
		{
			name:      "shutdown",
			diff:      -5,
			wantPhase: flightphase.Shutdown,
			// estimated total taxi-in window is constant
			wantEstExact: true,
			wantEstMin:   600,
			// remaining time until taxi-in start
			wantRemMin: 360,
			wantRemMax: 480,
		},
		{
			name:      "parked",
			diff:      -13,
			wantPhase: flightphase.Parked,
			// estimated parking window total
			wantEstExact: true,
			wantEstMin:   180,
			// remaining until parked time
			wantRemMin: 0,
			wantRemMax: 240,
		},
		{
			name:         "cruise_default",
			diff:         20,
			wantPhase:    flightphase.Cruise,
			wantEstExact: true,
			// estimated total cruise minutes based on schedule (20 mins -> 1200s)
			wantEstMin: 1200,
			wantRemMin: 180,
			wantRemMax: 420,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEngine(baseTime)
			schedule := buildArrivalSchedule(baseTime, tc.diff, 30)

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
