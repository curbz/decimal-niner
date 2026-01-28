package atc

import (
	"encoding/json"
	"io"
	"log"
	"time"

	"fmt"
	"math"
	"os"

	"github.com/curbz/decimal-niner/pkg/geometry"
	"github.com/curbz/decimal-niner/pkg/util"
	"github.com/curbz/decimal-niner/internal/trafficglobal"
)

type Service struct {
	Config        	*config
	Channel       	chan Aircraft
	Database      	[]Controller
	PhraseClasses 	PhraseClasses
	UserState     	UserState
	Airlines 	  	map[string]AirlineInfo
	FlightSchedules map[string][]trafficglobal.ScheduledFlight
}

type ServiceInterface interface {
	Run()
	NotifyAircraftChange(msg Aircraft)
	NotifyUserChange(pos Position, com1Freq, com2Freq map[int]int) 
	GetAirline(code string) *AirlineInfo
	GetUserState() UserState
	AddFlightPlan(ac *Aircraft, simTime time.Time)
}

// --- configuration structures ---
type config struct {
	ATC struct {
		MessageBufferSize int          `yaml:"message_buffer_size"`
		AtcDataFile       string       `yaml:"atc_data_file"`
		AtcRegionsFile    string       `yaml:"atc_regions_file"`
		AirportsDataFile  string       `yaml:"airports_data_file"`
		AirlinesFile 	  string 	   `yaml:"airlines_file"`
		Voices            VoicesConfig `yaml:"voices"`
		ListenAllFreqs	  bool		   `yaml:"listen_all_frequencies"`
	} `yaml:"atc"`
}

func New(cfgPath string, fScheds map[string][]trafficglobal.ScheduledFlight) *Service {

	log.Println("Starting ATC service - loading all configurations")

	cfg, err := util.LoadConfig[config](cfgPath)
	if err != nil {
		log.Fatalf("Error reading configuration file: %v\n", err)
	}

	phraseClasses := loadPhrases(cfg)

	// load atc and airport data
	log.Println("Loading X-Plane ATC and Airport data")
	start := time.Now()
	db := append(parseGeneric(cfg.ATC.AtcDataFile, false), parseApt(cfg.ATC.AirportsDataFile)...)
	db = append(db, parseGeneric(cfg.ATC.AtcRegionsFile, true)...)
	log.Printf("ATC controller database generated: %v (Count: %d)\n\n", time.Since(start), len(db))

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

	radioQueue = make(chan ATCMessage, cfg.ATC.MessageBufferSize)
	prepQueue = make(chan PreparedAudio, 2) // Buffer for pre-warmed audio

	go PrepSpeech(cfg.ATC.Voices.Piper.Application, cfg.ATC.Voices.Piper.VoiceDirectory) // Converts Text -> Piper Process
	go RadioPlayer()                                                                     // Converts Piper Process -> Speakers

	return &Service{
		Config:   cfg,
		Channel:  make(chan Aircraft, cfg.ATC.MessageBufferSize),
		Database: db,
		PhraseClasses: phraseClasses,
		Airlines: airlinesData,
		FlightSchedules: fScheds,
	}
}

func (s *Service) Run() {
	s.startComms()
}

