package d9traffic

import (
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"strings"
	"time"

	"github.com/curbz/decimal-niner/internal/atc"
	"github.com/curbz/decimal-niner/internal/flightclass"
	"github.com/curbz/decimal-niner/internal/flightphase"
	"github.com/curbz/decimal-niner/internal/flightplan"
	"github.com/curbz/decimal-niner/internal/logger"
	"github.com/curbz/decimal-niner/internal/traffic"
	"github.com/curbz/decimal-niner/pkg/geometry"
	"github.com/curbz/decimal-niner/pkg/util"
)

type D9TrafficEngine struct {
	AirportSchedules map[string]*AirportTimeline
	atcService       *atc.Service
	FlightPlanPath   string
	ActiveAircraft   map[string]*atc.Aircraft
	initialised      bool
	OccupiedParking  map[string]string
	AirportConfig    map[string]ActiveRunwaySet
	RunwayLocks      map[string]*RunwayLock
	RunwayQueues     map[string]map[string]time.Time
}

type D9TrafficConfig struct {
	D9Traffic struct {
		FlightPlanPath string `yaml:"flight_plan_directory"`
	} `yaml:"d9traffic"`
}

type AirportTimeline struct {
	Departures []flightplan.ScheduledFlight
	Arrivals   []flightplan.ScheduledFlight
}

type OccupiedSpot struct {
	Lat, Lon float64
	Radius   float64 // To ensure we don't spawn a ghost touching another plane
}

type ActiveRunwaySet struct {
	Arrival       *atc.Runway
	Departure     *atc.Runway
	LastWindDir   float64
	LastWindSpeed float64
}

type RunwayLock struct {
	OccupiedBy    *atc.Aircraft
	OccupiedSince time.Time // For timeout protection
}

const (
	// time difference (minutes) in relation to scheduled departure time - this is NOT a duration but relative time to departure
	DMINUS_PARKED_MINS    = 25
	DMINUS_STARTUP_MINS   = 15
	DMINUS_TAXIOUT_MINS   = 10
	DMINUS_TAKEOFF_MINS   = 0
	DMINUS_CLIMBOUT_MINS  = -1
	DMINUS_DEPARTURE_MINS = -5
	DMINUS_CRUISE_MINS    = -15

	// time difference (minutes) in relation to scheduled arrival time - this is NOT a duration but relative time to arrival
	AMINUS_ARRIVAL_MINS  = 15
	AMINUS_APPROACH_MINS = 7
	AMINUS_FINAL_MINS    = 2
	AMINUS_LAND_MINS     = 0
	AMINUS_BRAKING       = -1
	AMINUS_TAXIIN_MINS   = -2
	AMINUS_SHUTDOWN_MINS = -12
	AMINUS_PARKED_MINS   = -15

	// allowable time variance (minutes) in phase duration. example: Parked jitter of 240 means that the parked phase duration
	// can be reduced or increased by up to half of this time i.e. 120 seconds
	PARKED_JITTER_SECONDS    = 240
	STARTUP_JITTER_SECONDS   = 120
	TAXI_JITTER_SECONDS      = 240
	TAKEOFF_JITTER_SECONDS   = 120
	CLIMBOUT_JITTER_SECONDS  = 20
	DEPARTURE_JITTER_SECONDS = 200
	CRUISE_JITTER_SECONDS    = 240
	ARRIVAL_JITTER_SECONDS   = 200
	APPROACH_JITTER_SECONDS  = 120
	FINAL_JITTER_SECONDS     = 30
	BRAKING_JITTER_SECONDS   = 5
	SHUTDOWN_JITTER_SECONDS  = 120

	RUNWAY_LOCK_TIMEOUT_SECONDS = 300

	HOLDING_MIN_DURATION_MINS                     = 4
	TRAFFIC_MANAGEMENT_RUNWAY_QUEUE_THRESHOLD     = 10
	TRAFFIC_MANAGEMENT_PER_AIRCRAFT_DELAY_SECONDS = 90
	STAR_PROBABILITY_FACTOR                       = 0.3
	GOAROUND_TO_HOLD_PROBABILITY_FACTOR           = 0.3
)

func New(cfgPath string) (traffic.Engine, error) {
	cfg, err := util.LoadConfig[D9TrafficConfig](cfgPath)
	if err != nil {
		logger.Log.Errorf("Error reading configuration file: %v", err)
		return nil, err
	}

	return &D9TrafficEngine{
		FlightPlanPath:  cfg.D9Traffic.FlightPlanPath,
		ActiveAircraft:  make(map[string]*atc.Aircraft),
		OccupiedParking: make(map[string]string),
		AirportConfig:   make(map[string]ActiveRunwaySet),
		RunwayLocks:     make(map[string]*RunwayLock), // Key is a unique Runway ID (e.g., "EGLL-09L-27R")
		RunwayQueues:    make(map[string]map[string]time.Time),
	}, nil
}

func (tg *D9TrafficEngine) SetATCService(atcService *atc.Service) {
	tg.atcService = atcService
}

func (e *D9TrafficEngine) Start() {
	ticker := time.NewTicker(10 * time.Second)
	go func() {
		for range ticker.C {

			start := time.Now()

			currSimZTime := e.atcService.GetCurrentZuluTime()
			day := int(currSimZTime.Weekday())
			hour := currSimZTime.Hour()
			min := currSimZTime.Minute()

			// 2. Run the Spawn Check for all relevant airports
			relevantICAOs := e.getRelevantICAOs()
			for _, icao := range relevantICAOs {
				ap := e.atcService.GetAirport(icao)
				if ap == nil {
					continue
				}

				// Only refresh if config is missing OR wind has changed significantly
				if e.needsRunwayRefresh(ap) {
					e.refreshRunwayConfig(ap)
				}
				e.checkForDepartureSpawns(icao, day, hour, min)
				e.checkForArrivalSpawns(icao, day, hour, min)
			}

			// 3. Update existing aircraft (Phase transitions)
			e.updateActiveAircraft(relevantICAOs)

			util.LogWithLabel("D9TRAFFIC", "update cycle duration: %v, total active aircraft: %d", time.Since(start), len(e.ActiveAircraft))
		}
	}()
}

func (e *D9TrafficEngine) needsRunwayRefresh(ap *atc.Airport) bool {
	config, exists := e.AirportConfig[ap.ICAO]
	if !exists {
		return true
	} // Initial load

	currentWeather := e.atcService.GetWeatherState()

	// Check if wind shifted by more than 15 degrees
	// OR wind speed changed by more than 5 knots
	dirDelta := math.Abs(currentWeather.Wind.Direction - config.LastWindDir)
	speedDelta := math.Abs(currentWeather.Wind.Speed - config.LastWindSpeed)

	return dirDelta > 15.0 || speedDelta > 5.0
}

func (e *D9TrafficEngine) RequiresAircraftData() bool {
	return false
}

func (e *D9TrafficEngine) GetFlightPlanPath() string {
	return e.FlightPlanPath
}

func (e *D9TrafficEngine) LoadFlightPlans(path string) (map[string][]flightplan.ScheduledFlight, map[string]bool) {
	// For simplicity, we return an empty map here. In a real implementation,
	// this would read from the specified path and populate the flight plans.
	fscheds, airports := flightplan.LoadFlightPlans(path)
	e.ingestSchedules(fscheds)
	return fscheds, airports
}

func (e *D9TrafficEngine) ingestSchedules(rawMap map[string][]flightplan.ScheduledFlight) {
	e.AirportSchedules = make(map[string]*AirportTimeline)

	for _, legs := range rawMap {
		for _, leg := range legs {
			// 1. Assign to Origin (Departure Board)
			if _, ok := e.AirportSchedules[leg.IcaoOrigin]; !ok {
				e.AirportSchedules[leg.IcaoOrigin] = &AirportTimeline{}
			}
			e.AirportSchedules[leg.IcaoOrigin].Departures = append(
				e.AirportSchedules[leg.IcaoOrigin].Departures,
				leg,
			)

			// 2. Assign to Destination (Arrival Board)
			if _, ok := e.AirportSchedules[leg.IcaoDest]; !ok {
				e.AirportSchedules[leg.IcaoDest] = &AirportTimeline{}
			}
			e.AirportSchedules[leg.IcaoDest].Arrivals = append(
				e.AirportSchedules[leg.IcaoDest].Arrivals,
				leg,
			)
		}
	}

	// 3. Sort the boards for O(log n) or efficient linear lookup
	e.sortSchedules()

	util.LogWithLabel("D9TRAFFIC", "ingested %d airports from flight schedules", len(e.AirportSchedules))
}

