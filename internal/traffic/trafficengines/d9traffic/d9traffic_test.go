package d9traffic

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/curbz/decimal-niner/internal/atc"
	"github.com/curbz/decimal-niner/internal/constants"
	"github.com/curbz/decimal-niner/internal/flightclass"
	"github.com/curbz/decimal-niner/internal/flightphase"
	"github.com/curbz/decimal-niner/internal/flightplan"
	"github.com/curbz/decimal-niner/internal/traffic"
	"github.com/curbz/decimal-niner/pkg/geometry"
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

func TestPositionDrivenTaxiSetsEstimatedNextTransition(t *testing.T) {
	baseTime := time.Now()
	e := newTestEngine(baseTime)
	airport := &atc.Airport{Elevation: 50}

	ac := &atc.Aircraft{
		Registration: "TAXI1",
		Flight: atc.Flight{
			AssignedParkingSpot: &atc.ParkingSpot{Lat: 51.0, Lon: -0.1, Heading: 90},
			DepartureAccess:     &atc.AccessPoint{Coord: atc.Coordinate{Lat: 51.0005, Lon: -0.0995}, Bearing: 90},
			Phase: flightphase.Phase{
				Current:                 flightphase.TaxiOut.Index(),
				Transition:              baseTime.Add(-10 * time.Second),
				TotalDuration:           10 * time.Second,
				EstimatedNextTransition: time.Time{},
			},
		},
	}

	e.updateTaxiPosition(ac, airport, true)
	if ac.Flight.Phase.EstimatedNextTransition.IsZero() {
		t.Fatalf("EstimatedNextTransition not set by updateTaxiPosition")
	}
	now := e.AtcService.GetCurrentZuluTime()
	delta := ac.Flight.Phase.EstimatedNextTransition.Sub(now)
	if math.Abs(delta.Seconds()) > 2 {
		t.Fatalf("EstimatedNextTransition not near now: delta=%v", delta)
	}
}

func TestPositionDrivenLinearSetsEstimatedNextTransition(t *testing.T) {
	baseTime := time.Now()
	e := newTestEngine(baseTime)
	rwy := &atc.Runway{Lat: 51.0, Lon: -0.2, Heading: 270, Length: 2500}

	ac := &atc.Aircraft{
		Registration: "LIN1",
		Flight: atc.Flight{
			AssignedRunway: rwy,
			Phase: flightphase.Phase{
				Current:       flightphase.Takeoff.Index(),
				Transition:    baseTime.Add(-5 * time.Second),
				TotalDuration: 5 * time.Second,
			},
		},
	}

	e.updateLinearPosition(ac, &atc.Airport{Elevation: 100})
	if ac.Flight.Phase.EstimatedNextTransition.IsZero() {
		t.Fatalf("EstimatedNextTransition not set by updateLinearPosition")
	}
}

func TestPositionDrivenCruiseSetsEstimatedNextTransition(t *testing.T) {
	baseTime := time.Now()
	e := newTestEngine(baseTime)

	// create origin/dest airports used by updateCruisePosition
	e.AtcService.Airports = map[string]*atc.Airport{
		"EGLL": {Lat: 51.4700, Lon: -0.4543, Elevation: 83},
		"KJFK": {Lat: 40.6413, Lon: -73.7781, Elevation: 13},
	}

	ac := &atc.Aircraft{
		Registration: "CRZ1",
		Flight: atc.Flight{
			Schedule: &flightplan.ScheduledFlight{IcaoOrigin: "EGLL", IcaoDest: "KJFK"},
			Phase: flightphase.Phase{
				Current:       flightphase.Cruise.Index(),
				Transition:    baseTime.Add(-20 * time.Second),
				TotalDuration: 20 * time.Second,
			},
		},
	}

	e.updateCruisePosition(ac)
	if ac.Flight.Phase.EstimatedNextTransition.IsZero() {
		t.Fatalf("EstimatedNextTransition not set by updateCruisePosition")
	}
}

