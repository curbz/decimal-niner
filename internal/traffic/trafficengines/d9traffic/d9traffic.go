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
	// time difference (minutes) in relation to scheduled departure time
	DMINUS_PARKED_MINS   = 30
	DMINUS_STARTUP_MINS  = 15
	DMINUS_TAXIOUT_MINS  = 10
	DMINUS_DEPART_MINS   = 0
	DMINUS_CLIMBOUT_MINS = -5
	DMINUS_CRUISE        = -15

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
			relevantICAOs := e.getRelevantICAOs()
			for _, icao := range relevantICAOs {
				e.checkForNewSpawns(icao, day, hour, min)
			}

			// 3. Update existing aircraft (Phase transitions)
			e.updateActiveAircraft()

			util.LogWithLabel("D9TRAFFIC", "update cycle duration: %v, total spawned aircraft: %d", time.Since(start), len(e.Spawned))
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

func (e *D9TrafficEngine) checkForNewSpawns(icao string, day, h, m int) {
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
				if !e.isCurrentlyActive(f.AircraftRegistration) {
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

func (e *D9TrafficEngine) isCurrentlyActive(registration string) bool {
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

func (e *D9TrafficEngine) spawnGroundTraffic(f *flightplan.ScheduledFlight) {

	ttd := e.timeDiffToDeparture(f)
	initialPhase, dur := e.determineInitialPhase(ttd)

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

	// Create the "Live" entity
	//aircraft.Flight.Comms.CountryCode = airlineInfo.CountryCode

	// TODO: assign runway - based on weather/wind and runway availability
	newAc := &atc.Aircraft{
		Registration: f.AircraftRegistration,
		//TODO set correct sizeclass
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

	e.Spawned[f.AircraftRegistration] = true
	e.ActiveAircraft = append(e.ActiveAircraft, newAc)

	util.LogWithLabel("D9TRAFFIC", "successfully spawned aircraft: %s (%s %d) estimated next tranistion: %v",
		f.AircraftRegistration, f.AirlineName, f.Number, newAc.Flight.Phase.EstimatedNextTransition.Format(time.RFC3339))

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
			initialPhase, dur := e.determineInitialPhase(diff)
			ac.Flight.Phase.Current = initialPhase.Index()
			ac.Flight.Phase.Transition = currSimZTime
			ac.Flight.Phase.EstimatedNextTransition = currSimZTime.Add(time.Duration(math.Abs(float64(dur))) * time.Second)
			e.atcService.SetFlightPhaseClass(ac)
			continue
		case flightphase.Parked:
			if ac.Flight.AssignedParking == "" {

				spot := e.findAvailableParking(airport, ac.SizeClass, ac.Flight.Airline.CountryCode)
				if spot == nil {
					util.LogWarnWithLabel("D9TRAFFIC", "no available parking found for aircraft %s at airport %s - cannot spawn",
						ac.Registration, airport.ICAO)
					continue
				} else {
					util.LogWithLabel("D9TRAFFIC", "assigning parking for aircraft %s at airport %s to spot %s",
						ac.Registration, airport.ICAO, spot.Name)
				}

				ac.Flight.Position = atc.Position{
					Lat:      spot.Lat,
					Long:     spot.Lon,
					Heading:  spot.Heading,
					Altitude: airport.Elevation,
				}
				ac.Flight.AssignedParking = spot.Name
				key := fmt.Sprintf("%s_%s", airport.ICAO, spot.Name)
				e.OccupiedParking[key] = ac.Registration
				spot.IsOccupied = true
			}
			if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
				ac.Flight.Phase.Current = flightphase.Startup.Index()
				ac.Flight.Phase.Class = flightclass.Departing
				dur = DMINUS_STARTUP_MINS - DMINUS_TAXIOUT_MINS
			}
		case flightphase.Startup:
			if currSimZTime.After(ac.Flight.Phase.EstimatedNextTransition) {
				ac.Flight.Phase.Current = flightphase.TaxiOut.Index()
				ac.Flight.Phase.Class = flightclass.Departing
				if ac.Flight.AssignedParking != "" {
					e.releaseParking(f.IcaoOrigin, ac.Flight.AssignedParking)
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

// DetermineInitialPhase returns the initial phase of a new spawned aircraft and the estimated remaining duration
// of the phase in seconds. We add some random seconds to avoid all aircraft transitioning at the same time
func (e *D9TrafficEngine) determineInitialPhase(diff int) (flightphase.FlightPhase, int) {
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

func (e *D9TrafficEngine) releaseParking(icao, spotName string) {
	key := fmt.Sprintf("%s_%s", icao, spotName)
	delete(e.OccupiedParking, key)
	util.LogWithLabel("D9TRAFFIC", "Parking spot %s at %s is now vacant.", spotName, icao)
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
    countryCode :=  e.atcService.GetCountryFromRegistration(f.AircraftRegistration)
	if countryCode == "" {
		util.LogWarnWithLabel(f.AircraftRegistration, "could not determine country of registration - defaulting to %s", e.atcService.Config.ATC.AirlineCountryCodeFallback)
		countryCode = e.atcService.Config.ATC.AirlineCountryCodeFallback
	}
	if countryCode != "" {
		if code := getWeightedRandomAirline(map[string]float64{countryCode: 1.0}); code != "" {
            airline := e.atcService.GetAirlineByCode(code)
            if airline != nil {
                return airline
            }
		}
		code := e.atcService.GetRandomAirlineByCountry(countryCode)
		if code != "" {
            airline := e.atcService.GetAirlineByCode(code)
            if airline != nil {
                return airline
            }
		}
	}
	
	return nil
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
