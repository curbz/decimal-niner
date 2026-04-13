package d9traffic

import (
	"fmt"
	"math/rand/v2"
	"sort"
	"time"

	"github.com/curbz/decimal-niner/internal/atc"
	"github.com/curbz/decimal-niner/internal/flightclass"
	"github.com/curbz/decimal-niner/internal/flightphase"
	"github.com/curbz/decimal-niner/internal/flightplan"
	"github.com/curbz/decimal-niner/internal/logger"
	"github.com/curbz/decimal-niner/internal/traffic"
	"github.com/curbz/decimal-niner/pkg/util"
)

type D9TrafficEngine struct {
	ActiveAircraft   []*atc.Aircraft
	AirportSchedules map[string]*AirportTimeline
	atcService       *atc.Service
	FlightPlanPath   string
	Spawned          map[string]bool // TailNumber -> bool
	initialised      bool
	OccupiedParking  map[string]string
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

const (
	DMINUS_PARKED_MINS   = 30
	DMINUS_STARTUP_MINS  = 15
	DMINUS_TAXIOUT_MINS  = 10
	DMINUS_DEPART_MINS   = 0
	DMINUS_CLIMBOUT_MINS = -10
	DMINUS_CRUISE        = -20

	PARKED_JITTER_SECONDS   = 120
	STARTUP_JITTER_SECONDS  = 90
	TAXIOUT_JITTER_SECONDS  = 60
	DEPART_JITTER_SECONDS   = 120
	CLIMBOUT_JITTER_SECONDS = 45
)

func New(cfgPath string) (traffic.Engine, error) {
	cfg, err := util.LoadConfig[D9TrafficConfig](cfgPath)
	if err != nil {
		logger.Log.Errorf("Error reading configuration file: %v", err)
		return nil, err
	}

	return &D9TrafficEngine{
		FlightPlanPath:  cfg.D9Traffic.FlightPlanPath,
		Spawned:         make(map[string]bool),
		OccupiedParking: make(map[string]string),
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
			relevantICAOs := e.GetRelevantICAOs()
			for _, icao := range relevantICAOs {
				e.CheckForNewSpawns(icao, day, hour, min)
			}

			// 3. Update existing aircraft (Phase transitions)
			e.UpdateActiveAircraft(day, hour, min)

			logger.Log.Infof("ticker duration: %v, total spawned aircraft: %d", time.Since(start), len(e.Spawned))
		}
	}()
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
	e.IngestSchedules(fscheds)
	return fscheds, airports
}

func (e *D9TrafficEngine) IngestSchedules(rawMap map[string][]flightplan.ScheduledFlight) {
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
	e.SortSchedules()

	logger.Log.Infof("Ghost Traffic: Ingested %d airports from flight plan map", len(e.AirportSchedules))
}

func (e *D9TrafficEngine) SortSchedules() {
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

func (e *D9TrafficEngine) GetRelevantICAOs() []string {
	icaoMap := make(map[string]bool)

	for _, ctrl := range e.atcService.UserState.ActiveFacilities {
		// We only care about airport-specific controllers (TWR, GND, DEL, etc.)
		// Center/Approach might not have a single ICAO, so we filter.
		if ctrl.ICAO != "" {
			icaoMap[ctrl.ICAO] = true
		}
	}

	// if the user is on the ground, include the nearest airport as a fallback for visual/proximity traffic
	if e.atcService.UserState.NearestAirport != nil && atc.IsAirborne(e.atcService.UserState.FlightPhase.Index(), false) {
		icaoMap[e.atcService.UserState.NearestAirport.ICAO] = true
	}

	var result []string
	for icao := range icaoMap {
		result = append(result, icao)
	}
	return result
}

func (e *D9TrafficEngine) CheckForNewSpawns(icao string, day, h, m int) {
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
				if !e.IsCurrentlyActive(f.AircraftRegistration) {
					e.TrySpawnGroundTraffic(&f)
				}
			}

			// Optimization: Since it's sorted, if we've passed the window, stop
			if compareMins > nowMins+lookahead {
				break
			}
		}
	}
}

