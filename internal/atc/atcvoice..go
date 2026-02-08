package atc

import (
	"encoding/json"
	"fmt"
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
	"unicode"

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

var handoffMap = map[trafficglobal.FlightPhase]int{
    trafficglobal.Parked: 	2, // Delivery -> Ground
    trafficglobal.TaxiOut:  3, // Ground -> Tower
    trafficglobal.Depart:   4, // Tower -> Departure
    trafficglobal.Climbout: 6, // Departure -> Center
    trafficglobal.Cruise:   5, // Center -> Approach (or another Center)
    trafficglobal.Approach: 3, // Approach -> Tower
    trafficglobal.Braking:  2, // Tower -> Ground
}

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
				s.prepAndQueuePhrase(exchange.Pilot, "PILOT", ac, s.Weather.Baro)
				// if not unicom then ATC responds
				if ac.Flight.Comms.Controller.RoleID != 0 {
					s.prepAndQueuePhrase(exchange.ATC, phraseDef.facility, ac, s.Weather.Baro)
					s.prepAndQueuePhrase(autoReadback(exchange.ATC), "PILOT", ac, s.Weather.Baro)
				}
			}
			
			if exchange.Initiator == "atc" {
				s.prepAndQueuePhrase(exchange.ATC, phraseDef.facility, ac, s.Weather.Baro)
				if exchange.Pilot == "" {
					s.prepAndQueuePhrase(autoReadback(exchange.ATC), "PILOT", ac, s.Weather.Baro)
				} else {
					s.prepAndQueuePhrase(exchange.Pilot, "PILOT", ac, s.Weather.Baro)
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
func (s *Service) prepAndQueuePhrase(phrase, role string, ac Aircraft, baro Baro) {

	// construct message and replace all possible variables
	// TODO: add more as defined in phrase files

	phrase = strings.ReplaceAll(phrase, "{CALLSIGN}", ac.Flight.Comms.Callsign)
	phrase = strings.ReplaceAll(phrase, "{FACILITY}", ac.Flight.Comms.Controller.Name)
	phrase = strings.ReplaceAll(phrase, "{SQUAWK}", ac.Flight.Squawk)

	if strings.Contains(phrase, "{RUNWAY}") {
		phrase = strings.ReplaceAll(phrase, "{RUNWAY}", translateRunway(ac.Flight.AssignedRunway))
	}
	if strings.Contains(phrase, "{PARKING}") {
		phrase = strings.ReplaceAll(phrase, "{PARKING}", formatParking(ac.Flight.AssignedParking, ac.Flight.Comms.Controller.ICAO))
	}
	if ac.Flight.Destination != "" && strings.Contains(phrase, "{DESTINATION}") {
		phrase = strings.ReplaceAll(phrase, "{DESTINATION}", formatAirportName(ac.Flight.Destination, s.AirportNames))
	}
	if strings.Contains(phrase, "{ALTITUDE}") {
		phrase = strings.ReplaceAll(phrase, "{ALTITUDE}", formatAltitude(ac.Flight.Position.Altitude, baro.TransitionAlt, ac.Flight.Phase.Current))
	}
	if strings.Contains(phrase, "{BARO}") {
		phrase = strings.ReplaceAll(phrase, "{BARO}", formatBaro(ac.Flight.Comms.Controller.ICAO, baro.Sealevel))
	}
	if strings.Contains(phrase, "{HANDOFF}") {
		phrase = strings.ReplaceAll(phrase, "{HANDOFF}", s.generateHandoffPhrase(ac))
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
	var result strings.Builder
	for _, ch := range msg {
		if word, exists := numericMap[ch]; exists {
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

func formatAltitude(rawAlt float64, transitionLevel int, phase int) string {
	var roundedAlt int
	alt := int(rawAlt)

	// 1. Contextual Rounding Logic
	switch phase {
	case trafficglobal.Final.Index(), trafficglobal.Approach.Index():
		// Nearest 100ft for precision during landing (e.g., 2,412 -> 2,400)
		roundedAlt = ((alt + 50) / 100) * 100
	default:
		// Standard IFR rounding to nearest 1,000ft (e.g., 33,240 -> 33,000)
		roundedAlt = ((alt + 500) / 1000) * 1000
	}

	// 2. Flight Level Logic (At or above Transition Altitude)
	if roundedAlt >= transitionLevel || roundedAlt >= 18000 {
		fl := roundedAlt / 100
		
		// Ensure cruise flight levels are multiples of 10 (e.g., 330)
		if phase == trafficglobal.Cruise.Index() {
			fl = (fl / 10) * 10
		}
		
		// Returns "flight level 330"
		return fmt.Sprintf("flight level %d", fl)
	}

	// 3. Feet Logic (Below Transition Altitude)
	// If it's a clean thousand (e.g., 5000)
	if roundedAlt % 1000 == 0 {
		return fmt.Sprintf("%d thousand", roundedAlt/1000)
	}
	
	// Handle split altitudes like 2400 (common in approach/missed approach)
	thousands := roundedAlt / 1000
	hundreds := (roundedAlt % 1000) / 100
	
	// Returns "2 thousand 4 hundred"
	return fmt.Sprintf("%d thousand %d hundred", thousands, hundreds)
}


// formatParking applies logic to convert parking designations into more natural speech phrases
func formatParking(parking string, icao string) string {
    parking = strings.ToUpper(strings.TrimSpace(parking))
    if parking == "" {
        return "parking"
    }

    // 1. Detect Area-based parking (Ramp/Apron)
    if strings.Contains(parking, "RAMP") || strings.Contains(parking, "APRON") {
        // If X-Plane gives "NORTH RAMP 1", we want to ensure the words stay
        // but the digits are ready for your final translator.
        return phoneticiseSingleAlphas(parking)
    }

    // 2. Default to Gate/Stand logic
    prefix := "stand"
    if len(icao) > 0 && icao[0] == 'K' {
        prefix = "gate"
    }
    
	// 1. Check if it starts with a number (e.g., "201R")
    if unicode.IsDigit(rune(parking[0])) {
        // Separate digits and the alpha suffix
        digits := ""
        suffix := ""
        
        for i, char := range parking {
            if unicode.IsDigit(char) {
                digits += string(char)
            } else {
                // Once we hit a non-digit, the rest is the suffix
                suffix = parking[i:]
                break
            }
        }

        // 2. Handle the Suffix (Single Alpha)
        if len(suffix) == 1 {
            phonetic := phoneticMap[suffix]
            return fmt.Sprintf("%s %s %s", prefix, digits, phonetic)
        }

        return fmt.Sprintf("%s %s", prefix, digits)
    }

    // 3. Handle Alpha-First (e.g., "B12" -> "Gate Bravo 12")
    // Most common in US/Europe terminals
    firstChar := string(parking[0])
    if phonetic, exists := phoneticMap[firstChar]; exists {
        remaining := parking[1:]
        return fmt.Sprintf("%s %s %s", prefix, phonetic, remaining)
    }

    return parking
}

// phoneticiseSingleAlphas will replace single alphas in a phrase to their phonetic equivalents
func phoneticiseSingleAlphas(input string) string {
    words := strings.Fields(input)
    for i, word := range words {
        // Check if the word is a single letter to phoneticise it (e.g., "Ramp A")
        if len(word) == 1 && unicode.IsLetter(rune(word[0])) {
            words[i] = phoneticMap[word]
        }
    }
    return strings.ToLower(strings.Join(words, " "))
}

func formatAirportName(icao string, airportNameLookup map[string]string) string {

    name, exists := airportNameLookup[icao]
    if !exists {
        name = toPhonetics(icao)
		return name
    } 

	replacer := strings.NewReplacer(
		" Intl", "",
		" Arpt", "",
		" Airport", "",
		" Regional", "",
		" Municipal", "",
	)
	return strings.TrimSpace(replacer.Replace(name))

}

func toPhonetics(s string) string {
	var result strings.Builder
	for _, ch := range s {
		if unicode.IsLetter(ch) {
			result.WriteString(phoneticMap[string(ch)])
			result.WriteString(" ")
		}
	}
	return strings.TrimSpace(result.String())
}

func (s *Service) generateHandoffPhrase(ac Aircraft) string {
    // Identify the 'Next Role' based on the new phase
    nextRole, exists := handoffMap[trafficglobal.FlightPhase(ac.Flight.Phase.Current)]
    if !exists { 
		return "" 
	}

    // Determine context ICAO (Origin for departure, Destination for arrival)
    targetICAO := ac.Flight.Origin
    if ac.Flight.Phase.Current == trafficglobal.Approach.Index() || 
			ac.Flight.Phase.Current == trafficglobal.Braking.Index() ||
			ac.Flight.Phase.Current == trafficglobal.TaxiIn.Index() ||
			ac.Flight.Phase.Current == trafficglobal.Shutdown.Index() ||
			ac.Flight.Phase.Current == trafficglobal.Holding.Index() ||
			ac.Flight.Phase.Current == trafficglobal.GoAround.Index() ||
			ac.Flight.Phase.Current == trafficglobal.Final.Index() { 
        targetICAO = ac.Flight.Destination
    }
    if ac.Flight.Phase.Current == trafficglobal.Cruise.Index() { 
		targetICAO = "" // Force distance/polygon search when in cruise
	} 

    // Locate the "Next" controller
	pos := ac.Flight.Position
    nextController := s.LocateController("HANDOFF", 0, nextRole, pos.Lat, pos.Long, pos.Altitude, targetICAO)
    
    if nextController == nil {
		log.Printf("No controller found for handoff: role=%d, targetICAO=%s", nextRole, targetICAO)
		return ""
	}

	freqStr := fmt.Sprintf("%.3f", float64(nextController.Freqs[0])/1000.0)
	freqStr = strings.ReplaceAll(freqStr, ".", " decimal ")
	
	// generate the Handoff Phrase
	// TODO: add valediction - need local hour to determine good day, good evening, good night
	// TODO: add role type e.g. "ground", "tower" into phrase
	return fmt.Sprintf(" [contact] %s on %s", nextController.Name, freqStr)

}
