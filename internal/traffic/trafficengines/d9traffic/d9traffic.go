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
	AMINUS_FINAL_MINS    = 1
	AMINUS_LAND_MINS     = 0
	AMINUS_BRAKING       = -1
	AMINUS_TAXIIN_MINS   = -2
	AMINUS_SHUTDOWN_MINS = -12
	AMINUS_PARKED_MINS   = -15

	// allowable time variance (minutes) in phase duration. example: Parked jitter of 240 means that the parked phase duration
	// can be reduced or increased by up to half of this time i.e. 120 seconds
	PARKED_JITTER_SECONDS    = 120
	STARTUP_JITTER_SECONDS   = 120
	CLIMBOUT_JITTER_SECONDS  = 40
	DEPARTURE_JITTER_SECONDS = 120
	CRUISE_JITTER_SECONDS    = 120
	ARRIVAL_JITTER_SECONDS   = 120
	APPROACH_JITTER_SECONDS  = 60

	SHUTDOWN_JITTER_SECONDS = 120

	RUNWAY_LOCK_TIMEOUT_SECONDS = 300 // Safety mechanism in case aircraft does not voluntarily release the lock

	HOLDING_MIN_DURATION_MINS                     = 4
	TRAFFIC_MANAGEMENT_RUNWAY_QUEUE_THRESHOLD     = 10
	TRAFFIC_MANAGEMENT_PER_AIRCRAFT_DELAY_SECONDS = 90
	STAR_PROBABILITY_FACTOR                       = 0.3
	GOAROUND_TO_HOLD_PROBABILITY_FACTOR           = 0.3

	DEPARTURE_CONTEXT = 0
	ARRIVAL_CONTEXT   = 1
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
	var lastSpawnMin int = -1 // Track the last minute we checked for spawns

	go func() {
		for range ticker.C {
			start := time.Now()
			currSimZTime := e.atcService.GetCurrentZuluTime()

			// Time components
			day := int(currSimZTime.Weekday())
			hour := currSimZTime.Hour()
			currentMin := currSimZTime.Minute()

			relevantICAOs := e.getRelevantICAOs()

			// --- 1. SLOW CYCLE (Once per Minute) ---
			// Only check for new spawns and runway refreshes if the minute has rolled over
			if currentMin != lastSpawnMin {
				for _, icao := range relevantICAOs {
					ap := e.atcService.GetAirport(icao)
					if ap == nil {
						continue
					}

					if e.needsRunwayRefresh(ap) {
						e.refreshRunwayConfig(ap)
					}
					e.checkForDepartureSpawns(icao, day, hour, currentMin)
					e.checkForArrivalSpawns(icao, day, hour, currentMin)
				}
				lastSpawnMin = currentMin
			}

			// --- 2. FAST CYCLE (Every 10 Seconds) ---
			// Existing aircraft MUST move frequently to avoid "stepping" or "teleporting"
			e.updateActiveAircraft(relevantICAOs)

			util.LogWithLabel("D9TRAFFIC", "update cycle duration: %v, total active aircraft: %d",
				time.Since(start), len(e.ActiveAircraft))
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
					e.spawnDepartureTraffic(&f)
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
				e.spawnArrivalTraffic(&f)
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

func (e *D9TrafficEngine) spawnDepartureTraffic(f *flightplan.ScheduledFlight) {

	ttd := e.timeDiffToDeparture(f)
	initialPhase, remainingDurSecs, fullDurationSecs, delay := e.determineInitialDeparturePhase(ttd, f)
	if initialPhase == flightphase.Unknown {
		return
	}
	ip := initialPhase.Index()

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

	// If the phase usually takes 600s (full) and we have 200s left (dur), we've been in it for 400s.
	elapsedOffset := math.Max(0, float64(fullDurationSecs)-float64(remainingDurSecs))
	// backdate transition time
	transitionTime := currSimZTime.Add(-time.Duration(elapsedOffset) * time.Second)

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
				Current:  ip,
				Previous: flightphase.Unknown.Index(),
			},
			DepartureDelay: delay,
		},
	}

	// Set all pre-requisite states
	e.atcService.SetFlightPhaseClass(newAc)
	if ip < flightphase.Takeoff.Index() {
		// assign departure gate - do this BEFORE assigning the departure runway access as this may influence the selected access point
		e.assignParking(newAc, airport)
	}
	if ip > flightphase.Startup.Index() {
		// assign departure runway
		newAc.Flight.AssignedRunway = e.AirportConfig[airport.ICAO].Departure.Name
		// assign SID for departure
		e.assignProcedures(newAc, airport, true)
		if ip == flightphase.TaxiOut.Index() {
			// assign departure runway access
			e.assignRunwayAccessPoint(newAc, airport, DEPARTURE_CONTEXT)
		}
	}

	newAc.Flight.Phase.Transition = transitionTime // BACKDATED
	newAc.Flight.Phase.EstimatedNextTransition = currSimZTime.Add(time.Duration(remainingDurSecs) * time.Second)
	newAc.Flight.Phase.TotalDuration = time.Duration(fullDurationSecs) * time.Second
	// set initial altitude
	newAc.Flight.Phase.StartAltitude = e.estimatePhaseInitialAltitude(newAc, ip)

	// Determine which positioning functions should handle the initial placement
	if ip >= flightphase.Climbout.Index() {
		// If Cruise, flip to destination (arrival) runway BEFORE initializing, this will be revaluated at the start of the arrival phase
		if ip == flightphase.Cruise.Index() {
			//TODO: consider what to do when a departure spawn results in a cruise phase -terminate tracking?
			rwy := e.getFallbackRunway(f.IcaoDest, ARRIVAL_CONTEXT)
			newAc.Flight.AssignedRunway = rwy.Name
		}
		// Initial Position Snap
		if ip == flightphase.Cruise.Index() {
			// assign destination procedure
			destApt := e.atcService.Airports[f.IcaoDest]
			e.assignProcedures(newAc, destApt, false)
			e.updateCruisePosition(newAc)
		} else {
			e.updateLinearPosition(newAc)
		}
		//e.initialiseMidAirPhase(newAc)
	} else {
		switch {
		case ip <= flightphase.Startup.Index():
			// For Parked/Startup, use the static parking logic
			e.positionAtOriginParking(newAc)

		case ip == flightphase.TaxiOut.Index():
			// For Taxi, use the 2-leg triangle logic
			// (Ensure airport and boolean 'isDeparture' are passed correctly)
			e.updateTaxiPosition(newAc, airport, true)

		case ip >= flightphase.Takeoff.Index():
			// For Takeoff, Climbout, and Departure, use the Runway/SID linear logic
			e.updateLinearPosition(newAc)

		default:
			// Fallback for safety
			newAc.Flight.Position.Lat = airport.Lat
			newAc.Flight.Position.Long = airport.Lon
		}
	}

	// add to active aircraft map
	e.ActiveAircraft[getActiveAircraftKey(newAc)] = newAc

	util.LogWithLabel(f.AircraftRegistration, "spawned departure %s flight %d phase %s origin %s dest %s lat %0.6f lon %0.6f alt %0.6f hdg %d - estimated next transition: %v",
		f.AirlineName, f.Number, flightphase.FlightPhase(newAc.Flight.Phase.Current).String(), f.IcaoOrigin, f.IcaoDest,
		newAc.Flight.Position.Lat, newAc.Flight.Position.Long, newAc.Flight.Position.Altitude, int(newAc.Flight.Position.Heading),
		newAc.Flight.Phase.EstimatedNextTransition.Format(time.RFC3339))

}