func (e *D9TrafficEngine) IsCurrentlyActive(registration string) bool {
	active, exists := e.Spawned[registration]
	return exists && active
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

func (e *D9TrafficEngine) TrySpawnGroundTraffic(f *flightplan.ScheduledFlight) {

	ttd := e.timeDiffToDeparture(f)
	initialPhase, dur := e.DetermineInitialPhase(ttd)

	if initialPhase == flightphase.Unknown {
		return
	}

	currSimZTime := e.atcService.GetCurrentZuluTime()

	// Create the "Live" entity
	// TODO figure out airline code and classign - see callsign logic in xpconnect
	//aircraft.Flight.Comms.CountryCode = airlineInfo.CountryCode
	//aircraft.Flight.AirlineName = airlineInfo.AirlineName
	//aircraft.Flight.Comms.Callsign = fmt.Sprintf("%s %d %s", callsign, aircraft.Flight.Number, sizeClassStr)
	// TODO: assign runway - based on weather/wind and runway availability
	newAc := &atc.Aircraft{
		Registration: f.AircraftRegistration,
		//TODO set correct sizeclass
		SizeClass: atc.SizeClass[3],
		Flight: atc.Flight{
			Number:      f.Number,
			Origin:      f.IcaoOrigin,
			Destination: f.IcaoDest,
			Schedule:    f,
			// Squawk random number between 1200 and 6999
			Squawk:       fmt.Sprintf("%04d", 1200+rand.IntN(5800)),
			PlanAssigned: true,
			Phase: flightphase.Phase{
				Current:                 initialPhase.Index(),
				Previous:                flightphase.Unknown.Index(),
				Transition:              currSimZTime,
				EstimatedNextTransition: currSimZTime.Add(time.Duration(dur) * time.Second),
			},
		},
	}
	e.atcService.SetFlightPhaseClass(newAc)

	e.Spawned[f.AircraftRegistration] = true
	e.ActiveAircraft = append(e.ActiveAircraft, newAc)

	logger.Log.Infof("Successfully spawned ghost traffic: %s (%s %d)",
		f.AircraftRegistration, f.AirlineName, f.Number)

}

func (e *D9TrafficEngine) UpdateActiveAircraft(day, h, m int) {

	for _, ac := range e.ActiveAircraft {

		f := ac.Flight.Schedule
		diff := e.timeDiffToDeparture(f)
		currSimZTime := e.atcService.GetCurrentZuluTime()
		dur := 0

		switch flightphase.FlightPhase(ac.Flight.Phase.Current) {
		case flightphase.Unknown:
			// shouldn't be the case that the phase is unknown at this point, but this acts as a safety net
			initialPhase, dur := e.DetermineInitialPhase(diff)
			ac.Flight.Phase.Current = initialPhase.Index()
			ac.Flight.Phase.Transition = currSimZTime
			ac.Flight.Phase.EstimatedNextTransition = currSimZTime.Add(time.Duration(dur) * time.Second)
			e.atcService.SetFlightPhaseClass(ac)
			continue
		case flightphase.Parked:
			if ac.Flight.AssignedParking == "" {
				airport := e.atcService.Airports[f.IcaoOrigin]

				// Defaulting to "C" (Airliner)
				reqWidth := 15.0
				spot := e.FindAvailableParking(airport, reqWidth)

				if spot == nil {
					util.LogWarnWithLabel("D9TRAFFIC", "no available parking found for aircraft %s at airport %s - cannot spawn",
						ac.Registration, airport.ICAO)
					continue
				} else {
					util.LogWithLabel("D9TRAFFIC", "assigning parking for aircraft %s at airport %s to spot %s",
						ac.Registration, airport.ICAO, spot.Name)
				}

				ac.Flight.Position = atc.Position{
					Lat:     spot.Lat,
					Long:    spot.Lon,
					Heading: spot.Heading,
					//TODO: parse airport elevation from relevant xplane nav data
					//Altitude: airport.Elevation,
				}
				ac.Flight.AssignedParking = spot.Name
				key := fmt.Sprintf("%s_%s", airport.ICAO, spot.Name)
				e.OccupiedParking[key] = ac.Registration
				spot.IsOccupied = true
			}
			if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
				ac.Flight.Phase.Current = flightphase.Startup.Index()
				ac.Flight.Phase.Class = flightclass.Departing
				dur = DMINUS_TAXIOUT_MINS - DMINUS_STARTUP_MINS
			}
		case flightphase.Startup:
			if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
				ac.Flight.Phase.Current = flightphase.TaxiOut.Index()
				ac.Flight.Phase.Class = flightclass.Departing
				dur = DMINUS_DEPART_MINS - DMINUS_TAXIOUT_MINS
			}
		case flightphase.TaxiOut:
			if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
				ac.Flight.Phase.Current = flightphase.Depart.Index()
				ac.Flight.Phase.Class = flightclass.Departing
				dur = DMINUS_TAXIOUT_MINS - DMINUS_STARTUP_MINS
			}
		case flightphase.Depart:
			if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
				ac.Flight.Phase.Current = flightphase.Climbout.Index()
				ac.Flight.Phase.Class = flightclass.Departing
				dur = DMINUS_CLIMBOUT_MINS - DMINUS_TAXIOUT_MINS
			}
		case flightphase.Climbout:
			if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
				ac.Flight.Phase.Current = flightphase.Cruise.Index()
				ac.Flight.Phase.Class = flightclass.Cruising
				//TODO figure out what we do with further phases and what we set dur to
			}
		default:
			continue
		}

		if ac.Flight.Phase.Current != ac.Flight.Phase.Previous {

			ac.Flight.Phase.EstimatedNextTransition = currSimZTime.Add(time.Duration(dur) * time.Second)

			logMsg := ""
			if e.initialised {
				// Notify ATC service of flight phase change
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
				ac.Flight.Phase.EstimatedNextTransition.Format(time.RFC822),
			)

			ac.Flight.Phase.Previous = ac.Flight.Phase.Current
			ac.Flight.Phase.Transition = currSimZTime
		}
	}

	e.initialised = true
}