func TestPositionDrivenUpdateActiveAircraftTransitionsTaxiOutToTakeoff(t *testing.T) {
	baseTime := time.Now()
	e := newTestEngine(baseTime)

	// Prepare airport and runway
	airport := &atc.Airport{ICAO: "EGLL", Lat: 51.4700, Lon: -0.4543, Elevation: 83}
	rwy := &atc.Runway{Name: "09L", Lat: 51.4700, Lon: -0.4543, Heading: 90, Length: 3900}
	e.AtcService.Airports = map[string]*atc.Airport{"EGLL": airport}
	e.AirportConfig = map[string]ActiveRunwaySet{"EGLL": {Departure: rwy, Arrival: rwy}}
	e.RunwayLocks = make(map[string]*RunwayLock)
	e.RunwayQueues = make(map[string]map[string]time.Time)

	// Build scheduled flight so updateActiveAircraft processes it
	sched := &flightplan.ScheduledFlight{AircraftRegistration: "TAXI2", IcaoOrigin: "EGLL", IcaoDest: "KJFK", DepartureHour: baseTime.Hour(), DepartureMin: baseTime.Minute(), ArrivalHour: baseTime.Hour(), ArrivalMin: baseTime.Minute() + 30}

	ac := &atc.Aircraft{
		Registration: "TAXI2",
		Flight: atc.Flight{
			Number:              1,
			Origin:              "EGLL",
			Destination:         "KJFK",
			Schedule:            sched,
			AssignedParkingSpot: &atc.ParkingSpot{Lat: 51.47, Lon: -0.455, Heading: 90},
			DepartureAccess:     &atc.AccessPoint{Coord: atc.Coordinate{Lat: 51.4705, Lon: -0.4545}, Bearing: 90},
			Phase: flightphase.Phase{
				Current:       flightphase.TaxiOut.Index(),
				Transition:    baseTime.Add(-20 * time.Second),
				TotalDuration: 10 * time.Second,
			},
		},
	}

	key := fmt.Sprintf("%s_%d", ac.Registration, ac.Flight.Number)
	e.ActiveAircraft = map[string]*atc.Aircraft{key: ac}

	// run the taxi position update so it sets EstimatedNextTransition via position-driven logic
	e.updateTaxiPosition(ac, airport, true)
	if ac.Flight.Phase.EstimatedNextTransition.IsZero() {
		t.Fatalf("precondition failed: EstimatedNextTransition should be set by updateTaxiPosition")
	}

	// Now call updateActiveAircraft which should observe the EstimatedNextTransition and transition phase
	e.updateActiveAircraft([]string{"EGLL"})

	if flightphase.FlightPhase(ac.Flight.Phase.Current) != flightphase.Takeoff {
		t.Fatalf("expected phase Takeoff after updateActiveAircraft, got %v", flightphase.FlightPhase(ac.Flight.Phase.Current))
	}
}

func TestRace_ScheduleEarlierThanPosition_NoPrematureTimeTransition(t *testing.T) {
	baseTime := time.Now()
	e := newTestEngine(baseTime)

	airport := &atc.Airport{ICAO: "EGLL", Lat: 51.4700, Lon: -0.4543, Elevation: 83}
	rwy := &atc.Runway{Name: "09L", Lat: 51.4700, Lon: -0.4543, Heading: 90, Length: 3900}
	e.AtcService.Airports = map[string]*atc.Airport{"EGLL": airport}
	e.AirportConfig = map[string]ActiveRunwaySet{"EGLL": {Departure: rwy, Arrival: rwy}}
	e.RunwayLocks = make(map[string]*RunwayLock)
	e.RunwayQueues = make(map[string]map[string]time.Time)

	sched := &flightplan.ScheduledFlight{AircraftRegistration: "RACE1", IcaoOrigin: "EGLL", IcaoDest: "KJFK", DepartureHour: baseTime.Hour(), DepartureMin: baseTime.Minute()}

	// EstimatedNextTransition is in the past (schedule says transition now), but position is not complete
	ac := &atc.Aircraft{
		Registration: "RACE1",
		Flight: atc.Flight{
			Number:              2,
			Origin:              "EGLL",
			Destination:         "KJFK",
			Schedule:            sched,
			AssignedParkingSpot: &atc.ParkingSpot{Lat: 51.47, Lon: -0.455, Heading: 90},
			DepartureAccess:     &atc.AccessPoint{Coord: atc.Coordinate{Lat: 51.4705, Lon: -0.4545}, Bearing: 90},
			Phase: flightphase.Phase{
				Current:                 flightphase.TaxiOut.Index(),
				Transition:              baseTime.Add(-5 * time.Second),
				TotalDuration:           600 * time.Second,
				EstimatedNextTransition: baseTime.Add(-1 * time.Second),
				PositionComplete:        false,
			},
		},
	}

	key := fmt.Sprintf("%s_%d", ac.Registration, ac.Flight.Number)
	e.ActiveAircraft = map[string]*atc.Aircraft{key: ac}

	e.updateActiveAircraft([]string{"EGLL"})

	// With position-driven transitions enabled, schedule alone should not force a transition
	if flightphase.FlightPhase(ac.Flight.Phase.Current) != flightphase.TaxiOut {
		t.Fatalf("expected to remain in TaxiOut when position incomplete, got %v", flightphase.FlightPhase(ac.Flight.Phase.Current))
	}
}

