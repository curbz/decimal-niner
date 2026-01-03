package atc

import (
	"encoding/json"
	"io"
	"log"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"bufio"
	"fmt"
	"math"

	"github.com/curbz/decimal-niner/internal/model"
	"github.com/curbz/decimal-niner/pkg/util"
	"github.com/curbz/decimal-niner/trafficglobal"

)

type Service struct {
	Config			 *config
	Channel            chan model.Aircraft
	Database           []Controller
	Facilities         map[string]float64 // facility name to frequency
	UserTunedFrequency float64
	UserICAO           string
	phrases            map[string]map[string][]string
}

type ServiceInterface interface {
	Run()
	Notify(msg model.Aircraft)
}

// --- configuration structures ---
type config struct {
	ATC struct {
		MessageBufferSize int          `yaml:"message_buffer_size"`
		AtcDataFile       string       `yaml:"atc_data_file"`
		AtcRegionsFile    string       `yaml:"atc_regions_file"`
		AirportsDataFile  string       `yaml:"airports_data_file"`
		Voices           VoicesConfig `yaml:"voices"`
	} `yaml:"atc"`
	
}

type VoicesConfig struct {
	PhrasesFile string      `yaml:"phrases_file"`
	Piper       Piper `yaml:"piper"`
	Sox         Sox         `yaml:"sox"`
}

type Piper struct {
	Application string `yaml:"application"`
	VoiceDirectory  string `yaml:"voice_directory"`
}

type Sox struct {
	Application string `yaml:"application"`
}


// ATCMessage represents a single ATC communication message
type ATCMessage struct {
	AirportCode string
	Callsign    string
	Role        string
	Text        string
	VoiceDirectory string
	FlightPhase int
}

// PreparedAudio holds a ready-to-play piper command and its metadata
type PreparedAudio struct {
	PiperCmd   *exec.Cmd
	PiperOut   io.ReadCloser
	SampleRate int
	NoiseType  string
	Msg        ATCMessage
	Voice      string
}

var radioQueue chan ATCMessage
var prepQueue chan PreparedAudio

var sessionVoices = make(map[string]string)
var sessionMutex sync.Mutex

var rng = rand.New(rand.NewSource(time.Now().UnixNano()))

// PiperConfig represents the structure of the Piper ONNX model JSON config
type PiperConfig struct {
	Audio struct {
		SampleRate int `json:"sample_rate"`
	} `json:"audio"`
}

// -- Voice selections maps ---
var RegionalPools = map[string][]string{
	"UK":      {"en_GB-northern_english_male-medium", "en_GB-alan-low", "en_GB-southern_english_female-low"},
	"US":      {"en_US-john-medium", "en_US-danny-low"},
	"FRANCE":  {"fr_FR-gilles-low"},
	"GERMANY": {"de_DE-thorsten-low"},
	"GREECE":  {"el_GR-rapunzelina-low"},
}

var ICAOToRegion = map[string]string{
	"EG": "UK", "K": "US", "LF": "FRANCE", "ED": "GERMANY", "LG": "GREECE",
}

