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

	"github.com/curbz/decimal-niner/internal/model"
	"github.com/curbz/decimal-niner/pkg/util"
	"github.com/curbz/decimal-niner/trafficglobal"

)

type Service struct {
	Config			 *config
	Channel            chan model.Aircraft
	Facilities         map[string]float64 // facility name to frequency
	UserTunedFrequency float64
	UserICAO           string
	phrases            map[string]map[string][]string
}

type ServiceInterface interface {
	Run()
	Notify(msg model.Aircraft)
}

type config struct {
	ATC struct {
		MessageBufferSize int          `yaml:"message_buffer_size"`
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

	return &Service{
		Config: 		  cfg,
		Channel: make(chan model.Aircraft, cfg.ATC.MessageBufferSize),
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

				role := "PILOT"
				if i > 0 {
					role = facility
				}

				// send message
				Say(s.UserICAO, ac.Flight.Comms.Callsign, role, 
					ac.Flight.Phase.Current, message, s.Config.ATC.Voices)
			}
		}
	}()
	// Demo Sequence
	//apt := "EGNT"
	//Say(apt, "GNT049", "PILOT", "Newcastle Ground, Giant zero-four-niner, request taxi.")
	//Say(apt, "GNT049", "GROUND", "Giant zero-four-niner, Newcastle Ground, taxi to holding point runway two-seven.")

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

var sessionVoices = make(map[string]string)
var sessionMutex sync.Mutex

var rng = rand.New(rand.NewSource(time.Now().UnixNano()))

type PiperConfig struct {
	Audio struct {
		SampleRate int `json:"sample_rate"`
	} `json:"audio"`
}

func Say(airportCode, callsign, role string, flightPhase int, message string, voicesCfg VoicesConfig) {
	var wg sync.WaitGroup
	wg.Add(1)

	var sessionKey string
	if role != "PILOT" {
		sessionKey = airportCode + "_" + role
	} else {
		sessionKey = callsign + "_PILOT"
	}

	sessionMutex.Lock()
	selectedVoice, exists := sessionVoices[sessionKey]

	if !exists {
		var pool []string
		if role != "PILOT" {
			region := "UK"
			for prefix, r := range ICAOToRegion {
				if strings.HasPrefix(airportCode, prefix) {
					region = r
					break
				}
			}
			pool = RegionalPools[region]
		} else {
			prefix := ""
			if len(callsign) >= 3 {
				prefix = strings.ToUpper(callsign[:3])
			}
			region, known := AirlineRegions[prefix]
			if !known {
				allRegions := []string{"UK", "US", "FRANCE", "GERMANY", "GREECE"}
				region = allRegions[rand.Intn(len(allRegions))]
			}
			pool = RegionalPools[region]
		}

		// Shuffle and check for collisions
		rng.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
		selectedVoice = pool[0]
		for _, v := range pool {
			isUsed := false
			for _, assignedVoice := range sessionVoices {
				if assignedVoice == v {
					isUsed = true
					break
				}
			}
			if !isUsed {
				selectedVoice = v
				break
			}
		}
		sessionVoices[sessionKey] = selectedVoice
	}
	sessionMutex.Unlock()

	onnxPath := filepath.Join(voicesCfg.Piper.VoiceDirectory, selectedVoice+".onnx")
	sampleRate := getSampleRate(onnxPath + ".json")

	// --- Dynamic Noise Logic ---
	noiseType := noiseType(role, flightPhase)

	piperCmd := exec.Command(voicesCfg.Piper.Application, "--model", onnxPath, "--output-raw", "--length_scale", "0.8")
	piperStdin, _ := piperCmd.StdinPipe()
	piperStdout, _ := piperCmd.StdoutPipe()

	playCmd := exec.Command(voicesCfg.Sox.Application,
		"-t", "raw", "-r", strconv.Itoa(sampleRate), "-e", "signed-integer", "-b", "16", "-c", "1", "-",
		"bandpass", "1200", "1500",
		"overdrive", "20",
		"tremolo", "5", "40",
		"synth", noiseType, "mix", "0.2", 
		"pad", "0", "0.5",
	)
	playCmd.Stdin = piperStdout

	_ = playCmd.Start()
	_ = piperCmd.Start()

	// if the voice is non-English, translate numerics into words
	if !strings.HasPrefix(selectedVoice, "en_") {
		message = translateNumerics(message)
	}

	go func() {
		defer wg.Done()
		io.WriteString(piperStdin, message)
		piperStdin.Close()
		_ = piperCmd.Wait()
		_ = playCmd.Wait()
	}()

	log.Printf("[%s] %s @ %s (%s) [Noise: %s]: %s", role, callsign, airportCode, selectedVoice, noiseType, message)
	wg.Wait()
}

func getSampleRate(path string) int {
	file, err := os.Open(path)
	if err != nil {
		return 22050
	}
	defer file.Close()
	var cfg PiperConfig
	_ = json.NewDecoder(file).Decode(&cfg)
	return cfg.Audio.SampleRate
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