func (e *D9TrafficEngine) spawnArrivalTraffic(f *flightplan.ScheduledFlight) {

	tta := e.timeDiffToArrival(f)
	initialPhase, remainingDurSecs, fullDurationSecs := e.determineInitialArrivalPhase(tta, f)
	initialPhaseIdx := initialPhase.Index()

	airport := e.atcService.Airports[f.IcaoDest]

	currSimZTime := e.atcService.GetCurrentZuluTime()

	airline := e.resolveAirline(f)

	sizeClass := e.determineSizeClass(f, airline)
	sizeClassStr := ""
	if sizeClass == "E" || sizeClass == "F" {
		sizeClassStr = "Heavy"
	}

	//fullDurationSecs := e.calculateFullPhaseDuration(initialPhaseIdx, f)
	elapsedOffset := math.Max(0, float64(fullDurationSecs)-float64(remainingDurSecs))
	transitionTime := currSimZTime.Add(-time.Duration(elapsedOffset) * time.Second)

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
				Current:  initialPhaseIdx,
				Previous: flightphase.Unknown.Index(),
			},
		},
	}

	// set pre-requisite states
	e.atcService.SetFlightPhaseClass(newAc)
	// arrival runway must be assigned BEFORE assigning runway access point
	if initialPhaseIdx < flightphase.TaxiIn.Index() {
		newAc.Flight.AssignedRunway = e.AirportConfig[airport.ICAO].Arrival.Name
	}
	if initialPhaseIdx >= flightphase.Braking.Index() && initialPhaseIdx < flightphase.Parked.Index() {
		// assign parking BEFORE runway exit point as this may influence the selected exit
		e.assignParking(newAc, airport)
		e.assignRunwayAccessPoint(newAc, airport, ARRIVAL_CONTEXT)
	}

	if initialPhaseIdx >= flightphase.Arrival.Index() && initialPhaseIdx <= flightphase.Cruise.Index() {
		e.assignProcedures(newAc, airport, false)
	}

	newAc.Flight.Phase.TotalDuration = time.Duration(remainingDurSecs) * time.Second
	newAc.Flight.Phase.Transition = transitionTime
	newAc.Flight.Phase.EstimatedNextTransition = currSimZTime.Add(time.Duration(remainingDurSecs) * time.Second)
	newAc.Flight.Phase.StartAltitude = e.estimatePhaseInitialAltitude(newAc, initialPhaseIdx)

	// Only initialise mid-air (backdated) logic if the aircraft is flying.
	// For TaxiIn, Shutdown, or Parked, we want a fresh T+0 transition.
	// if initialPhaseIdx >= flightphase.Climbout.Index() && initialPhaseIdx <= flightphase.Final.Index() {
	// 	e.initialiseMidAirPhase(newAc)
	// }

	switch {
	case initialPhaseIdx == flightphase.Cruise.Index():
		// Handles airport-to-airport interpolation and TOD calculation
		e.updateCruisePosition(newAc)

	case initialPhaseIdx >= flightphase.Arrival.Index() && initialPhaseIdx <= flightphase.Braking.Index():
		// Handles STAR/Approach/Final/Braking relative to the arrival runway
		e.updateLinearPosition(newAc)

	case initialPhaseIdx == flightphase.TaxiIn.Index():
		// Handles the 2-leg triangle from Runway Access to Gate
		e.updateTaxiPosition(newAc, airport, false) // false = Inbound

	case initialPhaseIdx >= flightphase.Shutdown.Index():
		// Ensure the aircraft is snapped to its assigned parking spot
		e.positionAtDestParking(newAc)

	default:
		// Safety fallback to airport center
		newAc.Flight.Position.Lat = airport.Lat
		newAc.Flight.Position.Long = airport.Lon
	}

	// add to active aircraft map
	e.ActiveAircraft[getActiveAircraftKey(newAc)] = newAc

	util.LogWithLabel(f.AircraftRegistration, "spawned arrival %s flight %d phase %s origin %s dest %s lat %0.6f lon %0.6f alt %0.6f hdg %d - estimated next transition: %v",
		f.AirlineName, f.Number, flightphase.FlightPhase(newAc.Flight.Phase.Current).String(), f.IcaoOrigin, f.IcaoDest,
		newAc.Flight.Position.Lat, newAc.Flight.Position.Long, newAc.Flight.Position.Altitude, int(newAc.Flight.Position.Heading),
		newAc.Flight.Phase.EstimatedNextTransition.Format(time.RFC3339))
}

func (e *D9TrafficEngine) initialiseMidAirPhase(ac *atc.Aircraft) {

	origin := e.atcService.Airports[ac.Flight.Schedule.IcaoOrigin]
	dest := e.atcService.Airports[ac.Flight.Schedule.IcaoDest]

	currentPhaseIdx := ac.Flight.Phase.Current

	// 2. Hydrate Altitudes Defensively
	if ac.Flight.Position.Altitude <= 1.0 {
		switch currentPhaseIdx {
		case flightphase.Cruise.Index():
			if ac.Flight.CruiseAlt == 0 {
				ac.Flight.CruiseAlt = 35000 
			}
			ac.Flight.Position.Altitude = float64(ac.Flight.CruiseAlt)
		case flightphase.Arrival.Index(), flightphase.Approach.Index(): 
			ac.Flight.Position.Altitude = 10000.0
		case flightphase.Climbout.Index(), flightphase.Departure.Index(), flightphase.Takeoff.Index():
			// Initial safe altitude clearance out of the terminal zone
			ac.Flight.Position.Altitude = 5000.0
		}
		ac.Flight.Phase.StartAltitude = ac.Flight.Position.Altitude
	}

	// 3. Hydrate Vector/Heading Tracking Gates
	var targetLat, targetLon float64
	hasTarget := false

	switch currentPhaseIdx {
	// BUNDLE DEPARTURE STATES: Track the SID Exit point or fly Runway Heading
	case flightphase.Climbout.Index(), flightphase.Departure.Index(), flightphase.Takeoff.Index():
		if sid := ac.Flight.AssignedSID; sid != nil && sid.Exit.Fix.Lat != 0 {
			targetLat, targetLon = sid.Exit.Fix.Lat, sid.Exit.Fix.Lon
			hasTarget = true
		} else if origin != nil {
			// If no SID is assigned yet, fallback to tracking along the runway alignment
			targetLat, targetLon = origin.Lat, origin.Lon
			hasTarget = true
		}

	case flightphase.Cruise.Index():
		if star := ac.Flight.AssignedSTAR; star != nil && star.Entry.Fix.Lat != 0 {
			targetLat, targetLon = star.Entry.Fix.Lat, star.Entry.Fix.Lon
			hasTarget = true
		} else if dest != nil {
			targetLat, targetLon = dest.Lat, dest.Lon
			hasTarget = true
		}

	case flightphase.Arrival.Index(), flightphase.Approach.Index():
		if ac.Flight.Vectoring {
			if ac.Flight.Position.Heading != 0 {
				ac.Flight.Position.Heading = geometry.NormalizeHeading(ac.Flight.Position.Heading)
				return
			}
		}
		if star := ac.Flight.AssignedSTAR; star != nil && star.Exit.Fix.Lat != 0 {
			targetLat, targetLon = star.Exit.Fix.Lat, star.Exit.Fix.Lon
			hasTarget = true
		} else if dest != nil {
			targetLat, targetLon = dest.Lat, dest.Lon
			hasTarget = true
		}
	}

	// 4. Calculate Track Vector
	if hasTarget {
		hdg := geometry.CalculateBearing(ac.Flight.Position.Lat, ac.Flight.Position.Long, targetLat, targetLon)
		ac.Flight.Position.Heading = geometry.NormalizeHeading(hdg)
	} else {
		// Hard safety net targeting destination center directly
		if dest != nil {
			hdg := geometry.CalculateBearing(ac.Flight.Position.Lat, ac.Flight.Position.Long, dest.Lat, dest.Lon)
			ac.Flight.Position.Heading = geometry.NormalizeHeading(hdg)
		} else {
			ac.Flight.Position.Heading = 360.0
		}
	}
}