var AirlineRegions = map[string]string{
	"BAW": "UK", "EZY": "UK", "GNT": "UK",
	"DLH": "GERMANY", "AFR": "FRANCE",
	"DAL": "US", "AAL": "US", "OAL": "GREECE",
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

	cfg, err := util.LoadConfig[config](cfgPath)
	if err != nil {
		log.Fatalf("Error reading configuration file: %v\n", err)
	}

	if _, err := os.Stat(cfg.ATC.Voices.Piper.Application); os.IsNotExist(err) {
		log.Fatalf("FATAL: Piper binary not found at %s", cfg.ATC.Voices.Piper.Application)
	}
	if _, err := os.Stat(cfg.ATC.Voices.Piper.VoiceDirectory); os.IsNotExist(err) {
		log.Fatalf("FATAL: Voice directory not found at %s", cfg.ATC.Voices.Piper.VoiceDirectory)
	}
	if _, err := os.Stat(cfg.ATC.Voices.PhrasesFile); os.IsNotExist(err) {
		log.Fatalf("FATAL: Phrases file not found at %s", cfg.ATC.Voices.PhrasesFile)
	}

	// load phrases from JSON file
	phrasesFile, err := os.Open(cfg.ATC.Voices.PhrasesFile)
	if err != nil {
		log.Fatalf("FATAL: Could not open phrases.json: %v", err)
	}
	defer phrasesFile.Close()

	phrasesBytes, err := io.ReadAll(phrasesFile)
	if err != nil {
		log.Fatalf("FATAL: Could not read phrases.json: %v", err)
	}

	var phrases map[string]map[string][]string
	err = json.Unmarshal(phrasesBytes, &phrases)
	if err != nil {
		log.Fatalf("FATAL: Could not unmarshal phrases.json: %v", err)
	}

	// load atc and airport data
	start := time.Now()
	db := append(parseGeneric(cfg.ATC.AtcDataFile, false), parseApt(cfg.ATC.AirportsDataFile)...)
	db = append(db, parseGeneric(cfg.ATC.AtcRegionsFile, true)...)
	fmt.Printf("INITIAL ATC DATABASE LOAD: %v (Count: %d)\n\n", time.Since(start), len(db))

	radioQueue = make(chan ATCMessage, cfg.ATC.MessageBufferSize)
	prepQueue = make(chan PreparedAudio, 2) // Buffer for pre-warmed audio

	go PreWarmer(cfg.ATC.Voices.Piper.Application)   // Converts Text -> Piper Process
	go RadioPlayer() // Converts Piper Process -> Speakers

	return &Service{
		Config: 		  cfg,
		Channel: make(chan model.Aircraft, cfg.ATC.MessageBufferSize),
		Database:         db,
		// TODO: these frequencies should be set from xpconnect datarefs
		Facilities: map[string]float64{
			"Clearance Delivery": 118.1,
			"Ground":             121.9,
			"Tower":              118.1,
			"Departure":          122.6,
			"Center":             128.2,
			"Approach":           124.5,
			"TRACON":             127.2,
			"Oceanic":            135.0,
		},
		// TODO: remove these and set from xpconnect datarefs
		UserTunedFrequency: 121.9,
		UserICAO:           "EGNT",
		phrases:            phrases,
	}
}

// main function to run the ATC service
func (s *Service) Run() {

	// main loop to read from channel and process instructions
	go func() {
		for ac := range s.Channel {
			// process instructions here based on aircraft phase or other criteria
			// this process may generate a response to the communication

			var phaseGroup map[string][]string
			var facility string

			// determine atc facility based on aircraft position or phase
			switch ac.Flight.Phase.Current {
			case trafficglobal.Startup.Index(): // Taxi
				phaseGroup = s.phrases["startup"]
				facility = "Ground"
			default:
				log.Printf("No ATC instructions for phase %d", ac.Flight.Phase.Current)
				continue
			}

			// set the aircraft comms frequency to ground facility
			ac.Flight.Comms.Frequency = s.Facilities[facility]

			// if user is not tuned to frequency then skip
			if ac.Flight.Comms.Frequency != s.UserTunedFrequency {
				log.Printf("Skipping ATC message for %s: user not tuned to %.1f MHz", ac.Flight.Comms.Callsign, ac.Flight.Comms.Frequency)
				continue
			}

			callAndResponse := []string{"pilot_initial_calls", "atc_responses_instructions"}

			for i, groupName := range callAndResponse {
				// select random phrase
				phrases := phaseGroup[groupName]
				if len(phrases) == 0 {
					log.Printf("No phrases found for phase group %s", phaseGroup)
					continue
				}
				selectedPhrase := phrases[rand.Intn(len(phrases))]

				// construct message and replace all possible variables
				message := strings.ReplaceAll(selectedPhrase, "{CALLSIGN}", ac.Flight.Comms.Callsign)
				message = strings.ReplaceAll(message, "{AIRPORT}", s.UserICAO)
				
				// TODO: add more replacements as needed here

				message = translateNumerics(message)

				role := "PILOT"
				if i > 0 {
					role = facility
				}

				// send message to radio queue
				radioQueue <- ATCMessage{s.UserICAO, ac.Flight.Comms.Callsign, role, message,
					s.Config.ATC.Voices.Piper.VoiceDirectory, ac.Flight.Phase.Current,
				}
			}
		}
	}()
}