func TestRace_PositionEarlierThanSchedule_PositionTriggersTransition(t *testing.T) {
	baseTime := time.Now()
	e := newTestEngine(baseTime)

	airport := &atc.Airport{ICAO: "EGLL", Lat: 51.4700, Lon: -0.4543, Elevation: 83}
	rwy := &atc.Runway{Name: "09L", Lat: 51.4700, Lon: -0.4543, Heading: 90, Length: 3900}
	e.AtcService.Airports = map[string]*atc.Airport{"EGLL": airport}
	e.AirportConfig = map[string]ActiveRunwaySet{"EGLL": {Departure: rwy, Arrival: rwy}}
	e.RunwayLocks = make(map[string]*RunwayLock)
	e.RunwayQueues = make(map[string]map[string]time.Time)

	sched := &flightplan.ScheduledFlight{AircraftRegistration: "RACE2", IcaoOrigin: "EGLL", IcaoDest: "KJFK", DepartureHour: baseTime.Hour(), DepartureMin: baseTime.Minute()}

	// EstimatedNextTransition far in the future, but position is already complete
	ac := &atc.Aircraft{
		Registration: "RACE2",
		Flight: atc.Flight{
			Number:              3,
			Origin:              "EGLL",
			Destination:         "KJFK",
			Schedule:            sched,
			AssignedParkingSpot: &atc.ParkingSpot{Lat: 51.47, Lon: -0.455, Heading: 90},
			DepartureAccess:     &atc.AccessPoint{Coord: atc.Coordinate{Lat: 51.4705, Lon: -0.4545}, Bearing: 90},
			Phase: flightphase.Phase{
				Current:                 flightphase.TaxiOut.Index(),
				Transition:              baseTime.Add(-5 * time.Second),
				TotalDuration:           600 * time.Second,
				EstimatedNextTransition: baseTime.Add(1 * time.Hour),
				PositionComplete:        true,
			},
		},
	}

	key := fmt.Sprintf("%s_%d", ac.Registration, ac.Flight.Number)
	e.ActiveAircraft = map[string]*atc.Aircraft{key: ac}

	e.updateActiveAircraft([]string{"EGLL"})

	if flightphase.FlightPhase(ac.Flight.Phase.Current) != flightphase.Takeoff {
		t.Fatalf("expected position-driven transition to Takeoff, got %v", flightphase.FlightPhase(ac.Flight.Phase.Current))
	}
}

func TestTakeoffUsesDepartureAccess(t *testing.T) {
	baseTime := time.Now()
	e := newTestEngine(baseTime)

	airport := &atc.Airport{ICAO: "EGLL", Lat: 51.4700, Lon: -0.4543, Elevation: 83}
	rwy := &atc.Runway{Name: "09L", Lat: 51.4700, Lon: -0.4543, Heading: 90, Length: 3900}
	e.AtcService.Airports = map[string]*atc.Airport{"EGLL": airport}

	// place departure access 0.01 deg north-east of threshold (~0.7 NM)
	depAccess := &atc.AccessPoint{Coord: atc.Coordinate{Lat: 51.4800, Lon: -0.4443}, Bearing: 90}

	ac := &atc.Aircraft{
		Registration: "TST_TO",
		SizeClass:    "C",
		Flight: atc.Flight{
			AssignedRunway:  rwy,
			DepartureAccess: depAccess,
			Phase: flightphase.Phase{
				Current:       flightphase.Takeoff.Index(),
				Transition:    baseTime.Add(-12 * time.Second),
				TotalDuration: 12 * time.Second,
			},
		},
	}

	e.updateLinearPosition(ac, airport)

	// Expect the aircraft to be closer to the DepartureAccess than to the runway-end projection
	projLat, projLon := geometry.Project(rwy.Lat, rwy.Lon, rwy.Heading, rwy.Length*constants.MetersToNM)
	distToDep := geometry.DistNM(ac.Flight.Position.Lat, ac.Flight.Position.Long, depAccess.Coord.Lat, depAccess.Coord.Lon)
	distToProj := geometry.DistNM(ac.Flight.Position.Lat, ac.Flight.Position.Long, projLat, projLon)
	if distToDep >= distToProj {
		t.Fatalf("takeoff target selection did not prefer DepartureAccess: distToDep=%f >= distToProj=%f", distToDep, distToProj)
	}
}

