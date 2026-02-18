package atc

import (
	"encoding/json"
	"io"
	"log"
	"runtime"
	"time"

	"fmt"
	"math"
	"os"

	"github.com/curbz/decimal-niner/internal/simdata"
	"github.com/curbz/decimal-niner/internal/trafficglobal"
	"github.com/curbz/decimal-niner/pkg/geometry"
	"github.com/curbz/decimal-niner/pkg/util"
	"github.com/mohae/deepcopy"
)

type Service struct {
	Config          *config
	Channel         chan *Aircraft
	Database        []Controller
	PhraseClasses   PhraseClasses
	UserState       UserState
	Airlines        map[string]AirlineInfo
	AirportLocations map[string]AirportCoords
	FlightSchedules map[string][]trafficglobal.ScheduledFlight
	Weather         *Weather
	DataProvider    simdata.SimDataProvider
	SimInitTime     time.Time
	SessionInitTime time.Time
}

type ServiceInterface interface {
	Run()
	NotifyAircraftChange(msg *Aircraft)
	NotifyUserChange(pos Position, com1Freq, com2Freq map[int]int)
	GetAirline(code string) *AirlineInfo
	GetUserState() UserState
	GetWeatherState() *Weather
	AddFlightPlan(ac *Aircraft, simTime time.Time)
	SetSimTime(init time.Time, session time.Time)
	GetCurrentZuluTime() time.Time
	SetDataProvider(simdata.SimDataProvider)
}

// --- configuration structures ---
type config struct {
	ATC struct {
		MessageBufferSize     int          `yaml:"message_buffer_size"`
		AtcDataFile           string       `yaml:"atc_data_file"`
		AtcRegionsFile        string       `yaml:"atc_regions_file"`
		AirportsDataFile      string       `yaml:"airports_data_file"`
		AirlinesFile          string       `yaml:"airlines_file"`
		Voices                VoicesConfig `yaml:"voices"`
		ListenAllFreqs        bool         `yaml:"listen_all_frequencies"`
		StrictFlightPlanMatch bool         `yaml:"strict_flightplan_matching"`
	} `yaml:"atc"`
}

func New(cfgPath string, fScheds map[string][]trafficglobal.ScheduledFlight, requiredAirports map[string]bool) *Service {

	log.Println("Starting ATC service - loading all configurations")

	cfg, err := util.LoadConfig[config](cfgPath)
	if err != nil {
		log.Fatalf("Error reading configuration file: %v\n", err)
	}

	phraseClasses := loadPhrases(cfg)

	// load atc and airport data
	log.Println("Loading X-Plane ATC and Airport data")
	start := time.Now()
	arptControllers, airportLocations, err := parseApt(cfg.ATC.AirportsDataFile, requiredAirports)
	if err != nil {
		log.Fatalf("Error parsing airports data file: %v", err)
	}
	atcControllers, err := parseGeneric(cfg.ATC.AtcDataFile, false, requiredAirports)
	if err != nil {
		log.Fatalf("Erro parsing ATC data file: %v", err)
	}
	db := append(atcControllers, arptControllers...)
	regionControllers, err := parseGeneric(cfg.ATC.AtcRegionsFile, true, requiredAirports)
	if err != nil {
		log.Fatalf("Error parsing ATC regions file: %v", err)
	}
	db = append(db, regionControllers...)

	log.Printf("ATC controller database generated: %v (Count: %d)\n", time.Since(start), len(db))

	// load airlines from JSON file
	airlinesFile, err := os.Open(cfg.ATC.AirlinesFile)
	if err != nil {
		log.Fatalf("FATAL: Could not open airlines.json (%s): %v", cfg.ATC.AirlinesFile, err)
	}
	defer airlinesFile.Close()

	airlinesBytes, err := io.ReadAll(airlinesFile)
	if err != nil {
		log.Fatalf("FATAL: Could not read airlines.json (%s): %v", cfg.ATC.AirlinesFile, err)
	}

	var airlinesData map[string]AirlineInfo
	// Unmarshal the JSON into the map
	err = json.Unmarshal(airlinesBytes, &airlinesData)
	if err != nil {
		log.Fatalf("Error unmarshaling JSON for airlines.json (%s): %v", cfg.ATC.AirlinesFile, err)
	}
	log.Printf("Airlines loaded successfully (%d)", len(airlinesData))

	if runtime.GOOS == "windows" {
		if os.Getenv("AUDIODRIVER") == "" {
			log.Println("AUDIODRIVER env var is not set, setting for sox usage...")
			os.Setenv("AUDIODRIVER", "waveaudio")
		}
		log.Println("AUDIODRIVER env var is ", os.Getenv("AUDIODRIVER"))
	}

	radioQueue = make(chan ATCMessage, cfg.ATC.MessageBufferSize)
	prepQueue = make(chan PreparedAudio, 2) // Buffer for pre-warmed audio

	go PrepSpeech(cfg.ATC.Voices.Piper.Application, cfg.ATC.Voices.Piper.VoiceDirectory) // Converts Text -> Piper Process
	go RadioPlayer(cfg.ATC.Voices.Sox.Application)                                       // Converts Piper Process -> Speakers

	return &Service{
		Config:          cfg,
		Channel:         make(chan *Aircraft, cfg.ATC.MessageBufferSize),
		Database:        db,
		PhraseClasses:   phraseClasses,
		Airlines:        airlinesData,
		AirportLocations: airportLocations,
		FlightSchedules: fScheds,
		Weather:         &Weather{Wind: Wind{}, Baro: Baro{}},
	}
}