func (e *D9TrafficEngine) sortSchedules() {
	for icao := range e.AirportSchedules {
		timeline := e.AirportSchedules[icao]

		// Sort Departures
		sort.Slice(timeline.Departures, func(i, j int) bool {
			timeI := (timeline.Departures[i].DepatureHour * 60) + timeline.Departures[i].DepartureMin
			timeJ := (timeline.Departures[j].DepatureHour * 60) + timeline.Departures[j].DepartureMin
			return timeI < timeJ
		})

		// Sort Arrivals
		sort.Slice(timeline.Arrivals, func(i, j int) bool {
			timeI := (timeline.Arrivals[i].ArrivalHour * 60) + timeline.Arrivals[i].ArrivalMin
			timeJ := (timeline.Arrivals[j].ArrivalHour * 60) + timeline.Arrivals[j].ArrivalMin
			return timeI < timeJ
		})
	}
}

func (e *D9TrafficEngine) getRelevantICAOs() []string {
	icaoMap := make(map[string]bool)

	for _, ctrl := range e.atcService.UserState.ActiveFacilities {
		// We only care about airport-specific controllers (TWR, GND, DEL, etc.)
		// Center/Approach might not have a single ICAO, so we filter.
		if ctrl.ICAO != "" {
			icaoMap[ctrl.ICAO] = true
		}
	}

	// if the user is on the ground, include the nearest airport as a fallback for visual/proximity traffic
	if e.atcService.UserState.IsOnGround && e.atcService.UserState.NearestAirport != nil {
		icaoMap[e.atcService.UserState.NearestAirport.ICAO] = true
	}

	var result []string
	for icao := range icaoMap {
		result = append(result, icao)
	}
	return result
}

func (e *D9TrafficEngine) checkForDepartureSpawns(icao string, day, h, m int) {
	timeline, ok := e.AirportSchedules[icao]
	if !ok {
		return
	}

	nowMins := (h * 60) + m
	lookahead := 30

	// We check this day and also potentially the next day if we are near midnight
	daysToCheck := []int{day}
	if nowMins+lookahead >= 1440 {
		nextDay := (day + 1) % 7
		daysToCheck = append(daysToCheck, nextDay)
	}

	for _, targetDay := range daysToCheck {
		for _, f := range timeline.Departures {
			if f.DepartureDayOfWeek != targetDay {
				continue
			}

			fMins := (f.DepatureHour * 60) + f.DepartureMin

			// Adjust time for comparison if we are looking at 'nextDay'
			compareMins := fMins
			if targetDay != day {
				compareMins += 1440
			}

			// If the flight is in the future window [now, now + 30]
			if compareMins >= nowMins && compareMins <= nowMins+lookahead {
				if !e.isCurrentlyActive(f.AircraftRegistration, f.Number) {
					e.spawnGroundTraffic(&f)
				}
			}

			// Optimization: Since it's sorted, if we've passed the window, stop
			if compareMins > nowMins+lookahead {
				break
			}
		}
	}
}

func (e *D9TrafficEngine) checkForArrivalSpawns(icao string, day, h, m int) {
	timeline := e.AirportSchedules[icao]
	nowMins := (h * 60) + m

	for _, f := range timeline.Arrivals {
		if f.ArrivalDayOfWeek != day {
			continue
		}

		arrMins := (f.ArrivalHour * 60) + f.ArrivalMin
		// If arriving soon and not already active
		if arrMins >= nowMins && arrMins <= nowMins+30 {
			if !e.isCurrentlyActive(f.AircraftRegistration, f.Number) {
				e.spawnInboundTraffic(&f)
			}
		}
	}
}

func (e *D9TrafficEngine) isCurrentlyActive(registration string, flightNumber int) bool {
	_, exists := e.ActiveAircraft[fmt.Sprintf("%s_%d", registration, flightNumber)]
	return exists
}

func (e *D9TrafficEngine) timeDiffToDeparture(f *flightplan.ScheduledFlight) int {
	// Calculate diff at spawn time
	currSimZTime := e.atcService.GetCurrentZuluTime()
	h, m, _ := currSimZTime.Clock()
	nowMins := h*60 + m
	depMins := (f.DepatureHour * 60) + f.DepartureMin
	diff := depMins - nowMins
	return diff
}

func (e *D9TrafficEngine) spawnGroundTraffic(f *flightplan.ScheduledFlight) {

	ttd := e.timeDiffToDeparture(f)
	initialPhase, dur, delay := e.determineInitialDepaturePhase(ttd, f)
	tDur := time.Duration(math.Abs(float64(dur+delay))) * time.Second

	if initialPhase == flightphase.Unknown {
		return
	}

	airport := e.atcService.Airports[f.IcaoOrigin]
	currSimZTime := e.atcService.GetCurrentZuluTime()

	airline := e.resolveAirline(f)
	if airline == nil {
		util.LogWarnWithLabel(f.AircraftRegistration, "unable to resolve airline for flight %s %d - aircraft will not be spawned", f.AirlineName, f.Number)
		return
	}
	if airline.AirlineName != f.AirlineName {
		util.LogWarnWithLabel(f.AircraftRegistration, "airline %s reallocated to %s", f.AirlineName, airline.AirlineName)
	}

	sizeClass := e.determineSizeClass(f, airline)
	sizeClassStr := ""
	if sizeClass == "E" || sizeClass == "F" {
		sizeClassStr = "Heavy"
	}

	newAc := &atc.Aircraft{
		Registration: f.AircraftRegistration,
		SizeClass:    sizeClass,
		Flight: atc.Flight{
			Number:      f.Number,
			Origin:      f.IcaoOrigin,
			Destination: f.IcaoDest,
			Airline:     airline,
			Comms: atc.Comms{
				CountryCode: airline.CountryCode,
				Callsign:    fmt.Sprintf("%s %d %s", airline.Callsign, f.Number, sizeClassStr),
			},
			Position: atc.Position{
				Lat:  airport.Lat, // initialise to airport center, this will be updated in the first phase transition
				Long: airport.Lon,
			},
			Schedule: f,
			// Squawk random number between 1200 and 6999
			Squawk:       fmt.Sprintf("%04d", 1200+rand.IntN(5800)),
			PlanAssigned: true,
			Phase: flightphase.Phase{
				Current:                 initialPhase.Index(),
				Previous:                flightphase.Unknown.Index(),
				Transition:              currSimZTime,
				EstimatedNextTransition: currSimZTime.Add(tDur),
				TotalDuration:           tDur,
			},
			DepartureDelay: delay,
		},
	}
	e.atcService.SetFlightPhaseClass(newAc)

	if initialPhase.Index() > flightphase.Startup.Index() {
		newAc.Flight.AssignedRunway = e.AirportConfig[airport.ICAO].Departure.Name
		e.assignProcedures(newAc, airport, true)
	}

	e.ActiveAircraft[getActiveAircraftKey(newAc)] = newAc

	util.LogWithLabel(f.AircraftRegistration, "successfully spawned outbound aircraft: %s flight %d - estimated next tranistion: %v",
		f.AirlineName, f.Number, newAc.Flight.Phase.EstimatedNextTransition.Format(time.RFC3339))

}

func (e *D9TrafficEngine) spawnInboundTraffic(f *flightplan.ScheduledFlight) {

	tta := e.timeDiffToArrival(f)
	initialPhase, dur := e.determineInitialArrivalPhase(tta, f)
	tDur := time.Duration(math.Abs(float64(dur))) * time.Second

	airport := e.atcService.Airports[f.IcaoDest]

	currSimZTime := e.atcService.GetCurrentZuluTime()

	airline := e.resolveAirline(f)

	sizeClass := e.determineSizeClass(f, airline)
	sizeClassStr := ""
	if sizeClass == "E" || sizeClass == "F" {
		sizeClassStr = "Heavy"
	}

	newAc := &atc.Aircraft{
		Registration: f.AircraftRegistration,
		SizeClass:    sizeClass,
		Flight: atc.Flight{
			Number:      f.Number,
			Origin:      f.IcaoOrigin,
			Destination: f.IcaoDest,
			Airline:     airline,
			Comms: atc.Comms{
				CountryCode: airline.CountryCode,
				Callsign:    fmt.Sprintf("%s %d %s", airline.Callsign, f.Number, sizeClassStr),
			},
			Schedule: f,
			// Squawk random number between 1200 and 6999
			Squawk:       fmt.Sprintf("%04d", 1200+rand.IntN(5800)),
			PlanAssigned: true,
			Phase: flightphase.Phase{
				Current:                 initialPhase.Index(),
				Previous:                flightphase.Unknown.Index(),
				Transition:              currSimZTime,
				EstimatedNextTransition: currSimZTime.Add(tDur),
				TotalDuration:           tDur,
			},
		},
	}

	e.setInitialArrivalPosition(newAc, tta)
	e.atcService.SetFlightPhaseClass(newAc)

	if initialPhase.Index() < flightphase.TaxiIn.Index() {
		newAc.Flight.AssignedRunway = e.AirportConfig[airport.ICAO].Arrival.Name
	}

	if initialPhase.Index() == flightphase.Cruise.Index() {
		e.assignProcedures(newAc, airport, false)
	}

	e.ActiveAircraft[getActiveAircraftKey(newAc)] = newAc

	util.LogWithLabel(f.AircraftRegistration, "successfully spawned inbound aircraft: %s flight %d - estimated next transition: %v",
		f.AirlineName, f.Number, newAc.Flight.Phase.EstimatedNextTransition.Format(time.RFC3339))
}