// DetermineInitialPhase returns the initial phase of a new spawned aircraft and the estimated remaining duration
// of the phase in seconds. We add some random seconds to avoid all aircraft transitioning at the same time
func (e *D9TrafficEngine) DetermineInitialPhase(diff int) (flightphase.FlightPhase, int) {
	switch {
	case diff > DMINUS_PARKED_MINS:
		estimatedDuration := ((diff - DMINUS_STARTUP_MINS) * 60) + (rand.IntN((PARKED_JITTER_SECONDS*2)+1) - PARKED_JITTER_SECONDS) 
		return flightphase.Parked, estimatedDuration
	case diff > DMINUS_STARTUP_MINS && diff <= DMINUS_PARKED_MINS:
		estimatedDuration := ((diff - DMINUS_STARTUP_MINS) * 60) + (rand.IntN((STARTUP_JITTER_SECONDS*2)+1) - STARTUP_JITTER_SECONDS)
		return flightphase.Parked, estimatedDuration
	case diff > DMINUS_TAXIOUT_MINS && diff <= DMINUS_STARTUP_MINS:
		estimatedDuration := ((diff - DMINUS_TAXIOUT_MINS) * 60) + (rand.IntN((TAXIOUT_JITTER_SECONDS*2)+1) - TAXIOUT_JITTER_SECONDS)
		return flightphase.Startup, estimatedDuration
	case diff > DMINUS_DEPART_MINS && diff <= DMINUS_TAXIOUT_MINS:
		estimatedDuration := ((diff - DMINUS_DEPART_MINS) * 60) + (rand.IntN((DEPART_JITTER_SECONDS*2)+1) - DEPART_JITTER_SECONDS) 
		return flightphase.TaxiOut, estimatedDuration
	case diff <= DMINUS_DEPART_MINS && diff >= DMINUS_CLIMBOUT_MINS:
		estimatedDuration := ((diff - DMINUS_DEPART_MINS) * 60) + (rand.IntN((CLIMBOUT_JITTER_SECONDS*2)+1) - CLIMBOUT_JITTER_SECONDS) 
		return flightphase.Depart, estimatedDuration
	default:
		//TODO handle spawnig later phases
		return flightphase.Cruise, 0
	}
}

func (e *D9TrafficEngine) FindAvailableParking(airport *atc.Airport, reqRadius float64) *atc.ParkingSpot {
	reqClass := GetWidthClass(reqRadius)

	for i := range airport.Parking {
		spot := &airport.Parking[i]

		// 1. Check size class
		if spot.WidthClass < reqClass {
			continue
		}

		// 2. Check the "Global Map" using a composite key
		key := fmt.Sprintf("%s_%s", airport.ICAO, spot.Name)
		if _, occupied := e.OccupiedParking[key]; occupied {
			continue
		}

		// 3. Check if user is at this specific spot
		if e.atcService.UserState.NearestAirport.ICAO == airport.ICAO &&
			e.atcService.UserState.AssignedParking.Name == spot.Name {
			continue
		}

		return spot
	}
	return nil
}

func (e *D9TrafficEngine) ReleaseParking(icao, spotName string) {
	key := fmt.Sprintf("%s_%s", icao, spotName)
	delete(e.OccupiedParking, key)
	logger.Log.Debugf("Parking spot %s at %s is now vacant.", spotName, icao)
}

func GetWidthClass(radiusMeters float64) string {
	switch {
	case radiusMeters <= 7.5:
		return "A"
	case radiusMeters <= 12.0:
		return "B"
	case radiusMeters <= 18.0:
		return "C"
	case radiusMeters <= 26.0:
		return "D"
	case radiusMeters <= 32.5:
		return "E"
	default:
		return "F"
	}
}