func (s *Service) Run() {
	s.startComms()
}

func (s *Service) SetDataProvider(dp simdata.SimDataProvider) {
	s.DataProvider = dp
}

func (s *Service) GetCurrentZuluTime() time.Time {
	return s.SimInitTime.Add(time.Since(s.SessionInitTime))
}

func (s *Service) SetSimTime(init time.Time, session time.Time) {
	s.SimInitTime = init
	s.SessionInitTime = session
}

func (s *Service) NotifyAircraftChange(ac *Aircraft) {

	userActive := s.UserState.ActiveFacilities

	if len(userActive) == 0 {
		log.Println("User has no active tuned ATC facilities")
		return
	}

	// set flight phase classification
	s.setFlightPhaseClass(ac)
	log.Printf("%s flight %d phase classified as %d", 
				ac.Registration, ac.Flight.Number, ac.Flight.Phase.Class)

	// for a new aircraft in a post-flight context, there is nothing to do
	if ac.Flight.Phase.Class == PostflightParked { 
		return
	}

	if ac.Flight.Origin == "" {
		// no origin indicates this aircraft has no flight plan 
		s.AddFlightPlan(ac, s.GetCurrentZuluTime())
	}

	// make a snaphot copy of aircraft data and pass this snapshot into the phrase generation process.
	// it is safer to do it here rather than in the go routine as there would be a small chance that 
	// the aircraft could get updated concurrently during the deep copy process if this statement was 
	// placed within the go routine.
	acSnap := deepcopy.Copy(ac).(*Aircraft)

	go func() {
		// +-----------------------------------------------------------------+
		// | Only use acSnap to reference the aircraft within the go routine |
		// +-----------------------------------------------------------------+

		// Identify AI's intended facility
		searchICAO := airportICAObyPhaseClass(acSnap, acSnap.Flight.Phase.Class)
		phaseFacility := atcFacilityByPhaseMap[trafficglobal.FlightPhase(acSnap.Flight.Phase.Current)]
		aiRole := phaseFacility.roleId
		aiFac := s.LocateController(
			"AI_Lookup",
			0, aiRole, // Search by role, any freq
			acSnap.Flight.Position.Lat, acSnap.Flight.Position.Long, acSnap.Flight.Position.Altitude, searchICAO)

		// Fallback: If no Tower/Ground found, look for Unicom (Role 0)
		if aiFac == nil {
			aiFac = s.LocateController("AI_FALLBACK", 0, 0,
				acSnap.Flight.Position.Lat, acSnap.Flight.Position.Long, acSnap.Flight.Position.Altitude, "")
		}

		if aiFac == nil {
			log.Printf("No suitable ATC facility found for AI aircraft: %v", acSnap)
			return
		}

		log.Printf("Controller found for aircraft %s: %s %s Role ID: %d",
			acSnap.Registration, aiFac.Name, aiFac.ICAO, aiFac.RoleID)

		acSnap.Flight.Comms.Controller = aiFac

		// Check match against COM1 and COM2
		for _, userFac := range userActive {
			if userFac == nil {
				continue
			}

			// Match logic
			match := (userFac.ICAO == aiFac.ICAO && userFac.RoleID == aiFac.RoleID)

			// Fallback for Regions (Center/Approach) where ICAO might differ
			if !match && userFac.RoleID >= 4 && aiFac.RoleID >= 4 {
				match = (userFac.Name == aiFac.Name)
			}

			if match || s.Config.ATC.ListenAllFreqs {
				log.Printf("User on same frequency as aircraft %s - sending for phrase generation (listen all frequencies is %v)",
					 acSnap.Registration, s.Config.ATC.ListenAllFreqs)
				s.Channel <- acSnap
				return
			} else {
				log.Printf("User not on same frequency as aircraft %s - audio will not be generated", 
					acSnap.Registration)
			}
		}
	}()
}