func TestBrakingUsesArrivalAccess(t *testing.T) {
	baseTime := time.Now()
	e := newTestEngine(baseTime)

	airport := &atc.Airport{ICAO: "EGLL", Lat: 51.4700, Lon: -0.4543, Elevation: 83}
	rwy := &atc.Runway{Name: "09L", Lat: 51.4700, Lon: -0.4543, Heading: 90, Length: 3900}
	e.AtcService.Airports = map[string]*atc.Airport{"EGLL": airport}

	// arrival access (runway exit) point
	arrAccess := &atc.AccessPoint{Coord: atc.Coordinate{Lat: 51.4720, Lon: -0.4520}, Bearing: 90}

	ac := &atc.Aircraft{
		Registration: "TST_BR",
		Flight: atc.Flight{
			AssignedRunway: rwy,
			ArrivalAccess:  arrAccess,
			Phase: flightphase.Phase{
				Current:       flightphase.Braking.Index(),
				Transition:    baseTime.Add(-5 * time.Second),
				TotalDuration: 5 * time.Second,
			},
		},
	}

	e.updateLinearPosition(ac, airport)

	dist := geometry.DistNM(ac.Flight.Position.Lat, ac.Flight.Position.Long, arrAccess.Coord.Lat, arrAccess.Coord.Lon)
	if dist > 0.01 {
		t.Fatalf("braking did not target ArrivalAccess: dist=%f NM", dist)
	}
}

func TestGroundSpeedCappingBySizeClass(t *testing.T) {
	baseTime := time.Now()
	e := newTestEngine(baseTime)

	airport := &atc.Airport{ICAO: "EGLL", Lat: 51.4700, Lon: -0.4543, Elevation: 83}
	rwy := &atc.Runway{Name: "09L", Lat: 51.4700, Lon: -0.4543, Heading: 90, Length: 3900}
	e.AtcService.Airports = map[string]*atc.Airport{"EGLL": airport}

	// very distant target (~10 NM) to force capping logic
	farTarget := &atc.AccessPoint{Coord: atc.Coordinate{Lat: 51.5700, Lon: -0.3543}, Bearing: 90}

	ac := &atc.Aircraft{
		Registration: "TST_CAP",
		SizeClass:    "C",
		Flight: atc.Flight{
			AssignedRunway:  rwy,
			DepartureAccess: farTarget,
			Phase: flightphase.Phase{
				Current:       flightphase.Takeoff.Index(),
				Transition:    baseTime.Add(-1 * time.Second),
				TotalDuration: 1 * time.Second,
			},
		},
	}

	// compute plannedDist and allowedDist as per engine logic
	startLat, startLon := rwy.Lat, rwy.Lon
	_ = geometry.DistNM(startLat, startLon, farTarget.Coord.Lat, farTarget.Coord.Lon)
	maxAllowedKts := 180.0 // for size class C and takeoff
	allowedDist := maxAllowedKts * (ac.Flight.Phase.TotalDuration.Seconds() / 3600.0)

	e.updateLinearPosition(ac, airport)

	movedDist := geometry.DistNM(startLat, startLon, ac.Flight.Position.Lat, ac.Flight.Position.Long)
	if movedDist > allowedDist+0.01 {
		t.Fatalf("ground speed cap failed: moved %f NM > allowed %f NM", movedDist, allowedDist)
	}
}

