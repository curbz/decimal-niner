package atc

import (
	"encoding/json"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/curbz/decimal-niner/internal/flightphase"
	"github.com/curbz/decimal-niner/internal/flightplan"
	"github.com/curbz/decimal-niner/internal/logger"
	"github.com/curbz/decimal-niner/internal/simdata"
	"github.com/curbz/decimal-niner/pkg/util"
)

type Service struct {
	Config                *config
	Broadcast             chan *Aircraft
	Controllers           []*Controller
	Holds                 map[string]*Hold
	UserState             UserState
	AirlineByICAO         map[string]*AirlineInfo
	AirlineByName         map[string]*AirlineInfo // Keyed by Name "British Airways"
	AirlineCodesByCountry map[string][]string     // Keyed by CountryCode (e.g., "GB" -> ["BAW", "EZY"])
	Airports              map[string]*Airport
	AirportService        AirportProvider
	FlightSchedules       map[string][]flightplan.ScheduledFlight
	Weather               *Weather
	DataProvider          simdata.SimDataProvider
	SimInitTime           time.Time // the date/time within the sim
	SessionInitTime       time.Time // the real-world date/time when the SimInitTime was synced, used to calculate current sim time and elapsed time in sim
	VoiceManager          *VoiceManager
	TrafficEngine         TrafficEngine
}

type ServiceInterface interface {
	Run()
	NotifyFlightPhaseChange(msg *Aircraft)
	NotifyUserStateChange(pos Position, com1Freq, com2Freq map[int]int, isOnGround bool)
	NotifyCruisePositionChange(ac *Aircraft)
	GetAirlineByCode(code string) *AirlineInfo
	GetUserState() UserState
	GetWeatherState() *Weather
	AddFlightPlan(ac *Aircraft, simTime time.Time) bool
	AssignController(ac *Aircraft) *Controller
	SyncSimTime(init time.Time, session time.Time)
	GetCurrentZuluTime() time.Time
	SetDataProvider(simdata.SimDataProvider)
	CheckForCruiseSectorChange(ac *Aircraft)
	Transmit(userState UserState, ac *Aircraft)
	SetRadioMute(mute bool)
	GetCountryFromRegistration(reg string) string
	GetParkingSpotByName(icao, name string) *ParkingSpot
	AssignSID(ac *Aircraft, airport *Airport, rwy *Runway)
	AssignSTAR(ac *Aircraft, airport *Airport, rwy *Runway)
	AssignRunwayAccessPoint(ac *Aircraft, airport *Airport, context int)
	GetAirportRunway(airportICAO, rwyName string) *Runway
}

// AirportProvider defines the behavior for finding the nearest airport
type AirportProvider interface {
	GetClosestAirport(lat, long, maxRangeNm float64) string
}

// TrafficEngine is a minimal interface that represents the methods on a
// traffic engine that the ATC service needs to query. This is declared here to
// avoid an import cycle with the internal/traffic package.
type TrafficEngine interface {
	GetFlightPlanPath() string
	RequiresAircraftData() bool
	Enrich(*Aircraft)
}

// --- configuration structures ---
type config struct {
	ATC struct {
		MessageBufferSize          int          `yaml:"message_buffer_size"`
		AtcDataFile                string       `yaml:"atc_data_file"`
		AtcRegionsFile             string       `yaml:"atc_regions_file"`
		AtcHoldsFile               string       `yaml:"atc_holds_file"`
		AtcNavDataFile             string       `yaml:"atc_nav_data_file"`
		AtcFixesFile               string       `yaml:"atc_fixes_file"`
		AirportCIFPDir             string       `yaml:"airports_cifp_dir"`
		AirportsDataFile           string       `yaml:"airports_data_file"`
		AirlinesFile               string       `yaml:"airlines_file"`
		AirlineCountryCodeFallback string       `yaml:"airline_country_code_fallback"`
		Voices                     VoicesConfig `yaml:"voices"`
		ListenAllFreqs             bool         `yaml:"listen_all_frequencies"`
		StrictFlightPlanMatch      bool         `yaml:"strict_flightplan_matching"`
	} `yaml:"atc"`
}

type Coordinate struct {
	Lat float64
	Lon float64
}

