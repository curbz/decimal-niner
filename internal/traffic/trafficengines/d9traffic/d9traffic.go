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

    AMINUS_APPROACH_MINS = 8 // ~15-20 NM out
    AMINUS_FINAL_MINS    = 4  // ~4-5 NM out
    AMINUS_LAND_MINS     = 0  // Touchdown
	AMINUS_BRAKING		 = 1
    AMINUS_TAXIIN_MINS   = -10
	AMINUS_SHUTDOWN_MINS = -15
	AMINUS_PARKED_MINS   = -30

	// allowable time variance (minutes) in phase duration. example: Parked jitter of 240 means that the parked phase duration
	// can be reduced or increased by up to half of this time i.e. 120 seconds
	PARKED_JITTER_SECONDS   = 240
	STARTUP_JITTER_SECONDS  = 120
	TAXIOUT_JITTER_SECONDS  = 240
	DEPART_JITTER_SECONDS   = 120
	CLIMBOUT_JITTER_SECONDS = 180
	APPROACH_JITTER_SECONDS = 100
    FINAL_JITTER_SECONDS    = 60
	BRAKING_JITTER_SECONDS  = 20
	SHUTDOWN_JITTER_SECONDS = 120

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
	initialPhase, dur := e.determineInitialDepaturePhase(ttd, f)
	tDur := time.Duration(math.Abs(float64(dur))) * time.Second

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
				EstimatedNextTransition: currSimZTime.Add(tDur),
				TotalDuration: tDur,
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
				EstimatedNextTransition: currSimZTime.Add(tDur),
				TotalDuration: tDur,
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
    currSimZTime := e.atcService.GetCurrentZuluTime()

    for _, ac := range e.ActiveAircraft {
        f := ac.Flight.Schedule
        if f == nil { continue }

        // We determine the current relevant airport based on flight class
        var airport *atc.Airport
        if ac.Flight.Phase.Class == flightclass.Arriving || ac.Flight.Phase.Class == flightclass.PostflightParked {
            airport = e.atcService.Airports[f.IcaoDest]
        } else {
            airport = e.atcService.Airports[f.IcaoOrigin]
        }

        if airport == nil { continue }

        switch flightphase.FlightPhase(ac.Flight.Phase.Current) {

        case flightphase.Unknown:
            diff := e.timeDiffToDeparture(f)
            initialPhase, dur := e.determineInitialDepaturePhase(diff, f)
            // Initial spawn uses the math.Abs duration without extra jitter to respect schedule
            e.transitionToPhase(ac, initialPhase, dur*60, 0)
            e.atcService.SetFlightPhaseClass(ac)

        // --- DEPARTURE FLOW ---

        case flightphase.Parked:
            if ac.Flight.Phase.Class == flightclass.PostflightParked {
                e.endFlight(ac) // Cleanup logic
                continue
            }
            if e.positionAtOriginParking(ac) == nil { continue }

            if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
                dur := (DMINUS_PARKED_MINS - DMINUS_STARTUP_MINS) * 60
                e.transitionToPhase(ac, flightphase.Startup, dur, STARTUP_JITTER_SECONDS)
                ac.Flight.Phase.Class = flightclass.Departing
            }

        case flightphase.Startup:
            if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
                dur := (DMINUS_STARTUP_MINS - DMINUS_TAXIOUT_MINS) * 60
                if ac.Flight.AssignedParkingSpot != nil {
                    e.releaseParking(f.IcaoOrigin, ac.Flight.AssignedParkingSpot)
                }
                e.transitionToPhase(ac, flightphase.TaxiOut, dur, TAXIOUT_JITTER_SECONDS)
            }

        case flightphase.TaxiOut:
            if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
                dur := (DMINUS_TAXIOUT_MINS - DMINUS_DEPART_MINS) * 60
                e.transitionToPhase(ac, flightphase.Depart, dur, DEPART_JITTER_SECONDS)
                
                // Position at runway threshold
                if rwy := e.determineActiveRunway(airport); rwy != nil {
                    ac.Flight.Position.Lat = rwy.Lat
                    ac.Flight.Position.Long = rwy.Lon
                    ac.Flight.Position.Heading = rwy.Heading
                    ac.Flight.Position.Altitude = math.Max(airport.Elevation, rwy.ThresholdElevation)
                }
            }

        case flightphase.Depart:
            if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
                dur := (DMINUS_DEPART_MINS - DMINUS_CLIMBOUT_MINS) * 60
                e.transitionToPhase(ac, flightphase.Climbout, dur, CLIMBOUT_JITTER_SECONDS)
                
                // Jump 2NM out and up
                newLat, newLon := geometry.Project(ac.Flight.Position.Lat, ac.Flight.Position.Long, ac.Flight.Position.Heading, 2.0)
                ac.Flight.Position.Lat = newLat
                ac.Flight.Position.Long = newLon
                ac.Flight.Position.Altitude = airport.Elevation + 2500.0
            }

        case flightphase.Climbout:
            if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
                e.transitionToPhase(ac, flightphase.Cruise, 1800, 0)
                ac.Flight.Phase.Class = flightclass.Cruising
            }

        case flightphase.Cruise:
            // Cruise is the bridge between Departure and Arrival logic.
            // 1. If the aircraft is Departing/Cruising, it's heading away from Origin.
            // 2. If the simulation time reaches the "Arrival Window" (e.g., 8 mins before arrival),
            //    we flip the class to Arriving and start the Approach duration.
            
            tta := e.timeDiffToArrival(f) // Minutes until scheduled arrival
            
            if tta <= AMINUS_APPROACH_MINS {
                // Total duration for Approach = (8 mins - 4 mins) = 4 mins
                durSecs := (AMINUS_APPROACH_MINS - AMINUS_FINAL_MINS) * 60
                e.transitionToPhase(ac, flightphase.Approach, durSecs, APPROACH_JITTER_SECONDS)
                
                ac.Flight.Phase.Class = flightclass.Arriving
                
                util.LogWithLabel(ac.Registration, "commencing approach into %s (TTA: %d mins)", 
                    f.IcaoDest, tta)
            } else {
                // Optional: Simple linear movement between Origin and Destination
                // Or just let it "float" in the cruise state for now.
                util.LogWithLabel(ac.Registration, "cruising... %d minutes until approach window", 
                    tta - AMINUS_APPROACH_MINS)
            }

        // --- ARRIVAL FLOW ---

        case flightphase.Approach:
            e.updateInboundPosition(ac)
            if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
                dur := (AMINUS_APPROACH_MINS - AMINUS_FINAL_MINS) * 60
                e.transitionToPhase(ac, flightphase.Final, dur, FINAL_JITTER_SECONDS)
            }

        case flightphase.Final:
            e.updateInboundPosition(ac)
            if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
                dur := (AMINUS_FINAL_MINS - AMINUS_LAND_MINS) * 60
                e.transitionToPhase(ac, flightphase.Braking, dur, BRAKING_JITTER_SECONDS)
                ac.Flight.Position.Altitude = airport.Elevation
            }

        case flightphase.Braking:
            e.updateInboundPosition(ac)
            if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
                // Search for parking during rollout
                spot := e.findAvailableParking(airport, ac.SizeClass, ac.Flight.Airline.ICAO)
                if spot != nil {
                    ac.Flight.AssignedParkingSpot = spot
                    ac.Flight.AssignedParkingName = spot.Name
                    e.OccupiedParking[fmt.Sprintf("%s_%s", airport.ICAO, spot.Name)] = ac.Registration
                    spot.IsOccupied = true
                }

                dur := (AMINUS_BRAKING - AMINUS_TAXIIN_MINS) * 60
                e.transitionToPhase(ac, flightphase.TaxiIn, dur, TAXIOUT_JITTER_SECONDS)
            }

        case flightphase.TaxiIn:
            // Optional: Move aircraft toward gate
            if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
                e.positionAtDestParking(ac)
                dur := (AMINUS_TAXIIN_MINS - AMINUS_SHUTDOWN_MINS) * 60
                e.transitionToPhase(ac, flightphase.Shutdown, dur, SHUTDOWN_JITTER_SECONDS)
                ac.Flight.Phase.Class = flightclass.PostflightParked
            }

        case flightphase.Shutdown:
            if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
                dur := (AMINUS_SHUTDOWN_MINS - AMINUS_PARKED_MINS) * 60
                e.transitionToPhase(ac, flightphase.Parked, dur, PARKED_JITTER_SECONDS)
                ac.Flight.Phase.Class = flightclass.PostflightParked
            }
        }

        // --- LOGGING & STATE SYNC ---

        if ac.Flight.Phase.Current != ac.Flight.Phase.Previous {
			
			logMsg := ""
            if e.initialised {
                e.atcService.NotifyFlightPhaseChange(ac)
                util.LogWithLabel(ac.Registration, "changed phase from %s to %s. Next: %v",
                    flightphase.FlightPhase(ac.Flight.Phase.Previous).String(),
                    flightphase.FlightPhase(ac.Flight.Phase.Current).String(),
                    ac.Flight.Phase.EstimatedNextTransition.Format(time.Kitchen))
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
        actualSecs += (rand.IntN((jitterSecs * 2) + 1) - jitterSecs)
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
    
    // We need the total duration of this specific phase. 
    // You may need to store 'TotalDuration' in your Phase struct during transition.
    totalDuration := ac.Flight.Phase.TotalDuration.Seconds()
    if totalDuration <= 0 { return }

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

// DetermineInitialPhase returns the initial phase of a new spawned aircraft and the estimated remaining duration
// of the phase in seconds. We add some random seconds to avoid all aircraft transitioning at the same time
func (e *D9TrafficEngine) determineInitialDepaturePhase(diff int, f *flightplan.ScheduledFlight) (flightphase.FlightPhase, int) {
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
        // It's past the climbout phase, so it's cruising. 
        // Calculate how long until it needs to start its approach.
        tta := e.timeDiffToArrival(f)
        remainingCruise := (tta - AMINUS_APPROACH_MINS) * 60
        return flightphase.Cruise, int(math.Max(0, float64(remainingCruise)))
	}
}

func (e *D9TrafficEngine) determineInitialArrivalPhase(diff int, f *flightplan.ScheduledFlight) (flightphase.FlightPhase, int) {
    switch {
    // APPROACH: 
    case diff <= AMINUS_APPROACH_MINS && diff > AMINUS_FINAL_MINS:
        estimatedDuration := ((diff - AMINUS_FINAL_MINS) * 60) + 
            (rand.IntN((APPROACH_JITTER_SECONDS*2)+1) - APPROACH_JITTER_SECONDS)
        return flightphase.Approach, estimatedDuration

    // FINAL: 
    case diff <= AMINUS_FINAL_MINS && diff > AMINUS_LAND_MINS:
        estimatedDuration := (diff * 60) + 
            (rand.IntN((FINAL_JITTER_SECONDS*2)+1) - FINAL_JITTER_SECONDS)
        return flightphase.Final, estimatedDuration

    // BRAKING: 
    case diff <= AMINUS_LAND_MINS && diff > AMINUS_BRAKING:
        estimatedDuration := ((diff - AMINUS_BRAKING) * 60) + 
            (rand.IntN((BRAKING_JITTER_SECONDS*2)+1) - BRAKING_JITTER_SECONDS)
        return flightphase.Braking, estimatedDuration

    // TAXI IN: 
    case diff <= AMINUS_BRAKING && diff > AMINUS_TAXIIN_MINS:
        // We use TAXIOUT jitter here as a proxy for movement variance
        estimatedDuration := ((diff - AMINUS_TAXIIN_MINS) * 60) + 
            (rand.IntN((TAXIOUT_JITTER_SECONDS*2)+1) - TAXIOUT_JITTER_SECONDS)
        return flightphase.TaxiIn, estimatedDuration

    // SHUTDOWN: 
    case diff <= AMINUS_TAXIIN_MINS && diff > AMINUS_SHUTDOWN_MINS:
        estimatedDuration := ((diff - AMINUS_SHUTDOWN_MINS) * 60) + 
            (rand.IntN((SHUTDOWN_JITTER_SECONDS*2)+1) - SHUTDOWN_JITTER_SECONDS)
        return flightphase.Shutdown, estimatedDuration

    // PARKED: 
    case diff <= AMINUS_SHUTDOWN_MINS && diff >= AMINUS_PARKED_MINS:
        estimatedDuration := ((diff - AMINUS_PARKED_MINS) * 60) + 
            (rand.IntN((PARKED_JITTER_SECONDS*2)+1) - PARKED_JITTER_SECONDS)
        return flightphase.Parked, estimatedDuration

    // DEFAULT: 
    default:
        // It's too far out for the Approach phase (> 8 mins).
        // Calculate time remaining until it reaches that 8-minute mark.
        tta := e.timeDiffToArrival(f)
        remainingCruise := (tta - AMINUS_APPROACH_MINS) * 60
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