func getActiveAircraftKey(ac *atc.Aircraft) string {
	return fmt.Sprintf("%s_%d", ac.Registration, ac.Flight.Number)
}

func (e *D9TrafficEngine) timeDiffToArrival(f *flightplan.ScheduledFlight) int {
	currSimZTime := e.atcService.GetCurrentZuluTime()
	h, m, _ := currSimZTime.Clock()

	nowMins := (h * 60) + m
	arrMins := (f.ArrivalHour * 60) + f.ArrivalMin

	diff := arrMins - nowMins

	// Handle midnight wrap-around:
	// If it's 23:55 (1435 mins) and arrival is 00:05 (5 mins)
	// diff is -1430. Adding 1440 makes it a 10-minute TTA.
	if diff < -720 {
		diff += 1440
	} else if diff > 720 {
		diff -= 1440
	}

	return diff
}

func (e *D9TrafficEngine) updateActiveAircraft(relevantICAOs []string) {

	currSimZTime := e.atcService.GetCurrentZuluTime()

	for _, ac := range e.ActiveAircraft {
		f := ac.Flight.Schedule
		if f == nil {
			continue
		}

		// We determine the current/next relevant airport based on flight class
		var airport *atc.Airport
		var targetICAO string
		if ac.Flight.Phase.Class >= flightclass.Cruising {
			targetICAO = f.IcaoDest
		} else {
			targetICAO = f.IcaoOrigin
		}
		airport = e.atcService.Airports[targetICAO]

		// if airport is not relevant to the user's current context, skip it
		if airport != nil {
			isRelevant := false
			for _, icao := range relevantICAOs {
				if icao == airport.ICAO {
					isRelevant = true
					break
				}
			}
			if !isRelevant {
				util.LogWithLabel(ac.Registration, "skipping update - target icao of %s is no longer related to the user's current context: %v",
					airport.ICAO, relevantICAOs)
				continue
			}
		} else {
			util.LogWarnWithLabel(ac.Registration, "skipping update - target icao not found", targetICAO)
			continue
		}

		switch flightphase.FlightPhase(ac.Flight.Phase.Current) {

		case flightphase.Unknown:
			diff := e.timeDiffToDeparture(f)
			initialPhase, dur, delay := e.determineInitialDepaturePhase(diff, f)
			ac.Flight.DepartureDelay = delay
			e.transitionToPhase(ac, initialPhase, (dur+delay)*60, 0)
			e.atcService.SetFlightPhaseClass(ac)

		// --- DEPARTURE FLOW ---
		case flightphase.Parked:
			if ac.Flight.Phase.Class == flightclass.PostflightParked {
				e.endFlight(ac) // Cleanup logic
				continue
			}
			if e.positionAtOriginParking(ac) == nil {
				continue
			}

			if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
				dur := (DMINUS_STARTUP_MINS - DMINUS_TAXIOUT_MINS) * 60
				e.transitionToPhase(ac, flightphase.Startup, dur, PARKED_JITTER_SECONDS)
			}

		case flightphase.Startup:
			if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
				ac.Flight.AssignedRunway = e.AirportConfig[airport.ICAO].Departure.Name
				e.assignProcedures(ac, airport, true)
				dur := (DMINUS_TAXIOUT_MINS - DMINUS_TAKEOFF_MINS) * 60
				if ac.Flight.AssignedParkingSpot != nil {
					e.releaseParking(f.IcaoOrigin, ac.Flight.AssignedParkingSpot)
				}
				e.transitionToPhase(ac, flightphase.TaxiOut, dur, TAXI_JITTER_SECONDS)
			}

		case flightphase.TaxiOut:
			// TODO: calculate position - bear in mind that we cannot move aircraft position beyond assigned access point 
			// and that transition to the Takeoff phase can be indefinitely held if the runway lock cannot be obtained.
			if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
				if !e.getRunwayLock(airport, e.AirportConfig[airport.ICAO].Departure, ac) {
					util.LogWithLabel(ac.Registration, "active departure runway %s is occupied at %s - remaining in TaxiOut phase",
						e.AirportConfig[airport.ICAO].Departure.Name, airport.ICAO)
					continue
				}
				dur := (DMINUS_TAKEOFF_MINS - DMINUS_CLIMBOUT_MINS) * 60
				e.transitionToPhase(ac, flightphase.Takeoff, dur, TAKEOFF_JITTER_SECONDS)
				// Position at runway threshold
				rwy := e.atcService.GetAirportRunway(airport.ICAO, ac.Flight.AssignedRunway)
				if rwy != nil {
					ac.Flight.Position.Lat = rwy.Lat
					ac.Flight.Position.Long = rwy.Lon
					ac.Flight.Position.Heading = rwy.Heading
					ac.Flight.Position.Altitude = math.Max(airport.Elevation, rwy.ThresholdElevation)
				} else {
					util.LogWarnWithLabel(ac.Registration,
						"unable to position aircraft at runway threshold - runway %s not found at airport %s",
						ac.Flight.AssignedRunway, airport.ICAO)
				}
			}

		case flightphase.Takeoff:
			//TODO: calculate position
			if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
				e.releaseRunwayLock(airport, e.AirportConfig[airport.ICAO].Departure, ac)
				dur := (DMINUS_CLIMBOUT_MINS - DMINUS_DEPARTURE_MINS) * 60
				e.transitionToPhase(ac, flightphase.Climbout, dur, CLIMBOUT_JITTER_SECONDS)
				// Jump 2NM out and up - TODO: remove this code as it should be handled by the Clombout phase positioning
				newLat, newLon := geometry.Project(ac.Flight.Position.Lat, ac.Flight.Position.Long, ac.Flight.Position.Heading, 2.0)
				ac.Flight.Position.Lat = newLat
				ac.Flight.Position.Long = newLon
				ac.Flight.Position.Altitude = airport.Elevation + 2500.0
			}

		case flightphase.Climbout:
			//TODO: calculate position
			if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
				dur := (DMINUS_DEPARTURE_MINS - DMINUS_CRUISE_MINS) * 60
				e.transitionToPhase(ac, flightphase.Departure, DEPARTURE_JITTER_SECONDS, dur)
			}

		case flightphase.Departure:
			//TODO: calculate position
			if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
				tta := e.timeDiffToArrival(f) // Minutes until scheduled arrival
				dur := (tta - AMINUS_ARRIVAL_MINS) * 60
				e.transitionToPhase(ac, flightphase.Cruise, CRUISE_JITTER_SECONDS, dur)
			}

		case flightphase.Cruise:
			//TODO: calculate position
			tta := e.timeDiffToArrival(f) // Minutes until scheduled arrival
			if tta <= AMINUS_ARRIVAL_MINS {
				ac.Flight.AssignedRunway = e.AirportConfig[airport.ICAO].Arrival.Name
				e.assignProcedures(ac, airport, false)
				durSecs := (AMINUS_APPROACH_MINS - AMINUS_FINAL_MINS) * 60
				e.transitionToPhase(ac, flightphase.Arrival, durSecs, ARRIVAL_JITTER_SECONDS)
				util.LogWithLabel(ac.Registration, "commencing arrival into %s (TTA: %d mins)",
					f.IcaoDest, tta)
			} else {
				util.LogWithLabel(ac.Registration, "cruising... %d minutes until arrival window",
					tta-AMINUS_APPROACH_MINS)
			}

		case flightphase.Arrival:
			e.updateInboundPosition(ac)
			if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
				qKey := normalizeRunwayKey(airport.ICAO, e.AirportConfig[airport.ICAO].Arrival)
				if len(e.RunwayQueues[qKey]) >= TRAFFIC_MANAGEMENT_RUNWAY_QUEUE_THRESHOLD {
					// send to hold
					dur := (HOLDING_MIN_DURATION_MINS * 60) + 60
					e.transitionToPhase(ac, flightphase.Holding, dur, 0)
				} else {
					// start approach
					dur := (AMINUS_APPROACH_MINS - AMINUS_FINAL_MINS) * 60
					e.transitionToPhase(ac, flightphase.Approach, dur, APPROACH_JITTER_SECONDS)
				}
			}

		case flightphase.Holding:
			//TODO: calculate position
			if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
				e.tryExitHold(ac, airport)
			}

		case flightphase.Approach:
			e.updateInboundPosition(ac) //TODO may need to move this as we might 'freeze' aircraft indefintely in this phase
			if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
				if !e.getRunwayLock(airport, e.AirportConfig[airport.ICAO].Arrival, ac) {
					util.LogWithLabel(ac.Registration, "on approach: active arrival runway %s is occupied at %s - remaining in approach phase",
						e.AirportConfig[airport.ICAO].Departure.Name, airport.ICAO)
					continue
				}
				dur := (AMINUS_FINAL_MINS - AMINUS_LAND_MINS) * 60
				e.transitionToPhase(ac, flightphase.Final, dur, FINAL_JITTER_SECONDS)
			}

		case flightphase.Final:
			e.updateInboundPosition(ac)
			if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
				if !e.getRunwayLock(airport, e.AirportConfig[airport.ICAO].Arrival, ac) {
					util.LogWithLabel(ac.Registration, "on final: active arrival runway %s is occupied at %s - initiating go-around",
						e.AirportConfig[airport.ICAO].Departure.Name, airport.ICAO)
						e.transitionToPhase(ac, flightphase.GoAround, 80, 0)
					continue
				}
				dur := (AMINUS_LAND_MINS - AMINUS_BRAKING) * 60
				e.transitionToPhase(ac, flightphase.Braking, dur, BRAKING_JITTER_SECONDS)
				ac.Flight.Position.Altitude = airport.Elevation
			}
		
		case flightphase.GoAround:
			//TODO: calculate position
			if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
				e.releaseRunwayLock(airport, e.AirportConfig[airport.ICAO].Arrival, ac)
				// Randomized hold or back into approach flow
				if rand.Float32() < GOAROUND_TO_HOLD_PROBABILITY_FACTOR {
					// send to hold
					dur := (HOLDING_MIN_DURATION_MINS * 60) + 60
					e.transitionToPhase(ac, flightphase.Holding, dur, 0)
				} else {
					// send back around to approach
					e.tryExitHold(ac, airport)
				}
			}

		case flightphase.Braking:
			e.updateInboundPosition(ac)
			if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
				e.releaseRunwayLock(airport, e.AirportConfig[airport.ICAO].Arrival, ac)
				// Search for parking during rollout
				spot := e.findAvailableParking(airport, ac.SizeClass, ac.Flight.Airline.ICAO)
				if spot != nil {
					ac.Flight.AssignedParkingSpot = spot
					ac.Flight.AssignedParkingName = spot.Name
					e.OccupiedParking[fmt.Sprintf("%s_%s", airport.ICAO, spot.Name)] = ac.Registration
					spot.IsOccupied = true
				}

				dur := (AMINUS_BRAKING - AMINUS_TAXIIN_MINS) * 60
				e.transitionToPhase(ac, flightphase.TaxiIn, dur, TAXI_JITTER_SECONDS)
			}

		case flightphase.TaxiIn:
			//TODO: calculate position
			if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
				e.positionAtDestParking(ac)
				dur := (AMINUS_TAXIIN_MINS - AMINUS_SHUTDOWN_MINS) * 60
				e.transitionToPhase(ac, flightphase.Shutdown, dur, SHUTDOWN_JITTER_SECONDS)
			}

		case flightphase.Shutdown:
			if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
				dur := (AMINUS_SHUTDOWN_MINS - AMINUS_PARKED_MINS) * 60
				e.transitionToPhase(ac, flightphase.Parked, dur, PARKED_JITTER_SECONDS)
			}
		}

		// --- LOGGING & STATE SYNC ---

		if ac.Flight.Phase.Current != ac.Flight.Phase.Previous {

			logMsg := ""
			if e.initialised {
				e.atcService.NotifyFlightPhaseChange(ac)
				logMsg = "flight %d changed phase from %s to %s. Position is lat: %0.6f, lng: %0.6f, alt: %0.6f, hdg: %d estimated next transition at %v"
			} else {
				logMsg = "flight %d silently initialised with previous phase %s and current phase %s. Position is lat: %0.6f, lng: %0.6f, alt: %0.6f, hdg: %d next transition at %v"
			}

			util.LogWithLabel(ac.Registration,
				logMsg,
				ac.Flight.Number,
				flightphase.FlightPhase(ac.Flight.Phase.Previous).String(),
				flightphase.FlightPhase(ac.Flight.Phase.Current).String(),
				ac.Flight.Position.Lat,
				ac.Flight.Position.Long,
				ac.Flight.Position.Altitude,
				int(ac.Flight.Position.Heading),
				ac.Flight.Phase.EstimatedNextTransition.Format(time.RFC3339),
			)

			// lock in phase change
			ac.Flight.Phase.Previous = ac.Flight.Phase.Current
			ac.Flight.Phase.Transition = currSimZTime

		} else {
			util.LogDebugWithLabel(ac.Registration, "flight %d remains in phase %s. Position is lat: %0.6f, lng: %0.6f, alt: %0.6f, hdg: %d estimated next transition at %v",
				ac.Flight.Number,
				flightphase.FlightPhase(ac.Flight.Phase.Current).String(),
				ac.Flight.Position.Lat,
				ac.Flight.Position.Long,
				ac.Flight.Position.Altitude,
				int(ac.Flight.Position.Heading),
				ac.Flight.Phase.EstimatedNextTransition.Format(time.RFC3339),
			)
		}
	}

	e.initialised = true
}