func New(cfgPath string, fScheds map[string][]flightplan.ScheduledFlight, requiredAirports map[string]bool) (*Service, error) {

	logger.Log.Info("Starting ATC service - loading all configurations")

	cfg, err := util.LoadConfig[config](cfgPath)
	if err != nil {
		logger.Log.Errorf("Error reading configuration file: %v", err)
		return nil, err
	}

	if cfg.ATC.Voices.SayAgainFactor <= 0 {
		cfg.ATC.Voices.SayAgainFactor = 30
	}

	vm := NewVoiceManager(cfg)

	start := time.Now()

	// load hold data
	logger.Log.Info("Loading X-Plane Holds data")
	allHolds, airportHolds, allFixes, err := loadHolds(cfg.ATC.AtcNavDataFile, cfg.ATC.AtcHoldsFile, cfg.ATC.AtcFixesFile)
	if err != nil {
		logger.Log.Errorf("Error loading hold data: %v", err)
		return nil, err
	}
	logger.Log.Infof("Holds data loaded: seeded %d holds\n", len(allHolds))

	// load airports and controller data
	arptControllers, airports, err := parseApt(cfg.ATC.AirportsDataFile, requiredAirports)
	if err != nil {
		logger.Log.Errorf("Error parsing airports data file: %v", err)
		return nil, err
	}
	atcControllers, err := parseATCdatFiles(cfg.ATC.AtcDataFile, false, requiredAirports)
	if err != nil {
		logger.Log.Errorf("Error parsing ATC data file: %v", err)
		return nil, err
	}
	db := append(atcControllers, arptControllers...)
	regionControllers, err := parseATCdatFiles(cfg.ATC.AtcRegionsFile, true, requiredAirports)
	if err != nil {
		logger.Log.Errorf("Error parsing ATC regions file: %v", err)
		return nil, err
	}
	db = append(db, regionControllers...)

	// enrich airport data
	logger.Log.Info("Loading X-Plane airport files")

	err = loadAirports(cfg.ATC.AirportCIFPDir, airports, requiredAirports, airportHolds, allHolds, allFixes)
	if err != nil {
		logger.Log.Errorf("Error loading airport data from CIFP files: %v", err)
		return nil, err
	}
	logger.Log.Info("Airport data loaded: seeded ", len(airports), " airports")

	logger.Log.Infof("ATC controller database generated: seeded %d controllers\n", len(db))

	logger.Log.Infof("ATC data loaded in %v\n", time.Since(start))

	// load airlines from JSON file
	airlinesFile, err := os.Open(cfg.ATC.AirlinesFile)
	if err != nil {
		logger.Log.Errorf("Could not open airlines.json (%s): %v", cfg.ATC.AirlinesFile, err)
		return nil, err
	}
	defer airlinesFile.Close()

	airlinesBytes, err := io.ReadAll(airlinesFile)
	if err != nil {
		logger.Log.Errorf("Could not read airlines.json (%s): %v", cfg.ATC.AirlinesFile, err)
		return nil, err
	}

	var airlinesData map[string]*AirlineInfo
	// Unmarshal the JSON into the map
	err = json.Unmarshal(airlinesBytes, &airlinesData)
	if err != nil {
		logger.Log.Errorf("Error unmarshaling JSON for airlines.json (%s): %v", cfg.ATC.AirlinesFile, err)
		return nil, err
	}

	airlineByName := make(map[string]*AirlineInfo)
	airlineCodesByCountry := make(map[string][]string)
	for icao, info := range airlinesData {
		info.ICAO = icao
		airlineByName[info.AirlineName] = info
		airlineCodesByCountry[info.CountryCode] = append(airlineCodesByCountry[info.CountryCode], icao)
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

	util.GoSafe(func() { PrepSpeech(cfg.ATC.Voices.Piper.Application, vm) })
	util.GoSafe(func() { RadioPlayer(cfg.ATC.Voices.Sox.Application) })

	return &Service{
		Config:                cfg,
		Broadcast:             make(chan *Aircraft, cfg.ATC.MessageBufferSize),
		Controllers:           db,
		Holds:                 allHolds,
		AirlineByICAO:         airlinesData,
		AirlineByName:         airlineByName,
		AirlineCodesByCountry: airlineCodesByCountry,
		Airports:              airports,
		FlightSchedules:       fScheds,
		Weather:               &Weather{Wind: &Wind{}, Baro: &Baro{Sealevel: 101325, Flight: 101325}},
		VoiceManager:          vm,
	}, nil
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

func (s *Service) SyncSimTime(init time.Time, session time.Time) {
	s.SimInitTime = init
	s.SessionInitTime = session
}

// RegisterTrafficEngine registers the active traffic engine with the Service.
// This is intended to be called during initialization by the traffic engine's
// SetATCService implementation.
func (s *Service) RegisterTrafficEngine(e TrafficEngine) {
	s.TrafficEngine = e
}

// GetTrafficEngine returns the registered traffic engine or nil if none registered.
func (s *Service) GetTrafficEngine() TrafficEngine {
	return s.TrafficEngine
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
				util.LogWithLabel(ac.Registration, "user on same frequency - sending for phrase generation (listen all frequencies is %v)", s.Config.ATC.ListenAllFreqs)
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

// IsAirborne returns true if the phase is considered an airbourne phase. depatIsAirborne can be used to control whether
// the Depart phase is considered airborne or not given that technically, during the takeoff roll portion, the aircraft
// is not physically airborne
func IsAirborne(phase int, departIsAirborne bool) bool {
	beginPhase := flightphase.Takeoff
	if !departIsAirborne {
		beginPhase = flightphase.Climbout
	}
	return phase >= beginPhase.Index() && phase < flightphase.Braking.Index()
}

func isNorthAmerica(icao string) bool {
	if len(icao) == 0 {
		return false // Default to International/ICAO standard
	}
	prefix := icao[0]
	// K = USA, C = Canada
	if prefix == 'K' || prefix == 'C' {
		return true
	}
	// Also treat Alaska/Hawaii/Mexico as North American conventions
	if strings.HasPrefix(icao, "PA") || strings.HasPrefix(icao, "PH") || prefix == 'M' {
		return true
	}
	return false
}