// getFallbackRunway tries to get the active runway for the given flight context, if not set/available, fallsback to any runway.
// Returns nil if the fallback fails.
func (e *D9TrafficEngine) getFallbackRunway(icao string, arrOrDep int) *atc.Runway {
	// 1. Try your specific airport config first
	if config, found := e.AirportConfig[icao]; found {
		if arrOrDep == ARRIVAL_CONTEXT {
			return config.Arrival
		} else {
			return config.Departure
		}
	}

	// 2. Fallback: Get the first available runway from the global airport data
	if apt, found := e.atcService.Airports[icao]; found && len(apt.Runways) > 0 {
		// Just pick the first one available as a coordinate anchor
		for _, r := range apt.Runways {
			return r
		}
	}
	return nil
}

func (e *D9TrafficEngine) estimatePhaseInitialAltitude(ac *atc.Aircraft, phase int) float64 {

	apt, flightContext := e.getActiveAirport(ac)
	icao := apt.ICAO
	rwy := e.atcService.GetAirportRunway(icao, ac.Flight.AssignedRunway)
	if rwy == nil {
		rwy = e.getFallbackRunway(icao, flightContext)
	}

	p := flightphase.FlightPhase(phase)

	switch p {
	case flightphase.Takeoff, flightphase.Climbout:
		if rwy != nil {
			return rwy.ThresholdElevation
		}
		return apt.Elevation
	case flightphase.Departure:
		return apt.Elevation + 3000.0
	case flightphase.Cruise:
		if sid := ac.Flight.AssignedSID; sid != nil && sid.Exit.ConstraintAlt > 0 {
			return float64(sid.Exit.ConstraintAlt)
		}
		return apt.Elevation + 5000.0 // TOC fallback from Origin
	case flightphase.Arrival:
		return float64(ac.Flight.CruiseAlt)
	case flightphase.Approach:
		if star := ac.Flight.AssignedSTAR; star != nil && star.Exit.ConstraintAlt > 0 {
			return float64(star.Exit.ConstraintAlt)
		}
		return apt.Elevation + 4000.0
	case flightphase.Final:
		if rwy != nil && rwy.FAFalt > 0 {
			return float64(rwy.FAFalt)
		}
		return apt.Elevation + 1500.0
	default:
		return apt.Elevation
	}
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
				//TODO: consider what to do -terminate tracking?
				continue
			}
		} else {
			util.LogWarnWithLabel(ac.Registration, "skipping update - target icao not found", targetICAO)
			continue
		}

		switch flightphase.FlightPhase(ac.Flight.Phase.Current) {

		case flightphase.Unknown:
			// should never happen
			util.LogWarnWithLabel(ac.Registration, "unexpected flight phase of unknown - cannot update status")
			continue

		// --- DEPARTURE FLOW ---
		case flightphase.Parked:
			if ac.Flight.Phase.Class == flightclass.PostflightParked {
				e.endFlight(ac) // Cleanup logic
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
				e.assignRunwayAccessPoint(ac, airport, DEPARTURE_CONTEXT)
				dur := e.calculateTaxiDuration(ac, airport, DEPARTURE_CONTEXT)
				if ac.Flight.AssignedParkingSpot != nil {
					e.releaseParking(f.IcaoOrigin, ac.Flight.AssignedParkingSpot)
				}
				e.transitionToPhase(ac, flightphase.TaxiOut, dur, 0)
			}

		case flightphase.TaxiOut:
			e.updateTaxiPosition(ac, airport, true)
			if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
				if !e.getRunwayLock(airport, e.AirportConfig[airport.ICAO].Departure, ac) {
					util.LogWithLabel(ac.Registration, "active departure runway %s is occupied at %s - remaining in TaxiOut phase",
						e.AirportConfig[airport.ICAO].Departure.Name, airport.ICAO)
					continue
				}
				dur := (DMINUS_TAKEOFF_MINS - DMINUS_CLIMBOUT_MINS) * 60
				e.transitionToPhase(ac, flightphase.Takeoff, dur, 0)
				// Position at runway threshold
				rwy := e.atcService.GetAirportRunway(airport.ICAO, ac.Flight.AssignedRunway)
				if rwy != nil {
					ac.Flight.Position.Lat = rwy.Lat
					ac.Flight.Position.Long = rwy.Lon
					ac.Flight.Position.Heading = geometry.NormalizeHeading(rwy.Heading)
					ac.Flight.Position.Altitude = math.Max(airport.Elevation, rwy.ThresholdElevation)
				} else {
					util.LogWarnWithLabel(ac.Registration,
						"unable to position aircraft at runway threshold - runway %s not found at airport %s",
						ac.Flight.AssignedRunway, airport.ICAO)
				}
			}

		case flightphase.Takeoff:
			e.updateLinearPosition(ac)
			if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
				e.releaseRunwayLock(airport, e.AirportConfig[airport.ICAO].Departure, ac)
				dur := (DMINUS_CLIMBOUT_MINS - DMINUS_DEPARTURE_MINS) * 60
				e.transitionToPhase(ac, flightphase.Climbout, dur, CLIMBOUT_JITTER_SECONDS)
			}

		case flightphase.Climbout:
			e.updateLinearPosition(ac)
			if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
				dur := (DMINUS_DEPARTURE_MINS - DMINUS_CRUISE_MINS) * 60
				e.transitionToPhase(ac, flightphase.Departure, DEPARTURE_JITTER_SECONDS, dur)
			}

		case flightphase.Departure:
			e.updateLinearPosition(ac)
			if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
				tta := e.timeDiffToArrival(f) // Minutes until scheduled arrival
				dur := (tta - AMINUS_ARRIVAL_MINS) * 60
				e.transitionToPhase(ac, flightphase.Cruise, CRUISE_JITTER_SECONDS, dur)
			}

		case flightphase.Cruise:
			e.updateCruisePosition(ac)
			tta := e.timeDiffToArrival(f) // Minutes until scheduled arrival
			if tta <= AMINUS_ARRIVAL_MINS {
				// we are transitioning to arrival so assign or replace any earlier assigned runway
				ac.Flight.AssignedRunway = e.AirportConfig[airport.ICAO].Arrival.Name
				if ac.Flight.AssignedSTAR == nil && ac.Flight.Vectoring == false {
					e.assignProcedures(ac, airport, false)
				}
				durSecs := (AMINUS_APPROACH_MINS - AMINUS_FINAL_MINS) * 60
				e.transitionToPhase(ac, flightphase.Arrival, durSecs, ARRIVAL_JITTER_SECONDS)
				util.LogWithLabel(ac.Registration, "commencing arrival into %s (TTA: %d mins)",
					f.IcaoDest, tta)
			} else {
				util.LogWithLabel(ac.Registration, "cruising... %d minutes until arrival window",
					tta-AMINUS_APPROACH_MINS)
			}

		case flightphase.Arrival:
			e.updateLinearPosition(ac)
			if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
				qKey := normalizeRunwayKey(airport.ICAO, e.AirportConfig[airport.ICAO].Arrival)
				if len(e.RunwayQueues[qKey]) >= TRAFFIC_MANAGEMENT_RUNWAY_QUEUE_THRESHOLD {
					// send to hold
					e.atcService.AssignHold(ac, airport.ICAO)
					dur := (HOLDING_MIN_DURATION_MINS * 60) + 60
					e.transitionToPhase(ac, flightphase.Holding, dur, 0)
				} else {
					// start approach
					dur := (AMINUS_APPROACH_MINS - AMINUS_FINAL_MINS) * 60
					e.transitionToPhase(ac, flightphase.Approach, dur, APPROACH_JITTER_SECONDS)
				}
			}

		case flightphase.Holding:
			e.updateHoldingPosition(ac, airport)
			if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
				e.tryExitHold(ac, airport)
			}

		case flightphase.Approach:
			e.updateLinearPosition(ac)
			if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
				if !e.getRunwayLock(airport, e.AirportConfig[airport.ICAO].Arrival, ac) {
					util.LogWithLabel(ac.Registration, "on approach: active arrival runway %s is occupied at %s - remaining in approach phase",
						e.AirportConfig[airport.ICAO].Departure.Name, airport.ICAO)
					continue
				}
				dur := (AMINUS_FINAL_MINS - AMINUS_LAND_MINS) * 60
				e.transitionToPhase(ac, flightphase.Final, dur, 0)
			}

		case flightphase.Final:
			e.updateLinearPosition(ac)
			if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
				if !e.getRunwayLock(airport, e.AirportConfig[airport.ICAO].Arrival, ac) {
					util.LogWithLabel(ac.Registration, "on final: active arrival runway %s is occupied at %s - initiating go-around",
						e.AirportConfig[airport.ICAO].Departure.Name, airport.ICAO)
					e.transitionToPhase(ac, flightphase.GoAround, 80, 0)
					continue
				}
				dur := (AMINUS_LAND_MINS - AMINUS_BRAKING) * 60
				e.transitionToPhase(ac, flightphase.Braking, dur, 0)
				ac.Flight.Position.Altitude = airport.Elevation
			}

		case flightphase.GoAround:
			e.updateGoAroundPosition(ac, airport)
			if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
				e.releaseRunwayLock(airport, e.AirportConfig[airport.ICAO].Arrival, ac)
				// Randomized hold or back into approach flow
				if rand.Float32() < GOAROUND_TO_HOLD_PROBABILITY_FACTOR {
					// send to hold
					e.atcService.AssignHold(ac, airport.ICAO)
					dur := (HOLDING_MIN_DURATION_MINS * 60) + 60
					e.transitionToPhase(ac, flightphase.Holding, dur, 0)
				} else {
					// send back around to approach
					e.tryExitHold(ac, airport)
				}
			}

		case flightphase.Braking:
			e.updateLinearPosition(ac)
			if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
				e.releaseRunwayLock(airport, e.AirportConfig[airport.ICAO].Arrival, ac)
				e.assignParking(ac, airport)
				e.assignRunwayAccessPoint(ac, airport, ARRIVAL_CONTEXT)
				dur := e.calculateTaxiDuration(ac, airport, ARRIVAL_CONTEXT)
				e.transitionToPhase(ac, flightphase.TaxiIn, dur, 0)
			}

		case flightphase.TaxiIn:
			e.updateTaxiPosition(ac, airport, false)
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

	// Capture the 'Exit' altitude of the current phase to be the 'Start' of the next
	ac.Flight.Phase.StartAltitude = ac.Flight.Position.Altitude
	if ac.Flight.Phase.StartAltitude <= 0 {
		// Fallback to the elevation of the active airport for this phase
		apt, _ := e.getActiveAirport(ac)
		ac.Flight.Phase.StartAltitude = apt.Elevation
	}
	// Ensure we have at least a larger duration than the jitter
	if baseSecs <= 0 {
		baseSecs = jitterSecs + 1
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
	e.atcService.SetFlightPhaseClass(ac)
}

