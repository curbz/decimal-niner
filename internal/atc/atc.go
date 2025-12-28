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
)

type Service struct {
	// go channel to trigger instructions
	Channel   chan struct{}
	Positions []Position
}

type ServiceInterface interface {
	Run()
	Notify(msg *ATCMessage)
}

type ATCMessage struct {
}

type Position struct {
	Name      string
	Frequency float64
}

func New() *Service {

	if _, err := os.Stat(PiperPath); os.IsNotExist(err) {
		log.Fatalf("FATAL: Piper binary not found at %s", PiperPath)
	}
	if _, err := os.Stat(VoiceDir); os.IsNotExist(err) {
		log.Fatalf("FATAL: Voice directory not found at %s", VoiceDir)
	}

	return &Service{
		Channel: make(chan struct{}, msgBuffSize),
		Positions: []Position{
			{Name: "Clearance Delivery", Frequency: 118.1},
			{Name: "Ground", Frequency: 121.9},
			{Name: "Tower", Frequency: 118.1},
			{Name: "Departure", Frequency: 122.6},
			{Name: "Center", Frequency: 128.2},
			{Name: "Approach", Frequency: 124.5},
			{Name: "TRACON", Frequency: 127.2},
			{Name: "Oceanic", Frequency: 135.0},
		},
	}
}

// main function to run the ATC service
func (s *Service) Run() {

	// main loop to read from channel and process instructions
	go func() {
		for {
			<-s.Channel
			// process instructions here
			// e.g., generate and send ATC messages based on aircraft positions and phases
			Say("EGNT", "GNT049", "PILOT", "Newcastle Ground, Giant zero-four-niner, request taxi.")
		}
	}()
	// Demo Sequence
	//apt := "EGNT"
	//Say(apt, "GNT049", "PILOT", "Newcastle Ground, Giant zero-four-niner, request taxi.")
	//Say(apt, "GNT049", "GROUND", "Giant zero-four-niner, Newcastle Ground, taxi to holding point runway two-seven.")

}

func (s *Service) Notify(msg *ATCMessage) {
	// deterimine if user hears message by checking frequency

	// if so, send on channel
	go func() {
		select {
			case s.Channel <- struct{}{}:
				// Message sent successfully
			default:
				log.Println("ATC message buffer full: dropping message")
		}
	}()
}

const (
	PiperPath = "/home/dmorris/piper/piper"
	VoiceDir  = "/home/dmorris/piper-voices"
	msgBuffSize = 5
)

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

func Say(airportCode string, callsign string, role string, message string) {
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

	onnxPath := filepath.Join(VoiceDir, selectedVoice+".onnx")
	sampleRate := getSampleRate(onnxPath + ".json")

	// --- Dynamic Noise Logic ---
	noiseType := "brownnoise" // Default for Controllers
	if role == "PILOT" {
		noiseType = "pinknoise" // Brighter, harsher for Aircraft
	}

	piperCmd := exec.Command(PiperPath, "--model", onnxPath, "--output-raw", "--length_scale", "0.8")
	piperStdin, _ := piperCmd.StdinPipe()
	piperStdout, _ := piperCmd.StdoutPipe()

	playCmd := exec.Command("play",
		"-t", "raw", "-r", strconv.Itoa(sampleRate), "-e", "signed-integer", "-b", "16", "-c", "1", "-",
		"bandpass", "1200", "1500",
		"overdrive", "20",
		"tremolo", "5", "40",
		"synth", noiseType, "mix", "1", // Use the dynamic noise type here
		"pad", "0", "0.5",
	)
	playCmd.Stdin = piperStdout

	_ = playCmd.Start()
	_ = piperCmd.Start()

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