func (e *D9TrafficEngine) transitionToPhase(ac *atc.Aircraft, next flightphase.FlightPhase, baseSecs int, jitterSecs int) {
	currSimZTime := e.atcService.GetCurrentZuluTime()

	// Ensure we have at least a 1-second duration
	if baseSecs <= 0 {
		baseSecs = 1
	}

	// Apply jitter
	actualSecs := baseSecs
	if jitterSecs > 0 {
		actualSecs += (rand.IntN((jitterSecs*2)+1) - jitterSecs)
	}

	dur := time.Duration(actualSecs) * time.Second

	ac.Flight.Phase.Current = next.Index()
	ac.Flight.Phase.Transition = currSimZTime
	ac.Flight.Phase.TotalDuration = dur
	ac.Flight.Phase.EstimatedNextTransition = currSimZTime.Add(dur)
}

func (e *D9TrafficEngine) updateInboundPosition(ac *atc.Aircraft) {

	airport := e.atcService.Airports[ac.Flight.Destination]
	rwy := e.getAssignedRunway(airport, ac.Flight.AssignedRunway)
	currSimZTime := e.atcService.GetCurrentZuluTime()

	// 1. Calculate how much time is left vs total duration
	timeRemaining := ac.Flight.Phase.EstimatedNextTransition.Sub(currSimZTime).Seconds()
	totalDuration := ac.Flight.Phase.TotalDuration.Seconds()
	if totalDuration <= 0 {
		return
	}

	// Progress goes from 0.0 (start of phase) to 1.0 (end of phase)
	progress := 1.0 - (timeRemaining / totalDuration)
	progress = math.Max(0, math.Min(1, progress)) // Clamp 0-1

	var startDist, endDist float64
	currentPhase := flightphase.FlightPhase(ac.Flight.Phase.Current)

	switch currentPhase {
	case flightphase.Approach:
		startDist, endDist = 15.0, 4.0
	case flightphase.Final:
		startDist, endDist = 4.0, 0.01 // 0.01 is just before threshold
	case flightphase.Braking:
		// Move along the runway heading for a bit
		startDist, endDist = 0.0, -0.8
	default:
		return
	}

	// 2. Interpolate distance from threshold
	currentDist := startDist + (progress * (endDist - startDist))

	// 3. Project Lat/Lon
	// We project BACKWARDS (Heading + 180) from the runway threshold
	newLat, newLon := geometry.Project(rwy.Lat, rwy.Lon, rwy.Heading+180, currentDist)

	ac.Flight.Position.Lat = newLat
	ac.Flight.Position.Long = newLon
	ac.Flight.Position.Heading = rwy.Heading

	// 4. Interpolate Altitude (3-degree slope logic)
	// Only for airborne phases
	if currentPhase != flightphase.Braking {
		// Target 300ft per NM + airport elevation
		ac.Flight.Position.Altitude = airport.Elevation + (currentDist * 300.0)
	} else {
		ac.Flight.Position.Altitude = airport.Elevation
	}
}

func (e *D9TrafficEngine) endFlight(ac *atc.Aircraft) {
	delete(e.ActiveAircraft, getActiveAircraftKey(ac))
	if ac.Flight.AssignedParkingSpot != nil {
		e.releaseParking(ac.Flight.Destination, ac.Flight.AssignedParkingSpot)
	}
}