func (e *D9TrafficEngine) assignParking(ac *atc.Aircraft, airport *atc.Airport) {
	spot := e.findAvailableParking(airport, ac.SizeClass, ac.Flight.Airline.ICAO)
	if spot != nil {
		ac.Flight.AssignedParkingSpot = spot
		ac.Flight.AssignedParkingName = spot.Name
		e.OccupiedParking[fmt.Sprintf("%s_%s", airport.ICAO, spot.Name)] = ac.Registration
		spot.IsOccupied = true
	}
}

func (e *D9TrafficEngine) updateLinearPosition(ac *atc.Aircraft) {
	currSimZTime := e.atcService.GetCurrentZuluTime()
	elapsed := currSimZTime.Sub(ac.Flight.Phase.Transition).Seconds()
	totalDuration := ac.Flight.Phase.TotalDuration.Seconds()
	if totalDuration <= 0 {
		return
	}

	progress := math.Min(1.0, elapsed/totalDuration)
	phase := flightphase.FlightPhase(ac.Flight.Phase.Current)

	var startPos, targetPos atc.Position
	var startAlt, targetAlt float64
	var heading float64

	// Use the stored StartAltitude as the floor for vertical movement
	startAlt = ac.Flight.Phase.StartAltitude

	dest := e.atcService.Airports[ac.Flight.Schedule.IcaoDest]
	origin := e.atcService.Airports[ac.Flight.Schedule.IcaoOrigin]
	activeAirport, flightContext := e.getActiveAirport(ac)
	rwy := e.atcService.GetAirportRunway(activeAirport.ICAO, ac.Flight.AssignedRunway)

	if rwy == nil {
		rwy = e.getFallbackRunway(activeAirport.ICAO, flightContext)

		// Absolute final fallback if no runways exist in data
		if rwy == nil {
			// Calculate a general direction based on the flight path
			// instead of a startPos that doesn't exist yet.
			pathHeading := geometry.CalculateBearing(origin.Lat, origin.Lon, dest.Lat, dest.Lon)

			rwy = &atc.Runway{
				Lat:     activeAirport.Lat,
				Lon:     activeAirport.Lon,
				Heading: pathHeading,
			}
		}
	}

	// Determine Runway Length in NM (Default to 1.2 if missing)
	rwyLengthNM := 1.2
	if rwy.Length > 0 {
		rwyLengthNM = rwy.Length * 0.000164579 // Feet to NM
	}

	// Initialize with current position as a safety "anchor". These values should get modified by the
	// switch/case process
	startPos = ac.Flight.Position
	targetPos = ac.Flight.Position // Default to no movement

	switch phase {
	case flightphase.Takeoff:
		startPos = atc.Position{Lat: rwy.Lat, Long: rwy.Lon}
		targetPos.Lat, targetPos.Long = geometry.Project(rwy.Lat, rwy.Lon, rwy.Heading, rwyLengthNM)

		// Use the current progress to find the "live" altitude if the runway is sloped
		// Or simply follow the runway threshold elevation
		targetAlt = rwy.ThresholdElevation
		heading = rwy.Heading

	case flightphase.Climbout:
		// Start: End of Runway
		startPos.Lat, startPos.Long = geometry.Project(rwy.Lat, rwy.Lon, rwy.Heading, rwyLengthNM)

		if sid := ac.Flight.AssignedSID; sid != nil && sid.Entry.Fix.Lat != 0 {
			targetPos = atc.Position{Lat: sid.Entry.Fix.Lat, Long: sid.Entry.Fix.Lon}
			targetAlt = float64(sid.Entry.ConstraintAlt)
		} else {
			targetPos.Lat, targetPos.Long = geometry.Project(rwy.Lat, rwy.Lon, rwy.Heading, 5.0)
			targetAlt = startAlt + 3000
		}
		heading = rwy.Heading

	case flightphase.Departure:
		if sid := ac.Flight.AssignedSID; sid != nil && sid.Entry.Fix.Lat != 0 {
			startPos = atc.Position{Lat: sid.Entry.Fix.Lat, Long: sid.Entry.Fix.Lon}
		}

		if sid := ac.Flight.AssignedSID; sid != nil && sid.Exit.Fix.Lat != 0 {
			targetPos = atc.Position{Lat: sid.Exit.Fix.Lat, Long: sid.Exit.Fix.Lon}
			targetAlt = float64(sid.Exit.ConstraintAlt)
		} else {
			targetPos.Lat, targetPos.Long = geometry.Project(rwy.Lat, rwy.Lon, rwy.Heading, 40.0)
			targetAlt = float64(ac.Flight.CruiseAlt)
		}
		heading = geometry.CalculateBearing(startPos.Lat, startPos.Long, targetPos.Lat, targetPos.Long)

	case flightphase.Arrival:
		// Start: Current Cruise Position (implicitly handled by ac.Flight.Position at transition)
		// Use the aircraft's last known position as the start of the linear arrival
		startPos = ac.Flight.Position
		// Target: STAR Entry or 15NM Gate
		if star := ac.Flight.AssignedSTAR; star != nil && star.Entry.Fix.Lat != 0 {
			targetPos = atc.Position{Lat: star.Entry.Fix.Lat, Long: star.Entry.Fix.Lon}
			targetAlt = float64(star.Entry.ConstraintAlt)
		} else {
			targetPos.Lat, targetPos.Long = geometry.Project(rwy.Lat, rwy.Lon, geometry.NormalizeHeading(rwy.Heading+180), 15.0)
			targetAlt = float64(rwy.FAFalt) + 2000 // Arrival usually ends higher than Approach FAF
		}

	case flightphase.Approach:
		// FIX: Start at STAR Exit if available, otherwise 15NM gate
		if star := ac.Flight.AssignedSTAR; star != nil && star.Exit.Fix.Lat != 0 {
			startPos = atc.Position{Lat: star.Exit.Fix.Lat, Long: star.Exit.Fix.Lon}
		} else {
			startPos.Lat, startPos.Long = geometry.Project(rwy.Lat, rwy.Lon, geometry.NormalizeHeading(rwy.Heading+180), 15.0)
		}

		// Target: FAF (4.0NM out)
		targetPos.Lat, targetPos.Long = geometry.Project(rwy.Lat, rwy.Lon, geometry.NormalizeHeading(rwy.Heading+180), 4.0)
		targetAlt = float64(rwy.FAFalt)
		if targetAlt == 0 {
			targetAlt = dest.Elevation + 1500
		}
		heading = rwy.Heading

	case flightphase.Final:
		// Start: FAF (4.0NM out)
		startPos.Lat, startPos.Long = geometry.Project(rwy.Lat, rwy.Lon, geometry.NormalizeHeading(rwy.Heading+180), 4.0)

		targetPos = atc.Position{Lat: rwy.Lat, Long: rwy.Lon}
		targetAlt = rwy.ThresholdElevation
		if targetAlt == 0 {
			targetAlt = dest.Elevation
		}
		heading = rwy.Heading

	case flightphase.Braking:
		startPos = atc.Position{Lat: rwy.Lat, Long: rwy.Lon}
		targetPos.Lat, targetPos.Long = geometry.Project(rwy.Lat, rwy.Lon, rwy.Heading, rwyLengthNM*0.75)

		// Ensure we are at the runway's height
		targetAlt = rwy.ThresholdElevation
		heading = rwy.Heading
	}

	// --- Final Move ---
	ac.Flight.Position.Lat = startPos.Lat + (progress * (targetPos.Lat - startPos.Lat))
	ac.Flight.Position.Long = startPos.Long + (progress * (targetPos.Long - startPos.Long))
	ac.Flight.Position.Heading = geometry.NormalizeHeading(heading)

	// Override vertical interpolation for ground phases
	if phase == flightphase.Takeoff || phase == flightphase.Braking || phase == flightphase.TaxiOut || phase == flightphase.TaxiIn {
		ac.Flight.Position.Altitude = targetAlt
	} else {
		ac.Flight.Position.Altitude = startAlt + (progress * (targetAlt - startAlt))
	}

	// Safety Check: ensure calculated values are correct
	if ac.Flight.Position.Lat > 90 || ac.Flight.Position.Lat < -90 {
		util.LogWarnWithLabel(ac.Registration, "Latitude out of bounds: %f. Check phase %d logic.",
			ac.Flight.Position.Lat, phase)
	}
}