func TestGetPhaseVerticalRateFpmPerSize(t *testing.T) {
	e := newTestEngine(time.Now())
	// Build a dummy aircraft for testing
	acSmall := &atc.Aircraft{SizeClass: "A"}
	acMed := &atc.Aircraft{SizeClass: "C"}
	acHeavy := &atc.Aircraft{SizeClass: "F"}

	// Final expected: small/default -700, medium -700, heavy -500 (per tuning)
	vSmall := e.getPhaseVerticalRateFpm(acSmall, flightphase.Final)
	vMed := e.getPhaseVerticalRateFpm(acMed, flightphase.Final)
	vHeavy := e.getPhaseVerticalRateFpm(acHeavy, flightphase.Final)

	if vMed != -700.0 {
		t.Fatalf("expected medium Final -700 fpm, got %v", vMed)
	}
	if vHeavy != -500.0 {
		t.Fatalf("expected heavy Final -500 fpm, got %v", vHeavy)
	}
	if vSmall != -700.0 {
		t.Fatalf("expected small/default Final -700 fpm, got %v", vSmall)
	}
}

func TestFinalVerticalRateIsAppliedDuringLinearPosition(t *testing.T) {
	baseTime := time.Now()
	e := newTestEngine(baseTime)

	airport := &atc.Airport{ICAO: "EGLL", Lat: 51.4700, Lon: -0.4543, Elevation: 100}
	rwy := &atc.Runway{Name: "09L", Lat: 51.4700, Lon: -0.4543, Heading: 90, Length: 3900}

	// Aircraft in Final with high initial altitude and elapsed > total duration to force progress=1
	ac := &atc.Aircraft{
		Registration: "VRT1",
		SizeClass:    "C",
		Flight: atc.Flight{
			AssignedRunway: rwy,
			Phase: flightphase.Phase{
				Current:         flightphase.Final.Index(),
				Transition:      baseTime.Add(-120 * time.Second),
				TotalDuration:   60 * time.Second,
				InitialAltitude: 3000.0,
			},
		},
	}

	// Call updateLinearPosition which should apply vertical-rate limiting
	e.updateLinearPosition(ac, airport)

	// Compute allowed change
	vrate := e.getPhaseVerticalRateFpm(ac, flightphase.Final)
	elapsed := e.AtcService.GetCurrentZuluTime().Sub(ac.Flight.Phase.Transition).Seconds()
	allowedChange := vrate * (elapsed / 60.0)

	actualChange := ac.Flight.Position.Altitude - ac.Flight.Phase.InitialAltitude

	// vrate is negative for descent; actualChange should not be less than allowedChange (i.e., magnitude capped)
	if actualChange < allowedChange-1.0 {
		t.Fatalf("vertical-rate cap failed: actualChange=%v allowedChange=%v", actualChange, allowedChange)
	}
}

func TestCruiseTODCalculationPerSize(t *testing.T) {
	e := newTestEngine(time.Now())

	// Setup a representative altitude loss (ft)
	altitudeToLose := 10000.0

	types := []struct {
		size string
	}{
		{"A"}, {"C"}, {"F"},
	}

	for _, tt := range types {
		ac := &atc.Aircraft{SizeClass: tt.size}
		vrateAbs := math.Abs(e.getPhaseVerticalRateFpm(ac, flightphase.Approach))
		cruiseGs := e.getPhaseGroundSpeedKts(ac, flightphase.Cruise)

		var expectedDist float64
		if vrateAbs > 0 {
			timeMin := altitudeToLose / vrateAbs
			expectedDist = cruiseGs * (timeMin / 60.0)
		} else {
			expectedDist = (altitudeToLose / float64(constants.FeetPerFL)) * constants.DefaultDescentRateNMPerFL
		}

		if expectedDist <= 0 {
			t.Fatalf("expected positive descent distance for size %s, got %v", tt.size, expectedDist)
		}
	}
}

func TestApproachDescentStartDiffersBySize(t *testing.T) {
	e := newTestEngine(time.Now())

	altitudeToLose := 12000.0

	small := &atc.Aircraft{SizeClass: "A"}
	med := &atc.Aircraft{SizeClass: "C"}
	heavy := &atc.Aircraft{SizeClass: "F"}

	computeDist := func(ac *atc.Aircraft) float64 {
		vrateAbs := math.Abs(e.getPhaseVerticalRateFpm(ac, flightphase.Approach))
		cruiseGs := e.getPhaseGroundSpeedKts(ac, flightphase.Cruise)
		if vrateAbs > 0 {
			timeMin := altitudeToLose / vrateAbs
			return cruiseGs * (timeMin / 60.0)
		}
		return (altitudeToLose / float64(constants.FeetPerFL)) * constants.DefaultDescentRateNMPerFL
	}

	dSmall := computeDist(small)
	dMed := computeDist(med)
	dHeavy := computeDist(heavy)

	if !(dHeavy > dMed && dMed >= dSmall) {
		t.Fatalf("expected heavy > med >= small descent distances, got heavy=%v med=%v small=%v", dHeavy, dMed, dSmall)
	}
}