func (e *D9TrafficEngine) positionAtOriginParking(ac *atc.Aircraft) *atc.ParkingSpot {
	airport := e.atcService.Airports[ac.Flight.Origin]
	spot := ac.Flight.AssignedParkingSpot
	if spot == nil {
		spot = e.findAvailableParking(airport, ac.SizeClass, ac.Flight.Airline.ICAO)
		if spot == nil {
			util.LogWarnWithLabel(ac.Registration, "no suitable parking found at origin airport %s - terminating flight", airport.ICAO)
			delete(e.ActiveAircraft, getActiveAircraftKey(ac))
			//TODO consider strategy to prevent spawn re-selection, potentially delete schedule
			return nil
		} else {
			util.LogWithLabel(ac.Registration, "assigning parking at airport %s to spot %s", airport.ICAO, spot.Name)
		}
	}

	ac.Flight.Position = atc.Position{
		Lat:      spot.Lat,
		Long:     spot.Lon,
		Heading:  spot.Heading,
		Altitude: airport.Elevation,
	}
	ac.Flight.AssignedParkingSpot = spot
	ac.Flight.AssignedParkingName = spot.Name
	key := fmt.Sprintf("%s_%s", airport.ICAO, spot.Name)
	e.OccupiedParking[key] = ac.Registration
	spot.IsOccupied = true
	return spot
}

func (e *D9TrafficEngine) positionAtDestParking(ac *atc.Aircraft) *atc.ParkingSpot {
	airport := e.atcService.Airports[ac.Flight.Destination]
	spot := ac.Flight.AssignedParkingSpot
	if spot == nil {
		spot := e.findAvailableParking(airport, ac.SizeClass, ac.Flight.Airline.ICAO)
		if spot == nil {
			util.LogWarnWithLabel(ac.Registration, "no suitable parking found at airport %s - ending flight", airport.ICAO)
			e.endFlight(ac)
			return nil
		} else {
			util.LogWithLabel(ac.Registration, "assigning parking at airport %s to spot %s", airport.ICAO, spot.Name)
		}
	}
	ac.Flight.Position.Lat = spot.Lat
	ac.Flight.Position.Long = spot.Lon
	ac.Flight.Position.Heading = spot.Heading
	ac.Flight.Position.Altitude = airport.Elevation
	key := fmt.Sprintf("%s_%s", airport.ICAO, spot.Name)
	e.OccupiedParking[key] = ac.Registration
	spot.IsOccupied = true
	util.LogWithLabel(ac.Registration, "arrived at gate %s", spot.Name)
	return spot
}

func AbsDiff(a, b int) int {
    result := a - b
    if result < 0 {
        return -result
    }
    return result
}

// DetermineInitialPhase returns the initial phase of a new spawned aircraft and the estimated remaining duration
// of the phase in seconds. We add some random seconds to avoid all aircraft transitioning at the same time.
func (e *D9TrafficEngine) determineInitialDepaturePhase(diff int, f *flightplan.ScheduledFlight) (flightphase.FlightPhase, int, int) {
	delay := 0
	switch {
	// long term parked
	case diff > DMINUS_PARKED_MINS:
		//  if the departure runway is busy we add a delay based on queue length at this time - update ac.Flight.Delayed with this value
		flow, found := e.AirportConfig[f.IcaoOrigin]
		if found {
			qKey := normalizeRunwayKey(f.IcaoOrigin, flow.Departure)
			if len(e.RunwayQueues[qKey]) >= TRAFFIC_MANAGEMENT_RUNWAY_QUEUE_THRESHOLD {
				delay = len(e.RunwayQueues[qKey]) * TRAFFIC_MANAGEMENT_PER_AIRCRAFT_DELAY_SECONDS
				util.LogWithLabel(f.AircraftRegistration, "initial departure delay of %d seconds applied based on current traffic queue of %d for runway %s at %s",
					delay, len(e.RunwayQueues[qKey]), e.AirportConfig[f.IcaoOrigin].Departure.Name, f.IcaoOrigin)
			}
		} else {
			util.LogWarnWithLabel(f.AircraftRegistration, "unable to determine initial departure phase due to missing airport flow for %s", f.IcaoOrigin)
		}
		estimatedDuration := (AbsDiff(diff, DMINUS_STARTUP_MINS) * 60) + (rand.IntN((PARKED_JITTER_SECONDS*2)+1) - PARKED_JITTER_SECONDS)
		return flightphase.Parked, estimatedDuration, delay

	// still parked but tracking towards startup
	case diff > DMINUS_STARTUP_MINS && diff <= DMINUS_PARKED_MINS:
		estimatedDuration := (AbsDiff(diff, DMINUS_STARTUP_MINS) * 60) + (rand.IntN((PARKED_JITTER_SECONDS*2)+1) - PARKED_JITTER_SECONDS)
		return flightphase.Parked, estimatedDuration, delay

	// startup
	case diff > DMINUS_TAXIOUT_MINS && diff <= DMINUS_STARTUP_MINS:
		estimatedDuration := (AbsDiff(diff, DMINUS_TAXIOUT_MINS) * 60) + (rand.IntN((STARTUP_JITTER_SECONDS*2)+1) - STARTUP_JITTER_SECONDS)
		return flightphase.Startup, estimatedDuration, delay

	// taxi out
	case diff > DMINUS_TAKEOFF_MINS && diff <= DMINUS_TAXIOUT_MINS:
		estimatedDuration := (AbsDiff(diff, DMINUS_TAKEOFF_MINS) * 60) + (rand.IntN((TAXI_JITTER_SECONDS*2)+1) - TAXI_JITTER_SECONDS)
		return flightphase.TaxiOut, estimatedDuration, delay

	// takeoff - we do not permit initial spawn in takeoff phase due to runway lock charge so will be initialised in taxi out phase
	case diff >= DMINUS_CLIMBOUT_MINS && diff <= DMINUS_TAKEOFF_MINS:
		estimatedDuration := (AbsDiff(DMINUS_TAXIOUT_MINS, DMINUS_TAKEOFF_MINS) * 60) + (rand.IntN((TAXI_JITTER_SECONDS*2)+1) - TAXI_JITTER_SECONDS)
		return flightphase.TaxiOut, estimatedDuration, delay

	// climbout
	case diff >= DMINUS_DEPARTURE_MINS && diff <= DMINUS_CLIMBOUT_MINS:
		estimatedDuration := (AbsDiff(diff, DMINUS_CLIMBOUT_MINS) * 60) + (rand.IntN((CLIMBOUT_JITTER_SECONDS*2)+1) - CLIMBOUT_JITTER_SECONDS)
		return flightphase.Climbout, estimatedDuration, delay

	// departure
	case diff >= DMINUS_CRUISE_MINS && diff <= DMINUS_DEPARTURE_MINS:
		estimatedDuration := (AbsDiff(diff, DMINUS_CLIMBOUT_MINS) * 60) + (rand.IntN((DEPARTURE_JITTER_SECONDS*2)+1) - DEPARTURE_JITTER_SECONDS)
		return flightphase.Departure, estimatedDuration, delay

	default:
		// cruise
		tta := e.timeDiffToArrival(f)
		remainingCruise := (AbsDiff(tta, AMINUS_APPROACH_MINS) * 60) + (rand.IntN((CRUISE_JITTER_SECONDS*2)+1) - CRUISE_JITTER_SECONDS)
		return flightphase.Cruise, int(math.Max(0, float64(remainingCruise))), delay
	}
}