// getActiveAirport returns the origin (departing) airport when the aircraft is in a departing phase,
// otherwise the destination airport is returned. The context, arrival or departure, is also returned.
func (e *D9TrafficEngine) getActiveAirport(ac *atc.Aircraft) (*atc.Airport, int) {
	if ac.Flight.Phase.Class >= flightclass.Cruising {
		return e.atcService.Airports[ac.Flight.Schedule.IcaoDest], ARRIVAL_CONTEXT
	}
	return e.atcService.Airports[ac.Flight.Schedule.IcaoOrigin], DEPARTURE_CONTEXT
}

func (e *D9TrafficEngine) calculateTaxiDuration(ac *atc.Aircraft, airport *atc.Airport, flightContext int) int {
	// 1. Get the legs from the logic we established for updateTaxiPosition
	// Leg 1: Gate to Access Point | Leg 2: Access Point to Runway
	gate := ac.Flight.AssignedParkingSpot
	var access *atc.AccessPoint
	if flightContext == DEPARTURE_CONTEXT {
		access = ac.Flight.DepartureAccess
	} else {
		access = ac.Flight.ArrivalAccess
	}
	rwy := e.atcService.GetAirportRunway(airport.ICAO, ac.Flight.AssignedRunway)

	if gate == nil || access == nil || rwy == nil {
		return 300 // Fallback 5 mins
	}

	// Total distance in NM
	dist1 := geometry.DistNM(gate.Lat, gate.Lon, access.Coord.Lat, access.Coord.Lon)
	dist2 := geometry.DistNM(access.Coord.Lat, access.Coord.Lon, rwy.Lat, rwy.Lon)
	totalDist := dist1 + dist2

	// 2. Calculate time at ~18 knots (18nm per 3600s = 1nm per 200s)
	// Formula: (Distance / Speed) * 3600
	taxiSpeed := 18.0
	duration := (totalDist / taxiSpeed) * 3600

	// Add a small buffer for "startup/slowdown" feel
	return int(duration) + 30
}

func (e *D9TrafficEngine) updateTaxiPosition(ac *atc.Aircraft, airport *atc.Airport, isOutbound bool) {
	elapsed := e.atcService.GetCurrentZuluTime().Sub(ac.Flight.Phase.Transition).Seconds()
	totalDuration := ac.Flight.Phase.TotalDuration.Seconds()
	if totalDuration <= 0 {
		return
	}

	// Clamp progress 0.0 -> 1.0. If duration is exceeded, it stays at 1.0 (the destination)
	progress := math.Min(1.0, elapsed/totalDuration)

	var startLat, startLon, endLat, endLon float64
	var startHdg, endHdg float64

	if isOutbound {
		startLat = ac.Flight.AssignedParkingSpot.Lat
		startLon = ac.Flight.AssignedParkingSpot.Lon
		endLat = ac.Flight.DepartureAccess.Coord.Lat
		endLon = ac.Flight.DepartureAccess.Coord.Lon
		startHdg = ac.Flight.AssignedParkingSpot.Heading
		endHdg = ac.Flight.DepartureAccess.Bearing
	} else {
		startLat = ac.Flight.ArrivalAccess.Coord.Lat
		startLon = ac.Flight.ArrivalAccess.Coord.Lon
		endLat = ac.Flight.AssignedParkingSpot.Lat
		endLon = ac.Flight.AssignedParkingSpot.Lon
		startHdg = ac.Flight.ArrivalAccess.Bearing
		endHdg = ac.Flight.AssignedParkingSpot.Heading
	}

	// Define the "Corner" for the 90-degree taxi path
	// We move along start's Longitude first, then align to end's Latitude (or vice versa)
	cornerLat, cornerLon := startLat, endLon

	if progress < 0.5 {
		subP := progress * 2.0
		ac.Flight.Position.Lat = startLat + (subP * (cornerLat - startLat))
		ac.Flight.Position.Long = startLon + (subP * (cornerLon - startLon))
		ac.Flight.Position.Heading = geometry.NormalizeHeading(startHdg) // Maintain start heading for first leg
	} else {
		subP := (progress - 0.5) * 2.0
		ac.Flight.Position.Lat = cornerLat + (subP * (endLat - cornerLat))
		ac.Flight.Position.Long = cornerLon + (subP * (endLon - cornerLon))
		ac.Flight.Position.Heading = geometry.NormalizeHeading(endHdg) // Align to end heading for second leg
	}
	ac.Flight.Position.Altitude = airport.Elevation
}

