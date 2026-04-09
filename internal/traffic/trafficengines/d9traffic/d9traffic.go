package d9traffic

import (
	"fmt"
	"sort"
	"time"

	"github.com/curbz/decimal-niner/internal/atc"
	"github.com/curbz/decimal-niner/internal/flightphase"
	"github.com/curbz/decimal-niner/internal/flightplan"
	"github.com/curbz/decimal-niner/internal/logger"
	"github.com/curbz/decimal-niner/internal/simdata"
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

func New(cfgPath string) (traffic.Engine, error) {
	cfg, err := util.LoadConfig[D9TrafficConfig](cfgPath)
	if err != nil {
		logger.Log.Errorf("Error reading configuration file: %v", err)
		return nil, err
	}

	return &D9TrafficEngine{
		FlightPlanPath: cfg.D9Traffic.FlightPlanPath,
		Spawned:        make(map[string]bool),
	}, nil
}

func (tg *D9TrafficEngine) SetATCService(atcService *atc.Service) {
	tg.atcService = atcService
}

func (e *D9TrafficEngine) Start() {
	ticker := time.NewTicker(10 * time.Second)
	go func() {
		for range ticker.C {
			// 1. Get current Sim Time (this should come from your X-Plane Datarefs)
			curTime, err := e.atcService.DataProvider.GetSimTime()
			if err != nil {
				logger.Log.Errorf("error getting sim time: %v", err)
				return
			}
			zuluDateTime := simdata.GetZuluDateTime(curTime)
			day := int(zuluDateTime.Weekday()) + 1 // Convert to 1-7 (BGL format)
			hour := zuluDateTime.Hour()
			min := zuluDateTime.Minute()

			// 2. Get User Location (Dataref: sim/flightmodel/position/latitude etc)
			userNearestAirport := e.atcService.UserState.NearestAirport
			if userNearestAirport == nil {
				util.LogWarnWithLabel("D9TRAFFIC", "user nearest airport is nil - skipping traffic tick")
				return
			}

			// 3. Run the Spawn Check
			e.CheckForNewSpawns(userNearestAirport.ICAO, day, hour, min)

			// 4. Update existing aircraft (Phase transitions)
			e.UpdateActiveAircraft(day, hour, min)
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
		nextDay := (day % 7) + 1 // BGL days are usually 1-7
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
					e.TrySpawnGroundTraffic(f)
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

func (e *D9TrafficEngine) TrySpawnGroundTraffic(f flightplan.ScheduledFlight) {
	airport := e.atcService.Airports[f.IcaoOrigin]

	// Defaulting to "C" (Airliner)
	reqWidth := 15.0

	// 'occupied' comes from your logic to find the user/other AI
	occupied := e.GetOccupiedSpots()

	spot := e.FindAvailableParking(airport, reqWidth, occupied)
	if spot != nil {
		// Create the "Live" entity
		newAc := &atc.Aircraft{
			Registration: f.AircraftRegistration,
			Flight: atc.Flight{
				Number:      f.Number,
				Origin:      f.IcaoOrigin,
				Destination: f.IcaoDest,
				Phase: flightphase.Phase{
					Current:    flightphase.Parked.Index(),
					Previous:   flightphase.Unknown.Index(),
					Transition: time.Now(),
				},
				Position: atc.Position{
					Lat:     spot.Lat,
					Long:    spot.Lon,
					Heading: spot.Heading,
					//TODO: parse airport elevation from relevant xplane nav data
					//Altitude: airport.Elevation,
				},
				AssignedParking: spot.Name,
				Schedule: f,
			},
		}
		e.atcService.SetFlightPhaseClass(newAc)

        e.Spawned[f.AircraftRegistration] = true
        e.ActiveAircraft = append(e.ActiveAircraft, newAc)
        spot.IsOccupied = true 
        
        logger.Log.Infof("Successfully spawned ghost traffic: %s (%s %d) at Stand %s", 
            f.AircraftRegistration, f.AirlineName, f.Number, spot.Name)
    }
}

func (e *D9TrafficEngine) GetOccupiedSpots() []OccupiedSpot {
	return []OccupiedSpot{}
}

func (e *D9TrafficEngine) FindAvailableParking(airport *atc.Airport, reqRadius float64, occupied []OccupiedSpot) *atc.ParkingSpot {
	reqClass := GetWidthClass(reqRadius)

	for i := range airport.Parking {
		spot := &airport.Parking[i]

		// 1. Is the gate big enough?
		// (Alphabetical check: 'D' is bigger than 'C', so req <= spot)
		if spot.WidthClass < reqClass {
			continue
		}

		// 2. Is it internally occupied by our engine?
		if spot.IsOccupied {
			continue
		}

		// 3. Is it occupied by the 'forbidden' list (User or X-Plane AI)?
		isBlocked := false
		for _, occ := range occupied {
			dist := geometry.DistNM(spot.Lat, spot.Lon, occ.Lat, occ.Lon)
			// If the distance is less than a reasonable threshold (e.g., 20m)
			if dist < 0.010 { // Approx 20 meters in NM
				isBlocked = true
				break
			}
		}

		if !isBlocked {
			return spot
		}
	}
	return nil
}

func (e *D9TrafficEngine) UpdateActiveAircraft(day, h, m int) {
    nowMins := (h * 60) + m

    for _, ac := range e.ActiveAircraft {
        f := ac.Flight.Schedule
        depMins := (f.DepatureHour * 60) + f.DepartureMin
        diff := depMins - nowMins

        switch {
        case diff <= 15 && diff > 5:
            // Trigger Startup Radio Call if not already done
            if ac.Flight.Phase.Current == flightphase.Parked.Index() {
				fmt.Println("*** STARTUP ****")
                //ac.UpdatePhase(flightphase.Startup)
                // Trigger the actual ATC call logic here
            }
        case diff <= 5 && diff > 0:
            // Trigger Pushback
			fmt.Println("*** PUSHBACK ****")
        case diff <= 0:
            // Taxi / Depart
			fmt.Println("*** TAXI ****")
        }
    }
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