func (e *D9TrafficEngine) determineInitialArrivalPhase(diff int, f *flightplan.ScheduledFlight) (flightphase.FlightPhase, int) {

	switch {
	// ARRIVAL:
	case diff > AMINUS_APPROACH_MINS && diff <= AMINUS_ARRIVAL_MINS:
		estimatedDuration := (AbsDiff(diff, AMINUS_APPROACH_MINS) * 60) + (rand.IntN((ARRIVAL_JITTER_SECONDS*2)+1) - ARRIVAL_JITTER_SECONDS)
		return flightphase.Approach, estimatedDuration

	// APPROACH:
	case diff > AMINUS_FINAL_MINS && diff <= AMINUS_APPROACH_MINS:
		estimatedDuration := (AbsDiff(diff, AMINUS_FINAL_MINS) * 60) + (rand.IntN((APPROACH_JITTER_SECONDS*2)+1) - APPROACH_JITTER_SECONDS)
		return flightphase.Approach, estimatedDuration

	// FINAL: we do not permit initial spawn in final phase due to runway lock charge so will be initialised in approach phase
	case diff > AMINUS_LAND_MINS && diff <= AMINUS_FINAL_MINS:
		estimatedDuration := (AbsDiff(AMINUS_APPROACH_MINS, AMINUS_LAND_MINS) * 60) + (rand.IntN((APPROACH_JITTER_SECONDS*2)+1) - APPROACH_JITTER_SECONDS)
		return flightphase.Approach, estimatedDuration

	// BRAKING: we do not permit initial spawn in braking phase due to runway lock charge so will be initialised in approach out phase
	case diff > AMINUS_BRAKING && diff <= AMINUS_LAND_MINS:
		estimatedDuration := (AbsDiff(AMINUS_APPROACH_MINS, AMINUS_LAND_MINS) * 60) + (rand.IntN((APPROACH_JITTER_SECONDS*2)+1) - APPROACH_JITTER_SECONDS)
		return flightphase.Approach, estimatedDuration

	// TAXI IN:
	case diff > AMINUS_TAXIIN_MINS && diff <= AMINUS_BRAKING:
		// We use TAXIOUT jitter here as a proxy for movement variance
		estimatedDuration := (AbsDiff(diff, AMINUS_TAXIIN_MINS) * 60) + (rand.IntN((TAXI_JITTER_SECONDS*2)+1) - TAXI_JITTER_SECONDS)
		return flightphase.TaxiIn, estimatedDuration

	// SHUTDOWN:
	case diff > AMINUS_SHUTDOWN_MINS && diff <= AMINUS_TAXIIN_MINS:
		estimatedDuration := (AbsDiff(diff, AMINUS_SHUTDOWN_MINS) * 60) + (rand.IntN((SHUTDOWN_JITTER_SECONDS*2)+1) - SHUTDOWN_JITTER_SECONDS)
		return flightphase.Shutdown, estimatedDuration

	// PARKED:
	case diff >= AMINUS_PARKED_MINS && diff <= AMINUS_SHUTDOWN_MINS:
		estimatedDuration := (AbsDiff(diff, AMINUS_PARKED_MINS) * 60) + (rand.IntN((PARKED_JITTER_SECONDS*2)+1) - PARKED_JITTER_SECONDS)
		return flightphase.Parked, estimatedDuration

	// DEFAULT:
	default:
		tta := e.timeDiffToArrival(f)
		remainingCruise := (AbsDiff(tta, AMINUS_ARRIVAL_MINS) * 60) + (rand.IntN((CRUISE_JITTER_SECONDS*2)+1) - CRUISE_JITTER_SECONDS)
		return flightphase.Cruise, int(math.Max(0, float64(remainingCruise)))
	}
}

func (e *D9TrafficEngine) setInitialArrivalPosition(ac *atc.Aircraft, tta int) {
	airport := e.atcService.Airports[ac.Flight.Destination]
	rwy := e.getAssignedRunway(airport, ac.Flight.AssignedRunway)
	phase := flightphase.FlightPhase(ac.Flight.Phase.Current)

	var distance float64
	switch phase {
	case flightphase.Cruise:
		distance = float64(tta) * 4.0 // ~240kts ground speed
	case flightphase.Approach:
		distance = float64(tta) * 3.0 // ~180kts ground speed
	case flightphase.Final:
		distance = float64(tta) * 2.5 // ~150kts ground speed
	case flightphase.Braking, flightphase.TaxiIn:
		distance = 0.1 // On the runway
	}

	// Project backward from the runway threshold
	lat, lon := geometry.Project(rwy.Lat, rwy.Lon, rwy.Heading+180, distance)

	ac.Flight.Position.Lat = lat
	ac.Flight.Position.Long = lon
	ac.Flight.Position.Heading = rwy.Heading
	ac.Flight.Position.Altitude = airport.Elevation + (distance * 300)
}

func (e *D9TrafficEngine) tryExitHold(ac *atc.Aircraft, ap *atc.Airport) {
	qKey := normalizeRunwayKey(ap.ICAO, e.AirportConfig[ap.ICAO].Arrival)
	if len(e.RunwayQueues[qKey]) < TRAFFIC_MANAGEMENT_RUNWAY_QUEUE_THRESHOLD {
		// exit hold
		dur := ((AMINUS_APPROACH_MINS - AMINUS_FINAL_MINS) * 60) + 60
		e.transitionToPhase(ac, flightphase.Approach, dur, APPROACH_JITTER_SECONDS)
	} else {
		// continue in hold
		dur := HOLDING_MIN_DURATION_MINS * 60 * time.Second
		ac.Flight.Phase.EstimatedNextTransition = e.atcService.GetCurrentZuluTime().Add(dur)
		util.LogWithLabel(ac.Registration, "continue hold - traffic for runway %s at %s is high - estimated hold exit %v",
			qKey, ap.ICAO, ac.Flight.Phase.EstimatedNextTransition)
	}
}

func (e *D9TrafficEngine) findAvailableParking(airport *atc.Airport, reqClass string, airlineICAO string) *atc.ParkingSpot {
	// In v2, the top-level functions are automatically seeded and 
	// more performant than creating a new local generator.

	for pass := 0; pass < 2; pass++ {
		var candidates []*atc.ParkingSpot

		for i := range airport.Parking {
			spot := airport.Parking[i]

			// 1. Physical constraint
			if spot.WidthClass < reqClass {
				continue
			}

			// 2. Occupancy check
			key := fmt.Sprintf("%s_%s", airport.ICAO, spot.Name)
			if _, occupied := e.OccupiedParking[key]; occupied {
				continue
			}

			// 3. User proximity check
			if e.atcService.UserState.NearestAirport.ICAO == airport.ICAO &&
				e.atcService.UserState.AssignedParking.Name == spot.Name {
				continue
			}

			// 4. Airline Preference (Pass 0 only)
			if pass == 0 && airlineICAO != "" {
				if !strings.Contains(spot.AirlineCodes, airlineICAO) {
					continue
				}
			}

			candidates = append(candidates, spot)
		}

		// 5. Randomized selection from the candidate pool
		if len(candidates) > 0 {
			// In rand/v2, we use N(len) for a type-safe integer range
			return candidates[rand.N(len(candidates))]
		}
	}

	return nil
}

func (e *D9TrafficEngine) releaseParking(icao string, spot *atc.ParkingSpot) {
	spot.IsOccupied = false
	key := fmt.Sprintf("%s_%s", icao, spot.Name)
	delete(e.OccupiedParking, key)
	util.LogWithLabel("D9TRAFFIC", "Parking spot %s at %s is now vacant.", spot.Name, icao)
}

func (e *D9TrafficEngine) refreshRunwayConfig(ap *atc.Airport) {

	weather := e.atcService.GetWeatherState()

	// 1. Get the primary runway using the smart UTILITY score
	var primaryRwy *atc.Runway
	var fallbackRwy *atc.Runway
	highestScore := -1000.0

	for _, rwy := range ap.Runways {
		score := e.getRunwayUtilityScore(rwy, weather.Wind.Direction, weather.Wind.Speed)
		if score > highestScore {
			highestScore = score
			primaryRwy = rwy
		} else {
			fallbackRwy = rwy
		}
	}

	if primaryRwy == nil {
		if fallbackRwy != nil {
			util.LogWarnWithLabel("D9TRAFFIC", "unable to determine active runway for airport %s"+
				" fallback to %s for arrivals and departures",
				ap.ICAO, fallbackRwy.Name)
			e.AirportConfig[ap.ICAO] = ActiveRunwaySet{
				Arrival:       fallbackRwy,
				Departure:     fallbackRwy,
				LastWindSpeed: weather.Wind.Speed,
				LastWindDir:   weather.Wind.Direction,
			}
		} else {
			util.LogErrWithLabel("D9TRAFFIC", "unable to determine active runway for airport %s and no fallback available"+
				" - skipping runway config update which is likely to cause fatal errors in application", ap.ICAO)
		}
		return
	}

	// 2. Orientation Logic -  handle parallel runways
	activeOrientation := int(math.Round(primaryRwy.Heading / 10.0))
	viable := e.getViableRunways(ap)
	orientations := e.groupByOrientation(viable)
	candidates := orientations[activeOrientation]

	currArrRwy := e.AirportConfig[ap.ICAO].Arrival
	currDepRwy := e.AirportConfig[ap.ICAO].Departure

	// 3. Pair Identification (Outboard/Inboard Logic)
	if len(candidates) >= 2 {

		// Sort by Latitude to determine outboard vs inboard (assuming north-up data)
		sort.Slice(candidates, func(i, j int) bool {
			return candidates[i].Lat > candidates[j].Lat
		})

		// Standard Hub Logic: 0 is Outboard (Arrival), 1 is Inboard (Departure)
		e.AirportConfig[ap.ICAO] = ActiveRunwaySet{
			Arrival:       candidates[0],
			Departure:     candidates[len(candidates)-1],
			LastWindSpeed: weather.Wind.Speed,
			LastWindDir:   weather.Wind.Direction,
		}
		util.LogWithLabel("D9TRAFFIC", "%s runway config update: aircraft arriving %s and departing %s",
			ap.ICAO, candidates[0].Name, candidates[len(candidates)-1].Name)
	} else {
		e.AirportConfig[ap.ICAO] = ActiveRunwaySet{
			Arrival:       primaryRwy,
			Departure:     primaryRwy,
			LastWindSpeed: weather.Wind.Speed,
			LastWindDir:   weather.Wind.Direction,
		}
		util.LogWithLabel("D9TRAFFIC", "%s runway config update: aircraft arriving and departing %s",
			ap.ICAO, primaryRwy.Name)
	}

	// 4. Runway Queue Cleanup: If the active runway(s) have changed, we need clear the queues.
	if currArrRwy != nil && currArrRwy.Name != e.AirportConfig[ap.ICAO].Arrival.Name {
		currLockKey := normalizeRunwayKey(ap.ICAO, currArrRwy)
		delete(e.RunwayQueues, currLockKey)
	}
	if currDepRwy != nil && currDepRwy.Name != e.AirportConfig[ap.ICAO].Departure.Name {
		currLockKey := normalizeRunwayKey(ap.ICAO, currDepRwy)
		delete(e.RunwayQueues, currLockKey)
	}

}