func (s *Service) GetAirline(code string) *AirlineInfo {
	airlineInfo, exists := s.Airlines[code]
	if !exists {
		return nil
	}
	return &airlineInfo
}

func (s *Service) GetUserState() UserState {
	return s.UserState
}

func (s *Service) GetWeatherState() *Weather {
	return s.Weather
}

func (s *Service) NotifyUserChange(pos Position, tunedFreqs, tunedFacilities map[int]int) {

	s.UserState.Position = pos
	if s.UserState.ActiveFacilities == nil {
		s.UserState.ActiveFacilities = make(map[int]*Controller)
	}

	s.UserState.TunedFreqs = tunedFreqs
	s.UserState.TunedFacilities = tunedFacilities

	for idx, freq := range tunedFreqs {
		// Normalize 12170 to 121700
		uFreq := int(freq)
		if uFreq < 100000 {
			uFreq *= 10
		}

		controller := s.LocateController(
			fmt.Sprintf("User_COM%d", idx),
			uFreq,                // Search by freq
			tunedFacilities[idx], // role
			pos.Lat, pos.Long, pos.Altitude,
			"",
		)

		if controller != nil {
			s.UserState.ActiveFacilities[idx] = controller
			s.UserState.NearestICAO = controller.ICAO
			log.Printf("Controller found for user on COM%d %d: %s %s Role ID: %d", idx, uFreq,
				controller.Name, controller.ICAO, controller.RoleID)
		} else {
			log.Printf("No nearby controller found for user on COM%d %d", idx, uFreq)
		}
	}
}

func (s *Service) LocateController(label string, tFreq, tRole int, uLa, uLo, uAl float64, targetICAO string) *Controller {
    var bestMatch *Controller
    closestDist := math.MaxFloat64
    smallestArea := math.MaxFloat64

	log.Printf("Searching for %s at lat %f,lng  %f elev %f. Target Role: %d  Tuned Freq: %d  Target ICAO: %s",
		label, uLa, uLo, uAl, tRole, tFreq, targetICAO)
        
    for i := range s.Database {
        c := &s.Database[i]

        // 1. ICAO Filter: If we know the target airport (e.g. for Takeoff/Landing phase),
        // only look at controllers for that airport.
        if targetICAO != "" && c.ICAO != targetICAO {
            continue
        }

        // 2. Role Filter: Skip if it's not the role we are looking for.
        if tRole > 0 && c.RoleID != tRole {
            continue
        }

        // 3. Frequency Filter: Handle the /10 normalization for X-Plane freq format.
        if tFreq > 0 {
            fMatch := false
            for _, f := range c.Freqs {
                if f/10 == tFreq/10 {
                    fMatch = true
                    break
                }
            }
            if !fMatch {
                continue
            }
        }

        // 4. Polygon Matching (Airspace Boundaries)
        if len(c.Airspaces) > 0 {
            for _, poly := range c.Airspaces {
                // Vertical Check: Skip if aircraft is outside the floor/ceiling
                // Note: isRegion usually means we skip the floor/ceiling check for generic FIRs
                if !c.IsRegion && (uAl < poly.Floor || uAl > poly.Ceiling) {
                    continue
                }

                if geometry.IsPointInPolygon(uLa, uLo, poly.Points) {
                    area := geometry.CalculateRoughArea(poly.Points)
                    // Tie-breaker: Smaller areas (sectors) beat larger areas (FIRs)
                    if area < smallestArea {
                        smallestArea = area
                        bestMatch = c
                    }
                }
            }
        }

        // 5. Point Matching (Distance Fallback)
        // If we haven't found a polygon match yet, or if the point is extremely close (Airport Ops)
        dist := geometry.DistNM(uLa, uLo, c.Lat, c.Lon)
        maxRange := 60.0
        if c.RoleID >= 5 {
            maxRange = 250.0 // Center/Enroute range
        }

        if dist < maxRange && dist < closestDist {
            // We only let a point-match override a polygon-match if it's 
            // VERY close (within 2nm), suggesting it's the specific tower 
            // the AI is currently departing from.
            if smallestArea == math.MaxFloat64 || dist < 2.0 {
                closestDist = dist
                // Only override if we don't have a specific polygon match 
                // or if this point is physically at the airport.
                if bestMatch == nil || dist < 2.0 {
                    bestMatch = c
                }
            }
        }
    }

    return bestMatch
}

