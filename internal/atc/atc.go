package atc

import (

	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"bufio"
	"fmt"
	"math"

	"github.com/curbz/decimal-niner/pkg/util"
	"github.com/curbz/decimal-niner/trafficglobal"
)

type Service struct {
	Config        *config
	Channel       chan Aircraft
	Database      []Controller
	PhraseClasses PhraseClasses
	UserState     UserState
}

type ServiceInterface interface {
	Run()
	Notify(msg Aircraft)
	GetUserState() UserState
	UpdateUserState(pos Position, com1Freq, com2Freq map[int]int)
}

// --- configuration structures ---
type config struct {
	ATC struct {
		MessageBufferSize int          `yaml:"message_buffer_size"`
		AtcDataFile       string       `yaml:"atc_data_file"`
		AtcRegionsFile    string       `yaml:"atc_regions_file"`
		AirportsDataFile  string       `yaml:"airports_data_file"`
		Voices            VoicesConfig `yaml:"voices"`
	} `yaml:"atc"`
}

type UserState struct {
	NearestICAO      string
	Position         Position
	ActiveFacilities map[int]*Controller // Key: 1 for COM1, 2 for COM2
	TunedFreqs       map[int]int         // Key: 1 for COM1, 2 for COM2
	TunedFacilities  map[int]int         // Key: 1 for COM1, 2 for COM2
}

type Aircraft struct {
	Flight       Flight
	Type         string
	Class        string
	Code         string
	Airline      string
	Registration string
}

type Flight struct {
	Position    Position
	Number      int
	TaxiRoute   string
	Origin      string
	Destination string
	Phase       Phase
	Comms       Comms
}

type Position struct {
	Lat      float64
	Long     float64
	Altitude float64
	Heading  float64
}

type Phase struct {
	Current    int
	Previous   int // used for detecting changes, previous refers to last update and not necessarily the actual previous phase
	Transition time.Time
}

type Comms struct {
	Callsign         string
	Controller       *Controller
	LastTransmission string
	LastInstruction  string
}

// ATCMessage represents a single ATC communication message
type ATCMessage struct {
	ICAO        string
	Callsign    string
	Role        string
	Text        string
	FlightPhase int
}

// --- Data Structures ---
type Airspace struct {
	Floor, Ceiling float64
	Points         [][2]float64
}

type Controller struct {
	Name, ICAO string
	RoleID     int
	Freqs      []int
	Lat, Lon   float64
	IsPoint    bool
	IsRegion   bool
	Airspaces  []Airspace
}

func New(cfgPath string) *Service {

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
	fmt.Printf("INITIAL ATC DATABASE LOAD: %v (Count: %d)\n\n", time.Since(start), len(db))

	radioQueue = make(chan ATCMessage, cfg.ATC.MessageBufferSize)
	prepQueue = make(chan PreparedAudio, 2) // Buffer for pre-warmed audio

	go PrepSpeech(cfg.ATC.Voices.Piper.Application, cfg.ATC.Voices.Piper.VoiceDirectory) // Converts Text -> Piper Process
	go RadioPlayer()                                                                     // Converts Piper Process -> Speakers

	return &Service{
		Config:   cfg,
		Channel:  make(chan Aircraft, cfg.ATC.MessageBufferSize),
		Database: db,
		PhraseClasses: phraseClasses,
	}
}

// main function to run the ATC service
func (s *Service) Run() {
	s.startComms()
}

func (s *Service) Notify(ac Aircraft) {

	userActive := s.UserState.ActiveFacilities

	if len(userActive) == 0 {
		log.Println("User has no active tuned ATC facilities")
		return
	}

	go func() {
		// Identify AI's intended facility
		aiRole := s.getAITargetRole(ac.Flight.Phase.Current)
		aiFac := s.PerformSearch(
			"AI_Lookup",
			0, aiRole, // Search by role, any freq
			ac.Flight.Position.Lat, ac.Flight.Position.Long, ac.Flight.Position.Altitude)

		// 2. Fallback: If no Tower/Ground found, look for Unicom (Role 0)
		if aiFac == nil {
			aiFac = s.PerformSearch("AI_FALLBACK", 0, 0,
				ac.Flight.Position.Lat, ac.Flight.Position.Long, ac.Flight.Position.Altitude)
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

			if match {
				log.Printf("User on same frequency as aircraft %s - sending for phrase generation", ac.Registration)
				s.Channel <- ac
				return
			} else {
				log.Printf("User not on same frequency as aircraft %s - audio will not be generated", ac.Registration)
			}
		}
	}()
}

func (s *Service) GetUserState() UserState {
	return s.UserState
}