func (e *D9TrafficEngine) getViableRunways(ap *atc.Airport) []*atc.Runway {
	viable := []*atc.Runway{}
	for _, rwy := range ap.Runways {
		// Only consider runways longer than 5000ft (approx 1500m)
		// and avoid water/heliport surfaces if your data includes them
		if rwy.Length >= 5000 {
			viable = append(viable, rwy)
		}
	}
	return viable
}

func (e *D9TrafficEngine) groupByOrientation(runways []*atc.Runway) map[int][]*atc.Runway {
	groups := make(map[int][]*atc.Runway)
	for _, r := range runways {
		// Use the "Tens" digit (e.g., 274 degrees becomes 27)
		orientation := int(math.Round(r.Heading / 10.0))
		groups[orientation] = append(groups[orientation], r)
	}
	return groups
}

func (e *D9TrafficEngine) determineSizeClass(f *flightplan.ScheduledFlight, info *atc.AirlineInfo) string {
	// 1. Calculate the Distance Baseline
	distNM := e.calculateFlightDistance(f.IcaoOrigin, f.IcaoDest)

	// 2. Initial estimate based on distance
	size := "C"
	switch {
	case distNM < 450:
		size = "B"
	case distNM > 2800:
		size = "E" // Heavy
	}

	// 3. Apply Tier Constraints
	if info != nil {
		switch info.Tier {
		case "international":
			// Flag carriers can be anything, keep distance estimate
		case "budget":
			// Budget airlines almost never fly Heavies (E/F)
			// Even if the distance is long, cap it at 'C'
			if size == "E" || size == "F" {
				size = "C"
			}
		case "regional":
			// Regional airlines are capped at 'B' or 'C'
			if size == "E" || size == "F" {
				size = "B"
			}
		}
	}

	// 4. Final Physical Check
	// If the origin airport doesn't even have an 'E' gate,
	// we must downgrade to the largest available.
	return e.clampSizeToAirportCapability(f.IcaoOrigin, size)
}

func (e *D9TrafficEngine) clampSizeToAirportCapability(icao string, estimatedSize string) string {
	ap, ok := e.atcService.Airports[icao]
	if !ok {
		return estimatedSize
	}

	// Find the largest gate available at this airport
	maxClass := "A"
	for _, spot := range ap.Parking {
		if spot.WidthClass > maxClass {
			maxClass = spot.WidthClass
		}
	}

	// If our estimated size is bigger than the biggest gate, downgrade it
	if estimatedSize > maxClass {
		return maxClass
	}

	return estimatedSize
}

func (e *D9TrafficEngine) resolveAirline(f *flightplan.ScheduledFlight) *atc.AirlineInfo {
	// 1. Direct Match: The most efficient path.
	if info := e.atcService.GetAirlineByName(f.AirlineName); info != nil {
		return info
	}

	// --- FALLBACKS ---
	// At this point, we know we don't have a name match.
	// We will now find a code and immediately return its full info struct.
	// 2. Matching Pairs (Airlines at both ends)
	util.LogWarnWithLabel(f.AircraftRegistration, "airline %s not found - allocating by orign/destination gate pairing logic", f.AirlineName)
	origin := e.atcService.Airports[f.IcaoOrigin]
	dest := e.atcService.Airports[f.IcaoDest]
	if origin != nil && dest != nil {
		if code := getWeightedCommonAirline(origin, dest); code != "" {
			airline := e.atcService.GetAirlineByCode(code)
			if airline != nil {
				return airline
			}
		}
	}

	// 3. Origin Hub Weighted Selection
	util.LogWarnWithLabel(f.AircraftRegistration, "allocating airline by origin gate logic")
	if origin != nil && len(origin.HubWeights) > 0 {
		if code := getWeightedRandomAirline(origin.HubWeights); code != "" {
			airline := e.atcService.GetAirlineByCode(code)
			if airline != nil {
				return airline
			}
		}
	}

	// 4. Registration Country Fallback
	util.LogWarnWithLabel(f.AircraftRegistration, "allocating airline by country of registration logic")
	countryCode := e.atcService.GetCountryFromRegistration(f.AircraftRegistration)
	if countryCode == "" {
		countryCode = e.atcService.Config.ATC.AirlineCountryCodeFallback
	}

	if countryCode != "" {
		code := e.atcService.GetRandomAirlineByCountry(countryCode)
		if code != "" {
			return e.atcService.GetAirlineByCode(code)
		}
	}

	util.LogWarnWithLabel(f.AircraftRegistration, "unable to resolve airline, defaulting to BAW")
	return &atc.AirlineInfo{
		ICAO:        "UNK",
		AirlineName: "British Airways",
		Callsign:    "SPEEDBIRD",
		CountryCode: "GB",
		Tier:        "international",
	}
}

func (e *D9TrafficEngine) calculateFlightDistance(originICAO, destICAO string) float64 {
	origin, okO := e.atcService.Airports[originICAO]
	dest, okD := e.atcService.Airports[destICAO]

	// If we don't have coordinate data for both airports,
	// return a medium distance as a safe fallback for the size heuristic.
	if !okO || !okD {
		return 500.0
	}

	// Convert degrees to radians
	lat1 := origin.Lat * math.Pi / 180
	lon1 := origin.Lon * math.Pi / 180
	lat2 := dest.Lat * math.Pi / 180
	lon2 := dest.Lon * math.Pi / 180

	// Haversine formula
	diffLat := lat2 - lat1
	diffLon := lon2 - lon1

	a := math.Sin(diffLat/2)*math.Sin(diffLat/2) +
		math.Cos(lat1)*math.Cos(lat2)*
			math.Sin(diffLon/2)*math.Sin(diffLon/2)

	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))

	// Earth's radius in Nautical Miles is approximately 3440.065
	const earthRadiusNM = 3440.065
	return earthRadiusNM * c
}

func (e *D9TrafficEngine) getAssignedRunway(ap *atc.Airport, name string) *atc.Runway {
	if ap == nil {
		return nil
	}

	for _, rwy := range ap.Runways {
		if rwy.Name == name {
			return rwy
		}
	}

	// Fallback: If for some reason the name doesn't match, return the first available runway
	for _, rwy := range ap.Runways {
		return rwy
	}

	return nil
}

func (e *D9TrafficEngine) getRunwayUtilityScore(rwy *atc.Runway, windDir float64, windSpeed float64) float64 {
	// 1. Start with the "Static" score (Length and Procedures)
	score := float64(len(rwy.SIDs)*10 + len(rwy.STARs)*10)
	score += rwy.Length / 1000.0

	// 2. Add the "Dynamic" Weather Component
	// Calculate the angular difference between wind and runway heading
	diff := windDir - rwy.Heading
	radDiff := diff * math.Pi / 180.0

	// headwindComponent: 1.0 = Direct Headwind, -1.0 = Direct Tailwind
	headwindComponent := math.Cos(radDiff)

	// 3. Weight the wind heavily
	// We multiply the component by wind speed.
	// A 20kt tailwind should almost always disqualify a runway (-20 points)
	// A 20kt headwind should make it very attractive (+20 points)
	score += (headwindComponent * windSpeed)

	// 4. Bonus for Precision (ILS/RNAV)
	if rwy.HighestPrecisionApproach != "" {
		score += 50.0
	}

	return score
}