func (e *D9TrafficEngine) updateHoldingPosition(ac *atc.Aircraft, airport *atc.Airport) {
	elapsed := e.atcService.GetCurrentZuluTime().Sub(ac.Flight.Phase.Transition).Seconds()

	// 4-minute pattern: 2x 1min straights, 2x 1min 180° turns
	const cycleTime = 240.0
	t := math.Mod(elapsed, cycleTime) / cycleTime

	rwy := e.atcService.GetAirportRunway(airport.ICAO, ac.Flight.AssignedRunway)
	// Anchor the hold 10NM out from the threshold
	holdCenterLat, holdCenterLon := geometry.Project(rwy.Lat, rwy.Lon, geometry.NormalizeHeading(rwy.Heading+180), 10.0)

	// Parametric oval calculation
	angle := t * 2 * math.Pi
	ac.Flight.Position.Lat = holdCenterLat + (0.02 * math.Cos(angle))
	ac.Flight.Position.Long = holdCenterLon + (0.04 * math.Sin(angle))
	ac.Flight.Position.Heading = geometry.NormalizeHeading(math.Mod(rwy.Heading+(t*360), 360))
	ac.Flight.Position.Altitude = airport.Elevation + 4000.0 // Standard terminal hold altitude
}

func (e *D9TrafficEngine) updateGoAroundPosition(ac *atc.Aircraft, airport *atc.Airport) {
	elapsed := e.atcService.GetCurrentZuluTime().Sub(ac.Flight.Phase.Transition).Seconds()
	totalDuration := ac.Flight.Phase.TotalDuration.Seconds()

	// We don't clamp progress at 1.0 here—if the transition is delayed,
	// the aircraft keeps flying the vector.
	progress := elapsed / totalDuration

	rwy := e.atcService.GetAirportRunway(airport.ICAO, ac.Flight.AssignedRunway)

	// Project along runway heading + 10 degrees (standard missed approach)
	dist := progress * 6.0 // Expected to be 6NM at the end of the phase duration
	newLat, newLon := geometry.Project(rwy.Lat, rwy.Lon, geometry.NormalizeHeading(rwy.Heading+10), dist)

	ac.Flight.Position.Lat = newLat
	ac.Flight.Position.Long = newLon
	ac.Flight.Position.Heading = geometry.NormalizeHeading(rwy.Heading + 10)
	// Climb to 3000ft
	ac.Flight.Position.Altitude = math.Min(airport.Elevation+3000, airport.Elevation+(progress*3000))
}

func (e *D9TrafficEngine) updateCruisePosition(ac *atc.Aircraft) {
	currSimZTime := e.atcService.GetCurrentZuluTime()
	elapsed := currSimZTime.Sub(ac.Flight.Phase.Transition).Seconds()
	totalDuration := ac.Flight.Phase.TotalDuration.Seconds()

	if totalDuration <= 0 {
		return
	}

	// 1. Calculate Horizontal Progress (0.0 -> 1.0)
	progress := math.Min(1.0, elapsed/totalDuration)

	origin := e.atcService.Airports[ac.Flight.Schedule.IcaoOrigin]
	dest := e.atcService.Airports[ac.Flight.Schedule.IcaoDest]
	cruiseAlt := float64(ac.Flight.CruiseAlt)

	// 2. Identify Horizontal Start (SID Exit or Origin Center)
	var startPos atc.Position
	startAlt := origin.Elevation + 3000.0 // Default departure exit alt
	if sid := ac.Flight.AssignedSID; sid != nil && sid.Exit.Fix.Lat != 0 {
		startPos = atc.Position{Lat: sid.Exit.Fix.Lat, Long: sid.Exit.Fix.Lon}
		startAlt = float64(sid.Exit.ConstraintAlt)
	} else {
		startPos = atc.Position{Lat: origin.Lat, Long: origin.Lon}
	}

	// 3. Identify Horizontal Target (STAR Entry or Destination Center)
	var targetPos atc.Position
	targetAlt := 10000.0 // Default arrival entry alt
	if star := ac.Flight.AssignedSTAR; star != nil && star.Entry.Fix.Lat != 0 {
		targetPos = atc.Position{Lat: star.Entry.Fix.Lat, Long: star.Entry.Fix.Lon}
		targetAlt = float64(star.Entry.ConstraintAlt)
	} else {
		targetPos = atc.Position{Lat: dest.Lat, Long: dest.Lon}
	}

	// 4. Update Lat/Lon via Linear Interpolation
	ac.Flight.Position.Lat = startPos.Lat + (progress * (targetPos.Lat - startPos.Lat))
	ac.Flight.Position.Long = startPos.Long + (progress * (targetPos.Long - startPos.Long))

	// 5. Vertical Profile using the 3-to-1 Rule for Descent
	distToTarget := geometry.DistNM(ac.Flight.Position.Lat, ac.Flight.Position.Long, targetPos.Lat, targetPos.Long)

	// Math: 3 NM for every 1000ft of altitude loss
	altitudeToLose := cruiseAlt - targetAlt
	requiredDescentDist := (altitudeToLose / 1000.0) * 3.0

	var calculatedAlt float64

	if distToTarget <= requiredDescentDist && altitudeToLose > 0 {
		// --- PHASE: DESCENT (Post-TOD) ---
		// How far into the descent are we? (0.0 at TOD, 1.0 at Target)
		descentProgress := 1.0
		if requiredDescentDist > 0 {
			descentProgress = 1.0 - (distToTarget / requiredDescentDist)
		}
		calculatedAlt = cruiseAlt - (math.Max(0, descentProgress) * altitudeToLose)

		// Late-stage Procedure Assignment: Ensure STAR is assigned before Arrival phase starts
		if ac.Flight.AssignedSTAR == nil && ac.Flight.Vectoring == false && distToTarget < (requiredDescentDist+10.0) {
			e.assignProcedures(ac, dest, false)
		}
	} else {
		// --- PHASE: CLIMB or LEVEL ---
		// Check if we are still in the initial climb from Departure
		// We use a simple 10-minute climb window for TOC logic
		climbDurationSecs := 600.0
		if elapsed < climbDurationSecs {
			climbProgress := elapsed / climbDurationSecs
			calculatedAlt = startAlt + (climbProgress * (cruiseAlt - startAlt))
		} else {
			calculatedAlt = cruiseAlt
		}
	}

	// 6. Apply State
	ac.Flight.Position.Altitude = calculatedAlt
	hd := geometry.CalculateBearing(
		ac.Flight.Position.Lat,
		ac.Flight.Position.Long,
		targetPos.Lat,
		targetPos.Long,
	)
	ac.Flight.Position.Heading = geometry.NormalizeHeading(hd)
}

func (e *D9TrafficEngine) endFlight(ac *atc.Aircraft) {
	delete(e.ActiveAircraft, getActiveAircraftKey(ac))
	if ac.Flight.AssignedParkingSpot != nil {
		e.releaseParking(ac.Flight.Destination, ac.Flight.AssignedParkingSpot)
	}
}

