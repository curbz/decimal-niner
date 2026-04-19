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
	// time difference (minutes) in relation to scheduled departure time
	DMINUS_PARKED_MINS   = 30
	DMINUS_STARTUP_MINS  = 15
	DMINUS_TAXIOUT_MINS  = 10
	DMINUS_DEPART_MINS   = 0
	DMINUS_CLIMBOUT_MINS = -5
	DMINUS_CRUISE        = -15

    AMINUS_APPROACH_MINS = 12 // ~15-20 NM out
    AMINUS_FINAL_MINS    = 4  // ~4-5 NM out
    AMINUS_LAND_MINS     = 0  // Touchdown
    AMINUS_TAXIIN_MINS   = -2 // Off runway, taxing
	//TODO add shutdown and parked

	// allowable time variance (minutes) in phase duration. example: Parked jitter of 240 means that the parked phase duration
	// can be reduced or increased by up to half of this time i.e. 120 seconds
	PARKED_JITTER_SECONDS   = 240
	STARTUP_JITTER_SECONDS  = 120
	TAXIOUT_JITTER_SECONDS  = 240
	DEPART_JITTER_SECONDS   = 120
	CLIMBOUT_JITTER_SECONDS = 180
)

func New(cfgPath string) (traffic.Engine, error) {
	cfg, err := util.LoadConfig[D9TrafficConfig](cfgPath)
	if err != nil {
		logger.Log.Errorf("Error reading configuration file: %v", err)
		return nil, err
	}

	return &D9TrafficEngine{
		FlightPlanPath:  cfg.D9Traffic.FlightPlanPath,
		ActiveAircraft:         make(map[string]*atc.Aircraft),
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
			relevantICAOs := e.getRelevantICAOs()
			for _, icao := range relevantICAOs {
				e.checkForDepartureSpawns(icao, day, hour, min)
				e.checkForArrivalSpawns(icao, day, hour, min)
			}

			// 3. Update existing aircraft (Phase transitions)
			e.updateActiveAircraft()

			util.LogWithLabel("D9TRAFFIC", "update cycle duration: %v, total active aircraft: %d", time.Since(start), len(e.ActiveAircraft))
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
        if f.ArrivalDayOfWeek != day { continue }
        
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
	initialPhase, dur := e.determineInitialDepaturePhase(ttd)

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
		SizeClass: sizeClass,
		Flight: atc.Flight{
			Number:      f.Number,
			Origin:      f.IcaoOrigin,
			Destination: f.IcaoDest,
			Airline: airline,
			AssignedRunway: e.determineActiveRunway(airport).Name,
			Comms: atc.Comms{
				CountryCode: airline.CountryCode,
				Callsign: fmt.Sprintf("%s %d %s", airline.Callsign, f.Number, sizeClassStr),
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
				EstimatedNextTransition: currSimZTime.Add(time.Duration(math.Abs(float64(dur))) * time.Second),
			},
		},
	}
	e.atcService.SetFlightPhaseClass(newAc)

	e.ActiveAircraft[getActiveAircraftKey(newAc)] = newAc

	util.LogWithLabel(f.AircraftRegistration, "successfully spawned outbound aircraft: %s flight %d - estimated next tranistion: %v",
		f.AirlineName, f.Number, newAc.Flight.Phase.EstimatedNextTransition.Format(time.RFC3339))

}

