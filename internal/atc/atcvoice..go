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

	"github.com/curbz/decimal-niner/internal/trafficglobal"
	"github.com/curbz/decimal-niner/pkg/util"
)

type VoicesConfig struct {
	PhrasesFile       string `yaml:"phrases_file"`
	UnicomPhrasesFile string `yaml:"unicom_phrases_file"`
	Piper             Piper  `yaml:"piper"`
	Sox               Sox    `yaml:"sox"`
}

type PhraseClasses struct {
	phrases       map[string]map[string][]string
	phrasesUnicom map[string]map[string][]string
}

type Piper struct {
	Application    string `yaml:"application"`
	VoiceDirectory string `yaml:"voice_directory"`
}

type Sox struct {
	Application string `yaml:"application"`
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

var countryVoicePools map[string][]string

var rng = rand.New(rand.NewSource(time.Now().UnixNano()))

// PiperConfig represents the structure of the Piper ONNX model JSON config
type PiperConfig struct {
	Audio struct {
		SampleRate int `json:"sample_rate"`
	} `json:"audio"`
}

func loadPhrases(cfg *config) PhraseClasses {

	if _, err := os.Stat(cfg.ATC.Voices.Piper.Application); os.IsNotExist(err) {
		log.Fatalf("FATAL: Piper binary not found at %s", cfg.ATC.Voices.Piper.Application)
	}
	if _, err := os.Stat(cfg.ATC.Voices.Piper.VoiceDirectory); os.IsNotExist(err) {
		log.Fatalf("FATAL: Voice directory not found at %s", cfg.ATC.Voices.Piper.VoiceDirectory)
	}
	if _, err := os.Stat(cfg.ATC.Voices.PhrasesFile); os.IsNotExist(err) {
		log.Fatalf("FATAL: Phrases file not found at %s", cfg.ATC.Voices.PhrasesFile)
	}

	// load country voice pools 
	err := createVoicePools(cfg.ATC.Voices.Piper.VoiceDirectory)
	if err != nil {
		log.Fatalf("error creating voice pools: %v", err)
	}

	// load phrases from JSON file
	phrasesFile, err := os.Open(cfg.ATC.Voices.PhrasesFile)
	if err != nil {
		log.Fatalf("FATAL: Could not open phrases json file: %v", err)
	}
	defer phrasesFile.Close()

	phrasesBytes, err := io.ReadAll(phrasesFile)
	if err != nil {
		log.Fatalf("FATAL: Could not read phrases json file: %v", err)
	}

	var phrases map[string]map[string][]string
	err = json.Unmarshal(phrasesBytes, &phrases)
	if err != nil {
		log.Fatalf("FATAL: Could not unmarshal phrases json: %v", err)
	}

	// load unicom phrases from JSON file
	unicomPhrasesFile, err := os.Open(cfg.ATC.Voices.UnicomPhrasesFile)
	if err != nil {
		log.Fatalf("FATAL: Could not open unicom phrases json file: %v", err)
	}
	defer unicomPhrasesFile.Close()

	unicomPhrasesBytes, err := io.ReadAll(unicomPhrasesFile)
	if err != nil {
		log.Fatalf("FATAL: Could not read unicom phrases json file: %v", err)
	}

	var unicomPhrases map[string]map[string][]string
	err = json.Unmarshal(unicomPhrasesBytes, &unicomPhrases)
	if err != nil {
		log.Fatalf("FATAL: Could not unmarshal unicom phrases json: %v", err)
	}

	radioQueue = make(chan ATCMessage, cfg.ATC.MessageBufferSize)
	prepQueue = make(chan PreparedAudio, 2) // Buffer for pre-warmed audio

	go PrepSpeech(cfg.ATC.Voices.Piper.Application, cfg.ATC.Voices.Piper.VoiceDirectory) // Converts Text -> Piper Process
	go RadioPlayer()                                                                     // Converts Piper Process -> Speakers

	return PhraseClasses{
			phrases:       phrases,
			phrasesUnicom: unicomPhrases,
		}
}

func createVoicePools(path string) error {
	// Initialize the map
	countryVoicePools = make(map[string][]string)

	files, err := os.ReadDir(path)
	if err != nil {
		return err
	}

	for _, file := range files {
		if file.IsDir() {
			continue
		}

		fileName := file.Name()

		// Only process .onnx files
		if strings.HasSuffix(fileName, ".onnx") {
			// Extract the prefix (first 2 letters) for the key
			if len(fileName) >= 5 {
				code := strings.ToUpper(fileName[3:5])

				// Remove the extension for the value
				// filepath.Ext(fileName) returns ".onnx"
				cleanName := strings.TrimSuffix(fileName, filepath.Ext(fileName))

				// Populate map
				countryVoicePools[code] = append(countryVoicePools[code], cleanName)
			}
		}
	}

	if len(countryVoicePools) == 0 {
		log.Fatalf("no voice files found in folder %s", path)
	}

	return nil
}

// main function to recieve aircraft updates for phrase generation
func (s *Service) startComms() {

	// main loop to read from channel and process instructions
	go func() {
		for ac := range s.Channel {
			// process instructions here based on aircraft phase or other criteria
			// this process may generate a response to the communication

			var phaseGroup map[string][]string
			var facility string

			var phraseSource map[string]map[string][]string
			if ac.Flight.Comms.Controller.RoleID == 0 {
				phraseSource = s.PhraseClasses.phrasesUnicom
			} else {
				phraseSource = s.PhraseClasses.phrases
			}
			switch ac.Flight.Phase.Current {
			// --- PRE-FLIGHT & DEPARTURE ---
			case trafficglobal.Parked.Index():
				phaseGroup = phraseSource["pre_flight_parked"]
				facility = "Clearance" // or Delivery

			case trafficglobal.Startup.Index():
				phaseGroup = phraseSource["startup"]
				facility = "Ground"

			case trafficglobal.TaxiOut.Index():
				phaseGroup = phraseSource["taxi_out"]
				facility = "Ground"

			case trafficglobal.Depart.Index():
				phaseGroup = phraseSource["depart"]
				facility = "Tower"

			// --- IN-FLIGHT ---
			case trafficglobal.Climbout.Index():
				phaseGroup = phraseSource["climb_out"]
				facility = "Departure"

			case trafficglobal.Cruise.Index():
				phaseGroup = phraseSource["cruise"]
				facility = "Center"

			case trafficglobal.Approach.Index():
				phaseGroup = phraseSource["approach"]
				facility = "Approach"

			case trafficglobal.Final.Index():
				phaseGroup = phraseSource["final"]
				facility = "Tower"

			case trafficglobal.GoAround.Index():
				phaseGroup = phraseSource["go_around"]
				facility = "Tower"

			// --- ARRIVAL & TAXI-IN ---
			case trafficglobal.Braking.Index():
				// In Traffic Global, Braking usually covers the rollout and runway exit
				phaseGroup = phraseSource["braking"]
				facility = "Tower"

			case trafficglobal.TaxiIn.Index():
				phaseGroup = phraseSource["taxi_in"]
				facility = "Ground"

			case trafficglobal.Shutdown.Index():
				// Usually uses the end of Taxi-In or a "On Blocks" message
				phaseGroup = phraseSource["post_flight_parked"]
				facility = "Ground"

			default:
				log.Printf("No ATC instructions for aircraft %s flight phase %d role id %d",
					ac.Registration, ac.Flight.Phase.Current, ac.Flight.Comms.Controller.RoleID)
				continue
			}

			callAndResponse := []string{"pilot_initial_calls", "atc_responses_instructions"}

			for i, groupName := range callAndResponse {
				// select random phrase
				phrases := phaseGroup[groupName]
				if len(phrases) == 0 {
					log.Printf("No phrases found for phase group %s role id %d", phaseGroup, ac.Flight.Comms.Controller.RoleID)
					continue
				}
				selectedPhrase := phrases[rand.Intn(len(phrases))]

				// construct message and replace all possible variables
				message := strings.ReplaceAll(selectedPhrase, "{CALLSIGN}", ac.Flight.Comms.Callsign)
				message = strings.ReplaceAll(message, "{FACILITY}", ac.Flight.Comms.Controller.Name)
				message = strings.ReplaceAll(message, "{RUNWAY}", translateRunway(ac.Flight.AssignedRunway))
				message = strings.ReplaceAll(message, "{PARKING}", ac.Flight.AssignedParking)

				message = translateNumerics(message)

				var role string
				if i == 0 {
					role = "PILOT"
					ac.Flight.Comms.LastTransmission = message
				} else {
					role = facility
					ac.Flight.Comms.LastInstruction = message
				}

				// send message to radio queue
				radioQueue <- ATCMessage{ac.Flight.Comms.Controller.ICAO, ac.Flight.Comms.Callsign,
					role, message, ac.Flight.Phase.Current, ac.Flight.Comms.CountryCode,
				}

				if ac.Flight.Comms.Controller.RoleID == 0 {
					break
				}
			}
		}
	}()
}

// PrepSpeech picks up text and starts the Piper process immediately
func PrepSpeech(piperPath, voiceDir string) {
	for msg := range radioQueue {

		log.Printf("Processing message: %s", msg.Text)

		voice, onnx, rate, noise := resolveVoice(msg, voiceDir)

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
func resolveVoice(msg ATCMessage, voiceDir string) (string, string, int, string) {
	sessionMutex.Lock()
	defer sessionMutex.Unlock()

	key := msg.Callsign + "_PILOT"
	if msg.Role != "PILOT" {
		key = msg.ICAO + "_" + msg.Role
	}

	selectedVoice, exists := sessionVoices[key]
	if !exists {
		// assign voice logic
		var pool []string
		isoCountry, err := convertIcaoToIso(msg.CountryCode)
		if err != nil {
			//no country found - pick a random country
			rKey := util.PickRandomFromMap(icaoToIsoMap).(string)
			isoCountry = icaoToIsoMap[rKey]
			log.Printf("country code %s not found, %s selected at random", msg.CountryCode, isoCountry)
		}
		var found bool
		pool, found = countryVoicePools[isoCountry]
		if !found {
			// no pool found for country, pick random pool
			rKey := util.PickRandomFromMap(countryVoicePools).(string)
			pool = countryVoicePools[rKey]
			log.Printf("no voice pool found for country %s, selected %s at random", isoCountry, rKey)
		}

		rng.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
		selectedVoice = pool[0]
		for _, v := range pool {
			used := false
			for _, assigned := range sessionVoices {
				if assigned == v {
					used = true
					break
				}
			}
			if !used {
				selectedVoice = v
				break
			}
		}
		sessionVoices[key] = selectedVoice
	}

	onnxPath := filepath.Join(voiceDir, selectedVoice+".onnx")

	// --- Dynamic Noise Logic ---
	noise := noiseType(msg.Role, msg.FlightPhase)

	// Simple sample rate fetch (optimized)
	rate := 22050
	if f, err := os.Open(onnxPath + ".json"); err == nil {
		var cfg struct {
			Audio struct {
				SampleRate int `json:"sample_rate"`
			}
		}
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

func translateRunway(runway string) string {
	runway = strings.Replace(runway, "L", "left", 1)
	runway = strings.Replace(runway, "R", "right", 1)
	return runway
}