func (s *Service) NotifyAircraftChange(ac Aircraft) {

	userActive := s.UserState.ActiveFacilities

	if len(userActive) == 0 {
		log.Println("User has no active tuned ATC facilities")
		return
	}

	go func() {
		// Identify AI's intended facility
		aiRole := s.getAITargetRole(ac.Flight.Phase.Current)
		aiFac := s.LocateController(
			"AI_Lookup",
			0, aiRole, // Search by role, any freq
			ac.Flight.Position.Lat, ac.Flight.Position.Long, ac.Flight.Position.Altitude, "")

		// Fallback: If no Tower/Ground found, look for Unicom (Role 0)
		if aiFac == nil {
			aiFac = s.LocateController("AI_FALLBACK", 0, 0,
				ac.Flight.Position.Lat, ac.Flight.Position.Long, ac.Flight.Position.Altitude, "")
		}

		if aiFac == nil {
			log.Printf("No suitable ATC facility found for AI aircraft: %v", ac)
			return
		}

		log.Printf("Controller found for aircraft %s: %s %s Role ID: %d",
			ac.Registration, aiFac.Name, aiFac.ICAO, aiFac.RoleID)

		ac.Flight.Comms.Controller = aiFac

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
				log.Printf("User on same frequency as aircraft %s - sending for phrase generation (listen all frequencies is %v)", ac.Registration, s.Config.ATC.ListenAllFreqs)
				s.Channel <- ac
				return
			} else {
				log.Printf("User not on same frequency as aircraft %s - audio will not be generated", ac.Registration)
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

	log.Printf("Searching for %s at %f, %f elev %f. Target Role: %d  Freq: %d", label, uLa, uLo, uAl, tRole, tFreq)

	for i := range s.Database {
		c := &s.Database[i]

		// If we are looking for a specific airport (Origin/Destination), 
        // we skip any controller that isn't tied to that ICAO.
        if targetICAO != "" && c.ICAO != targetICAO {
            continue
        }

		// Short-circuit 1: Role
		if tRole > 0 && c.RoleID != tRole {
			continue
		}

		// Short-circuit 2: Freq
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

		// Expensive Math
		dist := geometry.DistNM(uLa, uLo, c.Lat, c.Lon)

		if c.IsPoint {
			maxRange := 60.0
			if c.RoleID >= 5 {
				maxRange = 200.0
			} // Center range

			if dist < maxRange && dist < closestDist {
				closestDist = dist
				bestMatch = c
			}
		} else {
			// Polygon logic for Regions
			for _, poly := range c.Airspaces {
				if !c.IsRegion && (uAl < poly.Floor || uAl > poly.Ceiling) {
					continue
				}
				if geometry.IsPointInPolygon(uLa, uLo, poly.Points) {
					area := geometry.CalculateRoughArea(poly.Points)
					if area < smallestArea {
						smallestArea = area
						if bestMatch == nil || !bestMatch.IsPoint || closestDist > 2.0 {
							bestMatch = c
						}
					}
				}
			}
		}
	}
	return bestMatch
}

func (s *Service) getAITargetRole(phase int) int {
	p := trafficglobal.FlightPhase(phase)
	switch p {
	case trafficglobal.Parked:
		return 1 // Delivery
	case trafficglobal.Startup, trafficglobal.TaxiOut, trafficglobal.TaxiIn, trafficglobal.Shutdown:
		return 2 // Ground
	case trafficglobal.Depart, trafficglobal.Braking, trafficglobal.Final:
		return 3 // Tower
	case trafficglobal.Climbout, trafficglobal.GoAround:
		return 4 // Departure
	case trafficglobal.Approach, trafficglobal.Holding:
		return 5 // Approach
	case trafficglobal.Cruise:
		return 6 // Center
	default:
		// If we don't know, Ground is the safest place for a radio to be tuned
		return 2
	}
}

func (s *Service) AddFlightPlan(ac *Aircraft, simTime time.Time) {

	simTodayDayOfWeek := util.GetISOWeekday(simTime)
	simYesterdayDayOfWeek := (simTodayDayOfWeek + 6) % 7
	simMinsSinceMidnight := simTime.Hour() * 60 + simTime.Minute()

	candidateScheds := make([]trafficglobal.ScheduledFlight, 0)

	adjDep := 0
	adjArr := 0
	
	for cnt := 0 ; cnt < 2; cnt++ {

		// get all scheds for yesterday and filter. For yesterday's departures, active
		// flights are those where the arrival day of week is today and arrival time is greater 
		// or eqaul to the current time
		key := fmt.Sprintf("%s_%d_%d", ac.Registration, ac.Flight.Number, simYesterdayDayOfWeek)
		scheds, found := s.FlightSchedules[key]
		if found {
			for _, f := range scheds {
				schedArrMinsSinceMidnight := f.ArrivalHour * 60 + f.ArrivalMin + adjArr
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
				schedDepMinsSinceMidnight := f.DepatureHour * 60 + f.DepartureMin + adjDep
				schedArrMinsSinceMidnight := f.ArrivalHour * 60 + f.ArrivalMin + adjArr
				if simMinsSinceMidnight >= schedDepMinsSinceMidnight && simMinsSinceMidnight <= schedArrMinsSinceMidnight {
					candidateScheds = append(candidateScheds, f)
				}
			}
		}

		if len(candidateScheds) > 0 {
			break
		}

		adjDep = -20
		adjArr = 20
	}

	if len(candidateScheds) == 0 {
		log.Printf("no active flight plan found for registration %s flight number %d days %d and %d", 
			ac.Registration, ac.Flight.Number, simTodayDayOfWeek, simYesterdayDayOfWeek)
		return
	}

	// there should only be one flight in the candidates, but capturing instances where
	// there is multiple for debugging
	if len(candidateScheds) > 1 {
		log.Printf("multiple active flight plans found for registration %s flight number %d days %d and %d", 
			ac.Registration, ac.Flight.Number, simTodayDayOfWeek, simYesterdayDayOfWeek)
		for i, c := range candidateScheds {
			log.Printf("duplicate active flight %d/%d: %v", i+1, len(candidateScheds), c)
		}
	}

	// use first candidate
	ac.Flight.Origin = candidateScheds[0].IcaoOrigin
	ac.Flight.Destination = candidateScheds[0].IcaoDest

	log.Printf("reg %s flight num %d origin %s", ac.Registration, ac.Flight.Number, ac.Flight.Origin)
	log.Printf("reg %s flight num %d destination %s", ac.Registration, ac.Flight.Number, ac.Flight.Destination)

}