func (e *D9TrafficEngine) spawnInboundTraffic(f *flightplan.ScheduledFlight) {

    tta := e.timeDiffToArrival(f)
    initialPhase, dur := e.determineInitialArrivalPhase(tta)

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
			Airline: airline,
			AssignedRunway: e.determineActiveRunway(airport).Name,
			Comms: atc.Comms{
				CountryCode: airline.CountryCode,
				Callsign: fmt.Sprintf("%s %d %s", airline.Callsign, f.Number, sizeClassStr),
			},
			Schedule: f,
			// Squawk random number between 1200 and 6999
			Squawk:       fmt.Sprintf("%04d", 1200+rand.IntN(5800)),
			PlanAssigned: true,
            Phase: flightphase.Phase{
                Current:    initialPhase.Index(),
				Previous:                flightphase.Unknown.Index(),
				Transition:              currSimZTime,
				EstimatedNextTransition: currSimZTime.Add(time.Duration(math.Abs(float64(dur))) * time.Second),
            },
        },
    }

	e.setInitialArrivalPosition(newAc, tta)
	e.atcService.SetFlightPhaseClass(newAc)

	e.ActiveAircraft[getActiveAircraftKey(newAc)] = newAc

	util.LogWithLabel(f.AircraftRegistration, "successfully spawned inbound aircraft: %s flight %d - estimated next tranistion: %v",
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

func (e *D9TrafficEngine) updateActiveAircraft() {

	for _, ac := range e.ActiveAircraft { 

		f := ac.Flight.Schedule
		if f == nil {
			util.LogWarnWithLabel(ac.Registration, "no flight schedule assigned - unable to update aircraft state")
			continue
		}
		airport, found := e.atcService.Airports[f.IcaoOrigin]
		if !found {
			util.LogWarnWithLabel(ac.Registration, "airport origin %s not found", f.IcaoOrigin)
			continue
		}

		diff := e.timeDiffToDeparture(f)
		currSimZTime := e.atcService.GetCurrentZuluTime()
		dur := 0

		switch flightphase.FlightPhase(ac.Flight.Phase.Current) {
		case flightphase.Unknown:
			// shouldn't be the case that the phase is unknown at this point, but this acts as a safety net
			initialPhase, dur := e.determineInitialDepaturePhase(diff)
			ac.Flight.Phase.Current = initialPhase.Index()
			ac.Flight.Phase.Transition = currSimZTime
			ac.Flight.Phase.EstimatedNextTransition = currSimZTime.Add(time.Duration(math.Abs(float64(dur))) * time.Second)
			e.atcService.SetFlightPhaseClass(ac)
			continue
		case flightphase.Parked:
			if ac.Flight.Phase.Class == flightclass.PostflightParked {
				e.endFlight(ac)
				continue
			} 
			if e.positionAtOriginParking(ac) == nil {
				continue
			}
			if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
				ac.Flight.Phase.Current = flightphase.Startup.Index()
				ac.Flight.Phase.Class = flightclass.Departing
				dur = DMINUS_STARTUP_MINS - DMINUS_TAXIOUT_MINS
			}
		case flightphase.Startup:
			if e.positionAtOriginParking(ac) == nil {
				continue
			}
			if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
				ac.Flight.Phase.Current = flightphase.TaxiOut.Index()
				ac.Flight.Phase.Class = flightclass.Departing
				if ac.Flight.AssignedParkingSpot != nil {
					e.releaseParking(f.IcaoOrigin, ac.Flight.AssignedParkingSpot)
				}
				dur = DMINUS_TAXIOUT_MINS - DMINUS_DEPART_MINS
			}
		case flightphase.TaxiOut:
			if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
				ac.Flight.Phase.Current = flightphase.Depart.Index()
				ac.Flight.Phase.Class = flightclass.Departing
				dur = DMINUS_DEPART_MINS - DMINUS_CLIMBOUT_MINS
				if rwy := e.determineActiveRunway(airport); rwy != nil {
					ac.Flight.Position.Lat = rwy.Lat
					ac.Flight.Position.Long = rwy.Lon
					ac.Flight.Position.Heading = rwy.Heading
					if rwy.ThresholdElevation == 0 {
						ac.Flight.Position.Altitude = airport.Elevation
					} else {
						ac.Flight.Position.Altitude = rwy.ThresholdElevation
					}
				}
			}
		case flightphase.Depart:
			if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
				ac.Flight.Phase.Current = flightphase.Climbout.Index()
				ac.Flight.Phase.Class = flightclass.Departing
				dur = DMINUS_CLIMBOUT_MINS - DMINUS_CRUISE
				// Move them "Up and Out"
				// Offset the Lat/Lon slightly in the direction of the heading
				newLat, newLon := geometry.Project(ac.Flight.Position.Lat, ac.Flight.Position.Long, ac.Flight.Position.Heading, 2.0) // 2NM out

				ac.Flight.Position.Lat = newLat
				ac.Flight.Position.Long = newLon
				ac.Flight.Position.Altitude = airport.Elevation + 2500.0

			}
		case flightphase.Climbout:
			if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
				ac.Flight.Phase.Current = flightphase.Cruise.Index()
				ac.Flight.Phase.Class = flightclass.Cruising
				//TODO figure out what we do with further phases and what we set dur to
			}

		case flightphase.Approach, flightphase.Final, flightphase.Braking:
			// 1. Move the aircraft based on current phase speed
			e.updateInboundPosition(ac)

			// 2. Check for phase transitions based on distance
			dist := e.calculateDistanceToRunway(ac)
			
			if ac.Flight.Phase.Current == flightphase.Approach.Index() && dist <= 4.0 {
				ac.Flight.Phase.Current = flightphase.Final.Index()
				util.LogWithLabel(ac.Registration, "intercepted final approach for %s", ac.Flight.AssignedRunway)
			} else if ac.Flight.Phase.Current == flightphase.Final.Index() && dist <= 0.1 {
				ac.Flight.Phase.Current = flightphase.Braking.Index()
				// Set a 40-second timer for the rollout/braking
				ac.Flight.Phase.EstimatedNextTransition = currSimZTime.Add(40 * time.Second)
				util.LogWithLabel(ac.Registration, "touchdown at %s", ac.Flight.Destination)
			} else if ac.Flight.Phase.Current == flightphase.Braking.Index() && currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
				spot := e.findAvailableParking(airport, ac.SizeClass, ac.Flight.Airline.ICAO)
				if spot == nil {
					util.LogWarnWithLabel(ac.Registration, "no suitable parking found at airport %s - flight ended", airport.ICAO)
					//TODO: remove from active flights
					continue
				} else {
					util.LogWithLabel(ac.Registration, "assigning parking at airport %s to spot %s", airport.ICAO, spot.Name)
				}
				ac.Flight.AssignedParkingSpot = spot
				ac.Flight.AssignedParkingName = spot.Name
				key := fmt.Sprintf("%s_%s", airport.ICAO, spot.Name)
				e.OccupiedParking[key] = ac.Registration
				spot.IsOccupied = true
				// Give them 5 minutes to reach the gate
				ac.Flight.Phase.EstimatedNextTransition = currSimZTime.Add(5 * time.Minute)
				ac.Flight.Phase.Current = flightphase.TaxiIn.Index()
			}

		case flightphase.TaxiIn:
			// Logic to move toward AssignedParking (which was found during Braking)
			if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
				ac.Flight.Phase.Current = flightphase.Shutdown.Index()
				// Finalize position to exact gate coords
				e.positionAtDestParking(ac)
			}
		//TODO handle transition from shutdown to parked = must ensure parked results in flighclass of postflight parked so that it is removed from active flights in updateActiveAircraft
		default:
			continue
		}

		if ac.Flight.Phase.Current != ac.Flight.Phase.Previous {
			logMsg := ""
			if e.initialised {
				ac.Flight.Phase.EstimatedNextTransition = currSimZTime.Add(time.Duration(math.Abs(float64(dur))) * time.Second)
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
				ac.Flight.Phase.EstimatedNextTransition.Format(time.RFC3339),
			)

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
		spot := e.findAvailableParking(airport, ac.SizeClass, ac.Flight.Airline.ICAO)
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

// DetermineInitialPhase returns the initial phase of a new spawned aircraft and the estimated remaining duration
// of the phase in seconds. We add some random seconds to avoid all aircraft transitioning at the same time
func (e *D9TrafficEngine) determineInitialDepaturePhase(diff int) (flightphase.FlightPhase, int) {
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
		return flightphase.Cruise, 0
	}
}