func (e *D9TrafficEngine) positionAtOriginParking(ac *atc.Aircraft) {
	airport := e.atcService.Airports[ac.Flight.Origin]
	if ac.Flight.AssignedParkingSpot == nil {
		e.assignParking(ac, airport)
		if ac.Flight.AssignedParkingSpot == nil {
			util.LogWarnWithLabel(ac.Registration, "no suitable parking found at origin airport %s - terminating flight", airport.ICAO)
			delete(e.ActiveAircraft, getActiveAircraftKey(ac))
			//TODO consider strategy to prevent spawn re-selection, potentially delete schedule
			return
		} else {
			util.LogWithLabel(ac.Registration, "assigning parking at airport %s to spot %s", airport.ICAO, ac.Flight.AssignedParkingSpot.Name)
		}
	}
	ac.Flight.Position = atc.Position{
		Lat:      ac.Flight.AssignedParkingSpot.Lat,
		Long:     ac.Flight.AssignedParkingSpot.Lon,
		Heading:  geometry.NormalizeHeading(ac.Flight.AssignedParkingSpot.Heading),
		Altitude: airport.Elevation,
	}
}

func (e *D9TrafficEngine) positionAtDestParking(ac *atc.Aircraft) {
	airport := e.atcService.Airports[ac.Flight.Destination]
	if ac.Flight.AssignedParkingSpot == nil {
		e.assignParking(ac, airport)
		if ac.Flight.AssignedParkingSpot == nil {
			util.LogWarnWithLabel(ac.Registration, "no suitable parking found at airport %s - ending flight", airport.ICAO)
			e.endFlight(ac)
			return
		} else {
			util.LogWithLabel(ac.Registration, "assigning parking at airport %s to spot %s", airport.ICAO, ac.Flight.AssignedParkingSpot.Name)
		}
	}
	ac.Flight.Position.Lat = ac.Flight.AssignedParkingSpot.Lat
	ac.Flight.Position.Long = ac.Flight.AssignedParkingSpot.Lon
	ac.Flight.Position.Heading = geometry.NormalizeHeading(ac.Flight.AssignedParkingSpot.Heading)
	ac.Flight.Position.Altitude = airport.Elevation

	util.LogWithLabel(ac.Registration, "arrived at gate %s", ac.Flight.AssignedParkingSpot.Name)
}

func AbsDiff(a, b int) int {
	result := a - b
	if result < 0 {
		return -result
	}
	return result
}

func AbsInt(v int) int {
	return int(math.Abs(float64(v)))
}

// DetermineInitialDeparturePhase returns the initial phase of a new spawned aircraft and the estimated remaining duration
// of the phase in seconds. We add some random seconds to avoid all aircraft transitioning at the same time.
func (e *D9TrafficEngine) determineInitialDeparturePhase(diff int, f *flightplan.ScheduledFlight) (flightphase.FlightPhase, int, int, int) {
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
		jitter := rand.IntN((PARKED_JITTER_SECONDS*2)+1) - PARKED_JITTER_SECONDS
		estimatedDuration := (AbsDiff(diff, DMINUS_STARTUP_MINS) * 60) + jitter
		return flightphase.Parked, estimatedDuration, AbsInt((diff-DMINUS_STARTUP_MINS)*60) + jitter, delay

	// still parked but tracking towards startup
	case diff > DMINUS_STARTUP_MINS && diff <= DMINUS_PARKED_MINS:
		jitter := rand.IntN((PARKED_JITTER_SECONDS*2)+1) - PARKED_JITTER_SECONDS
		estimatedDuration := (AbsDiff(diff, DMINUS_STARTUP_MINS) * 60) + jitter
		return flightphase.Parked, estimatedDuration, AbsInt(((DMINUS_PARKED_MINS - DMINUS_STARTUP_MINS) * 60) + jitter), delay

	// startup
	case diff > DMINUS_TAXIOUT_MINS && diff <= DMINUS_STARTUP_MINS:
		jitter := rand.IntN((STARTUP_JITTER_SECONDS*2)+1) - STARTUP_JITTER_SECONDS
		estimatedDuration := (AbsDiff(diff, DMINUS_TAXIOUT_MINS) * 60) + jitter
		return flightphase.Startup, estimatedDuration, AbsInt(((DMINUS_STARTUP_MINS - DMINUS_TAXIOUT_MINS) * 60) + jitter), delay

	// taxi out
	case diff > DMINUS_TAKEOFF_MINS && diff <= DMINUS_TAXIOUT_MINS:
		// note: no jitter for taxi phase as this will be recalculated based on distance
		estimatedDuration := (DMINUS_TAXIOUT_MINS - DMINUS_TAKEOFF_MINS) * 60
		return flightphase.TaxiOut, estimatedDuration, estimatedDuration, delay

	// takeoff - we do not permit initial spawn in takeoff phase due to runway lock charge so will be initialised in taxi out phase
	case diff >= DMINUS_CLIMBOUT_MINS && diff <= DMINUS_TAKEOFF_MINS:
		estimatedDuration := (DMINUS_TAXIOUT_MINS - DMINUS_TAKEOFF_MINS) * 60
		return flightphase.TaxiOut, estimatedDuration, AbsInt(DMINUS_TAXIOUT_MINS-DMINUS_TAKEOFF_MINS) * 60, delay

	// climbout
	case diff >= DMINUS_DEPARTURE_MINS && diff < DMINUS_CLIMBOUT_MINS:
		jitter := rand.IntN((CLIMBOUT_JITTER_SECONDS*2)+1) - CLIMBOUT_JITTER_SECONDS
		estimatedDuration := (AbsDiff(diff, DMINUS_DEPARTURE_MINS) * 60) + jitter
		return flightphase.Climbout, estimatedDuration, AbsInt(((DMINUS_CLIMBOUT_MINS - DMINUS_DEPARTURE_MINS) * 60) + jitter), delay

	// departure
	case diff >= DMINUS_CRUISE_MINS && diff <= DMINUS_DEPARTURE_MINS:
		jitter := rand.IntN((DEPARTURE_JITTER_SECONDS*2)+1) - DEPARTURE_JITTER_SECONDS
		estimatedDuration := (AbsDiff(diff, DMINUS_CRUISE_MINS) * 60) + jitter
		return flightphase.Departure, estimatedDuration, AbsInt(((DMINUS_DEPARTURE_MINS - DMINUS_CRUISE_MINS) * 60) + jitter), delay

	default:
		// cruise
		tta := e.timeDiffToArrival(f)
		jitter := rand.IntN((CRUISE_JITTER_SECONDS*2)+1) - CRUISE_JITTER_SECONDS
		remainingCruise := (AbsDiff(tta, AMINUS_APPROACH_MINS) * 60) + jitter
		return flightphase.Cruise, int(math.Max(0, float64(remainingCruise))),
			AbsInt(AbsInt(diff)+tta-AMINUS_ARRIVAL_MINS) * 60, delay
	}
}

