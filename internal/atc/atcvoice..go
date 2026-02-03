package atc

import (
	"encoding/json"
	"io"
	"log"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
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

type Exchange struct {
    ID        string `json:"id"`
    Initiator string `json:"initiator"` // "pilot" or "atc"
    Pilot     string `json:"pilot"`
    ATC       string `json:"atc"`
}

type PhraseDatabase struct {
    PreFlight      []Exchange `json:"pre_flight_parked"`
    Startup        []Exchange `json:"startup"`
    TaxiOut        []Exchange `json:"taxi_out"`
    Depart         []Exchange `json:"depart"`
    Climb          []Exchange `json:"climb"`
    Cruise         []Exchange `json:"cruise"`
    Descent        []Exchange `json:"descent"`
    Approach       []Exchange `json:"approach"`
    Final          []Exchange `json:"final"`
    Braking        []Exchange `json:"braking"`
    TaxiIn         []Exchange `json:"taxi_in"`
    PostFlight     []Exchange `json:"post_flight_parked"`
    GoAround       []Exchange `json:"go_around"`
    Holding        []Exchange `json:"holding"`
}

type PhraseClasses struct {
	phrases       map[string][]Exchange
	phrasesUnicom map[string][]Exchange
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
var regionVoicePools map[string][]string

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
	if _, err := os.Stat(cfg.ATC.Voices.Sox.Application); os.IsNotExist(err) {
		log.Fatalf("FATAL: Sox binary not found at %s", cfg.ATC.Voices.Sox.Application)
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

	var phrases map[string][]Exchange
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

	var unicomPhrases map[string][]Exchange
	err = json.Unmarshal(unicomPhrasesBytes, &unicomPhrases)
	if err != nil {
		log.Fatalf("FATAL: Could not unmarshal unicom phrases json: %v", err)
	}

	radioQueue = make(chan ATCMessage, cfg.ATC.MessageBufferSize)
	prepQueue = make(chan PreparedAudio, 2) // Buffer for pre-warmed audio

	go PrepSpeech(cfg.ATC.Voices.Piper.Application, cfg.ATC.Voices.Piper.VoiceDirectory) // Converts Text -> Piper Process
	go RadioPlayer(cfg.ATC.Voices.Sox.Application)                                       // Converts Piper Process -> Speakers

	return PhraseClasses{
		phrases:       phrases,
		phrasesUnicom: unicomPhrases,
	}
}

func createVoicePools(path string) error {

	// Initialize the map
	countryVoicePools = make(map[string][]string)
	regionVoicePools = make(map[string][]string)

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

	// create region voice pools
	for k, v := range icaoToIsoMap {
		cvp, cvpfound := countryVoicePools[v]
		if !cvpfound {
			continue
		}
		regionCode := k[:1]
		regionVoicePools[regionCode] = append(regionVoicePools[regionCode], cvp...)
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

			var phraseSource map[string][]Exchange
			if ac.Flight.Comms.Controller.RoleID == 0 {
				phraseSource = s.PhraseClasses.phrasesUnicom
			} else {
				phraseSource = s.PhraseClasses.phrases
			}

			phraseDef := getPhraseDef(phraseSource, ac.Flight.Phase.Current)
			if phraseDef == nil || len(phraseDef.exchanges) == 0 {
				log.Printf("error: no phrases found for flight phase %d", ac.Flight.Phase.Current)
				continue
			}

			// select random exchange
			exchange := phraseDef.exchanges[rand.Intn(len(phraseDef.exchanges))]

			if exchange.Initiator == "pilot" {
				PrepAndQueuePhrase(exchange.Pilot, "PILOT", ac)
				// if not unicom then ATC responds
				if ac.Flight.Comms.Controller.RoleID != 0 {
					PrepAndQueuePhrase(exchange.ATC, phraseDef.facility, ac)
					PrepAndQueuePhrase(autoReadback(exchange.ATC), "PILOT", ac)
				}
			}
			
			if exchange.Initiator == "atc" {
				PrepAndQueuePhrase(exchange.ATC, phraseDef.facility, ac)
				if exchange.Pilot == "" {
					PrepAndQueuePhrase(autoReadback(exchange.ATC), "PILOT", ac)
				} else {
					PrepAndQueuePhrase(exchange.Pilot, "PILOT", ac)
				}
			}
		}
	}()
}

// autoReadback will generate the readback phrase from the original
// this entails moving {CALLSIGN} from the beginning to the end and 
// removng any text enclosed in square brackets
func autoReadback(phrase string) string {
	phrase = strings.TrimPrefix(phrase, "{CALLSIGN}")
	phrase = strings.TrimPrefix(phrase, ",")
	phrase = strings.TrimSuffix(phrase, ".")
	phrase = phrase + " {CALLSIGN}"
	phrase = removeBracketedPhrases(phrase)
	return phrase
}

func removeBracketedPhrases(input string) string {
	re := regexp.MustCompile((`\[[^\]]*\]`))
	result := re.ReplaceAllString(input, "")
	return result
}

// PrepPhrase prepares the phrase and queues for speech generation
// role is either "PILOT" or the facility name
func PrepAndQueuePhrase(phrase, role string, ac Aircraft) {

	// construct message and replace all possible variables
	// TODO: add more as defined in phrase files
	phrase = strings.ReplaceAll(phrase, "{CALLSIGN}", ac.Flight.Comms.Callsign)
	phrase = strings.ReplaceAll(phrase, "{FACILITY}", ac.Flight.Comms.Controller.Name)
	phrase = strings.ReplaceAll(phrase, "{RUNWAY}", translateRunway(ac.Flight.AssignedRunway))
	// TODO: if parking contains numbers, does not contain RAMP or STOP, prefix with GATE
	phrase = strings.ReplaceAll(phrase, "{PARKING}", ac.Flight.AssignedParking)
	phrase = strings.ReplaceAll(phrase, "{SQUAWK}", ac.Flight.Squawk)
	// TODO: lookup destination name from airport code
	if ac.Flight.Destination != "" {
		phrase = strings.ReplaceAll(phrase, "{DESTINATION}", ac.Flight.Destination)
	}
	phrase = strings.ReplaceAll(phrase, "[", "")
	phrase = strings.ReplaceAll(phrase, "]", "")

	phrase = translateNumerics(phrase)

	if role == "PILOT" {
		ac.Flight.Comms.LastTransmission = phrase
	} else {
		ac.Flight.Comms.LastInstruction = phrase
	}

	// send message to radio queue
	radioQueue <- ATCMessage{ac.Flight.Comms.Controller.ICAO, ac.Flight.Comms.Callsign,
		role, phrase, ac.Flight.Phase.Current, ac.Flight.Comms.CountryCode,
	}
}

// PrepSpeech picks up text and starts the Piper process immediately
func PrepSpeech(piperPath, voiceDir string) {
	for msg := range radioQueue {

		log.Printf("Processing message: %s", msg.Text)

		voice, onnx, rate, noise := resolveVoice(msg, voiceDir)

		cmd := exec.Command(piperPath, "--model", onnx, "--output-raw", "--length_scale", "0.7")
		stdin, err := cmd.StdinPipe()
		if err != nil {
			log.Printf("Error obtaining piper stdin pipe: %v", err)
			continue
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			log.Printf("Error obtaining piper stdout pipe: %v", err)
			continue
		}

		if err := cmd.Start(); err != nil {
			log.Printf("Error starting piper: %v", err)
			continue
		}

		// Feed text immediately so Piper starts synthesizing in the background
		// Must close stdin to signal EOF to piper
		go func(s io.WriteCloser, t string) {
			defer s.Close()
			_, err := io.WriteString(s, t)
			if err != nil {
				log.Printf("Error writing to piper stdin: %v", err)
				return
			}
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
func RadioPlayer(soxPath string) {

	for audio := range prepQueue {
		args := []string{
			"-t", "raw", "-r", strconv.Itoa(audio.SampleRate), "-e", "signed-integer", "-b", "16", "-c", "1", "-",
		}
		if runtime.GOOS == "windows" {
			args = append(args, "-d")
		}
		args = append(args,
			// SoX effects chain
			"bandpass", "1200", "1500", "overdrive", "20", "tremolo", "5", "40",
			"pad", "0.3", "0.3", "synth", audio.NoiseType, "mix",
		)

		playCmd := exec.Command(soxPath, args...)
		playCmd.Stdin = audio.PiperOut

		log.Printf("[%s] %s (%s) starting playback...", audio.Msg.Role, audio.Msg.Callsign, audio.Voice)

		err := playCmd.Start()
		if err != nil {
			log.Printf("Error starting sox: %v", err)
			continue
		}

		err = audio.PiperCmd.Wait()
		if err != nil {
			log.Printf("Error waiting for piper to finish: %v", err)
		}
		err = playCmd.Wait()
		if err != nil {
			log.Printf("Error waiting for sox to finish: %v", err)
		}

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
			log.Printf("icao country code '%s' not found, '%s' iso country code selected at random for voice", msg.CountryCode, isoCountry)
		}
		var found bool
		pool, found = countryVoicePools[isoCountry]
		if !found {
			// no country voice pool found, pick from region pool
			regionCode := ""
			if msg.CountryCode != "" {
				regionCode = msg.CountryCode[:1]
				pool, found = regionVoicePools[regionCode]
			} else {
				log.Printf("WARN: no country code provided in message, cannot determine region code: %v", msg)
			}
			if !found {
				// no pool found for region, pick random pool
				rKey := util.PickRandomFromMap(countryVoicePools).(string)
				pool = countryVoicePools[rKey]
				log.Printf("no voice pool found for icao region '%s', selected iso country '%s' at random for voice", regionCode, rKey)
			} else {
				log.Printf("no voice pool found for iso country code '%s' not found, selected icoa region pool '%s' for voice", isoCountry, regionCode)
			}
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

type phraseDef struct {
	exchanges      []Exchange
	facility        string
}

func getPhraseDef(phraseSource map[string][]Exchange, flightPhase int) *phraseDef {

	var exchanges []Exchange
	var facility string

	switch flightPhase {
	// --- PRE-FLIGHT & DEPARTURE ---
	case trafficglobal.Parked.Index():
		exchanges = phraseSource["pre_flight_parked"]
		facility = "Clearance" // or Delivery
	case trafficglobal.Startup.Index():
		exchanges = phraseSource["startup"]
		facility = "Ground"
	case trafficglobal.TaxiOut.Index():
		exchanges = phraseSource["taxi_out"]
		facility = "Ground"
	case trafficglobal.Depart.Index():
		exchanges = phraseSource["depart"]
		facility = "Tower"
	case trafficglobal.Climbout.Index():
		exchanges = phraseSource["climb_out"]
		facility = "Departure"
	// --- ENROUTE & ARRIVAL ---
	case trafficglobal.Cruise.Index():
		exchanges = phraseSource["cruise"]
		facility = "Center"
	case trafficglobal.Approach.Index():
		exchanges = phraseSource["approach"]
		facility = "Approach"
	case trafficglobal.Holding.Index():
		exchanges = phraseSource["holding"]
		facility = "Approach"
	case trafficglobal.Final.Index():
		exchanges = phraseSource["final"]
		facility = "Tower"
	case trafficglobal.GoAround.Index():
		exchanges = phraseSource["go_around"]
		facility = "Tower"
	// --- LANDING & TAXI-IN ---
	case trafficglobal.Braking.Index():
		// In Traffic Global, Braking usually covers the rollout and runway exit
		exchanges = phraseSource["braking"]
		facility = "Tower"
	case trafficglobal.TaxiIn.Index():
		exchanges = phraseSource["taxi_in"]
		facility = "Ground"
	case trafficglobal.Shutdown.Index():
		// Usually uses the end of Taxi-In or a "On Blocks" message
		exchanges = phraseSource["post_flight_parked"]
		facility = "Ground"
	default:
		return nil
	}

	return &phraseDef{
		exchanges:       exchanges,
		facility:        facility,
	}
}