func (s *Service) Notify(ac model.Aircraft) {
	// deterimine if user hears message by checking frequency

	// if so, send on channel
	go func() {
		select {
		case s.Channel <- ac:
			// Message sent successfully
			log.Println("ATC notification sent for aircraft:", ac.Flight.Comms.Callsign)
		default:
			log.Println("ATC notification buffer full: dropping message")
		}
	}()
}

// PreWarmer picks up text and starts the Piper process immediately
func PreWarmer(piperPath string) {
	for msg := range radioQueue {

		log.Printf("Processing message: %s", msg.Text)
		
		voice, onnx, rate, noise := resolveVoice(msg)

		cmd := exec.Command(piperPath, "--model", onnx, "--output-raw", "--length_scale", "0.8")
		stdin, _ := cmd.StdinPipe()
		stdout, _ := cmd.StdoutPipe()

		if err := cmd.Start(); err != nil {
			log.Printf("Error starting piper: %v", err)
			continue
		}

		// Feed text immediately so Piper starts synthesizing in the background
		go func(s io.WriteCloser, t string) {
			io.WriteString(s, t)
			s.Close()
		}(stdin, msg.Text)

		// Send the running process to the player queue
		prepQueue <- PreparedAudio{
			PiperCmd:   cmd,
			PiperOut:   stdout,
			SampleRate: rate,
			NoiseType:  noise,
			Msg:        msg,
			Voice:      voice,
		}
	}
}

// RadioPlayer takes prepared Piper processes and pipes them to SoX sequentially
func RadioPlayer() {
	for audio := range prepQueue {
		playCmd := exec.Command("play",
			"-t", "raw", "-r", strconv.Itoa(audio.SampleRate), "-e", "signed-integer", "-b", "16", "-c", "1", "-",
			"bandpass", "1200", "1500", "overdrive", "20", "tremolo", "5", "40",
			"synth", audio.NoiseType, "mix", "1", "pad", "0", "0.1",
		)
		playCmd.Stdin = audio.PiperOut

		_ = playCmd.Start()
		
		log.Printf("[%s] %s (%s) starting playback...", audio.Msg.Role, audio.Msg.Callsign, audio.Voice)
		
		_ = audio.PiperCmd.Wait()
		_ = playCmd.Wait()
		
		// Small gap between transmissions
		min := 400
		max := 1200
		randomMillis := rand.Intn(max-min+1) + min
		time.Sleep(time.Duration(randomMillis) * time.Millisecond)
	}
}