func (e *D9TrafficEngine) assignProcedures(ac *atc.Aircraft, airport *atc.Airport, isDeparture bool) {
	config := e.AirportConfig[airport.ICAO]

	if isDeparture {
		//SID assignment
		destAirport := e.atcService.GetAirport(ac.Flight.Destination)
		if destAirport == nil {
			util.LogWarnWithLabel(ac.Registration, "destination airport %s not found - unable to assign SID", ac.Flight.Destination)
			return
		}

		// Calculate the bearing from the airport to the destination
		bearingToTarget := geometry.CalculateBearing(airport.Lat, airport.Lon, destAirport.Lat, destAirport.Lon)

		targetRwy := config.Departure
		var bestSID *atc.Procedure
		minDiff := 360.0

		for i := range targetRwy.SIDs {
			sid := targetRwy.SIDs[i]
			// For a SID, we look at the EXIT fix (where the plane enters the enroute structure)
			sidBearing := geometry.CalculateBearing(airport.Lat, airport.Lon, sid.Exit.Fix.LatRad*180/math.Pi, sid.Exit.Fix.LonRad*180/math.Pi)

			diff := math.Abs(geometry.BearingDiff(bearingToTarget, sidBearing))
			if diff < minDiff {
				minDiff = diff
				bestSID = sid
			}
		}

		if bestSID != nil {
			ac.Flight.AssignedSID = bestSID
			util.LogWithLabel(ac.Registration, "assigned %s SID", bestSID.Name)
		}

	} else {
		// STAR assignment
		targetRwy := config.Arrival
		// 30% probability of STAR assignment to allow for vectoring as alternative
		if rand.Float32() < STAR_PROBABILITY_FACTOR && len(targetRwy.STARs) > 0 {
			var bestSTAR *atc.Procedure
			minDiff := 360.0

			origAirport := e.atcService.GetAirport(ac.Flight.Origin)
			if origAirport == nil {
				util.LogWarnWithLabel(ac.Registration, "origin airport %s not found - unable to assign STAR", ac.Flight.Origin)
				return
			}

			// Calculate the bearing from the origin to the destination
			bearingToTarget := geometry.CalculateBearing(origAirport.Lat, origAirport.Lon, airport.Lat, airport.Lon)

			for i := range targetRwy.STARs {
				star := targetRwy.STARs[i]
				// For a STAR, we look at the ENTRY fix (where the plane starts the arrival)
				starBearing := geometry.CalculateBearing(airport.Lat, airport.Lon, star.Entry.Fix.LatRad*180/math.Pi, star.Entry.Fix.LonRad*180/math.Pi)

				diff := math.Abs(geometry.BearingDiff(bearingToTarget, starBearing))
				if diff < minDiff {
					minDiff = diff
					bestSTAR = star
				}
			}

			if bestSTAR != nil {
				ac.Flight.AssignedSTAR = bestSTAR
				util.LogWithLabel(ac.Registration, "assigned %s STAR", bestSTAR.Name)
			}
		} else {
			util.LogWithLabel(ac.Registration, "no arrival procedure assigned - aircraft will be vectored to runway by ATC")
		}
	}
}

// getRunwayLock attempts to acquire a lock on the runway for the given aircraft.
// returns true if the lock was successfully acquired, or false if the runway is already locked by another aircraft.
// If the runway is currently locked by the same aircraft, it will return true to allow them to maintain their lock.
// If the runway is currently unlocked, it will be locked for the requesting aircraft with the current timestamp.
func (e *D9TrafficEngine) getRunwayLock(ap *atc.Airport, rwy *atc.Runway, ac *atc.Aircraft) bool {

	rwyLockKey := normalizeRunwayKey(ap.ICAO, rwy)

	if e.atcService.UserHasRunwayClearance(rwy) {
		e.addToQueue(rwyLockKey, ac.Registration)
		return false
	}

	lock, locked := e.RunwayLocks[rwyLockKey]
	if locked {
		if lock.OccupiedBy.Registration == ac.Registration {
			return true // Already locked by this aircraft
		}
		if lock.OccupiedSince.Add(RUNWAY_LOCK_TIMEOUT_SECONDS * time.Second).Before(e.atcService.GetCurrentZuluTime()) {
			// Lock has expired, allow new lock - set to false and fall through to acquire
			locked = false
			util.LogWarnWithLabel(ac.Registration, "runway lock for %s at %s has expired, overriding previous lock held by %s", rwy.Name, ap.ICAO, lock.OccupiedBy.Registration)
		}
	}
	if !locked {
		// acquire lock on runway
		e.RunwayLocks[rwyLockKey] = &RunwayLock{
			OccupiedBy:    ac,
			OccupiedSince: e.atcService.GetCurrentZuluTime(),
		}
		// we got the lock, so no longer queuing for the runway, remove queue entry
		e.removeFromQueue(rwyLockKey, ac.Registration)
		util.LogWithLabel(ac.Registration, "acquired lock on runway %s at %s", rwy.Name, ap.ICAO)
		return true
	}

	// did not obtain lock
	e.addToQueue(rwyLockKey, ac.Registration)
	return false
}

// releaseRunwayLock releases the lock on the runway if it is currently held by the given aircraft.
func (e *D9TrafficEngine) releaseRunwayLock(ap *atc.Airport, rwy *atc.Runway, ac *atc.Aircraft) {
	rwyLockKey := normalizeRunwayKey(ap.ICAO, rwy)
	lock, lockExists := e.RunwayLocks[rwyLockKey]
	if lockExists && lock.OccupiedBy.Registration == ac.Registration {
		delete(e.RunwayLocks, rwyLockKey)
		util.LogWithLabel(ac.Registration, "released lock on runway %s at %s", rwy.Name, ap.ICAO)
	}
}

func (e *D9TrafficEngine) addToQueue(lockKey string, reg string) {
	if e.RunwayQueues[lockKey] == nil {
		e.RunwayQueues[lockKey] = make(map[string]time.Time)
	}
	// Only add if not already present to preserve the original wait time
	if _, exists := e.RunwayQueues[lockKey][reg]; !exists {
		e.RunwayQueues[lockKey][reg] = time.Now()
		util.LogWithLabel(reg, "queued for runway %s queue length is %d", lockKey, len(e.RunwayQueues[lockKey]))
	}
}

func (e *D9TrafficEngine) removeFromQueue(lockKey string, reg string) {
	if e.RunwayQueues[lockKey] != nil {
		delete(e.RunwayQueues[lockKey], reg)
	}
	util.LogWithLabel(reg, "dequeued from runway %s queue length is %d", lockKey, len(e.RunwayQueues[lockKey]))
}

func getWeightedCommonAirline(origin, dest *atc.Airport) string {
	// 1. Find airlines that exist in BOTH hub weight maps
	commonWeights := make(map[string]float64)

	for code, originWeight := range origin.HubWeights {
		if destWeight, exists := dest.HubWeights[code]; exists {
			// We average the weights to find a "mutual" probability.
			// If BA is 80% at LHR and 10% at JFK, their mutual weight is 45%.
			commonWeights[code] = (originWeight + destWeight) / 2.0
		}
	}

	// 2. If no common airlines found, return empty so the cascade continues
	if len(commonWeights) == 0 {
		return ""
	}

	// 3. Use the Weighted Random selector we wrote previously
	return getWeightedRandomAirline(commonWeights)
}

func getWeightedRandomAirline(weights map[string]float64) string {
	if len(weights) == 0 {
		return ""
	}

	// 1. Calculate the total sum of weights
	var totalWeight float64
	for _, w := range weights {
		totalWeight += w
	}

	// 2. Pick a random number in the range [0.0, totalWeight)
	r := rand.Float64() * totalWeight

	// 3. Iterate and subtract until we find the winner
	var cumulative float64
	for code, weight := range weights {
		cumulative += weight
		if r <= cumulative {
			return code
		}
	}

	// Fallback to a random key if logic fails
	for code := range weights {
		return code
	}
	return ""
}

// NormalizeRunwayKey creates a consistent ID for the physical concrete
func normalizeRunwayKey(icao string, rwy *atc.Runway) string {
	recip := getReciprocalName(rwy.Name)
	// Sort them so the key is always the same regardless of which end we use
	if rwy.Name < recip {
		return fmt.Sprintf("%s-%s-%s", icao, rwy.Name, recip)
	}
	return fmt.Sprintf("%s-%s-%s", icao, recip, rwy.Name)
}

func getReciprocalName(name string) string {
	// 1. Separate numbers from letters
	var numPart int
	var letterPart string
	fmt.Sscanf(name, "%d%s", &numPart, &letterPart)

	// 2. Flip the number
	recipNum := numPart + 18
	if recipNum > 36 {
		recipNum -= 18
	}

	// 3. Flip the letter
	recipLetter := letterPart
	switch letterPart {
	case "L":
		recipLetter = "R"
	case "R":
		recipLetter = "L"
	}

	return fmt.Sprintf("%02d%s", recipNum, recipLetter)
}

// TODO: call at runtime
func getBestPath(rwy *atc.Runway, spot *atc.ParkingSpot) string {
    var bestTaxiName string
    minDistToGate := math.MaxFloat64

    // We iterate over the map of "Tied" or "Close" taxiway entries
    for name, access := range rwy.DepartureAccess {
        // Which of these qualified entries is closest to our PARKED position?
        dist := geometry.DistNM(spot.Lat, spot.Lon, access.Coord.Lat, access.Coord.Lon)
        if dist < minDistToGate {
            minDistToGate = dist
            bestTaxiName = name
        }
    }
    return bestTaxiName // Returns Alpha for North gates, Bravo for South gates
}