func (s *Service) UpdateUserState(pos Position, tunedFreqs, tunedFacilities map[int]int) {

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

		controller := s.PerformSearch(
			fmt.Sprintf("User_COM%d", idx),
			uFreq,                // Search by freq
			tunedFacilities[idx], // role
			pos.Lat, pos.Long, pos.Altitude,
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

// --- Geometry Helpers ---

func distNM(lat1, lon1, lat2, lon2 float64) float64 {
	const R = 3440.06
	r1, r2 := lat1*math.Pi/180, lat2*math.Pi/180

	dLat := (lat2 - lat1) * math.Pi / 180
	dLon := (lon2 - lon1) * math.Pi / 180

	// --- handle dateline crossing ---
	for dLon > math.Pi {
		dLon -= 2 * math.Pi
	}
	for dLon < -math.Pi {
		dLon += 2 * math.Pi
	}
	// --------------------------------

	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(r1)*math.Cos(r2)*math.Sin(dLon/2)*math.Sin(dLon/2)

	return R * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

func isPointInPolygon(lat, lon float64, polygon [][2]float64) bool {
	if len(polygon) < 3 {
		return false
	}
	inside, j := false, len(polygon)-1
	for i := 0; i < len(polygon); i++ {
		if ((polygon[i][1] > lon) != (polygon[j][1] > lon)) &&
			(lat < (polygon[j][0]-polygon[i][0])*(lon-polygon[i][1])/(polygon[j][1]-polygon[i][1])+polygon[i][0]) {
			inside = !inside
		}
		j = i
	}
	return inside
}

func calculateRoughArea(pts [][2]float64) float64 {
	minLat, maxLat := 90.0, -90.0
	minLon, maxLon := 180.0, -180.0
	for _, p := range pts {
		if p[0] < minLat {
			minLat = p[0]
		}
		if p[0] > maxLat {
			maxLat = p[0]
		}
		if p[1] < minLon {
			minLon = p[1]
		}
		if p[1] > maxLon {
			maxLon = p[1]
		}
	}
	return (maxLat - minLat) * (maxLon - minLon)
}

// --- Parsers ---

func parseApt(path string) []Controller {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	var list []Controller
	var curICAO, curName string
	var curLat, curLon float64

	roleMap := map[string]int{
		"1051": 0, // Unicom / CTAF
		"1052": 1, // Delivery
		"1053": 2, // Ground
		"1054": 3, // Tower
		"1056": 4, // Departure
		"1055": 5, // Approach
	}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		p := strings.Fields(line)
		if len(p) < 2 {
			continue
		}
		code := p[0]

		if code == "1" || code == "16" || code == "17" {
			curLat, curLon = 0, 0
			if len(p) >= 5 {
				curICAO = p[4]
				curName = strings.Join(p[5:], " ")
			}
			continue
		}

		// Use Runway (100) to find the airport center
		if (code == "100" || code == "101" || code == "102") && curLat == 0 {
			if len(p) >= 11 {
				la, _ := strconv.ParseFloat(p[9], 64)
				lo, _ := strconv.ParseFloat(p[10], 64)
				if math.Abs(la) <= 90 {
					curLat, curLon = la, lo
				}
			}
		}

		fRaw, _ := strconv.Atoi(p[1])
		fNorm := fRaw
		for fNorm > 0 && fNorm < 100000 {
			fNorm *= 10
		}

		// ALIASSING LOGIC: If an airport has Unicom (1051) or Tower (1054),
		// it likely handles Ground/Delivery duties too.
		if code == "1051" || code == "1054" {
			roles := []int{3} // Tower
			if code == "1051" || code == "1054" {
				roles = append(roles, 1, 2)
			}
			for _, r := range roles {
				list = append(list, Controller{
					Name: curName, ICAO: curICAO, RoleID: r,
					Freqs: []int{fNorm}, Lat: curLat, Lon: curLon, IsPoint: true,
				})
			}
		} else if rID, ok := roleMap[code]; ok {
			list = append(list, Controller{
				Name: curName, ICAO: curICAO, RoleID: rID,
				Freqs: []int{fNorm}, Lat: curLat, Lon: curLon, IsPoint: true,
			})
		}
	}
	return list
}

func parseGeneric(path string, isRegion bool) []Controller {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	var list []Controller
	var cur *Controller
	var curPoly *Airspace
	roleMap := map[string]int{"del": 1, "gnd": 2, "twr": 3, "tracon": 4, "ctr": 5}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		p := strings.Fields(line)

		switch strings.ToUpper(p[0]) {
		case "CONTROLLER":
			cur = &Controller{IsRegion: isRegion, IsPoint: false}
		case "NAME":
			if cur != nil {
				cur.Name = strings.Join(p[1:], " ")
			}
		case "FACILITY_ID", "ICAO":
			if cur != nil {
				cur.ICAO = p[1]
			}
		case "ROLE":
			if cur != nil {
				cur.RoleID = roleMap[strings.ToLower(p[1])]
			}
		case "FREQ", "CHAN":
			if cur != nil {
				f, _ := strconv.Atoi(p[1])
				for f > 0 && f < 100000 {
					f *= 10
				}
				cur.Freqs = append(cur.Freqs, f)
			}
		case "AIRSPACE_POLYGON_BEGIN":
			f, c := -99999.0, 99999.0
			if len(p) >= 3 {
				f, _ = strconv.ParseFloat(p[1], 64)
				c, _ = strconv.ParseFloat(p[2], 64)
			}
			curPoly = &Airspace{Floor: f, Ceiling: c}
		case "POINT":
			la, _ := strconv.ParseFloat(p[1], 64)
			lo, _ := strconv.ParseFloat(p[2], 64)
			if curPoly != nil {
				curPoly.Points = append(curPoly.Points, [2]float64{la, lo})
			}
			if cur != nil && cur.Lat == 0 {
				cur.Lat, cur.Lon = la, lo
			}
		case "AIRSPACE_POLYGON_END":
			if cur != nil && curPoly != nil {
				cur.Airspaces = append(cur.Airspaces, *curPoly)
			}
			curPoly = nil
		case "CONTROLLER_END":
			if cur != nil {
				list = append(list, *cur)
			}
			cur = nil
		}
	}
	return list
}

func (s *Service) PerformSearch(label string, tFreq, tRole int, uLa, uLo, uAl float64) *Controller {
	var bestMatch *Controller
	closestDist := math.MaxFloat64
	smallestArea := math.MaxFloat64

	log.Printf("Searching for %s at %f, %f elev %f. Target Role: %d  Freq: %d", label, uLa, uLo, uAl, tRole, tFreq)

	for i := range s.Database {
		c := &s.Database[i]

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
		dist := distNM(uLa, uLo, c.Lat, c.Lon)

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
				if isPointInPolygon(uLa, uLo, poly.Points) {
					area := calculateRoughArea(poly.Points)
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