func (e *D9TrafficEngine) determineInitialArrivalPhase(tta int) (flightphase.FlightPhase, int) {
    switch {
    case tta > AMINUS_APPROACH_MINS:
        // Still far out, spawn in Cruise/Descent
        dur := (tta - AMINUS_APPROACH_MINS) * 60
        return flightphase.Cruise, dur + rand.IntN(60)

    case tta <= AMINUS_APPROACH_MINS && tta > AMINUS_FINAL_MINS:
        // Between 12 and 4 mins to landing: Approach
        dur := (tta - AMINUS_FINAL_MINS) * 60
        return flightphase.Approach, dur + rand.IntN(30)

    case tta <= AMINUS_FINAL_MINS && tta > AMINUS_LAND_MINS:
        // Between 4 and 0 mins: Final
        dur := (tta - AMINUS_LAND_MINS) * 60
        return flightphase.Final, dur + rand.IntN(15)

    case tta <= AMINUS_LAND_MINS && tta > AMINUS_TAXIIN_MINS:
        // Just landed: Braking
        dur := (tta - AMINUS_TAXIIN_MINS) * 60
        return flightphase.Braking, dur

    default:
        // Already should be at the gate or taxiing
        return flightphase.TaxiIn, 300 // Give them 5 mins to reach gate
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

func (e *D9TrafficEngine) updateInboundPosition(ac *atc.Aircraft) {
    var speedKnots float64
    currentPhase := flightphase.FlightPhase(ac.Flight.Phase.Current)

    switch currentPhase {
    case flightphase.Approach:
        speedKnots = 200.0
    case flightphase.Final:
        speedKnots = 150.0
    case flightphase.Braking:
        // Linear deceleration from 140 to 20 knots
        speedKnots = 80.0 
    default:
        return
    }

    // Distance covered in 10 seconds: (Speed / 3600) * 10
    distPerTick := (speedKnots / 360) 

    // Move Lat/Lon forward
    newLat, newLon := geometry.Project(ac.Flight.Position.Lat, ac.Flight.Position.Long, ac.Flight.Position.Heading, distPerTick)
    ac.Flight.Position.Lat = newLat
    ac.Flight.Position.Long = newLon

    // Update Altitude for Approach/Final (3-degree slope)
    // Rule of thumb: 300ft per NM
    if currentPhase != flightphase.Braking {
        airport := e.atcService.Airports[ac.Flight.Destination]
        distToRwy := e.calculateDistanceToRunway(ac)
        ac.Flight.Position.Altitude = airport.Elevation + (distToRwy * 300.0)
    }
}

func (e *D9TrafficEngine) findAvailableParking(airport *atc.Airport, reqClass string, airlineICAO string) *atc.ParkingSpot {
    // We run two passes: 
    // Pass 0: Try to find a match for Size AND Airline
    // Pass 1: Fallback to any spot that fits the Size
    for pass := 0; pass < 2; pass++ {
        for i := range airport.Parking {
            spot := &airport.Parking[i]

            // 1. Physical constraint: Must be at least as big as the aircraft
            if spot.WidthClass < reqClass {
                continue
            }

            // 2. Occupancy check (Global map)
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
                // spot.AirlineCodes is usually space-separated: "BAW VIR BEE"
                if !strings.Contains(spot.AirlineCodes, airlineICAO) {
                    continue
                }
            }

            // If we are in Pass 1, or we found an airline match in Pass 0, return it
            return spot
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

func (e *D9TrafficEngine) determineActiveRunway(airport *atc.Airport) *atc.Runway {

	// 1. Get current weather for the airport
	// This assumes your atcService can provide wind dir/speed
	windDir := e.atcService.GetWeatherState().Wind.Direction

	var bestRunway *atc.Runway
	maxHeadwindScore := -2.0 // Range is -1 to 1

	for _, rwy := range airport.Runways {
		// Calculate the angular difference
		diff := float64(windDir) - rwy.Heading
		radDiff := diff * math.Pi / 180

		// Score is the Cosine of the difference.
		// 1.0 = Direct Headwind (Perfect)
		// 0.0 = Direct Crosswind
		// -1.0 = Direct Tailwind (Avoid!)
		score := math.Cos(radDiff)

		if score > maxHeadwindScore {
			maxHeadwindScore = score
			bestRunway = rwy
		}
	}

	// Fallback: If wind is calm, just pick the first runway in the map
	if bestRunway == nil {
		for _, r := range airport.Runways {
			return r
		}
	}

	return bestRunway
}

func (e *D9TrafficEngine) determineSizeClass(f *flightplan.ScheduledFlight, info *atc.AirlineInfo) string {
    // 1. Calculate the Distance Baseline
    distNM := e.calculateFlightDistance(f.IcaoOrigin, f.IcaoDest)
    
    // 2. Initial estimate based on distance
    size := "C" 
    switch {
    case distNM < 450:  size = "B"
    case distNM > 2800: size = "E" // Heavy
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
		ICAO: 	  "UNK",
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

func (e *D9TrafficEngine) calculateDistanceToRunway(ac *atc.Aircraft) float64 {
    airport := e.atcService.Airports[ac.Flight.Destination]
    // We assume the aircraft is assigned to the "best" runway calculated at spawn
    rwy := e.getAssignedRunway(airport, ac.Flight.AssignedRunway)
    
    // Haversine distance between current position and runway threshold
    return geometry.DistNM(ac.Flight.Position.Lat, ac.Flight.Position.Long, rwy.Lat, rwy.Lon)
}

func (e *D9TrafficEngine) getAssignedRunway(ap *atc.Airport, name string) *atc.Runway {
    if ap == nil {
        return nil
    }
    
    // Most airports only have a few runways, so a simple loop is efficient.
    for _, rwy := range ap.Runways {
        if rwy.Name == name {
            return rwy
        }
    }
    
    // Fallback: If for some reason the name doesn't match, 
    // return the first available runway so the geometry doesn't crash.
    for _, rwy := range ap.Runways {
        return rwy
    }
    
    return nil
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