// resolveVoice handles all the mapping and collision logic
func resolveVoice(msg ATCMessage) (string, string, int, string) {
	sessionMutex.Lock()
	defer sessionMutex.Unlock()

	key := msg.Callsign + "_PILOT"
	if msg.Role != "PILOT" {
		key = msg.AirportCode + "_" + msg.Role
	}

	selectedVoice, exists := sessionVoices[key]
	if !exists {
		var pool []string
		if msg.Role != "PILOT" {
			region := "UK"
			for prefix, r := range ICAOToRegion {
				if strings.HasPrefix(msg.AirportCode, prefix) {
					region = r
					break
				}
			}
			pool = RegionalPools[region]
		} else {
			prefix := strings.ToUpper(msg.Callsign[:3])
			region, known := AirlineRegions[prefix]
			if !known {
				allRegions := []string{"UK", "US", "FRANCE", "GERMANY", "GREECE"}
				region = allRegions[rng.Intn(len(allRegions))]
			}
			pool = RegionalPools[region]
		}

		rng.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
		selectedVoice = pool[0]
		for _, v := range pool {
			used := false
			for _, assigned := range sessionVoices {
				if assigned == v { used = true; break }
			}
			if !used { selectedVoice = v; break }
		}
		sessionVoices[key] = selectedVoice
	}

	onnxPath := filepath.Join(msg.VoiceDirectory, selectedVoice+".onnx")

	// --- Dynamic Noise Logic ---
	noise := noiseType(msg.Role, msg.FlightPhase)

	// Simple sample rate fetch (optimized)
	rate := 22050
	if f, err := os.Open(onnxPath + ".json"); err == nil {
		var cfg struct{ Audio struct{ SampleRate int `json:"sample_rate"` } }
		json.NewDecoder(f).Decode(&cfg)
		rate = cfg.Audio.SampleRate
		f.Close()
	}

	return selectedVoice, onnxPath, rate, noise
}

func noiseType(role string, flightPhase int) string {
	if role == "PILOT" {
		if flightPhase == trafficglobal.Cruise.Index() ||
			flightPhase == trafficglobal.Climbout.Index() ||
			flightPhase == trafficglobal.Depart.Index() ||
			flightPhase == trafficglobal.GoAround.Index() ||
			flightPhase == trafficglobal.Approach.Index() ||
			flightPhase == trafficglobal.Final.Index() ||
			flightPhase == trafficglobal.Braking.Index() ||
			flightPhase == trafficglobal.Holding.Index() {
			return "pinknoise"
		}
	}
	return "brownnoise"
}

// translateNumerics converts numeric digits in a string to their word equivalents
func translateNumerics(msg string) string {
	numMap := map[rune]string{
		'0': "zero",
		'1': "one",
		'2': "two",
		'3': "three",
		'4': "four",
		'5': "five",
		'6': "six",
		'7': "seven",
		'8': "eight",
		'9': "niner",
	}
	var result strings.Builder
	for _, ch := range msg {
		if word, exists := numMap[ch]; exists {
			result.WriteString(word)
			result.WriteString(" ")
		} else {
			result.WriteRune(ch)
		}
	}
	return result.String()
}

// --- Geometry Helpers ---

func distNM(lat1, lon1, lat2, lon2 float64) float64 {
	const R = 3440.06
	r1, r2 := lat1*math.Pi/180, lat2*math.Pi/180
	dLat, dLon := (lat2-lat1)*math.Pi/180, (lon2-lon1)*math.Pi/180
	a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(r1)*math.Cos(r2)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return R * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

func isPointInPolygon(lat, lon float64, polygon [][2]float64) bool {
	if len(polygon) < 3 { return false }
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
		if p[0] < minLat { minLat = p[0] }
		if p[0] > maxLat { maxLat = p[0] }
		if p[1] < minLon { minLon = p[1] }
		if p[1] > maxLon { maxLon = p[1] }
	}
	return (maxLat - minLat) * (maxLon - minLon)
}

// --- Parsers ---