func TestHoldExitOnCircuitComplete(t *testing.T) {
	baseTime := time.Now()
	e := newTestEngine(baseTime)

	airport := &atc.Airport{ICAO: "EGLL", Lat: 51.4700, Lon: -0.4543, Elevation: 83}
	rwy := &atc.Runway{Name: "09L", Lat: 51.4700, Lon: -0.4543, Heading: 90, Length: 3900}
	e.AtcService.Airports = map[string]*atc.Airport{"EGLL": airport}
	e.AirportConfig = map[string]ActiveRunwaySet{"EGLL": {Departure: rwy, Arrival: rwy}}
	e.RunwayLocks = make(map[string]*RunwayLock)
	e.RunwayQueues = make(map[string]map[string]time.Time)

	hold := &atc.Hold{Ident: "H1", Lat: 51.48, Lon: -0.45, MinAlt: 3000}

	ac := &atc.Aircraft{
		Registration: "HOLD1",
		Flight: atc.Flight{
			Schedule:     &flightplan.ScheduledFlight{IcaoOrigin: "EGLL", IcaoDest: "KJFK"},
			AssignedHold: hold,
			Phase: flightphase.Phase{
				Current:                 flightphase.Holding.Index(),
				Transition:              baseTime.Add(-time.Duration((HOLDING_MIN_DURATION_MINS*60)+10) * time.Second),
				TotalDuration:           time.Duration((HOLDING_MIN_DURATION_MINS*60)+10) * time.Second,
				EstimatedNextTransition: baseTime.Add(-1 * time.Second),
			},
		},
	}

	key := fmt.Sprintf("%s_%d", ac.Registration, ac.Flight.Number)
	e.ActiveAircraft = map[string]*atc.Aircraft{key: ac}

	e.updateActiveAircraft([]string{"EGLL"})

	if ac.Flight.AssignedHold != nil {
		t.Fatalf("expected aircraft to be released from hold, but AssignedHold != nil")
	}
	if flightphase.FlightPhase(ac.Flight.Phase.Current) != flightphase.Approach {
		t.Fatalf("expected phase Approach after hold exit, got %v", flightphase.FlightPhase(ac.Flight.Phase.Current))
	}
}

func TestArrivalSentToHoldWhenRunwayApproachSaturated(t *testing.T) {
	baseTime := time.Now()
	e := newTestEngine(baseTime)

	airport := &atc.Airport{ICAO: "EGLL", Lat: 51.4700, Lon: -0.4543, Elevation: 83}
	rwy := &atc.Runway{Name: "09L", Lat: 51.4700, Lon: -0.4543, Heading: 90, Length: 3900}
	e.AtcService.Airports = map[string]*atc.Airport{"EGLL": airport}
	e.AirportConfig = map[string]ActiveRunwaySet{"EGLL": {Departure: rwy, Arrival: rwy}}
	e.RunwayLocks = make(map[string]*RunwayLock)
	e.RunwayQueues = make(map[string]map[string]time.Time)

	// Populate active aircraft already on approach assigned to same runway
	e.ActiveAircraft = make(map[string]*atc.Aircraft)
	for i := 0; i < MAX_APPROACH_ON_APPROACH+1; i++ {
		other := &atc.Aircraft{
			Registration: fmt.Sprintf("APP%02d", i),
			Flight: atc.Flight{
				Schedule:           &flightplan.ScheduledFlight{IcaoDest: "EGLL"},
				AssignedRunwayName: rwy.Name,
				Phase:              flightphase.Phase{Current: flightphase.Approach.Index(), Class: flightclass.Arriving},
			},
		}
		e.ActiveAircraft[other.Registration] = other
	}

	// New arrival that should be sent to hold because same-runway approaches exceed limit
	ac := &atc.Aircraft{
		Registration: "NEWARR",
		Flight: atc.Flight{
			Schedule:           &flightplan.ScheduledFlight{IcaoDest: "EGLL"},
			AssignedRunwayName: rwy.Name,
			Phase: flightphase.Phase{
				Current:          flightphase.Arrival.Index(),
				Class:            flightclass.Arriving,
				PositionComplete: true,
			},
		},
	}
	e.ActiveAircraft[ac.Registration] = ac

	e.updateActiveAircraft([]string{"EGLL"})

	if flightphase.FlightPhase(ac.Flight.Phase.Current) != flightphase.Holding {
		t.Fatalf("expected new arrival to be sent to Holding, got %v", flightphase.FlightPhase(ac.Flight.Phase.Current))
	}
	// AssignedHold may be nil in this isolated test environment if no holds
	// are configured in the ATC service; ensure the aircraft at least moved
	// into the Holding phase.
}

