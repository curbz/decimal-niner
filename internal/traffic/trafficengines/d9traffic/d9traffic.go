package d9traffic

import (
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"strings"
	"time"

	"github.com/curbz/decimal-niner/internal/atc"
	"github.com/curbz/decimal-niner/internal/constants"
	"github.com/curbz/decimal-niner/internal/flightclass"
	"github.com/curbz/decimal-niner/internal/flightphase"
	"github.com/curbz/decimal-niner/internal/flightplan"
	"github.com/curbz/decimal-niner/internal/logger"
	"github.com/curbz/decimal-niner/internal/traffic"
	"github.com/curbz/decimal-niner/pkg/geometry"
	"github.com/curbz/decimal-niner/pkg/util"
	"github.com/mohae/deepcopy"
)

type D9TrafficEngine struct {
	traffic.CommonTrafficEngine
	AirportSchedules map[string]*AirportTimeline
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
	AMINUS_APPROACH_MINS = 6
	AMINUS_FINAL_MINS    = 2
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
	DEPARTURE_JITTER_SECONDS = 60
	CRUISE_JITTER_SECONDS    = 120
	ARRIVAL_JITTER_SECONDS   = 60
	APPROACH_JITTER_SECONDS  = 30

	SHUTDOWN_JITTER_SECONDS = 60

	RUNWAY_LOCK_TIMEOUT_SECONDS = 300 // Safety mechanism in case aircraft does not voluntarily release the lock

	HOLDING_MIN_DURATION_MINS                     = 4
	TRAFFIC_MANAGEMENT_RUNWAY_QUEUE_THRESHOLD     = 6   // both arrivals and departure
	TRAFFIC_MANAGEMENT_PER_AIRCRAFT_DELAY_SECONDS = 180 // delay time multiplied by current queue length
	GOAROUND_TO_HOLD_PROBABILITY_FACTOR           = 0.3
	// maximum number of aircraft allowed on approach for a single airport before
	// new arrivals are sent to hold
	MAX_APPROACH_ON_APPROACH = 4
)