func (s *Service) GetClosestAirport(aiLat, aiLon float64) string {
    var closestICAO string
    minDist := 4.0 // 4 Nautical Miles threshold

    for icao, coords := range s.AirportLocations {
        // Using your existing DistNM function here
        dist := geometry.DistNM(aiLat, aiLon, coords.Lat, coords.Lon)
        
        if dist < minDist {
            minDist = dist
            closestICAO = icao
        }
    }

    return closestICAO 
}

func (s *Service) AddFlightPlan(ac *Aircraft, simTime time.Time) {

	simTodayDayOfWeek := util.GetISOWeekday(simTime)
	simYesterdayDayOfWeek := (simTodayDayOfWeek + 6) % 7
	simMinsSinceMidnight := simTime.Hour()*60 + simTime.Minute()

	candidateScheds := make([]trafficglobal.ScheduledFlight, 0)

	adjDep := 0
	//adjArr := 0

	// find active flights using schedule times
	// when no flight found, expand search by 20 minutes up to 4 hours
	for adjArr := 0; adjArr <= 240; adjArr = adjArr + 20 {

		adjDep = -adjArr

		// get all scheds for yesterday and filter. For yesterday's departures, active
		// flights are those where the arrival day of week is today and arrival time is greater
		// or eqaul to the current time
		key := fmt.Sprintf("%s_%d_%d", ac.Registration, ac.Flight.Number, simYesterdayDayOfWeek)
		scheds, found := s.FlightSchedules[key]
		if found {
			for _, f := range scheds {
				schedArrMinsSinceMidnight := f.ArrivalHour*60 + f.ArrivalMin + adjArr
				if f.ArrivalDayOfWeek == simTodayDayOfWeek && schedArrMinsSinceMidnight >= simMinsSinceMidnight {
					candidateScheds = append(candidateScheds, f)
				}
			}
		}

		// get all scheds for today and filter. For today's departures, active
		// flights are those where the current time is between the departure time
		// and arrival time
		key = fmt.Sprintf("%s_%d_%d", ac.Registration, ac.Flight.Number, simTodayDayOfWeek)
		scheds, found = s.FlightSchedules[key]
		if found {
			for _, f := range scheds {
				schedDepMinsSinceMidnight := f.DepatureHour*60 + f.DepartureMin + adjDep
				schedArrMinsSinceMidnight := f.ArrivalHour*60 + f.ArrivalMin + adjArr
				if simMinsSinceMidnight >= schedDepMinsSinceMidnight && simMinsSinceMidnight <= schedArrMinsSinceMidnight {
					candidateScheds = append(candidateScheds, f)
				}
			}
		}

		if len(candidateScheds) > 0 {
			// no need to expand search further, we have candidate flights so jump out here
			break
		}

	}

	if len(candidateScheds) == 0 {
		log.Printf("no active flight plan found for registration %s flight no. %d days %d and %d",
			ac.Registration, ac.Flight.Number, simTodayDayOfWeek, simYesterdayDayOfWeek)
		if s.Config.ATC.StrictFlightPlanMatch {
			return
		}
		// fallback to find by tail number and flight only, on any day and time
		log.Printf("find inactive flight plan for registration %s flight no. %d",
			ac.Registration, ac.Flight.Number)
		for i := simTodayDayOfWeek; i <= (simTodayDayOfWeek + 6); i++ {
			day := i % 7
			key := fmt.Sprintf("%s_%d_%d", ac.Registration, ac.Flight.Number, day)
			scheds, found := s.FlightSchedules[key]
			if found {
				for _, f := range scheds {
					candidateScheds = append(candidateScheds, f)
				}
			}
		}

		if len(candidateScheds) == 0 {
			log.Printf("no inactive flight plan found for registration %s flight no. %d",
				ac.Registration, ac.Flight.Number)
			return
		}
	}

	// there should only be one flight in the candidates, but capturing instances where
	// there is multiple for diagnostics
	if len(candidateScheds) > 1 {
		log.Printf("multiple (%d) flight plans found for registration %s flight number %d days %d and %d",
			len(candidateScheds), ac.Registration, ac.Flight.Number, simTodayDayOfWeek, simYesterdayDayOfWeek)
		for i, c := range candidateScheds {
			log.Printf("duplicate flight %d of %d: %v - will try again to determine orgin/dest on flight phase changes", i+1, len(candidateScheds), c)
		}
		return
	}

	// use remaining candidate i.e. [0]
	ac.Flight.Origin = candidateScheds[0].IcaoOrigin
	ac.Flight.Destination = candidateScheds[0].IcaoDest
	ac.Flight.AltClearance = candidateScheds[0].CruiseAlt * 100

	log.Printf("reg %s flight no. %d origin %s", ac.Registration, ac.Flight.Number, ac.Flight.Origin)
	log.Printf("reg %s flight no. %d destination %s (cruise alt: %d)", ac.Registration, ac.Flight.Number, ac.Flight.Destination, ac.Flight.AltClearance)

}