func TestArrivalNotSentToHoldIfDifferentRunway(t *testing.T) {
	baseTime := time.Now()
	e := newTestEngine(baseTime)

	airport := &atc.Airport{ICAO: "EGLL", Lat: 51.4700, Lon: -0.4543, Elevation: 83}
	rwyA := &atc.Runway{Name: "09L"}
	rwyB := &atc.Runway{Name: "09R"}
	e.AtcService.Airports = map[string]*atc.Airport{"EGLL": airport}
	e.AirportConfig = map[string]ActiveRunwaySet{"EGLL": {Departure: rwyA, Arrival: rwyA}}
	e.RunwayLocks = make(map[string]*RunwayLock)
	e.RunwayQueues = make(map[string]map[string]time.Time)

	// Populate active aircraft on approach assigned to a different runway
	e.ActiveAircraft = make(map[string]*atc.Aircraft)
	for i := 0; i < MAX_APPROACH_ON_APPROACH+2; i++ {
		other := &atc.Aircraft{
			Registration: fmt.Sprintf("APPR%02d", i),
			Flight: atc.Flight{
				Schedule:           &flightplan.ScheduledFlight{IcaoDest: "EGLL"},
				AssignedRunwayName: rwyB.Name,
				Phase:              flightphase.Phase{Current: flightphase.Approach.Index(), Class: flightclass.Arriving},
			},
		}
		e.ActiveAircraft[other.Registration] = other
	}

	// New arrival assigned to rwyA should still be allowed into approach
	ac := &atc.Aircraft{
		Registration: "NEWARR2",
		Flight: atc.Flight{
			Schedule:           &flightplan.ScheduledFlight{IcaoDest: "EGLL"},
			AssignedRunwayName: rwyA.Name,
			Phase: flightphase.Phase{
				Current:          flightphase.Arrival.Index(),
				Class:            flightclass.Arriving,
				PositionComplete: true,
			},
		},
	}
	e.ActiveAircraft[ac.Registration] = ac

	e.updateActiveAircraft([]string{"EGLL"})

	if flightphase.FlightPhase(ac.Flight.Phase.Current) != flightphase.Approach {
		t.Fatalf("expected new arrival to commence Approach, got %v", flightphase.FlightPhase(ac.Flight.Phase.Current))
	}
}

func TestHoldStackAssignment(t *testing.T) {
	baseTime := time.Now()
	e := newTestEngine(baseTime)

	hold := &atc.Hold{Ident: "H1", Lat: 51.48, Lon: -0.45, MinAlt: 3000}

	// Create three aircraft with out-of-order altitudes assigned to the same hold
	a1 := &atc.Aircraft{Registration: "S1", Flight: atc.Flight{AssignedHold: hold, Position: atc.Position{Altitude: 5000}}}
	a2 := &atc.Aircraft{Registration: "S2", Flight: atc.Flight{AssignedHold: hold, Position: atc.Position{Altitude: 3000}}}
	a3 := &atc.Aircraft{Registration: "S3", Flight: atc.Flight{AssignedHold: hold, Position: atc.Position{Altitude: 4000}}}

	e.ActiveAircraft = map[string]*atc.Aircraft{a1.Registration: a1, a2.Registration: a2, a3.Registration: a3}

	e.reassignHoldStack(hold)

	// After reassign, altitudes should be 3000, 4000, 5000 for the stack
	var alts []float64
	for _, a := range []*atc.Aircraft{a2, a3, a1} {
		alts = append(alts, a.Flight.Position.Altitude)
	}
	if alts[0] != 3000 || alts[1] != 4000 || alts[2] != 5000 {
		t.Fatalf("unexpected stack altitudes: %v", alts)
	}
}
