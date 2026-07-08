package d9traffic

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/curbz/decimal-niner/internal/atc"
	"github.com/curbz/decimal-niner/internal/constants"
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
		vrateAbs := math.Abs(e.getPhaseVerticalRateFpm(ac.SizeClass, flightphase.Approach))
		cruiseGs := e.getPhaseGroundSpeedKts(ac.SizeClass, flightphase.Cruise)

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

// Mock helper to set up a baseline engine instance
func setupMockEngine() *D9TrafficEngine {

	baseTime := time.Now().Truncate(time.Minute)
	svc := &atc.Service{}
	svc.SyncSimTime(baseTime, baseTime)

	engine := &D9TrafficEngine{
		CommonTrafficEngine: traffic.CommonTrafficEngine{
			AtcService: svc,
		},
		ActiveAircraft: make(map[string]*atc.Aircraft),
		AirportConfig:  make(map[string]ActiveRunwaySet),
		// Initialize minimum required service layer maps here
	}
	return engine
}


// TestUpdateLateralApproach_Scenarios checks deterministic geometric behaviors 
// using precise test setups.
// TestUpdateLateralApproach_Scenarios checks deterministic geometric behaviors 
// using precise test setups.
func TestUpdateLateralApproach_Scenarios(t *testing.T) {
	e := &D9TrafficEngine{}

	tests := []struct {
		name           string
		rwyLat         float64
		rwyLong        float64
		rwyHdg         float64
		acLat          float64
		acLong         float64
		initialHeading float64
		dt             float64
		expectedMinHdg float64
		expectedMaxHdg float64
	}{
		{
			name:           "Turn Rate Limiting - Right Turn Cap",
			rwyLat:         0.0,
			rwyLong:        0.0,
			rwyHdg:         90.0,
			acLat:          0.0,
			acLong:         -0.125, // Point A for Rwy 09
			initialHeading: 80.0,
			dt:             1.0, // 3 deg/sec max change -> 83.0
			expectedMinHdg: 82.99,
			expectedMaxHdg: 83.01,
		},
		{
			name:           "Turn Rate Limiting - Left Turn Cap",
			rwyLat:         0.0,
			rwyLong:        0.0,
			rwyHdg:         90.0,
			acLat:          0.0,
			acLong:         -0.125,
			initialHeading: 100.0,
			dt:             2.0, // 3 * 2 = 6 deg max change -> 94.0
			expectedMinHdg: 93.99,
			expectedMaxHdg: 94.01,
		},
		{
			name:           "Heading Wrap-around Crossing 360",
			rwyLat:         0.0,
			rwyLong:        0.0,
			rwyHdg:         90.0,
			acLat:          0.0,
			acLong:         -0.125,
			initialHeading: 359.0, // Shorter turn is right across 360 to get to 090
			dt:             1.0,   // 3 deg turn -> 359 + 3 = 362 -> 2.0
			expectedMinHdg: 1.99,
			expectedMaxHdg: 2.01,
		},
		{
			name:           "Heading Wrap-around Below 0",
			rwyLat:         0.0,
			rwyLong:        0.0,
			rwyHdg:         270.0, // Changed to a West runway alignment
			acLat:          0.0,
			acLong:         0.125, // Positioned East of the runway on extended centerline
			initialHeading: 1.0,   // Shorter turn to 270 is now left (counter-clockwise) across 0
			dt:             1.0,   // 3 deg turn left -> 1 - 3 = -2 -> Wraps cleanly to 358.0
			expectedMinHdg: 357.99,
			expectedMaxHdg: 358.01,
		},
		{
			name:           "Snap Directly to Target within Limiting Threshold",
			rwyLat:         0.0,
			rwyLong:        0.0,
			rwyHdg:         90.0,
			acLat:          0.0,
			acLong:         -0.125,
			initialHeading: 88.5,
			dt:             1.0, // Max turn is 3.0, diff is 1.5 -> Lock to 90.0
			expectedMinHdg: 89.99,
			expectedMaxHdg: 90.01,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ac := &atc.Aircraft{
				Flight: atc.Flight{
					Position: atc.Position{
						Lat:     tt.acLat,
						Long:    tt.acLong,
						Heading: tt.initialHeading,
					},
				},
			}

			e.UpdateLateralApproach(ac, tt.rwyLat, tt.rwyLong, tt.rwyHdg, tt.dt)

			actualHdg := ac.Flight.Position.Heading
			if actualHdg < tt.expectedMinHdg || actualHdg > tt.expectedMaxHdg {
				t.Errorf("Expected heading between %f and %f, got %f", tt.expectedMinHdg, tt.expectedMaxHdg, actualHdg)
			}
		})
	}
}

// TestUpdateLateralApproach_ArcTurnPhase verifies that an aircraft on the North Intercept Circle
// abandons the point segments and smoothly tracks the tangent vector of the tracking arc.
// TestUpdateLateralApproach_ArcTurnPhase verifies that an aircraft on the North Intercept Circle
// abandons the point segments and smoothly tracks the tangent vector of the tracking arc.
func TestUpdateLateralApproach_ArcTurnPhase(t *testing.T) {
	e := &D9TrafficEngine{}
	
	// Runway at (0.0, 0.0) Heading 090.0
	// Point A is at (0.0, -0.125)
	// North circle center (oN) is 1.5 NM North (heading 000) from Point A:
	// Lat = 0.0 + (1.5 / 60) = 0.025, Long = -0.125
	rwyLat := 0.0
	rwyLong := 0.0
	rwyHdg := 90.0

	// Position aircraft exactly 1.5 NM East of the North circle center:
	// oN Lat = 0.025, oN Long = -0.125
	// Placing AC at Lat = 0.025, Long = -0.100 (which is exactly 1.5 NM away)
	ac := &atc.Aircraft{
		Flight: atc.Flight{
			Position: atc.Position{
				Lat:     0.025,
				Long:    -0.100,
				Heading: 0.0, // Tangent target heading for this radial should be 0.0 (Counter-Clockwise)
			},
		},
	}

	// Run with a large dt so it immediately matches the target heading if calculated correctly
	e.UpdateLateralApproach(ac, rwyLat, rwyLong, rwyHdg, 10.0)

	// Since the aircraft is directly on the East arc point of the North circle, 
	// the bearing from center is 090. Tangent turn rules subtract 90 deg -> target is 000.0.
	expectedHdg := 0.0
	
	// Compute the circular/angular difference instead of a naive scalar difference
	diff := math.Abs(ac.Flight.Position.Heading - expectedHdg)
	if diff > 180.0 {
		diff = 360.0 - diff
	}

	if diff > 0.05 {
		t.Errorf("Arc intercept phase failed. Expected tangent tracking heading near %f, got %f (angular diff: %f)", expectedHdg, ac.Flight.Position.Heading, diff)
	}
}