func (s *Service) setFlightPhaseClass(ac *Aircraft) {

	ph := &ac.Flight.Phase

	switch ph.Current {
	case trafficglobal.Parked.Index():
		if ph.Previous == trafficglobal.Unknown.Index() {
			// new aircraft flight - determine if preflight or postflight
			if ac.Flight.Origin == "" || ac.Flight.Destination == "" {
				log.Printf("WARN: no origin/destination for parked aircraft %s flight %d - unable to determine flight phase classification", 
					ac.Registration, ac.Flight.Number)
				ph.Class = Unknown
			}
			currAirport := s.GetClosestAirport(ac.Flight.Position.Lat, ac.Flight.Position.Long)
			if ac.Flight.Destination == currAirport {
				log.Printf("%s flight %d is parked at destination airport %s", 
					ac.Registration, ac.Flight.Number, ac.Flight.Destination)
				ph.Class = PostflightParked
				return
			} else {
				log.Printf("%s flight %d is parked at origin airport %s", 
					ac.Registration, ac.Flight.Number, ac.Flight.Origin)
				ph.Class = PreflightParked
				return
			}
		} else {
			ph.Class = PostflightParked
			return
		}
	case trafficglobal.Startup.Index(),
			trafficglobal.TaxiOut.Index(),
			trafficglobal.Depart.Index(),
			trafficglobal.Climbout.Index():
		ph.Class = Departing
		return
	case trafficglobal.Approach.Index(),
			trafficglobal.Holding.Index(),
			trafficglobal.Final.Index(),
			trafficglobal.GoAround.Index(),
			trafficglobal.Braking.Index(),
			trafficglobal.TaxiIn.Index(),
			trafficglobal.Shutdown.Index():
		ph.Class = Arriving
		return
	case trafficglobal.Cruise.Index():
		ph.Class = Cruising
	default:
		ph.Class = Unknown
	}
}

func airportICAObyPhaseClass(ac *Aircraft, phaseClass PhaseClass) string {
	switch phaseClass {
		case PreflightParked, Departing:
			return ac.Flight.Origin
		case Cruising:
			return "" // This forces the coordinate/polygon distance search
		case Arriving, PostflightParked:
			return ac.Flight.Destination
		default:
			return ""
	}
}