func (e *D9TrafficEngine) determineInitialArrivalPhase(diff int, f *flightplan.ScheduledFlight) (flightphase.FlightPhase, int, int) {

	switch {
	// ARRIVAL:
	case diff > AMINUS_APPROACH_MINS && diff <= AMINUS_ARRIVAL_MINS:
		jitter := rand.IntN((ARRIVAL_JITTER_SECONDS*2)+1) - ARRIVAL_JITTER_SECONDS
		estimatedDuration := (AbsDiff(diff, AMINUS_APPROACH_MINS) * 60) + jitter
		return flightphase.Approach, AbsInt(((AMINUS_ARRIVAL_MINS - AMINUS_APPROACH_MINS) * 60) + jitter), estimatedDuration

	// APPROACH:
	case diff > AMINUS_FINAL_MINS && diff <= AMINUS_APPROACH_MINS:
		jitter := rand.IntN((APPROACH_JITTER_SECONDS*2)+1) - APPROACH_JITTER_SECONDS
		estimatedDuration := (AbsDiff(diff, AMINUS_FINAL_MINS) * 60) + jitter
		return flightphase.Approach, AbsInt(((AMINUS_APPROACH_MINS - AMINUS_FINAL_MINS) * 60) + jitter), estimatedDuration

	// FINAL: we do not permit initial spawn in final phase due to runway lock charge so will be initialised in approach phase
	case diff > AMINUS_LAND_MINS && diff <= AMINUS_FINAL_MINS:
		estimatedDuration := AbsInt(AMINUS_APPROACH_MINS-AMINUS_FINAL_MINS) * 60
		return flightphase.Approach, estimatedDuration, estimatedDuration

	// BRAKING: we do not permit initial spawn in braking phase due to runway lock charge so will be initialised in approach out phase
	case diff > AMINUS_BRAKING && diff <= AMINUS_LAND_MINS:
		estimatedDuration := AbsInt(AMINUS_APPROACH_MINS-AMINUS_FINAL_MINS) * 60
		return flightphase.Approach, estimatedDuration, estimatedDuration

	// TAXI IN:
	case diff > AMINUS_TAXIIN_MINS && diff <= AMINUS_BRAKING:
		// note: no jitter for taxi phase as this will be recalculated based on distance
		estimatedDuration := AbsInt(AMINUS_TAXIIN_MINS-AMINUS_SHUTDOWN_MINS) * 60
		return flightphase.TaxiIn, estimatedDuration, estimatedDuration

	// SHUTDOWN:
	case diff > AMINUS_SHUTDOWN_MINS && diff <= AMINUS_TAXIIN_MINS:
		jitter := rand.IntN((SHUTDOWN_JITTER_SECONDS*2)+1) - SHUTDOWN_JITTER_SECONDS
		estimatedDuration := (AbsDiff(diff, AMINUS_SHUTDOWN_MINS) * 60) + jitter
		return flightphase.Shutdown, AbsInt(AMINUS_TAXIIN_MINS-AMINUS_SHUTDOWN_MINS) * 60, estimatedDuration

	// PARKED:
	case diff >= AMINUS_PARKED_MINS && diff <= AMINUS_SHUTDOWN_MINS:
		jitter := rand.IntN((PARKED_JITTER_SECONDS*2)+1) - PARKED_JITTER_SECONDS
		estimatedDuration := (AbsDiff(diff, AMINUS_PARKED_MINS) * 60) + jitter
		return flightphase.Parked, AbsInt(AMINUS_SHUTDOWN_MINS-AMINUS_PARKED_MINS) * 60, estimatedDuration

	default:
		// CRUISE:
		tta := e.timeDiffToArrival(f)
		jitter := rand.IntN((CRUISE_JITTER_SECONDS*2)+1) - CRUISE_JITTER_SECONDS
		remainingCruise := (AbsDiff(tta, DMINUS_DEPARTURE_MINS) * 60) + jitter
		return flightphase.Cruise, int(math.Max(0, float64(remainingCruise))),
			AbsInt(AbsInt(diff)+tta-AMINUS_ARRIVAL_MINS) * 60
	}
}

func (e *D9TrafficEngine) tryExitHold(ac *atc.Aircraft, ap *atc.Airport) {
	qKey := normalizeRunwayKey(ap.ICAO, e.AirportConfig[ap.ICAO].Arrival)
	if len(e.RunwayQueues[qKey]) < TRAFFIC_MANAGEMENT_RUNWAY_QUEUE_THRESHOLD {
		// exit hold
		ac.Flight.AssignedHold = nil
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
	lat1 := geometry.DegToRad(origin.Lat)
	lon1 := geometry.DegToRad(origin.Lon)
	lat2 := geometry.DegToRad(dest.Lat)
	lon2 := geometry.DegToRad(dest.Lon)

	// Haversine formula
	diffLat := lat2 - lat1
	diffLon := lon2 - lon1

	a := math.Sin(diffLat/2)*math.Sin(diffLat/2) +
		math.Cos(lat1)*math.Cos(lat2)*
			math.Sin(diffLon/2)*math.Sin(diffLon/2)

	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))

	return geometry.EarthRadiusNM * c
}

func (e *D9TrafficEngine) getRunwayUtilityScore(rwy *atc.Runway, windDir float64, windSpeed float64) float64 {
	// 1. Start with the "Static" score (Length and Procedures)
	score := float64(len(rwy.SIDs)*10 + len(rwy.STARs)*10)
	score += rwy.Length / 1000.0

	// 2. Add the "Dynamic" Weather Component
	// Calculate the angular difference between wind and runway heading
	diff := windDir - rwy.Heading
	radDiff := geometry.DegToRad(diff)

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
			sidBearing := geometry.CalculateBearing(airport.Lat, airport.Lon, sid.Exit.Fix.Lat, sid.Exit.Fix.Lon)

			diff := math.Abs(geometry.BearingDiff(bearingToTarget, sidBearing))
			if diff < minDiff {
				minDiff = diff
				bestSID = sid
			}
		}

		if bestSID != nil {
			ac.Flight.AssignedSID = bestSID
			util.LogWithLabel(ac.Registration, "assigned %s SID", bestSID.Name)
			return
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
				starBearing := geometry.CalculateBearing(airport.Lat, airport.Lon, star.Entry.Fix.Lat, star.Entry.Fix.Lon)

				diff := math.Abs(geometry.BearingDiff(bearingToTarget, starBearing))
				if diff < minDiff {
					minDiff = diff
					bestSTAR = star
				}
			}

			if bestSTAR != nil {
				ac.Flight.AssignedSTAR = bestSTAR
				util.LogWithLabel(ac.Registration, "assigned %s STAR", bestSTAR.Name)
				return
			}
		} else {
			ac.Flight.Vectoring = true
			util.LogWithLabel(ac.Registration, "no arrival procedure assigned - aircraft will be vectored to runway by ATC")
			return
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
		util.LogWithLabel(reg, "dequeued from runway %s queue length is %d", lockKey, len(e.RunwayQueues[lockKey]))
	}
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

// assignRunwayAccessPoint assigns the runway access or exit point depending on whether the arrOrDep flag
// is set to arrival (0) or departure (1)
func (e *D9TrafficEngine) assignRunwayAccessPoint(ac *atc.Aircraft, ap *atc.Airport, arrOrDep int) {

	minDistToGate := math.MaxFloat64
	var selected *atc.AccessPoint
	spot := ac.Flight.AssignedParkingSpot

	rwy, exists := ap.Runways[ac.Flight.AssignedRunway]
	if !exists {
		util.LogWarnWithLabel(ac.Registration, "unable to assign runway access - runway name %s not found at %s",
			ac.Flight.AssignedRunway, ap.ICAO)
		return
	}

	var accessMap map[string]*atc.AccessPoint
	if arrOrDep == ARRIVAL_CONTEXT {
		accessMap = rwy.ArrivalAccess
		//TODO: decide on if we want logic to consider IsHighSpeed, aircraft size, IsNearEnd etc. for arrivals
	} else {
		accessMap = rwy.DepartureAccess
	}

	for _, access := range accessMap {
		// Which of these qualified entries is closest to our PARKED position?
		dist := geometry.DistNM(spot.Lat, spot.Lon, access.Coord.Lat, access.Coord.Lon)
		if dist < minDistToGate {
			minDistToGate = dist
			selected = access
		}
	}

	if arrOrDep == ARRIVAL_CONTEXT {
		ac.Flight.ArrivalAccess = selected
	} else {
		ac.Flight.DepartureAccess = selected
	}

}