func New(cfgPath string) (atc.TrafficEngine, error) {
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

func (e *D9TrafficEngine) SetATCService(atcService *atc.Service) {
	e.AtcService = atcService
	if atcService != nil {
		atcService.RegisterTrafficEngine(e)
	}
}

func (e *D9TrafficEngine) Start() {
	ticker := time.NewTicker(10 * time.Second)
	var lastSpawnMin int = -1 // Track the last minute we checked for spawns

	go func() {
		for range ticker.C {
			start := time.Now()
			currSimZTime := e.AtcService.GetCurrentZuluTime()

			// Time components
			day := int(currSimZTime.Weekday())
			hour := currSimZTime.Hour()
			currentMin := currSimZTime.Minute()

			relevantICAOs := e.getRelevantICAOs()

			// --- 1. SLOW CYCLE (Once per Minute) ---
			// Only check for new spawns and runway refreshes if the minute has rolled over
			if currentMin != lastSpawnMin {
				for _, icao := range relevantICAOs {
					ap := e.AtcService.GetAirportByICAO(icao)
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

func (e *D9TrafficEngine) Enrich(ac *atc.Aircraft, ap *atc.Airport) {
	//NOOP for D9TrafficEngine
}

func (e *D9TrafficEngine) needsRunwayRefresh(ap *atc.Airport) bool {
	config, exists := e.AirportConfig[ap.ICAO]
	if !exists {
		return true
	} // Initial load

	currentWeather := e.AtcService.GetWeatherState()

	// Check if wind shifted by more than configured degrees
	// OR wind speed changed by more than configured knots
	dirDelta := math.Abs(currentWeather.Wind.Direction - config.LastWindDir)
	speedDelta := math.Abs(currentWeather.Wind.Speed - config.LastWindSpeed)

	return dirDelta > constants.WindDirShiftDeg || speedDelta > constants.WindSpeedDeltaKts
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
			timeI := (timeline.Departures[i].DepartureHour * 60) + timeline.Departures[i].DepartureMin
			timeJ := (timeline.Departures[j].DepartureHour * 60) + timeline.Departures[j].DepartureMin
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

	for _, ctrl := range e.AtcService.UserState.ActiveFacilities {
		// We only care about airport-specific controllers (TWR, GND, DEL, etc.)
		// Center/Approach might not have a single ICAO, so we filter.
		if ctrl.ICAO != "" {
			icaoMap[ctrl.ICAO] = true
		}
	}

	// if the user is on the ground, include the nearest airport as a fallback for visual/proximity traffic
	if e.AtcService.UserState.IsOnGround && e.AtcService.UserState.NearestAirport != nil {
		icaoMap[e.AtcService.UserState.NearestAirport.ICAO] = true
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

			fMins := (f.DepartureHour * 60) + f.DepartureMin

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

func (e *D9TrafficEngine) timeDiffToScheduledDeparture(f *flightplan.ScheduledFlight) int {
	// Calculate diff at spawn time
	currSimZTime := e.AtcService.GetCurrentZuluTime()
	h, m, _ := currSimZTime.Clock()
	nowMins := h*60 + m
	depMins := (f.DepartureHour * 60) + f.DepartureMin
	diff := depMins - nowMins
	return diff
}

func (e *D9TrafficEngine) spawnDepartureTraffic(f *flightplan.ScheduledFlight) {

	ttd := e.timeDiffToScheduledDeparture(f)
	initialPhase, remainingDurSecs, fullDurationSecs, delay := e.determineInitialDeparturePhase(ttd, f)
	if initialPhase == flightphase.Unknown {
		return
	}
	ip := initialPhase.Index()

	airport := e.AtcService.Airports[f.IcaoOrigin]
	currSimZTime := e.AtcService.GetCurrentZuluTime()

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
			CruiseAlt: f.CruiseAlt * 100,
			Schedule:  f,
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

	// Set all pre-requisite states - strict order is important
	e.AtcService.SetFlightPhaseClass(newAc)
	if ip < flightphase.Takeoff.Index() {
		// assign departure gate - do this BEFORE assigning the departure runway access as this may influence the selected access point
		e.assignParking(newAc, airport)
	}

	// assign departure runway
	newAc.Flight.AssignedRunwayName = e.AirportConfig[airport.ICAO].Departure.Name
	newAc.Flight.AssignedRunway = e.AtcService.GetAirportRunway(airport, newAc.Flight.AssignedRunwayName)
	// assign SID for departure
	e.AtcService.AssignSID(newAc, airport, newAc.Flight.AssignedRunway)

	if ip < flightphase.Takeoff.Index() {
		// assign departure runway access - must be done after parking assignment
		e.AtcService.AssignRunwayAccessPoint(newAc, airport, atc.DEPARTURE_CONTEXT)
	}

	newAc.Flight.Phase.Transition = transitionTime // BACKDATED
	newAc.Flight.Phase.EstimatedNextTransition = currSimZTime.Add(time.Duration(remainingDurSecs) * time.Second)
	newAc.Flight.Phase.TotalDuration = time.Duration(fullDurationSecs) * time.Second
	// set initial altitude
	e.assignPhaseInitialAltitude(newAc, ip)

	// Determine which positioning functions should handle the initial placement
	if ip >= flightphase.Climbout.Index() {
		// If Cruise, flip to destination (arrival) runway BEFORE initializing, this will be revaluated at the start of the arrival phase
		if ip == flightphase.Cruise.Index() {
			//TODO: consider what to do when a departure spawn results in a cruise phase -terminate tracking?
			rwy := e.getFallbackRunway(f.IcaoDest, atc.ARRIVAL_CONTEXT)
			destApt := e.AtcService.Airports[f.IcaoDest]
			newAc.Flight.AssignedRunwayName = rwy.Name
			newAc.Flight.AssignedRunway = e.AtcService.GetAirportRunway(destApt, newAc.Flight.AssignedRunwayName)
			// assign destination procedure
			e.AtcService.AssignSTAR(newAc, destApt, rwy)
			e.updateCruisePosition(newAc)
		} else {
			e.updateLinearPosition(newAc, airport)
		}
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
			e.updateLinearPosition(newAc, airport)

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

	tta := e.timeDiffToScheduledArrival(f)
	initialPhase, remainingDurSecs, fullDurationSecs := e.determineInitialArrivalPhase(tta, f)
	initialPhaseIdx := initialPhase.Index()

	airport := e.AtcService.Airports[f.IcaoDest]

	currSimZTime := e.AtcService.GetCurrentZuluTime()

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
			CruiseAlt: f.CruiseAlt * 100,
			Schedule:  f,
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
	e.AtcService.SetFlightPhaseClass(newAc)
	// arrival runway must be assigned BEFORE assigning runway access point
	if initialPhaseIdx <= flightphase.TaxiIn.Index() {
		newAc.Flight.AssignedRunwayName = e.AirportConfig[airport.ICAO].Arrival.Name
		newAc.Flight.AssignedRunway = e.AtcService.GetAirportRunway(airport, newAc.Flight.AssignedRunwayName)
	}
	if initialPhaseIdx >= flightphase.Braking.Index() && initialPhaseIdx <= flightphase.Shutdown.Index()+1 {
		// assign parking BEFORE runway exit point as this may influence the selected exit
		e.assignParking(newAc, airport)
		e.AtcService.AssignRunwayAccessPoint(newAc, airport, atc.ARRIVAL_CONTEXT)
	}

	if initialPhaseIdx >= flightphase.Cruise.Index() && initialPhaseIdx <= flightphase.Approach.Index() {
		e.AtcService.AssignSTAR(newAc, airport, newAc.Flight.AssignedRunway)
	}

	newAc.Flight.Phase.TotalDuration = time.Duration(fullDurationSecs) * time.Second
	elapsedOffset := math.Max(0, float64(fullDurationSecs)-float64(remainingDurSecs))
	newAc.Flight.Phase.Transition = currSimZTime.Add(-time.Duration(elapsedOffset) * time.Second)
	newAc.Flight.Phase.EstimatedNextTransition = currSimZTime.Add(time.Duration(remainingDurSecs) * time.Second)
	e.assignPhaseInitialAltitude(newAc, initialPhaseIdx)

	switch {
	case initialPhaseIdx == flightphase.Cruise.Index():
		// Handles airport-to-airport interpolation and TOD calculation
		e.updateCruisePosition(newAc)

	case initialPhaseIdx >= flightphase.Arrival.Index() && initialPhaseIdx <= flightphase.Braking.Index():
		// Handles STAR/Approach/Final/Braking relative to the arrival runway
		e.updateLinearPosition(newAc, airport)

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

// getFallbackRunway tries to get the active runway for the given flight context, if not set/available, fallsback to any runway.
// Returns nil if the fallback fails.
func (e *D9TrafficEngine) getFallbackRunway(icao string, arrOrDep int) *atc.Runway {
	// 1. Try your specific airport config first
	if config, found := e.AirportConfig[icao]; found {
		if arrOrDep == atc.ARRIVAL_CONTEXT {
			return config.Arrival
		} else {
			return config.Departure
		}
	}

	// 2. Fallback: Get the first available runway from the global airport data
	if apt, found := e.AtcService.Airports[icao]; found && len(apt.Runways) > 0 {
		// Just pick the first one available as a coordinate anchor
		for _, r := range apt.Runways {
			return r
		}
	}
	return nil
}

// assignPhaseInitialAltitude sets the Phase.InitialAltitude value which is the value that defines the target altitude for
// the aircraft at the beginning of the provided phase.
func (e *D9TrafficEngine) assignPhaseInitialAltitude(ac *atc.Aircraft, phase int) {

	phaseInitAlt := 0.0

	ap, flightContext := e.getActiveAirport(ac)
	icao := ap.ICAO
	rwy := e.AtcService.GetAirportRunway(ap, ac.Flight.AssignedRunwayName)
	if rwy == nil {
		rwy = e.getFallbackRunway(icao, flightContext)
	}
	ac.Flight.AssignedRunway = rwy

	p := flightphase.FlightPhase(phase)

	switch p {
	case flightphase.Takeoff, flightphase.Braking:
		if rwy != nil {
			phaseInitAlt = atc.GetElevation(ap, rwy)
		}

	case flightphase.Climbout:
		phaseInitAlt = atc.GetElevation(ap, rwy) + float64(constants.RunwayElevationOffsetFt)

	case flightphase.Departure:
		if sid := ac.Flight.AssignedSID; sid != nil && sid.Entry.ConstraintAlt > 0 {
			phaseInitAlt = float64(sid.Entry.ConstraintAlt)
		} else {
			phaseInitAlt = atc.GetElevation(ap, rwy) + float64(constants.DefaultClimbExitDepartureEntryAltFt)
		}

	case flightphase.Cruise:
		phaseInitAlt = float64(ac.Flight.CruiseAlt)
		if sid := ac.Flight.AssignedSID; sid != nil && sid.Exit.ConstraintAlt > 0 {
			phaseInitAlt = float64(sid.Exit.ConstraintAlt)
			if float64(ac.Flight.CruiseAlt) < phaseInitAlt {
				util.LogDebugWithLabel(ac.Registration, "bumping scheduled cruise altitude from %d to %sd as assigned SID is higher",
					ac.Flight.CruiseAlt, int(phaseInitAlt))
				ac.Flight.CruiseAlt = int(phaseInitAlt)
			}
		} else {
			phaseInitAlt = atc.GetMinSafeAltitude(
				math.Max(
					float64(constants.DefaultDepartureExitCruiseEntryAltFt),
					float64(ac.Flight.CruiseAlt)), ap)
		}

	case flightphase.Arrival:
		if star := ac.Flight.AssignedSTAR; star != nil && star.Entry.ConstraintAlt > 0 {
			phaseInitAlt = float64(star.Entry.ConstraintAlt)
		} else {
			phaseInitAlt = atc.GetMinSafeAltitude(float64(constants.DefaultCruiseExitArrivalEntryAltFt), ap)
		}

	case flightphase.Approach:
		if star := ac.Flight.AssignedSTAR; star != nil && star.Exit.ConstraintAlt > 0 {
			phaseInitAlt = float64(star.Exit.ConstraintAlt)
		} else {
			phaseInitAlt = atc.GetElevation(ap, rwy) + float64(constants.DefaultArrivalExitApproachEntryAltFt)
		}

	case flightphase.Final:
		if rwy != nil && rwy.FAFalt > 0 {
			phaseInitAlt = float64(rwy.FAFalt)
		} else {
			phaseInitAlt = atc.GetElevation(ap, rwy) + float64(constants.DefaultApproachExitFinalEntryAltFt)
		}
	}

	if phaseInitAlt == 0.0 {
		phaseInitAlt = ap.Elevation
	}

	ac.Flight.Phase.InitialAltitude = phaseInitAlt
	util.LogDebugWithLabel(ac.Registration, "initial altitude for phase %s set to %f", flightphase.FlightPhase(phase).String(),
		ac.Flight.Phase.InitialAltitude)
}

func (e *D9TrafficEngine) CheckForSubPhaseChange(ac *atc.Aircraft) {

	// if last check position has not yet been set, set it now so that we have something to compare against and return
	if ac.Flight.LastCheckedPosition.Lat == 0 && ac.Flight.LastCheckedPosition.Long == 0 {
		ac.Flight.LastCheckedPosition = ac.Flight.Position
		return
	}

	switch flightphase.FlightPhase(ac.Flight.Phase.Current) {
	case flightphase.Cruise:
		// check for possible sector change
		e.CheckForCruiseSectorChange(ac)
		// check for TOD
		e.CheckForTOD(ac)
	}
}

func (e *D9TrafficEngine) CheckForTOD(ac *atc.Aircraft) {
	//NOOP
}

func getActiveAircraftKey(ac *atc.Aircraft) string {
	return fmt.Sprintf("%s_%d", ac.Registration, ac.Flight.Number)
}

func (e *D9TrafficEngine) timeDiffToScheduledArrival(f *flightplan.ScheduledFlight) int {
	currSimZTime := e.AtcService.GetCurrentZuluTime()
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

	currSimZTime := e.AtcService.GetCurrentZuluTime()

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
		airport = e.AtcService.Airports[targetICAO]

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
			// Check for Startup transition
			if ac.Flight.Phase.Class == flightclass.PostflightParked {
				e.endFlight(ac) // Cleanup logic
				continue
			}
			if ac.Flight.Phase.PositionComplete || currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
				dur := (DMINUS_STARTUP_MINS - DMINUS_TAXIOUT_MINS) * 60
				e.transitionToPhase(ac, flightphase.Startup, dur, PARKED_JITTER_SECONDS)
			}

		case flightphase.Startup:
			// Check for TaxiOut transition
			if ac.Flight.Phase.PositionComplete || currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
				ac.Flight.AssignedRunwayName = e.AirportConfig[airport.ICAO].Departure.Name
				ac.Flight.AssignedRunway = e.AtcService.GetAirportRunway(airport, ac.Flight.AssignedRunwayName)
				e.AtcService.AssignSID(ac, airport, ac.Flight.AssignedRunway)
				e.AtcService.AssignRunwayAccessPoint(ac, airport, atc.DEPARTURE_CONTEXT)
				dur := e.calculateTaxiDuration(ac, atc.DEPARTURE_CONTEXT)
				if ac.Flight.AssignedParkingSpot != nil {
					e.releaseParking(f.IcaoOrigin, ac.Flight.AssignedParkingSpot)
				}
				e.transitionToPhase(ac, flightphase.TaxiOut, dur, 0)
			}

		case flightphase.TaxiOut:
			// Position-driven Takeoff transition: only transition when position indicates arrival at runway
			if ac.Flight.Phase.PositionComplete {
				if !e.getRunwayLock(airport, e.AirportConfig[airport.ICAO].Departure, ac) {
					util.LogWithLabel(ac.Registration, "active departure runway %s is occupied at %s - remaining in TaxiOut phase",
						e.AirportConfig[airport.ICAO].Departure.Name, airport.ICAO)
					continue
				}
				dur := (DMINUS_TAKEOFF_MINS - DMINUS_CLIMBOUT_MINS) * 60
				e.transitionToPhase(ac, flightphase.Takeoff, dur, 0)
				rwy := ac.Flight.AssignedRunway
				if rwy != nil {
					ac.Flight.Position.Lat = rwy.Lat
					ac.Flight.Position.Long = rwy.Lon
					ac.Flight.Position.Heading = geometry.NormalizeHeading(rwy.Heading)
					ac.Flight.Position.Altitude = atc.GetElevation(airport, rwy)
				} else {
					util.LogWarnWithLabel(ac.Registration,
						"unable to position aircraft at runway threshold - runway %s not found at airport %s",
						ac.Flight.AssignedRunwayName, airport.ICAO)
				}
				e.updateLinearPosition(ac, airport)
			} else {
				e.updateTaxiPosition(ac, airport, true)
			}

		case flightphase.Takeoff:
			// Position-driven Climbout transition
			if ac.Flight.Phase.PositionComplete {
				e.releaseRunwayLock(airport, e.AirportConfig[airport.ICAO].Departure, ac)
				dur := (DMINUS_CLIMBOUT_MINS - DMINUS_DEPARTURE_MINS) * 60
				e.transitionToPhase(ac, flightphase.Climbout, dur, CLIMBOUT_JITTER_SECONDS)
			}
			e.updateLinearPosition(ac, airport)

		case flightphase.Climbout:
			// Position-driven Departure transition
			if ac.Flight.Phase.PositionComplete {
				dur := (DMINUS_DEPARTURE_MINS - DMINUS_CRUISE_MINS) * 60
				e.transitionToPhase(ac, flightphase.Departure, dur, DEPARTURE_JITTER_SECONDS)
			}
			e.updateLinearPosition(ac, airport)

		case flightphase.Departure:
			// Position-driven Cruise transition
			if ac.Flight.Phase.PositionComplete {
				tta := e.timeDiffToScheduledArrival(f) // Minutes until scheduled arrival
				dur := (tta - AMINUS_ARRIVAL_MINS) * 60
				e.transitionToPhase(ac, flightphase.Cruise, dur, CRUISE_JITTER_SECONDS)
				e.updateCruisePosition(ac)
			} else {
				e.updateLinearPosition(ac, airport)
			}

		case flightphase.Cruise:
			// Update cruise position first so position-driven readiness can be set
			e.updateCruisePosition(ac)
			// Check for Arrival transition (position-driven readiness only)
			if ac.Flight.Phase.PositionComplete {
				// we are transitioning to arrival so assign or replace any earlier assigned runway
				ac.Flight.AssignedRunwayName = e.AirportConfig[airport.ICAO].Arrival.Name
				ac.Flight.AssignedRunway = e.AtcService.GetAirportRunway(airport, ac.Flight.AssignedRunwayName)
				if ac.Flight.AssignedSTAR == nil && ac.Flight.Vectoring == false {
					e.AtcService.AssignSTAR(ac, airport, ac.Flight.AssignedRunway)
				}
				durSecs := (AMINUS_APPROACH_MINS - AMINUS_FINAL_MINS) * 60
				e.transitionToPhase(ac, flightphase.Arrival, durSecs, ARRIVAL_JITTER_SECONDS)
				e.updateLinearPosition(ac, airport)
				util.LogWithLabel(ac.Registration, "commencing arrival into %s (position-driven)",
					f.IcaoDest)
			} else {
				// still cruising
				tta := e.timeDiffToScheduledArrival(f) // Minutes until scheduled arrival
				util.LogWithLabel(ac.Registration, "cruising... %d minutes until arrival window",
					tta-AMINUS_APPROACH_MINS)
			}

		case flightphase.Arrival:
			// Position-driven Approach transition
			if ac.Flight.Phase.PositionComplete {
				// If there are already too many aircraft on approach for this airport,
				// send this arrival to hold instead of commencing approach.
				approachCount := 0
				for _, other := range e.ActiveAircraft {
					if other == nil || other.Flight.Schedule == nil {
						continue
					}
					// Only count aircraft that are assigned to the same runway and are in the Approach phase
					if other.Flight.AssignedRunwayName == ac.Flight.AssignedRunwayName && flightphase.FlightPhase(other.Flight.Phase.Current) == flightphase.Approach {
						approachCount++
					}
				}
				if approachCount > MAX_APPROACH_ON_APPROACH {
					// send to hold due to approach saturation
					e.AtcService.AssignHold(ac, airport.ICAO)
					dur := (HOLDING_MIN_DURATION_MINS * 60) + 60
					e.transitionToPhase(ac, flightphase.Holding, dur, 0)
					e.updateHoldingPosition(ac, e.AirportConfig[airport.ICAO].Arrival)
				} else {
					qKey := normalizeRunwayKey(airport.ICAO, e.AirportConfig[airport.ICAO].Arrival)
					if len(e.RunwayQueues[qKey]) >= TRAFFIC_MANAGEMENT_RUNWAY_QUEUE_THRESHOLD {
						// send to hold
						e.AtcService.AssignHold(ac, airport.ICAO)
						dur := (HOLDING_MIN_DURATION_MINS * 60) + 60
						e.transitionToPhase(ac, flightphase.Holding, dur, 0)
						e.updateHoldingPosition(ac, e.AirportConfig[airport.ICAO].Arrival)
					} else {
						// start approach
						dur := (AMINUS_APPROACH_MINS - AMINUS_FINAL_MINS) * 60
						e.transitionToPhase(ac, flightphase.Approach, dur, APPROACH_JITTER_SECONDS)
						e.updateLinearPosition(ac, airport)
					}
				}
			} else {
				e.updateLinearPosition(ac, airport)
			}

		case flightphase.Holding:
			if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
				e.tryExitHold(ac, airport)
			} else {
				e.updateHoldingPosition(ac, e.AirportConfig[airport.ICAO].Arrival)
			}

		case flightphase.Approach:
			// Position-driven Final transition
			if ac.Flight.Phase.PositionComplete {
				if !e.getRunwayLock(airport, e.AirportConfig[airport.ICAO].Arrival, ac) {
					// Runway is occupied; send to holding instead of remaining in approach.
					e.AtcService.AssignHold(ac, airport.ICAO)
					dur := (HOLDING_MIN_DURATION_MINS * 60) + 60
					e.transitionToPhase(ac, flightphase.Holding, dur, 0)
					e.updateHoldingPosition(ac, e.AirportConfig[airport.ICAO].Arrival)
					util.LogWithLabel(ac.Registration, "on approach: active arrival runway %s is occupied at %s - diverting to hold",
						e.AirportConfig[airport.ICAO].Arrival.Name, airport.ICAO)
				} else {
					dur := (AMINUS_FINAL_MINS - AMINUS_LAND_MINS) * 60
					e.transitionToPhase(ac, flightphase.Final, dur, 0)
				}
			}
			e.updateLinearPosition(ac, airport)

		case flightphase.Final:
			// Position-driven Braking transition
			if ac.Flight.Phase.PositionComplete {
				if !e.getRunwayLock(airport, e.AirportConfig[airport.ICAO].Arrival, ac) {
					// go-around
					util.LogWithLabel(ac.Registration, "on final: active arrival runway %s is occupied at %s - initiating go-around",
						e.AirportConfig[airport.ICAO].Arrival.Name, airport.ICAO)
					e.transitionToPhase(ac, flightphase.GoAround, 80, 0)
					e.updateGoAroundPosition(ac, airport)
				} else {
					// transition to braking phase
					e.assignParking(ac, airport)
					e.AtcService.AssignRunwayAccessPoint(ac, airport, atc.ARRIVAL_CONTEXT)
					dur := (AMINUS_LAND_MINS - AMINUS_BRAKING) * 60
					e.transitionToPhase(ac, flightphase.Braking, dur, 0)
					ac.Flight.Position.Altitude = atc.GetElevation(airport, e.AirportConfig[airport.ICAO].Arrival)
					e.updateLinearPosition(ac, airport)
				}
			} else {
				e.updateLinearPosition(ac, airport)
			}

		case flightphase.GoAround:
			if ac.Flight.Phase.PositionComplete {
				e.releaseRunwayLock(airport, e.AirportConfig[airport.ICAO].Arrival, ac)
				// Randomized hold or back into approach flow
				if rand.Float32() < GOAROUND_TO_HOLD_PROBABILITY_FACTOR {
					// send to hold
					e.AtcService.AssignHold(ac, airport.ICAO)
					dur := (HOLDING_MIN_DURATION_MINS * 60) + 60
					e.transitionToPhase(ac, flightphase.Holding, dur, 0)
					e.updateHoldingPosition(ac, e.AirportConfig[airport.ICAO].Arrival)
				} else {
					// send back around to approach
					e.tryExitHold(ac, airport)
				}
			} else {
				e.updateGoAroundPosition(ac, airport)
			}

		case flightphase.Braking:
			if ac.Flight.Phase.PositionComplete {
				e.releaseRunwayLock(airport, e.AirportConfig[airport.ICAO].Arrival, ac)
				dur := e.calculateTaxiDuration(ac, atc.ARRIVAL_CONTEXT)
				e.transitionToPhase(ac, flightphase.TaxiIn, dur, 0)
			} else {
				e.updateLinearPosition(ac, airport)
			}

		case flightphase.TaxiIn:
			// Position-driven TaxiIn->Shutdown
			if ac.Flight.Phase.PositionComplete {
				e.positionAtDestParking(ac)
				dur := (AMINUS_TAXIIN_MINS - AMINUS_SHUTDOWN_MINS) * 60
				e.transitionToPhase(ac, flightphase.Shutdown, dur, SHUTDOWN_JITTER_SECONDS)
			} else {
				e.updateTaxiPosition(ac, airport, false)
			}

		case flightphase.Shutdown:
			if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
				e.positionAtDestParking(ac)
				dur := (AMINUS_SHUTDOWN_MINS - AMINUS_PARKED_MINS) * 60
				e.transitionToPhase(ac, flightphase.Parked, dur, PARKED_JITTER_SECONDS)
			}
		}

		// --- LOGGING & STATE SYNC ---

		if ac.Flight.Phase.Current != ac.Flight.Phase.Previous {
			// phase has changed
			logMsg := ""
			if e.initialised {
				e.AtcService.NotifyFlightPhaseChange(ac)
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
			// check for subphases
			e.CheckForSubPhaseChange(ac)
		}
	}

	e.initialised = true
}

func (e *D9TrafficEngine) transitionToPhase(ac *atc.Aircraft, next flightphase.FlightPhase, baseSecs int, jitterSecs int) {

	currSimZTime := e.AtcService.GetCurrentZuluTime()

	// Capture the 'Exit' altitude of the current phase to be the 'Start' of the next
	ac.Flight.Phase.InitialAltitude = ac.Flight.Position.Altitude
	if ac.Flight.Phase.InitialAltitude <= 0 {
		e.assignPhaseInitialAltitude(ac, next.Index())
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
	// reset any position-driven completion marker when entering a new phase
	ac.Flight.Phase.PositionComplete = false
	e.AtcService.SetFlightPhaseClass(ac)
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

// updateLinearPosition handles the position updates for Takeoff, Climbout, Departure, Arrival, Approach, Final, and Braking phases.
// The provided airport should be the relevant airport for the current phase (origin for departure phases, destination for arrival phases) as the logic relies on runway and procedure data from that airport.
func (e *D9TrafficEngine) updateLinearPosition(ac *atc.Aircraft, ctxAp *atc.Airport) {

	currSimZTime := e.AtcService.GetCurrentZuluTime()
	elapsed := currSimZTime.Sub(ac.Flight.Phase.Transition).Seconds()
	totalDuration := ac.Flight.Phase.TotalDuration.Seconds()
	if totalDuration <= 0 {
		return
	}

	// Calculate progress based on nominal ground speed for the current phase and aircraft size class.
	// This yields more realistic movement (distance-based) rather than purely time-based interpolation.
	var progress float64
	// If the phase duration has elapsed, force completion to ensure deterministic testability and
	// to respect cases where phase timing indicates completion regardless of speed caps.
	if elapsed >= totalDuration {
		progress = 1.0
	} else {
		// Default to time-based progress as a safe initial value; will be overridden by distance-based calculation below.
		progress = math.Min(1.0, elapsed/totalDuration)
	}
	phase := flightphase.FlightPhase(ac.Flight.Phase.Current)

	var startPos, targetPos atc.Position
	var phaseInitAlt, targetAlt float64
	var heading float64

	// Use the stored InitialAltitude as the floor for vertical movement
	phaseInitAlt = ac.Flight.Phase.InitialAltitude

	rwy := ac.Flight.AssignedRunway
	if rwy == nil {
		util.LogErrWithLabel(ac.Registration, "updateLinerPosition failed - no runway assigned")
		return
	}

	// Determine Runway Length in NM (Default to 1.2 if missing ~ 2200 meters)
	rwyLengthNM := constants.RunwayLengthNM
	if rwy.Length > 0 {
		rwyLengthNM = rwy.Length * constants.MetersToNM
	}

	// Initialize with current position as a safety "anchor". These values should get modified by the
	// switch/case process
	startPos = ac.Flight.Position
	targetPos = ac.Flight.Position // Default to no movement

	switch phase {
	case flightphase.Takeoff:
		//Start: snap to runway threshold
		startPos = atc.Position{Lat: rwy.Lat, Long: rwy.Lon}
		// Prefer an assigned departure access point as the rollout target when available
		if ac.Flight.DepartureAccess != nil {
			// Convert AccessPoint coordinate to Position
			targetPos.Lat = ac.Flight.DepartureAccess.Coord.Lat
			targetPos.Long = ac.Flight.DepartureAccess.Coord.Lon
		} else {
			targetPos.Lat, targetPos.Long = geometry.Project(rwy.Lat, rwy.Lon, rwy.Heading, rwyLengthNM)
		}
		// Smooth rotation altitude above runway elevation to avoid abrupt jump into climbout
		rwyElev := atc.GetElevation(ctxAp, rwy)
		rotationAlt := rwyElev + 150.0 // small positive altitude at rotation
		targetAlt = rotationAlt
		heading = rwy.Heading

	case flightphase.Climbout:
		// Start: End of Runway
		startPos.Lat, startPos.Long = geometry.Project(rwy.Lat, rwy.Lon, rwy.Heading, rwyLengthNM)
		if sid := ac.Flight.AssignedSID; sid != nil && sid.Entry.Fix.Lat != 0 {
			targetPos = atc.Position{Lat: sid.Entry.Fix.Lat, Long: sid.Entry.Fix.Lon}
			targetAlt = float64(sid.Entry.ConstraintAlt)
		} else {
			targetPos.Lat, targetPos.Long = geometry.Project(rwy.Lat, rwy.Lon, rwy.Heading, constants.DefaultClimbExitDepartureEntryNM)
		}
		heading = rwy.Heading
		if targetAlt == 0 {
			targetAlt = atc.GetElevation(ctxAp, rwy) + float64(constants.DefaultClimbExitDepartureEntryAltFt)
		}

	case flightphase.Departure:
		startPos.Lat, startPos.Long = geometry.Project(rwy.Lat, rwy.Lon, rwy.Heading, constants.DefaultClimbExitDepartureEntryNM)
		targetPos.Lat, targetPos.Long = geometry.Project(rwy.Lat, rwy.Lon, rwy.Heading, constants.DefaultDepartureExitCruiseEntryNM)
		if sid := ac.Flight.AssignedSID; sid != nil {
			if sid.Entry.Fix.Lat != 0 {
				startPos = atc.Position{Lat: sid.Entry.Fix.Lat, Long: sid.Entry.Fix.Lon}
			}
			if sid.Exit.Fix.Lat != 0 {
				targetPos = atc.Position{Lat: sid.Exit.Fix.Lat, Long: sid.Exit.Fix.Lon}
				targetAlt = float64(sid.Exit.ConstraintAlt)
			}
		}
		heading = geometry.CalculateBearing(startPos.Lat, startPos.Long, targetPos.Lat, targetPos.Long)
		if targetAlt == 0 {
			targetAlt = atc.GetMinSafeAltitude(float64(constants.DefaultDepartureExitCruiseEntryAltFt), ctxAp)
		}

	case flightphase.Arrival:
		// 1. Establish initial entry point
		if star := ac.Flight.AssignedSTAR; star != nil && star.Entry.Fix.Lat != 0 {
			startPos = atc.Position{Lat: star.Entry.Fix.Lat, Long: star.Entry.Fix.Lon}
		} else {
			// Project out on extended centerline
			startPos.Lat, startPos.Long = geometry.Project(rwy.Lat, rwy.Lon, geometry.NormalizeHeading(rwy.Heading+180.0), constants.DefaultCruiseExitArrivalEntryNM)
		}

		// 2. Establish target exit point of arrival phase (15NM gate)
		if star := ac.Flight.AssignedSTAR; star != nil && star.Exit.Fix.Lat != 0 {
			targetPos = atc.Position{Lat: star.Exit.Fix.Lat, Long: star.Exit.Fix.Lon}
			targetAlt = float64(star.Exit.ConstraintAlt)
		} else {
			// No STAR: Target/Start at the 15NM extended centerline point
			centerline15NMLat, centerline15NMLon := geometry.Project(rwy.Lat, rwy.Lon, geometry.NormalizeHeading(rwy.Heading+180.0), constants.DefaultArrivalExitApproachEntryNM)

			offsetHeading := geometry.NormalizeHeading(rwy.Heading + 90.0)
			if len(ac.Registration) > 0 && ac.Registration[len(ac.Registration)-1]%2 == 0 {
				offsetHeading = geometry.NormalizeHeading(rwy.Heading - 90.0)
			}

			// Push the aircraft 2.88 NM out to the side instead of 5.0 NM
			// This mathematically forces a standard 30-degree intercept track to the 10NM gate!
			targetPos.Lat, targetPos.Long = geometry.Project(centerline15NMLat, centerline15NMLon, offsetHeading, constants.InterceptLOCSegmentANM)
		}
		heading = geometry.CalculateBearing(startPos.Lat, startPos.Long, targetPos.Lat, targetPos.Long)
		if targetAlt == 0 {
			targetAlt = atc.GetElevation(ctxAp, rwy) + float64(constants.DefaultArrivalExitApproachEntryAltFt)
		}

	case flightphase.Approach:
		// 1. Establish the Entry point (Must perfectly match Arrival's exit target!)
		if star := ac.Flight.AssignedSTAR; star != nil && star.Exit.Fix.Lat != 0 {
			startPos = atc.Position{Lat: star.Exit.Fix.Lat, Long: star.Exit.Fix.Lon}
		} else {
			// No STAR: Target/Start at the 15NM extended centerline point
			centerline15NMLat, centerline15NMLon := geometry.Project(rwy.Lat, rwy.Lon, geometry.NormalizeHeading(rwy.Heading+180.0), constants.DefaultArrivalExitApproachEntryNM)

			offsetHeading := geometry.NormalizeHeading(rwy.Heading + 90.0)
			if len(ac.Registration) > 0 && ac.Registration[len(ac.Registration)-1]%2 == 0 {
				offsetHeading = geometry.NormalizeHeading(rwy.Heading - 90.0)
			}

			// Push the aircraft 2.88 NM out to the side instead of 5.0 NM
			// This mathematically forces a standard 30-degree intercept track to the 10NM gate!
			targetPos.Lat, targetPos.Long = geometry.Project(centerline15NMLat, centerline15NMLon, offsetHeading, constants.InterceptLOCSegmentANM)
		}

		// 2. Establish Final Exit point (FAF at 4.0NM out)
		finalTargetPos := atc.Position{}
		finalTargetPos.Lat, finalTargetPos.Long = geometry.Project(rwy.Lat, rwy.Lon, geometry.NormalizeHeading(rwy.Heading+180.0), constants.DefaultApproachExitFinalEntryNM)

		targetAlt = float64(rwy.FAFalt)
		if targetAlt == 0 {
			targetAlt = atc.GetElevation(ctxAp, rwy) + float64(constants.DefaultApproachExitFinalEntryAltFt)
		}

		// 3. Calculate the Intermediate Intercept Gate (Localizer Capture at 10.0NM out)
		interceptPos := atc.Position{}
		interceptPos.Lat, interceptPos.Long = geometry.Project(rwy.Lat, rwy.Lon, geometry.NormalizeHeading(rwy.Heading+180.0), constants.InterceptLOCProjectNM)
		interceptAlt := targetAlt + (float64(constants.InterceptLOCMultiplier) * float64(constants.InterceptLOCUnitFt))

		// 4. Time-Split Vector Segment Logic (60% to gate, 40% on centerline)
		splitPoint := 0.60

		if progress < splitPoint {
			// SEGMENT A: Tracking from the 15NM offset gate to the 10NM localizer gate
			segProgress := progress / splitPoint

			ac.Flight.Position.Lat = startPos.Lat + (segProgress * (interceptPos.Lat - startPos.Lat))
			ac.Flight.Position.Long = startPos.Long + (segProgress * (interceptPos.Long - startPos.Long))
			ac.Flight.Position.Altitude = phaseInitAlt + (segProgress * (interceptAlt - phaseInitAlt))
			heading = geometry.CalculateBearing(startPos.Lat, startPos.Long, interceptPos.Lat, interceptPos.Long)
		} else {
			// SEGMENT B: Locked on Runway Centerline (10NM down to FAF)
			segProgress := (progress - splitPoint) / (1.0 - splitPoint)

			ac.Flight.Position.Lat = interceptPos.Lat + (segProgress * (finalTargetPos.Lat - interceptPos.Lat))
			ac.Flight.Position.Long = interceptPos.Long + (segProgress * (finalTargetPos.Long - interceptPos.Long))
			ac.Flight.Position.Altitude = interceptAlt + (segProgress * (targetAlt - interceptAlt))
			heading = rwy.Heading
		}

		ac.Flight.Position.Heading = geometry.NormalizeHeading(heading)
	case flightphase.Final:
		// Start: FAF (4.0NM out)
		startPos.Lat, startPos.Long = geometry.Project(rwy.Lat, rwy.Lon, geometry.NormalizeHeading(rwy.Heading+180), constants.DefaultApproachExitFinalEntryNM)

		targetPos = atc.Position{Lat: rwy.Lat, Long: rwy.Lon}
		targetAlt = atc.GetElevation(ctxAp, rwy)
		heading = rwy.Heading

	case flightphase.Braking:
		startPos = atc.Position{Lat: rwy.Lat, Long: rwy.Lon}
		// Prefer a real taxiway/exit point if assigned; fallback to a projection along the runway
		if ac.Flight.ArrivalAccess != nil {
			// Convert AccessPoint coordinate to Position
			targetPos.Lat = ac.Flight.ArrivalAccess.Coord.Lat
			targetPos.Long = ac.Flight.ArrivalAccess.Coord.Lon
		} else {
			targetPos.Lat, targetPos.Long = geometry.Project(rwy.Lat, rwy.Lon, rwy.Heading, rwyLengthNM*0.75)
		}

		// Ensure we are at the runway's height
		targetAlt = atc.GetElevation(ctxAp, rwy)
		heading = rwy.Heading
	}

	// Determine total planned distance for interpolation (NM)
	var plannedDist float64
	if phase == flightphase.Approach {
		// For approach, calculate path length as start->intercept + intercept->finalTarget
		interceptPos := atc.Position{}
		interceptPos.Lat, interceptPos.Long = geometry.Project(rwy.Lat, rwy.Lon, geometry.NormalizeHeading(rwy.Heading+180.0), constants.InterceptLOCProjectNM)
		finalTargetPos := atc.Position{}
		finalTargetPos.Lat, finalTargetPos.Long = geometry.Project(rwy.Lat, rwy.Lon, geometry.NormalizeHeading(rwy.Heading+180.0), constants.DefaultApproachExitFinalEntryNM)
		seg1 := geometry.DistNM(startPos.Lat, startPos.Long, interceptPos.Lat, interceptPos.Long)
		seg2 := geometry.DistNM(interceptPos.Lat, interceptPos.Long, finalTargetPos.Lat, finalTargetPos.Long)
		plannedDist = seg1 + seg2
	} else {
		plannedDist = geometry.DistNM(startPos.Lat, startPos.Long, targetPos.Lat, targetPos.Long)
	}

	// Nominal ground speed for phase (kts)
	speedKts := e.getPhaseGroundSpeedKts(ac, phase)
	if elapsed < totalDuration && plannedDist > 0 && speedKts > 0 {
		distanceCovered := speedKts * (elapsed / 3600.0)
		progress = math.Min(1.0, distanceCovered/plannedDist)
	}

	// --- Final Move ---
	if phase != flightphase.Approach {
		// Protect against zero-distance interpolation
		plannedDist := geometry.DistNM(startPos.Lat, startPos.Long, targetPos.Lat, targetPos.Long)

		// For Takeoff/Braking, cap the implied ground rollout speed to avoid unrealistic teleporting
		scaledProgress := progress
		if phase == flightphase.Takeoff || phase == flightphase.Braking {
			// Ground-speed caps tuned by aircraft SizeClass to avoid unrealistic teleporting
			var maxAllowedKts float64
			switch ac.SizeClass {
			case "E", "F":
				// Heavies: slightly lower ground rollout speeds
				if phase == flightphase.Takeoff {
					maxAllowedKts = 150.0
				} else {
					maxAllowedKts = 100.0
				}
			case "C", "D":
				if phase == flightphase.Takeoff {
					maxAllowedKts = 180.0
				} else {
					maxAllowedKts = 120.0
				}
			default:
				// Small aircraft
				if phase == flightphase.Takeoff {
					maxAllowedKts = 140.0
				} else {
					maxAllowedKts = 100.0
				}
			}
			// allowed distance over the phase duration at capped speed
			allowedDist := maxAllowedKts * (ac.Flight.Phase.TotalDuration.Seconds() / 3600.0)
			if plannedDist > 0 && allowedDist < plannedDist {
				scale := allowedDist / plannedDist
				scaledProgress = math.Min(1.0, progress*scale)
			}
		}

		ac.Flight.Position.Lat = startPos.Lat + (scaledProgress * (targetPos.Lat - startPos.Lat))
		ac.Flight.Position.Long = startPos.Long + (scaledProgress * (targetPos.Long - startPos.Long))
		ac.Flight.Position.Heading = geometry.NormalizeHeading(heading)

		switch phase {
		case flightphase.Takeoff:
			// Smooth rotation altitude from runway elevation to the small rotation altitude assigned earlier
			runwayElev := atc.GetElevation(ctxAp, rwy)
			rotationAlt := targetAlt
			ac.Flight.Position.Altitude = runwayElev + (scaledProgress * (rotationAlt - runwayElev))
		case flightphase.Braking, flightphase.TaxiOut, flightphase.TaxiIn:
			ac.Flight.Position.Altitude = targetAlt
		default:
			// Compute intended altitude via interpolation
			intendedAlt := phaseInitAlt + (progress * (targetAlt - phaseInitAlt))
			// Apply vertical-rate limiting so altitude changes don't exceed realistic climb/descent rates
			vrate := e.getPhaseVerticalRateFpm(ac, phase)
			if vrate == 0 || elapsed <= 0 {
				ac.Flight.Position.Altitude = intendedAlt
			} else {
				// Allowed change since phase start (ft)
				allowedChange := vrate * (elapsed / 60.0)
				// Compute desired change from initial altitude
				desiredChange := intendedAlt - phaseInitAlt
				var actualChange float64
				if vrate > 0 {
					actualChange = math.Min(desiredChange, allowedChange)
				} else {
					actualChange = math.Max(desiredChange, allowedChange)
				}
				ac.Flight.Position.Altitude = phaseInitAlt + actualChange
			}
		}
	}
	// Note: flightphase.Approach is completely self-contained, preventing cross-contamination from global progress ratios.

	// Position-based transition trigger: if we've completed the linear progress or are within
	// a small distance of the target, mark the phase as ready to transition.
	// Conservative threshold in NM (~0.01 NM ~= 18m)
	const posTransitionThresholdNM = 0.01

	// Defensive: if target is identical to start (no movement), skip distance check
	if !(targetPos.Lat == startPos.Lat && targetPos.Long == startPos.Long) {
		d := geometry.DistNM(ac.Flight.Position.Lat, ac.Flight.Position.Long, targetPos.Lat, targetPos.Long)
		if progress >= 0.999 || d <= posTransitionThresholdNM {
			ac.Flight.Phase.EstimatedNextTransition = e.AtcService.GetCurrentZuluTime()
			ac.Flight.Phase.PositionComplete = true
			util.LogDebugWithLabel(ac.Registration, "position-driven: linear phase %s reached target (progress=%0.3f dist=%0.3fNM), marking EstimatedNextTransition=%v",
				flightphase.FlightPhase(ac.Flight.Phase.Current).String(), progress, d, ac.Flight.Phase.EstimatedNextTransition)
		}
	}

	// Safety Checks: ensure calculated values are correct
	if ac.Flight.Position.Lat > 90 || ac.Flight.Position.Lat < -90 {
		util.LogWarnWithLabel(ac.Registration, "Latitude out of bounds: %f. Check phase %d logic.",
			ac.Flight.Position.Lat, phase)
	}
	if targetAlt <= 0 {
		util.LogWarnWithLabel(ac.Registration, "Target altitude is non-positive: %f. Check phase %d logic.",
			targetAlt, phase)
	}

	util.LogDebugWithLabel(ac.Registration, "phase: %s phaseInitAlt: %f targetAlt: %f",
		flightphase.FlightPhase(ac.Flight.Phase.Current).String(), phaseInitAlt, targetAlt)
}

// getActiveAirport returns the origin (departing) airport when the aircraft is in a departing phase,
// otherwise the destination airport is returned. The context, arrival or departure, is also returned.
func (e *D9TrafficEngine) getActiveAirport(ac *atc.Aircraft) (*atc.Airport, int) {
	if ac.Flight.Phase.Class >= flightclass.Cruising {
		return e.AtcService.Airports[ac.Flight.Schedule.IcaoDest], atc.ARRIVAL_CONTEXT
	}
	return e.AtcService.Airports[ac.Flight.Schedule.IcaoOrigin], atc.DEPARTURE_CONTEXT
}

func (e *D9TrafficEngine) calculateTaxiDuration(ac *atc.Aircraft, flightContext int) int {
	// 1. Get the legs from the logic we established for updateTaxiPosition
	// Leg 1: Gate to Access Point | Leg 2: Access Point to Runway
	spot := ac.Flight.AssignedParkingSpot
	var access *atc.AccessPoint
	if flightContext == atc.DEPARTURE_CONTEXT {
		access = ac.Flight.DepartureAccess
	} else {
		access = ac.Flight.ArrivalAccess
	}

	rwy := ac.Flight.AssignedRunway
	if spot == nil || access == nil || rwy == nil {
		return 300 // Fallback 5 mins
	}

	// Total distance in NM
	dist1 := geometry.DistNM(spot.Lat, spot.Lon, access.Coord.Lat, access.Coord.Lon)
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
	elapsed := e.AtcService.GetCurrentZuluTime().Sub(ac.Flight.Phase.Transition).Seconds()
	totalDuration := ac.Flight.Phase.TotalDuration.Seconds()
	if totalDuration <= 0 {
		return
	}

	// Clamp progress 0.0 -> 1.0. If duration is exceeded, it stays at 1.0 (the destination)
	progress := math.Min(1.0, elapsed/totalDuration)

	var startLat, startLon, endLat, endLon float64
	var startHdg float64

	if isOutbound {
		startLat = ac.Flight.AssignedParkingSpot.Lat
		startLon = ac.Flight.AssignedParkingSpot.Lon
		endLat = ac.Flight.DepartureAccess.Coord.Lat
		endLon = ac.Flight.DepartureAccess.Coord.Lon
		startHdg = ac.Flight.AssignedParkingSpot.Heading
	} else {
		startLat = ac.Flight.ArrivalAccess.Coord.Lat
		startLon = ac.Flight.ArrivalAccess.Coord.Lon
		endLat = ac.Flight.AssignedParkingSpot.Lat
		endLon = ac.Flight.AssignedParkingSpot.Lon
		startHdg = ac.Flight.ArrivalAccess.Bearing
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
		// Use a 90-degree change from the start heading at the halfway point to
		// produce a simple two-leg taxi route (gate -> corner -> runway).
		ac.Flight.Position.Heading = geometry.NormalizeHeading(startHdg + 90.0)
	}
	ac.Flight.Position.Altitude = airport.Elevation

	// If we've reached (or practically reached) the taxi destination, trigger a position-driven transition
	if progress >= 0.999 {
		ac.Flight.Phase.EstimatedNextTransition = e.AtcService.GetCurrentZuluTime()
		ac.Flight.Phase.PositionComplete = true
		util.LogDebugWithLabel(ac.Registration, "position-driven: taxi progress >= 0.999, marking EstimatedNextTransition=%v", ac.Flight.Phase.EstimatedNextTransition)
	}
}

func (e *D9TrafficEngine) updateHoldingPosition(ac *atc.Aircraft, rwy *atc.Runway) {
	// Implement a racetrack holding pattern with 1-minute legs and standard-rate turns.
	// Leg length is derived from nominal hold speed per aircraft SizeClass.
	currSimZTime := e.AtcService.GetCurrentZuluTime()
	elapsed := currSimZTime.Sub(ac.Flight.Phase.Transition).Seconds()

	hold := ac.Flight.AssignedHold
	if hold == nil {
		return
	}

	// Determine inbound course: prefer assigned runway vector if available,
	// otherwise use the provided arrival runway (`rwy`) and finally fallback to
	// bearing from hold to current aircraft position.
	var inboundCourse float64
	if ac.Flight.AssignedRunway != nil {
		inboundCourse = geometry.CalculateBearing(hold.Lat, hold.Lon, ac.Flight.AssignedRunway.Lat, ac.Flight.AssignedRunway.Lon)
	} else if rwy != nil {
		inboundCourse = geometry.CalculateBearing(hold.Lat, hold.Lon, rwy.Lat, rwy.Lon)
	} else {
		inboundCourse = geometry.CalculateBearing(hold.Lat, hold.Lon, ac.Flight.Position.Lat, ac.Flight.Position.Long)
	}
	inboundCourse = geometry.NormalizeHeading(inboundCourse)
	outboundCourse := geometry.NormalizeHeading(inboundCourse + 180.0)

	// Hold speed (kts) using Approach nominal as a proxy; ensure a minimum
	holdSpeed := e.getPhaseGroundSpeedKts(ac, flightphase.Approach)
	if holdSpeed < 120.0 {
		holdSpeed = 120.0
	}

	// Leg timing and distances
	legMinutes := 1.0
	legSeconds := legMinutes * 60.0
	// Standard-rate turn: 180deg at 3 deg/sec = 60s
	turnSeconds := 60.0
	cycleTime := (2 * legSeconds) + (2 * turnSeconds)

	// Distance from fix for leg endpoints (NM)
	legDist := holdSpeed * (legMinutes / 60.0)

	// Stack altitude assignment: ensure separation of +1000ft above the hold minimum
	// Build ordered list to assign altitudes deterministically
	var stack []*atc.Aircraft
	for _, other := range e.ActiveAircraft {
		if other != nil && other.Flight.AssignedHold == hold {
			stack = append(stack, other)
		}
	}
	// sort by current altitude ascending so lowest stays lowest in stack
	sort.Slice(stack, func(i, j int) bool { return stack[i].Flight.Position.Altitude < stack[j].Flight.Position.Altitude })
	// assign altitudes starting at MinAlt
	for i, a := range stack {
		tgtAlt := float64(hold.MinAlt + (i * 1000))
		a.Flight.Position.Altitude = tgtAlt
	}

	// Compute position along racetrack
	t := math.Mod(elapsed, cycleTime)
	var pos atc.Position
	var heading float64

	if t < legSeconds {
		// Outbound straight leg: from fix outward along outboundCourse
		p := t / legSeconds
		posLat, posLon := geometry.Project(hold.Lat, hold.Lon, outboundCourse, p*legDist)
		pos = atc.Position{Lat: posLat, Long: posLon}
		heading = outboundCourse
	} else if t < legSeconds+turnSeconds {
		// Outbound -> inbound turn: sweep bearing from outboundCourse -> inboundCourse
		tt := (t - legSeconds) / turnSeconds
		bearing := geometry.NormalizeHeading(outboundCourse + (180.0 * tt))
		posLat, posLon := geometry.Project(hold.Lat, hold.Lon, bearing, legDist)
		pos = atc.Position{Lat: posLat, Long: posLon}
		heading = geometry.NormalizeHeading(bearing + 90.0)
	} else if t < (legSeconds + turnSeconds + legSeconds) {
		// Inbound straight leg: from radius point toward fix
		tt := (t - (legSeconds + turnSeconds)) / legSeconds
		dist := legDist * (1.0 - tt)
		posLat, posLon := geometry.Project(hold.Lat, hold.Lon, inboundCourse, dist)
		pos = atc.Position{Lat: posLat, Long: posLon}
		heading = inboundCourse
	} else {
		// Inbound -> outbound turn
		tt := (t - (legSeconds + turnSeconds + legSeconds)) / turnSeconds
		bearing := geometry.NormalizeHeading(inboundCourse + (180.0 * tt))
		posLat, posLon := geometry.Project(hold.Lat, hold.Lon, bearing, legDist)
		pos = atc.Position{Lat: posLat, Long: posLon}
		heading = geometry.NormalizeHeading(bearing + 90.0)
	}

	ac.Flight.Position.Lat = pos.Lat
	ac.Flight.Position.Long = pos.Long
	ac.Flight.Position.Heading = geometry.NormalizeHeading(heading)
}

func (e *D9TrafficEngine) updateGoAroundPosition(ac *atc.Aircraft, airport *atc.Airport) {
	elapsed := e.AtcService.GetCurrentZuluTime().Sub(ac.Flight.Phase.Transition).Seconds()
	totalDuration := ac.Flight.Phase.TotalDuration.Seconds()

	// We don't clamp progress at 1.0 here—if the transition is delayed,
	// the aircraft keeps flying the vector.
	progress := elapsed / totalDuration

	rwy := ac.Flight.AssignedRunway

	// Project along runway heading + 10 degrees (standard missed approach)
	dist := progress * 6.0 // Expected to be 6NM at the end of the phase duration
	newLat, newLon := geometry.Project(rwy.Lat, rwy.Lon, geometry.NormalizeHeading(rwy.Heading+10), dist)

	ac.Flight.Position.Lat = newLat
	ac.Flight.Position.Long = newLon
	ac.Flight.Position.Heading = geometry.NormalizeHeading(rwy.Heading + 10)
	// Climb to VerticalStepFt
	ac.Flight.Position.Altitude = math.Min(airport.Elevation+float64(constants.DefaultClimbExitDepartureEntryAltFt), airport.Elevation+(progress*float64(constants.DefaultClimbExitDepartureEntryAltFt)))
}

func (e *D9TrafficEngine) updateCruisePosition(ac *atc.Aircraft) {

	currSimZTime := e.AtcService.GetCurrentZuluTime()
	elapsed := currSimZTime.Sub(ac.Flight.Phase.Transition).Seconds()
	totalDuration := ac.Flight.Phase.TotalDuration.Seconds()

	if totalDuration <= 0 {
		return
	}

	// We'll compute horizontal progress based on distance covered using a nominal cruise ground speed.
	// Default to time-based progress as a safe fallback.
	var progress float64
	if elapsed >= totalDuration {
		progress = 1.0
	} else {
		progress = math.Min(1.0, elapsed/totalDuration)
	}

	originAp := e.AtcService.Airports[ac.Flight.Schedule.IcaoOrigin]
	destAp := e.AtcService.Airports[ac.Flight.Schedule.IcaoDest]

	// 2. Identify Horizontal Start (SID Exit or Origin Center)
	var startPos atc.Position
	startAlt := atc.GetMinSafeAltitude(float64(constants.DefaultDepartureExitCruiseEntryAltFt), originAp)
	if sid := ac.Flight.AssignedSID; sid != nil && sid.Exit.Fix.Lat != 0 {
		startPos = atc.Position{Lat: sid.Exit.Fix.Lat, Long: sid.Exit.Fix.Lon}
		// Check if constraint is explicitly defined
		if sid.Exit.ConstraintAlt > 0 {
			// Scaled constraint fix (handling the under-1000 ARINC rule)
			if sid.Exit.ConstraintAlt < 1000 {
				startAlt = float64(sid.Exit.ConstraintAlt * 10)
			} else {
				startAlt = float64(sid.Exit.ConstraintAlt)
			}
		} else if sid.Entry.Fix != nil && sid.Entry.Fix.Lat != 0 {
			// BACKFILL RULE: Extract baseline from opposite end (Entry)
			var entryAlt float64
			if sid.Entry.ConstraintAlt < 1000 && sid.Entry.ConstraintAlt > 0 {
				entryAlt = float64(sid.Entry.ConstraintAlt * 10)
			} else if sid.Entry.ConstraintAlt >= 1000 {
				entryAlt = float64(sid.Entry.ConstraintAlt)
			} else {
				entryAlt = originAp.Elevation + float64(constants.DefaultClimbExitDepartureEntryAltFt) // True runway structural backup
			}

			// Calculate distance between Entry and Exit gates
			distNM := geometry.DistNM(sid.Entry.Fix.Lat, sid.Entry.Fix.Lon, sid.Exit.Fix.Lat, sid.Exit.Fix.Lon)

			// Climb slope: FeetPerFL per 3NM added to the entry baseline
			startAlt = entryAlt + ((distNM / constants.DefaultClimbRateNMPerFL) * float64(constants.FeetPerFL))
		} else {
			startAlt = atc.GetMinSafeAltitude(float64(constants.DefaultDepartureExitCruiseEntryAltFt), originAp)
		}
	} else {
		// Calculate the initial track from origin to destination
		bearing := geometry.CalculateBearing(originAp.Lat, originAp.Lon, destAp.Lat, destAp.Lon)

		// Project 30.0 NM forward along that track from the origin airport
		startLat, startLon := geometry.Project(originAp.Lat, originAp.Lon, bearing, constants.DefaultDepartureExitCruiseEntryNM)

		startPos = atc.Position{
			Lat:  startLat,
			Long: startLon,
		}
	}

	// 3. Identify Horizontal Target (STAR Entry or Destination Center)
	var targetPos atc.Position
	targetAlt := atc.GetMinSafeAltitude(float64(constants.DefaultCruiseExitArrivalEntryAltFt), destAp)
	if star := ac.Flight.AssignedSTAR; star != nil && star.Entry.Fix.Lat != 0 {
		targetPos = atc.Position{Lat: star.Entry.Fix.Lat, Long: star.Entry.Fix.Lon}

		// Check if constraint is explicitly defined
		if star.Entry.ConstraintAlt > 0 {
			// Scaled constraint fix (handling the under-1000 ARINC rule)
			if star.Entry.ConstraintAlt < 1000 {
				targetAlt = float64(star.Entry.ConstraintAlt * 10)
			} else {
				targetAlt = float64(star.Entry.ConstraintAlt)
			}
		} else if star.Exit.Fix != nil && star.Exit.Fix.Lat != 0 {
			// BACKFILL RULE: Extract baseline from opposite end (Exit)
			var exitAlt float64
			if star.Exit.ConstraintAlt < 1000 && star.Exit.ConstraintAlt > 0 {
				exitAlt = float64(star.Exit.ConstraintAlt * 10)
			} else if star.Exit.ConstraintAlt >= 1000 {
				exitAlt = float64(star.Exit.ConstraintAlt)
			} else {
				exitAlt = float64(constants.DefaultArrivalExitApproachEntryAltFt) // Standard terminal approach platform floor
			}

			// Calculate distance between Entry and Exit gates
			distNM := geometry.DistNM(star.Entry.Fix.Lat, star.Entry.Fix.Lon, star.Exit.Fix.Lat, star.Exit.Fix.Lon)

			// Descent slope back-projection: FeetPerFL per 3NM added to the exit platform floor
			targetAlt = exitAlt + ((distNM / constants.DefaultDescentRateNMPerFL) * float64(constants.FeetPerFL))
		} else {
			targetAlt = atc.GetMinSafeAltitude(float64(constants.DefaultDepartureExitCruiseEntryAltFt), destAp)
		}
	} else {

		// No STAR or usable STAR entry fix: we need to fabricate a reasonable target point for the aircraft to track toward as it approaches
		// the destination airport for its descent preparation. We want this point to be far enough out to allow for a natural-feeling descent,
		// but not so far that the aircraft is unnaturally "floating" toward the airport for an extended period of time.
		// We also want it to be on a logical track that would make sense for a real-world arrival into the airport.

		// project 30NM out from destination
		// 1. Get the track from origin to destination
		bearing := geometry.CalculateBearing(originAp.Lat, originAp.Lon, destAp.Lat, destAp.Lon)

		// 2. Invert the bearing to get the reverse track looking backward from the airport
		// (e.g., if inbound track is 090°, looking backward is 270°)
		reverseBearing := geometry.NormalizeHeading(bearing + 180.0)

		// 3. Safely project 40.0 NM backward away from the destination airport center
		startLat, startLon := geometry.Project(destAp.Lat, destAp.Lon, reverseBearing, constants.DefaultCruiseExitArrivalEntryNM)

		targetPos = atc.Position{
			Lat:  startLat,
			Long: startLon,
		}
	}

	// 4. Update Lat/Lon via Linear Interpolation
	// Prefer distance-driven progress: compute planned distance and advance by nominal cruise ground speed
	plannedDist := geometry.DistNM(startPos.Lat, startPos.Long, targetPos.Lat, targetPos.Long)
	if plannedDist > 0 && elapsed < totalDuration {
		speedKts := e.getPhaseGroundSpeedKts(ac, flightphase.Cruise)
		distanceCovered := speedKts * (elapsed / 3600.0)
		progress = math.Min(1.0, distanceCovered/plannedDist)
	}

	ac.Flight.Position.Lat = startPos.Lat + (progress * (targetPos.Lat - startPos.Lat))
	ac.Flight.Position.Long = startPos.Long + (progress * (targetPos.Long - startPos.Long))

	// 5. Vertical Profile using the 3-to-1 Rule for Descent
	cruiseAlt := float64(ac.Flight.CruiseAlt)
	if cruiseAlt < atc.GetMinSafeAltitude(float64(constants.DefaultDepartureExitCruiseEntryAltFt), destAp) {
		cruiseAlt = atc.GetMinSafeAltitude(float64(constants.DefaultDepartureExitCruiseEntryAltFt), destAp)
		util.LogErrWithLabel(ac.Registration, "cruise altitude is set to %d - too low for local terrain, resetting to %d - possible data error", ac.Flight.CruiseAlt, int(cruiseAlt))
	}
	distToTarget := geometry.DistNM(ac.Flight.Position.Lat, ac.Flight.Position.Long, targetPos.Lat, targetPos.Long)

	altitudeToLose := cruiseAlt - targetAlt
	// Compute required descent distance (NM) using per-size vertical rate and cruise ground speed.
	// time (min) = altitudeToLose (ft) / vrate (ft/min)
	// distance (NM) = groundSpeedKts * (time / 60)
	var requiredDescentDist float64
	vrateAbs := math.Abs(e.getPhaseVerticalRateFpm(ac, flightphase.Approach))
	cruiseGs := e.getPhaseGroundSpeedKts(ac, flightphase.Cruise)
	if vrateAbs > 0 {
		timeMin := altitudeToLose / vrateAbs
		requiredDescentDist = cruiseGs * (timeMin / 60.0)
	} else {
		// Fallback to original 3NM per FL rule if vertical rate is not defined
		requiredDescentDist = (altitudeToLose / float64(constants.FeetPerFL)) * constants.DefaultDescentRateNMPerFL
	}

	var calculatedAlt float64

	inDescent := false
	descentProgress := 0.0
	if distToTarget <= requiredDescentDist && altitudeToLose > 0 {
		// --- PHASE: DESCENT (Post-TOD) ---
		inDescent = true
		// How far into the descent are we? (0.0 at TOD, 1.0 at Target)
		descentProgress = 1.0
		if requiredDescentDist > 0 {
			descentProgress = 1.0 - (distToTarget / requiredDescentDist)
		}
		// Intended altitude based on descent progress
		intendedAlt := cruiseAlt - (math.Max(0, descentProgress) * altitudeToLose)
		// Apply vertical-rate limiting (use Approach rates while descending from cruise)
		vrate := e.getPhaseVerticalRateFpm(ac, flightphase.Approach)
		if vrate == 0 || elapsed <= 0 {
			calculatedAlt = intendedAlt
		} else {
			// Allowed change since cruise phase start (ft)
			allowedChange := vrate * (elapsed / 60.0)
			desiredChange := intendedAlt - startAlt
			var actualChange float64
			if vrate > 0 {
				actualChange = math.Min(desiredChange, allowedChange)
			} else {
				actualChange = math.Max(desiredChange, allowedChange)
			}
			calculatedAlt = startAlt + actualChange
		}
		util.LogDebugWithLabel(ac.Registration, "elapsed cruise is %0.2f seconds - in descent phase, distance to target is %0.2f NM, required descent distance is %0.2f NM, descent progress is %0.2f%%, calculated altitude is %0.2f",
			elapsed, distToTarget, requiredDescentDist, descentProgress*100, calculatedAlt)
		// if we don't have arrival procedures defined, set to vectoring
		if ac.Flight.AssignedSTAR == nil && ac.Flight.Vectoring == false {
			ac.Flight.Vectoring = true
		}
	} else {
		// --- PHASE: CLIMB or LEVEL ---
		// Check if we are still in the initial climb from Departure
		// We use a simple 10-minute climb window for TOC logic
		climbDurationSecs := 600.0
		if elapsed < climbDurationSecs {
			climbProgress := elapsed / climbDurationSecs
			intendedAlt := startAlt + (climbProgress * (cruiseAlt - startAlt))
			// Apply climb rate limiting
			vrate := e.getPhaseVerticalRateFpm(ac, flightphase.Climbout)
			if vrate == 0 || elapsed <= 0 {
				calculatedAlt = intendedAlt
			} else {
				allowedChange := vrate * (elapsed / 60.0)
				desiredChange := intendedAlt - startAlt
				var actualChange float64
				if vrate > 0 {
					actualChange = math.Min(desiredChange, allowedChange)
				} else {
					actualChange = math.Max(desiredChange, allowedChange)
				}
				calculatedAlt = startAlt + actualChange
			}
			util.LogDebugWithLabel(ac.Registration, "elapsed cruise is %0.2f seconds - in initial climb phase, calculated altitude is %f", elapsed, calculatedAlt)
		} else {
			calculatedAlt = cruiseAlt
			util.LogDebugWithLabel(ac.Registration, "elapsed cruise is %0.2f seconds - in cruise phase, maintaining cruise altitude of %f", elapsed, calculatedAlt)
		}
	}

	// 6. Apply State
	ac.Flight.Position.Altitude = calculatedAlt

	// Calculate heading based on the underlying path track vectors
	hd := geometry.CalculateBearing(
		startPos.Lat,
		startPos.Long,
		targetPos.Lat,
		targetPos.Long,
	)

	ac.Flight.Position.Heading = geometry.NormalizeHeading(hd)

	// Cruise position-based transition: when we're sufficiently close to the fabricated/STAR target
	// mark the phase ready for transition. Use a slightly larger threshold for cruise (0.1 NM ~= 185m).
	const cruiseTransitionThresholdNM = 0.1
	if distToTarget <= cruiseTransitionThresholdNM || progress >= 0.999 {
		ac.Flight.Phase.EstimatedNextTransition = e.AtcService.GetCurrentZuluTime()
		ac.Flight.Phase.PositionComplete = true
		util.LogDebugWithLabel(ac.Registration, "position-driven: cruise approaching target (dist=%0.3fNM progress=%0.3f), marking EstimatedNextTransition=%v",
			distToTarget, progress, ac.Flight.Phase.EstimatedNextTransition)
	}

	if inDescent && ac.Flight.ClearedTOD == false && descentProgress < 0.05 {
		ac.Flight.ClearedTOD = true
		util.LogWithLabel(ac.Registration, "TOD reached at %0.2f NM from target - beginning descent from cruise altitude of %0.2f to target entry altitude of %0.2f over the next %0.2f NM",
			distToTarget, cruiseAlt, targetAlt, requiredDescentDist)
		v := deepcopy.Copy(ac)
		acSnap, ok := v.(*atc.Aircraft)
		if !ok {
			util.LogWarnWithLabel(ac.Registration, "failed to deepcopy aircraft snapshot for cruise TOD; skipping phrase generation")
		} else {
			// send to phrase generation
			util.GoSafe(func() {
				// +-----------------------------------------------------------------+
				// | Only use acSnap to reference the aircraft within the go routine |
				// +-----------------------------------------------------------------+
				acSnap.Flight.Comms.Controller = e.AtcService.AssignController(acSnap)
				if acSnap.Flight.Comms.Controller != nil {
					e.AtcService.Transmit(e.AtcService.UserState, acSnap)
				}
			})
		}
	}
}

func (e *D9TrafficEngine) endFlight(ac *atc.Aircraft) {
	delete(e.ActiveAircraft, getActiveAircraftKey(ac))
	if ac.Flight.AssignedParkingSpot != nil {
		e.releaseParking(ac.Flight.Destination, ac.Flight.AssignedParkingSpot)
	}
}

func (e *D9TrafficEngine) positionAtOriginParking(ac *atc.Aircraft) {
	airport := e.AtcService.Airports[ac.Flight.Origin]
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
	airport := e.AtcService.Airports[ac.Flight.Destination]
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

	util.LogWithLabel(ac.Registration, "positioned at gate %s", ac.Flight.AssignedParkingSpot.Name)
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

func (e *D9TrafficEngine) determineInitialDeparturePhase(minsToSchedDep int, f *flightplan.ScheduledFlight) (flightphase.FlightPhase, int, int, int) {
	delay := 0
	switch {
	// 1. LONG TERM PARKED (Way out before the tracking window)
	case minsToSchedDep > DMINUS_PARKED_MINS:
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
		remainingDur := (AbsDiff(minsToSchedDep, DMINUS_STARTUP_MINS) * 60) + jitter
		// Keep total clamped to a realistic standard baseline if it's way out
		totalDur := AbsInt((DMINUS_PARKED_MINS-DMINUS_STARTUP_MINS)*60) + jitter
		if remainingDur > totalDur {
			totalDur = remainingDur
		}
		return flightphase.Parked, remainingDur, totalDur, delay

	// 2. ACTIVE PRE-STARTUP PARKING
	case minsToSchedDep > DMINUS_STARTUP_MINS && minsToSchedDep <= DMINUS_PARKED_MINS:
		jitter := rand.IntN((PARKED_JITTER_SECONDS*2)+1) - PARKED_JITTER_SECONDS
		remainingDur := (AbsDiff(minsToSchedDep, DMINUS_STARTUP_MINS) * 60) + jitter
		return flightphase.Parked, remainingDur, AbsInt(((DMINUS_PARKED_MINS - DMINUS_STARTUP_MINS) * 60) + jitter), delay

	// 3. STARTUP
	case minsToSchedDep > DMINUS_TAXIOUT_MINS && minsToSchedDep <= DMINUS_STARTUP_MINS:
		jitter := rand.IntN((STARTUP_JITTER_SECONDS*2)+1) - STARTUP_JITTER_SECONDS
		remainingDur := (AbsDiff(minsToSchedDep, DMINUS_TAXIOUT_MINS) * 60) + jitter
		return flightphase.Startup, remainingDur, AbsInt(((DMINUS_STARTUP_MINS - DMINUS_TAXIOUT_MINS) * 60) + jitter), delay

	// 4. TAXI OUT
	case minsToSchedDep > DMINUS_TAKEOFF_MINS && minsToSchedDep <= DMINUS_TAXIOUT_MINS:
		remainingDur := (AbsDiff(minsToSchedDep, DMINUS_TAKEOFF_MINS) * 60)
		totalDur := (DMINUS_TAXIOUT_MINS - DMINUS_TAKEOFF_MINS) * 60
		return flightphase.TaxiOut, remainingDur, totalDur, delay

		// 5. TAKEOFF OVERRIDE: Align to full taxi parameters
	case minsToSchedDep >= DMINUS_CLIMBOUT_MINS && minsToSchedDep <= DMINUS_TAKEOFF_MINS:
		// Calculate the maximum standard duration a full taxi takes at this airport
		fullTaxiDurationSecs := AbsInt(DMINUS_TAXIOUT_MINS-DMINUS_TAKEOFF_MINS) * 60
		// Because we are resetting the aircraft to the gate to start a fresh taxi,
		// the remaining time in this phase is the FULL taxi time.
		remainingDur := fullTaxiDurationSecs
		return flightphase.TaxiOut, remainingDur, fullTaxiDurationSecs, delay

	// 6. CLIMBOUT
	case minsToSchedDep >= DMINUS_DEPARTURE_MINS && minsToSchedDep < DMINUS_CLIMBOUT_MINS:
		jitter := rand.IntN((CLIMBOUT_JITTER_SECONDS*2)+1) - CLIMBOUT_JITTER_SECONDS
		remainingDur := (AbsDiff(minsToSchedDep, DMINUS_DEPARTURE_MINS) * 60) + jitter
		return flightphase.Climbout, remainingDur, AbsInt(((DMINUS_CLIMBOUT_MINS - DMINUS_DEPARTURE_MINS) * 60) + jitter), delay

	// 7. DEPARTURE (En-route transition segment)
	case minsToSchedDep >= DMINUS_CRUISE_MINS && minsToSchedDep <= DMINUS_DEPARTURE_MINS:
		jitter := rand.IntN((DEPARTURE_JITTER_SECONDS*2)+1) - DEPARTURE_JITTER_SECONDS
		remainingDur := (AbsDiff(minsToSchedDep, DMINUS_CRUISE_MINS) * 60) + jitter
		return flightphase.Departure, remainingDur, AbsInt(((DMINUS_DEPARTURE_MINS - DMINUS_CRUISE_MINS) * 60) + jitter), delay

	// 8. CRUISE EXPLICIT BOUNDARY
	case minsToSchedDep < DMINUS_CRUISE_MINS:
		jitter := rand.IntN((CRUISE_JITTER_SECONDS*2)+1) - CRUISE_JITTER_SECONDS

		// Remaining time in cruise uses your timeDiffToScheduledArrival helper
		tta := e.timeDiffToScheduledArrival(f)
		remainingCruiseSecs := (AbsDiff(tta, AMINUS_ARRIVAL_MINS) * 60) + jitter

		// Total Cruise Duration extraction — compute duration between departure
		// and arrival while handling midnight wrap-around (e.g., dep 23:57 -> arr 00:27).
		depMins := f.DepartureHour*60 + f.DepartureMin
		arrMins := f.ArrivalHour*60 + f.ArrivalMin

		diff := arrMins - depMins
		if diff < -720 {
			diff += 1440
		} else if diff > 720 {
			diff -= 1440
		}

		totalCruiseMins := AbsInt(diff) - AbsInt(DMINUS_DEPARTURE_MINS) - AMINUS_ARRIVAL_MINS
		if totalCruiseMins*60 <= remainingCruiseSecs {
			totalCruiseMins = (remainingCruiseSecs / 60) + 15
		}
		if totalCruiseMins < 0 {
			totalCruiseMins = 0
		}

		totalCruiseSecs := totalCruiseMins * 60

		return flightphase.Cruise, AbsInt(remainingCruiseSecs), AbsInt(totalCruiseSecs), delay

	default:
		// STALE/HISTORICAL FALLBACK:
		// Catches any orphan tracking frames safely
		return flightphase.Parked, 0, 0, delay
	}
}

func (e *D9TrafficEngine) determineInitialArrivalPhase(minsToSchedArr int, f *flightplan.ScheduledFlight) (flightphase.FlightPhase, int, int) {

	switch {
	// ARRIVAL
	case minsToSchedArr > AMINUS_APPROACH_MINS && minsToSchedArr <= AMINUS_ARRIVAL_MINS:
		jitter := rand.IntN((ARRIVAL_JITTER_SECONDS*2)+1) - ARRIVAL_JITTER_SECONDS
		remainingDur := (AbsDiff(minsToSchedArr, AMINUS_APPROACH_MINS) * 60) + jitter
		return flightphase.Arrival, remainingDur, AbsInt(((AMINUS_ARRIVAL_MINS - AMINUS_APPROACH_MINS) * 60) + jitter)

	// APPROACH:
	case minsToSchedArr > AMINUS_FINAL_MINS && minsToSchedArr <= AMINUS_APPROACH_MINS:
		jitter := rand.IntN((APPROACH_JITTER_SECONDS*2)+1) - APPROACH_JITTER_SECONDS
		remainingDur := (AbsDiff(minsToSchedArr, AMINUS_FINAL_MINS) * 60) + jitter
		return flightphase.Approach, remainingDur, AbsInt(((AMINUS_APPROACH_MINS - AMINUS_FINAL_MINS) * 60) + jitter)

	// FINAL: Redirect to Approach, but scale remaining time based on proximity to landing
	case minsToSchedArr > AMINUS_LAND_MINS && minsToSchedArr <= AMINUS_FINAL_MINS:
		totalApproachWindow := AbsInt(AMINUS_APPROACH_MINS-AMINUS_FINAL_MINS) * 60
		remainingDur := AbsInt(minsToSchedArr-AMINUS_LAND_MINS) * 60
		return flightphase.Approach, remainingDur, totalApproachWindow

		// BRAKING OVERRIDE: Redirect to TaxiIn instead of Approach.
	// This clears the runway immediately and feeds the ground network a realistic timeline.
	case minsToSchedArr > AMINUS_BRAKING && minsToSchedArr <= AMINUS_LAND_MINS:
		// Calculate the complete standard duration it takes to taxi to the gate
		fullTaxiInWindow := AbsInt(AMINUS_TAXIIN_MINS-AMINUS_SHUTDOWN_MINS) * 60
		// Because the aircraft is resetting to the runway exit to start a fresh taxi,
		// we grant it the full duration to execute the path realistically.
		remainingDur := fullTaxiInWindow
		return flightphase.TaxiIn, remainingDur, fullTaxiInWindow

	// TAXI IN:
	case minsToSchedArr > AMINUS_TAXIIN_MINS && minsToSchedArr <= AMINUS_BRAKING:
		remainingDur := AbsInt(minsToSchedArr-AMINUS_TAXIIN_MINS) * 60
		return flightphase.TaxiIn, remainingDur, AbsInt(AMINUS_BRAKING-AMINUS_TAXIIN_MINS) * 60

	// SHUTDOWN:
	case minsToSchedArr > AMINUS_SHUTDOWN_MINS && minsToSchedArr <= AMINUS_TAXIIN_MINS:
		jitter := rand.IntN((SHUTDOWN_JITTER_SECONDS*2)+1) - SHUTDOWN_JITTER_SECONDS
		remainingDur := (AbsDiff(minsToSchedArr, AMINUS_SHUTDOWN_MINS) * 60) + jitter
		return flightphase.Shutdown, remainingDur, AbsInt(AMINUS_TAXIIN_MINS-AMINUS_SHUTDOWN_MINS) * 60

	// PARKED:
	case minsToSchedArr >= AMINUS_PARKED_MINS && minsToSchedArr <= AMINUS_SHUTDOWN_MINS:
		jitter := rand.IntN((PARKED_JITTER_SECONDS*2)+1) - PARKED_JITTER_SECONDS
		remainingDur := (AbsDiff(minsToSchedArr, AMINUS_PARKED_MINS) * 60) + jitter
		return flightphase.Parked, remainingDur, AbsInt(AMINUS_SHUTDOWN_MINS-AMINUS_PARKED_MINS) * 60

	// CRUISE EXPLICIT CASE:
	case minsToSchedArr > AMINUS_ARRIVAL_MINS:
		jitter := rand.IntN((CRUISE_JITTER_SECONDS*2)+1) - CRUISE_JITTER_SECONDS

		remainingCruiseMins := minsToSchedArr - AMINUS_ARRIVAL_MINS
		remainingCruiseSecs := (remainingCruiseMins * 60) + jitter

		totalCruiseMins := AbsDiff(f.ArrivalHour*60+f.ArrivalMin, f.DepartureHour*60+f.DepartureMin) - DMINUS_DEPARTURE_MINS - AMINUS_ARRIVAL_MINS
		if totalCruiseMins <= remainingCruiseMins {
			totalCruiseMins = remainingCruiseMins + 15
		}
		totalCruiseSecs := totalCruiseMins * 60

		return flightphase.Cruise, AbsInt(remainingCruiseSecs), AbsInt(totalCruiseSecs)

	default:
		// STALE/HISTORICAL FALLBACK:
		// SAfety net catches flights that are way past their schedule window so they don't spawn in Cruise
		return flightphase.Parked, 0, 0
	}
}

func (e *D9TrafficEngine) tryExitHold(ac *atc.Aircraft, ap *atc.Airport) {
	qKey := normalizeRunwayKey(ap.ICAO, e.AirportConfig[ap.ICAO].Arrival)
	if len(e.RunwayQueues[qKey]) < TRAFFIC_MANAGEMENT_RUNWAY_QUEUE_THRESHOLD {
		// exit hold
		// capture hold reference so we can reassign the stack after release
		releasedHold := ac.Flight.AssignedHold
		ac.Flight.AssignedHold = nil
		dur := ((AMINUS_APPROACH_MINS - AMINUS_FINAL_MINS) * 60) + 60
		e.transitionToPhase(ac, flightphase.Approach, dur, APPROACH_JITTER_SECONDS)
		e.updateLinearPosition(ac, ap)
		// After releasing one aircraft from the hold, reassign altitudes for remaining aircraft in the same hold
		if releasedHold != nil {
			e.reassignHoldStack(releasedHold)
		}
	} else {
		// continue in hold for another cycle
		dur := HOLDING_MIN_DURATION_MINS * 60 * time.Second
		ac.Flight.Phase.EstimatedNextTransition = e.AtcService.GetCurrentZuluTime().Add(dur)
		e.updateHoldingPosition(ac, e.AirportConfig[ap.ICAO].Arrival)
		util.LogWithLabel(ac.Registration, "continue hold - traffic for runway %s at %s is high - estimated hold exit %v",
			qKey, ap.ICAO, ac.Flight.Phase.EstimatedNextTransition)
	}
}

// reassignHoldStack re-computes stack altitudes for all active aircraft assigned to the given hold.
// Lowest aircraft receives hold.MinAlt, each subsequent aircraft is +1000ft above.
func (e *D9TrafficEngine) reassignHoldStack(h *atc.Hold) {
	if h == nil {
		return
	}
	var stack []*atc.Aircraft
	for _, other := range e.ActiveAircraft {
		if other != nil && other.Flight.AssignedHold == h {
			stack = append(stack, other)
		}
	}
	if len(stack) == 0 {
		return
	}
	// sort by current altitude ascending so lowest remains lowest
	sort.Slice(stack, func(i, j int) bool { return stack[i].Flight.Position.Altitude < stack[j].Flight.Position.Altitude })
	for i, a := range stack {
		tgtAlt := float64(h.MinAlt + (i * 1000))
		a.Flight.Position.Altitude = tgtAlt
	}
}

func (e *D9TrafficEngine) findAvailableParking(airport *atc.Airport, reqClass string, airlineICAO string) *atc.ParkingSpot {

	for pass := 0; pass < 2; pass++ {
		var candidates []*atc.ParkingSpot

		for _, spot := range airport.Parking {

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
			if e.AtcService.UserState.NearestAirport.ICAO == airport.ICAO &&
				e.AtcService.UserState.AssignedParking.Name == spot.Name {
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

	weather := e.AtcService.GetWeatherState()

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
	candidates := []*atc.Runway{}
	if group, exists := orientations[activeOrientation]; exists {
		candidates = group
	} else {
		util.LogWarnWithLabel("D9TRAFFIC", "no viable runways found for active orientation %d at airport %s",
			activeOrientation, ap.ICAO, primaryRwy.Name)
	}

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
		// Only consider runways longer than configured minimum (meters)
		if rwy.Length >= constants.RunwayLengthNM*constants.MetersToNM {
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
	ap, ok := e.AtcService.Airports[icao]
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
	if info := e.AtcService.GetAirlineByName(f.AirlineName); info != nil {
		return info
	}

	// --- FALLBACKS ---
	// At this point, we know we don't have a name match.
	// We will now find a code and immediately return its full info struct.
	// 2. Matching Pairs (Airlines at both ends)
	util.LogWarnWithLabel(f.AircraftRegistration, "airline %s not found - allocating by orign/destination gate pairing logic", f.AirlineName)
	origin := e.AtcService.Airports[f.IcaoOrigin]
	dest := e.AtcService.Airports[f.IcaoDest]
	if origin != nil && dest != nil {
		if code := getWeightedCommonAirline(origin, dest); code != "" {
			airline := e.AtcService.GetAirlineByCode(code)
			if airline != nil {
				return airline
			}
		}
	}

	// 3. Origin Hub Weighted Selection
	util.LogWarnWithLabel(f.AircraftRegistration, "allocating airline by origin gate logic")
	if origin != nil && len(origin.HubWeights) > 0 {
		if code := getWeightedRandomAirline(origin.HubWeights); code != "" {
			airline := e.AtcService.GetAirlineByCode(code)
			if airline != nil {
				return airline
			}
		}
	}

	// 4. Registration Country Fallback
	util.LogWarnWithLabel(f.AircraftRegistration, "allocating airline by country of registration logic")
	countryCode := e.AtcService.GetCountryFromRegistration(f.AircraftRegistration)
	if countryCode == "" {
		countryCode = e.AtcService.Config.ATC.AirlineCountryCodeFallback
	}

	if countryCode != "" {
		code := e.AtcService.GetRandomAirlineByCountry(countryCode)
		if code != "" {
			return e.AtcService.GetAirlineByCode(code)
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
	origin, okO := e.AtcService.Airports[originICAO]
	dest, okD := e.AtcService.Airports[destICAO]

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

// getPhaseGroundSpeedKts returns a nominal ground speed (knots) appropriate for the phase and aircraft size class.
func (e *D9TrafficEngine) getPhaseGroundSpeedKts(ac *atc.Aircraft, phase flightphase.FlightPhase) float64 {
	// Default conservative speeds
	switch phase {
	case flightphase.TaxiOut, flightphase.TaxiIn:
		return 18.0
	case flightphase.Takeoff:
		switch ac.SizeClass {
		case "E", "F":
			return 140.0
		case "C", "D":
			return 160.0
		default:
			return 120.0
		}
	case flightphase.Climbout, flightphase.Departure:
		switch ac.SizeClass {
		case "E", "F":
			return 220.0
		case "C", "D":
			return 240.0
		default:
			return 200.0
		}
	case flightphase.Cruise:
		// Use a high nominal speed but Cruise uses its own interpolation logic elsewhere
		return 420.0
	case flightphase.Arrival, flightphase.Approach, flightphase.Final:
		switch ac.SizeClass {
		case "E", "F":
			return 140.0
		case "C", "D":
			return 130.0
		default:
			return 110.0
		}
	case flightphase.Braking:
		return 90.0
	default:
		return 120.0
	}
}

// getPhaseVerticalRateFpm returns a nominal vertical rate (feet per minute) for the given phase and aircraft.
func (e *D9TrafficEngine) getPhaseVerticalRateFpm(ac *atc.Aircraft, phase flightphase.FlightPhase) float64 {
	switch phase {
	case flightphase.Takeoff:
		switch ac.SizeClass {
		case "E", "F":
			return 2500.0
		case "C", "D":
			return 3000.0
		default:
			return 3500.0
		}
	case flightphase.Climbout:
		switch ac.SizeClass {
		case "E", "F":
			return 1500.0
		case "C", "D":
			return 2000.0
		default:
			return 2500.0
		}
	case flightphase.Departure:
		switch ac.SizeClass {
		case "E", "F":
			return 1200.0
		case "C", "D":
			return 1500.0
		default:
			return 1800.0
		}
	case flightphase.Cruise:
		return 0.0
	case flightphase.Arrival:
		switch ac.SizeClass {
		case "E", "F":
			return -600.0
		case "C", "D":
			return -900.0
		default:
			return -1000.0
		}
	case flightphase.Approach:
		switch ac.SizeClass {
		case "E", "F":
			return -600.0
		case "C", "D":
			return -800.0
		default:
			return -900.0
		}
	case flightphase.Final:
		// Final descent tuned per-size; default is -700 fpm per request
		switch ac.SizeClass {
		case "E", "F":
			return -500.0
		case "C", "D":
			return -700.0
		default:
			return -700.0
		}
	case flightphase.Braking, flightphase.TaxiIn, flightphase.TaxiOut:
		return 0.0
	default:
		return 0.0
	}
}

// getRunwayLock attempts to acquire a lock on the runway for the given aircraft.
// returns true if the lock was successfully acquired, or false if the runway is already locked by another aircraft.
// If the runway is currently locked by the same aircraft, it will return true to allow them to maintain their lock.
// If the runway is currently unlocked, it will be locked for the requesting aircraft with the current timestamp.
func (e *D9TrafficEngine) getRunwayLock(ap *atc.Airport, rwy *atc.Runway, ac *atc.Aircraft) bool {

	rwyLockKey := normalizeRunwayKey(ap.ICAO, rwy)

	if e.AtcService.UserHasRunwayClearance(rwy) {
		e.addToQueue(rwyLockKey, ac.Registration)
		return false
	}

	lock, locked := e.RunwayLocks[rwyLockKey]
	if locked {
		if lock.OccupiedBy.Registration == ac.Registration {
			return true // Already locked by this aircraft
		}
		if lock.OccupiedSince.Add(RUNWAY_LOCK_TIMEOUT_SECONDS * time.Second).Before(e.AtcService.GetCurrentZuluTime()) {
			// Lock has expired, allow new lock - set to false and fall through to acquire
			locked = false
			util.LogWarnWithLabel(ac.Registration, "runway lock for %s at %s has expired, overriding previous lock held by %s", rwy.Name, ap.ICAO, lock.OccupiedBy.Registration)
		}
	}
	if !locked {
		// acquire lock on runway
		e.RunwayLocks[rwyLockKey] = &RunwayLock{
			OccupiedBy:    ac,
			OccupiedSince: e.AtcService.GetCurrentZuluTime(),
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
