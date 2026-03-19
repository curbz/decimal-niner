package atc

import (
	"encoding/json"
	"io"
	"os"
	"runtime"
	"time"

	"github.com/curbz/decimal-niner/internal/logger"
	"github.com/curbz/decimal-niner/internal/simdata"
	"github.com/curbz/decimal-niner/internal/trafficglobal"
	"github.com/curbz/decimal-niner/pkg/util"
)

type Service struct {
	Config          *config
	Broadcast       chan *Aircraft
	Controllers     []*Controller
	Holds           map[string]*Hold
	UserState       UserState
	Airlines        map[string]AirlineInfo
	Airports        map[string]*Airport
	AirportService  AirportProvider
	FlightSchedules map[string][]trafficglobal.ScheduledFlight
	Weather         *Weather
	DataProvider    simdata.SimDataProvider
	SimInitTime     time.Time
	SessionInitTime time.Time
	VoiceManager    *VoiceManager
}

type ServiceInterface interface {
	Run()
	NotifyFlightPhaseChange(msg *Aircraft)
	NotifyUserStateChange(pos Position, com1Freq, com2Freq map[int]int)
	NotifyCruisePositionChange(ac *Aircraft)
	GetAirline(code string) *AirlineInfo
	GetUserState() UserState
	GetWeatherState() *Weather
	AddFlightPlan(ac *Aircraft, simTime time.Time) bool
	AssignController(ac *Aircraft) *Controller
	SetSimTime(init time.Time, session time.Time)
	GetCurrentZuluTime() time.Time
	SetDataProvider(simdata.SimDataProvider)
	CheckForCruiseSectorChange(ac *Aircraft)
	Transmit(userState UserState, ac *Aircraft)
}

// AirportProvider defines the behavior for finding the nearest airport
type AirportProvider interface {
	GetClosestAirport(lat, long float64) string
}

// --- configuration structures ---
type config struct {
	ATC struct {
		MessageBufferSize     int          `yaml:"message_buffer_size"`
		AtcDataFile           string       `yaml:"atc_data_file"`
		AtcRegionsFile        string       `yaml:"atc_regions_file"`
		AtcHoldsFile          string       `yaml:"atc_holds_file"`
		AtcNavDataFile        string       `yaml:"atc_nav_data_file"`
		AtcFixesFile          string       `yaml:"atc_fixes_file"`
		AirportCIFPDir        string       `yaml:"airports_cifp_dir"`
		AirportsDataFile      string       `yaml:"airports_data_file"`
		AirlinesFile          string       `yaml:"airlines_file"`
		Voices                VoicesConfig `yaml:"voices"`
		ListenAllFreqs        bool         `yaml:"listen_all_frequencies"`
		StrictFlightPlanMatch bool         `yaml:"strict_flightplan_matching"`
	} `yaml:"atc"`
}