func parseApt(path string) []Controller {
	file, err := os.Open(path)
	if err != nil { return nil }
	defer file.Close()
	
	scanner := bufio.NewScanner(file)
	var list []Controller
	var curICAO, curName string
	var curLat, curLon float64

	// X-Plane Radio Codes: 1051=Unicom, 1052=Del, 1053=Gnd, 1054=Twr, 1055/1056=App/Dep
	roleMap := map[string]int{"1052": 1, "1053": 2, "1054": 3, "1055": 4, "1056": 4}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		p := strings.Fields(line)
		if len(p) < 2 { continue }
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
				if math.Abs(la) <= 90 { curLat, curLon = la, lo }
			}
		}

		fRaw, _ := strconv.Atoi(p[1])
		fNorm := fRaw
		for fNorm > 0 && fNorm < 100000 { fNorm *= 10 }

		// ALIASSING LOGIC: If an airport has Unicom (1051) or Tower (1054), 
		// it likely handles Ground/Delivery duties too.
		if code == "1051" || code == "1054" {
			roles := []int{3} // Tower
			if code == "1051" || code == "1054" { roles = append(roles, 1, 2) }
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
	if err != nil { return nil }
	defer file.Close()
	
	scanner := bufio.NewScanner(file)
	var list []Controller
	var cur *Controller
	var curPoly *Airspace
	roleMap := map[string]int{"del": 1, "gnd": 2, "twr": 3, "tracon": 4, "ctr": 5}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line[0] == '#' { continue }
		p := strings.Fields(line)

		switch strings.ToUpper(p[0]) {
		case "CONTROLLER":
			cur = &Controller{IsRegion: isRegion, IsPoint: false}
		case "NAME":
			if cur != nil { cur.Name = strings.Join(p[1:], " ") }
		case "FACILITY_ID", "ICAO":
			if cur != nil { cur.ICAO = p[1] }
		case "ROLE":
			if cur != nil { cur.RoleID = roleMap[strings.ToLower(p[1])] }
		case "FREQ", "CHAN":
			if cur != nil {
				f, _ := strconv.Atoi(p[1])
				for f > 0 && f < 100000 { f *= 10 }
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
			if curPoly != nil { curPoly.Points = append(curPoly.Points, [2]float64{la, lo}) }
			if cur != nil && cur.Lat == 0 { cur.Lat, cur.Lon = la, lo }
		case "AIRSPACE_POLYGON_END":
			if cur != nil && curPoly != nil { cur.Airspaces = append(cur.Airspaces, *curPoly) }
			curPoly = nil
		case "CONTROLLER_END":
			if cur != nil { list = append(list, *cur) }
			cur = nil
		}
	}
	return list
}

// --- Search Engine ---

func PerformSearch(db []Controller, label string, tFreq, tRole int, uLa, uLo, uAl float64) *Controller {
	if tFreq > 0 {
		for tFreq > 0 && tFreq < 100000 { tFreq *= 10 }
	}
	fmt.Printf("--- Searching for: %s ---\n", label)
	
	var bestMatch *Controller
	closestDist := math.MaxFloat64
	smallestArea := math.MaxFloat64

	for i := range db {
		c := &db[i]
		if c.RoleID != tRole { continue }
		
		if tFreq > 0 {
			fMatch := false
			for _, f := range c.Freqs {
				if f/10 == tFreq/10 { fMatch = true; break }
			}
			if !fMatch { continue }
		}

		dist := distNM(uLa, uLo, c.Lat, c.Lon)

		if c.IsPoint {
			if dist < closestDist {
				closestDist = dist
				bestMatch = c
				fmt.Printf("  [Candidate Point] %s (%s) dist: %.2f nm (New Lead)\n", c.Name, c.ICAO, dist)
			}
		} else {
			for _, poly := range c.Airspaces {
				if c.IsRegion || (uAl >= poly.Floor && uAl <= poly.Ceiling) {
					if isPointInPolygon(uLa, uLo, poly.Points) {
						area := calculateRoughArea(poly.Points)
						if area < smallestArea {
							smallestArea = area
							// Point stations within 2nm take priority over any polygon
							if bestMatch == nil || bestMatch.IsPoint == false || closestDist > 2.0 {
								bestMatch = c
								fmt.Printf("  [Candidate Poly] %s (%s) Area: %.2f (New Lead)\n", c.Name, c.ICAO, area)
							}
						}
					}
				}
			}
		}
	}
	return bestMatch
}