func New(cfgPath string, fScheds map[string][]trafficglobal.ScheduledFlight, requiredAirports map[string]bool) *Service {

	logger.Log.Info("Starting ATC service - loading all configurations")

	cfg, err := util.LoadConfig[config](cfgPath)
	if err != nil {
		logger.Log.Errorf("Error reading configuration file: %v", err)
		return nil
	}

	start := time.Now()

	// load hold data
	logger.Log.Info("Loading X-Plane Holds data")
	globalHolds, airportHolds, err := loadHolds(cfg.ATC.AtcNavDataFile, cfg.ATC.AtcHoldsFile, cfg.ATC.AtcFixesFile)
	if err != nil {
		logger.Log.Errorf("Error loading hold data: %v", err)
		return nil
	}
	logger.Log.Infof("Holds data loaded: seeded %d holds\n", len(globalHolds))

	// load controller data and create airports
	arptControllers, airports, err := parseApt(cfg.ATC.AirportsDataFile, requiredAirports)
	if err != nil {
		logger.Log.Errorf("Error parsing airports data file: %v", err)
		return nil
	}
	atcControllers, err := parseATCdatFiles(cfg.ATC.AtcDataFile, false, requiredAirports)
	if err != nil {
		logger.Log.Errorf("Error parsing ATC data file: %v", err)
		return nil
	}
	db := append(atcControllers, arptControllers...)
	regionControllers, err := parseATCdatFiles(cfg.ATC.AtcRegionsFile, true, requiredAirports)
	if err != nil {
		logger.Log.Errorf("Error parsing ATC regions file: %v", err)
		return nil
	}
	db = append(db, regionControllers...)

	// enrich airport data
	logger.Log.Info("Loading X-Plane airport files")

	err = loadAirports(cfg.ATC.AirportCIFPDir, airports, requiredAirports, airportHolds, globalHolds)
	if err != nil {
		logger.Log.Errorf("Error loading airport data from CIFP files: %v", err)
		return nil
	}
	logger.Log.Info("Airport data loaded: seeded", len(airports), "airports")

	logger.Log.Infof("ATC controller database generated: seeded %d controllers\n", len(db))

	logger.Log.Infof("ATC data loaded in %v\n", time.Since(start))

	// load airlines from JSON file
	airlinesFile, err := os.Open(cfg.ATC.AirlinesFile)
	if err != nil {
		logger.Log.Errorf("Could not open airlines.json (%s): %v", cfg.ATC.AirlinesFile, err)
		return nil
	}
	defer airlinesFile.Close()

	airlinesBytes, err := io.ReadAll(airlinesFile)
	if err != nil {
		logger.Log.Errorf("Could not read airlines.json (%s): %v", cfg.ATC.AirlinesFile, err)
		return nil
	}

	var airlinesData map[string]AirlineInfo
	// Unmarshal the JSON into the map
	err = json.Unmarshal(airlinesBytes, &airlinesData)
	if err != nil {
		logger.Log.Errorf("Error unmarshaling JSON for airlines.json (%s): %v", cfg.ATC.AirlinesFile, err)
		return nil
	}
	logger.Log.Infof("Airlines loaded successfully (%d)", len(airlinesData))

	if runtime.GOOS == "windows" {
		if os.Getenv("AUDIODRIVER") == "" {
			logger.Log.Info("AUDIODRIVER env var is not set, setting for sox usage...")
			os.Setenv("AUDIODRIVER", "waveaudio")
		}
		logger.Log.Info("AUDIODRIVER env var is ", os.Getenv("AUDIODRIVER"))
	}

	radioQueue = make(chan *ATCMessage, cfg.ATC.MessageBufferSize)
	radioPlayer = make(chan *PreparedAudio, 1) // Buffer for pre-warmed audio

	vm := NewVoiceManager(cfg)

	util.GoSafe(func() { PrepSpeech(cfg.ATC.Voices.Piper.Application, vm) })
	util.GoSafe(func() { RadioPlayer(cfg.ATC.Voices.Sox.Application) })

	return &Service{
		Config:          cfg,
		Broadcast:       make(chan *Aircraft, cfg.ATC.MessageBufferSize),
		Controllers:     db,
		Holds:           globalHolds,
		Airlines:        airlinesData,
		Airports:        airports,
		FlightSchedules: fScheds,
		Weather:         &Weather{Wind: Wind{}, Baro: Baro{}},
		VoiceManager:    vm,
	}
}

func (s *Service) Run() {
	s.startComms()
	util.GoSafe(func() {
		s.VoiceManager.startCleaner(30*time.Second, func() (float64, float64) {
			us := s.GetUserState()
			return us.Position.Lat, us.Position.Long
		})
	})
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

// Transmit checks tuned frequencies to determine if pilot will hear transmissions. If so, then the aircraft data
// will be sent to the broadcast channel to be processed where the appropriate comms will be determined and broadcast.
// Always ensure the Aircraft pointer is referecing a deep copy of the original aircraft to avoid state conflicts
func (s *Service) Transmit(userState UserState, ac *Aircraft) {

	aiFac := ac.Flight.Comms.Controller

	// Check match against COM1 and COM2
	for _, userFac := range userState.ActiveFacilities {
		if userFac == nil {
			continue
		}

		// match when user and aircraft ICAO are the same and the roles are the same (e.g. both are Tower)
		match := (userFac.ICAO == aiFac.ICAO && userFac.RoleID == aiFac.RoleID)

		// fallback for Regions (Center/Approach) where ICAO might differ
		if !match && userFac.RoleID >= 4 && aiFac.RoleID >= 4 {
			match = (userFac.Name == aiFac.Name)
		}

		if match || s.Config.ATC.ListenAllFreqs {
			// NON-BLOCKING SEND
			select {
			case s.Broadcast <- ac:
				util.LogWithLabel(ac.Registration, "User on same frequency - sending for phrase generation (listen all frequencies is %v)", s.Config.ATC.ListenAllFreqs)
			default:
				// drop the message as channel buffer is full
				util.LogWarnWithLabel(ac.Registration, "voice queue full, dropping transmission")
			}
			return
		} else {
			util.LogWithLabel(ac.Registration, "User not on same frequency - audio will not be generated")
		}
	}
}

func GetCountryFromRegistration(reg string) string {
	// Standard registration format is Prefix-Suffix or Prefix1234
	// We check the first 1 or 2 characters
	if len(reg) < 1 {
		return ""
	}

	// Check 2-char prefixes first (e.g., XB, EI)
	if len(reg) >= 2 {
		if code, ok := registrationMap[reg[:2]]; ok {
			return code
		}
	}

	// Check 1-char prefixes (e.g., G, N)
	if code, ok := registrationMap[reg[:1]]; ok {
		return code
	}

	return ""
}
