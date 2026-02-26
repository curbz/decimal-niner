package atc

import (
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	"golang.org/x/text/runes"

	"github.com/curbz/decimal-niner/internal/trafficglobal"
	"github.com/curbz/decimal-niner/pkg/util"
)

type VoicesConfig struct {
	PhrasesFile              string `yaml:"phrases_file"`
	UnicomPhrasesFile        string `yaml:"unicom_phrases_file"`
	Piper                    Piper  `yaml:"piper"`
	Sox                      Sox    `yaml:"sox"`
	HandoffValedictionFactor int    `yaml:"handoff_valediction_factor"`
	SayAgainFactor           int    `yaml:"say_again_factor"`
}

type Exchange struct {
	ID        string `json:"id"`
	Initiator string `json:"initiator"` // "pilot" or "atc"
	Pilot     string `json:"pilot"`
	ATC       string `json:"atc"`
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
	VoiceLock  *sync.Mutex
}

var radioQueue chan ATCMessage
var prepQueue chan PreparedAudio

// PiperConfig represents the structure of the Piper ONNX model JSON config
type PiperConfig struct {
	Audio struct {
		SampleRate int `json:"sample_rate"`
	} `json:"audio"`
}

// main function to recieve aircraft updates for phrase generation
func (s *Service) startComms() {

	// main loop to read from channel and process instructions
	go func() {
		for ac := range s.Channel {
			// process instructions here based on aircraft phase or other criteria
			// this process may generate a response to the communication

			phaseFacility := atcFacilityByPhaseMap[trafficglobal.FlightPhase(ac.Flight.Phase.Current)]

			if ac.Flight.Comms.CruiseHandoff != NoHandoff {
				switch ac.Flight.Comms.CruiseHandoff {
				case HandoffEnterSector:
					util.LogWithLabel(ac.Registration, "Processing handoff enter sector scenario for controller %s", ac.Flight.Comms.Controller.Name)
					phrase := "{FACILITY}, {CALLSIGN} {ALTITUDE}"
					s.prepAndQueuePhrase(phrase, "PILOT", ac, s.Weather.Baro)
					phrase = "{CALLSIGN} , {FACILITY} identified"
					s.prepAndQueuePhrase(phrase, roleNameMap[phaseFacility.roleId], ac, s.Weather.Baro)
					ac.Flight.Comms.CruiseHandoff = NoHandoff
					s.Transmit(s.UserState, ac)
				case HandoffExitSector:
					util.LogWithLabel(ac.Registration, "Processing handoff exit sector scenario for controller %s", ac.Flight.Comms.Controller.Name)
					// select next controller's first listed frequency
					freqStr := fmt.Sprintf("%.3f", float64(ac.Flight.Comms.NextController.Freqs[0])/1000.0)
					freqStr = strings.ReplaceAll(freqStr, ".", " decimal ")
					phrase := fmt.Sprintf("{CALLSIGN} contact %s on %s {{VALEDICTION}}", ac.Flight.Comms.Controller.Name, freqStr)
					s.prepAndQueuePhrase(phrase, roleNameMap[phaseFacility.roleId], ac, s.Weather.Baro)
					s.prepAndQueuePhrase(autoReadback(phrase), "PILOT", ac, s.Weather.Baro)
					go func() {
						time.Sleep(20 * time.Second)
						ac.Flight.Comms.Controller = ac.Flight.Comms.NextController
						ac.Flight.Comms.CruiseHandoff = HandoffEnterSector
						s.Transmit(s.UserState, ac)
					}()
				}

				continue
			}

			var phraseSource map[string][]Exchange
			if ac.Flight.Comms.Controller.RoleID == 0 {
				phraseSource = s.VoiceManager.PhraseClasses.phrasesUnicom
			} else {
				phraseSource = s.VoiceManager.PhraseClasses.phrases
			}

			exchanges, exists := phraseSource[phaseFacility.atcPhase]
			if !exists || len(exchanges) == 0 {
				util.LogWithLabel(ac.Registration, "error: no phrases found for flight phase %d", ac.Flight.Phase.Current)
				continue
			}

			// select random exchange
			exchange := exchanges[rand.Intn(len(exchanges))]

			// didSayAgain bool ensures 'say again' cannot be repeated for the same pilot/controller exchange
			didSayAgain := false
			if exchange.Initiator == "pilot" {
				// pilot's initial phrase
				s.prepAndQueuePhrase(exchange.Pilot, "PILOT", ac, s.Weather.Baro)
				// if not unicom then ATC responds
				if ac.Flight.Comms.Controller.RoleID != 0 {
					// randomised 'say again'
					if rand.Intn(s.Config.ATC.Voices.SayAgainFactor) == 0 && !didSayAgain {
						// atc asks pilot to repeat request
						s.prepAndQueuePhrase("{CALLSIGN} say again", roleNameMap[phaseFacility.roleId], ac, s.Weather.Baro)
						// pilot repeats phrase
						s.prepAndQueuePhrase(exchange.Pilot, "PILOT", ac, s.Weather.Baro)
					}
					// atc responds
					s.prepAndQueuePhrase(exchange.ATC, roleNameMap[phaseFacility.roleId], ac, s.Weather.Baro)
					// pilot reads back atc instructions, but not for shutdown phase to avoid unecessary repetition
					if ac.Flight.Phase.Current != trafficglobal.Shutdown.Index() {
						s.prepAndQueuePhrase(autoReadback(exchange.ATC), "PILOT", ac, s.Weather.Baro)
					}
				}
			}

			if exchange.Initiator == "atc" {
				// atc initiates call to pilot
				s.prepAndQueuePhrase(exchange.ATC, roleNameMap[phaseFacility.roleId], ac, s.Weather.Baro)
				// randomised 'say again'
				if rand.Intn(s.Config.ATC.Voices.SayAgainFactor) == 0 && !didSayAgain {
					// pilot asks atc to repeat request
					s.prepAndQueuePhrase("{FACILITY} say again", "PILOT", ac, s.Weather.Baro)
					// atc repeats instructions
					s.prepAndQueuePhrase(exchange.ATC, roleNameMap[phaseFacility.roleId], ac, s.Weather.Baro)
				}
				if exchange.Pilot == "" {
					// if the selected exchange does not specify a pilot response, the pilot will read back atc instructions
					s.prepAndQueuePhrase(autoReadback(exchange.ATC), "PILOT", ac, s.Weather.Baro)
				} else {
					// else the pilot responds with the specified exchange phrase
					s.prepAndQueuePhrase(exchange.Pilot, "PILOT", ac, s.Weather.Baro)
				}
			}

			// if the flight has reached shutdown phase, we can release the voice session immediately as there will be no further communications and this allows for quicker recycling of voices in busy airspaces. For other phases we rely on the periodic cleaner to evict stale sessions after a timeout
			if ac.Flight.Phase.Current == trafficglobal.Shutdown.Index() {
				s.VoiceManager.ReleaseSession(ac)
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
// role is either "PILOT" or the facility type e.g "Tower"
func (s *Service) prepAndQueuePhrase(phrase, role string, ac *Aircraft, baro Baro) {

	// construct message and replace all placeholder variables

	phrase = strings.ReplaceAll(phrase, "{CALLSIGN}", ac.Flight.Comms.Callsign)
	phrase = strings.ReplaceAll(phrase, "{FACILITY}", ac.Flight.Comms.Controller.Name)

	if strings.Contains(phrase, "{SQUAWK}") {
		phrase = strings.ReplaceAll(phrase, "{SQUAWK}", ac.Flight.Squawk)
	}

	if strings.Contains(phrase, "{RUNWAY}") {
		phrase = strings.ReplaceAll(phrase, "{RUNWAY}", translateRunway(ac.Flight.AssignedRunway))
	}
	if strings.Contains(phrase, "{PARKING}") {
		phrase = strings.ReplaceAll(phrase, "{PARKING}", formatParking(ac.Flight.AssignedParking, ac.Flight.Comms.Controller.ICAO))
	}
	if strings.Contains(phrase, "{DESTINATION}") {
		sayDest := ac.Flight.Destination
		if sayDest == "" {
			sayDest = "as filed"
		} else {
			sayDest = formatAirportName(sayDest, s.AirportLocations)
		}
		phrase = strings.ReplaceAll(phrase, "{DESTINATION}", sayDest)
	}
	if strings.Contains(phrase, "{ALTITUDE}") {
		// TODO: call getTransitionLevel instead of using baro.TransitionAlt
		phrase = strings.ReplaceAll(phrase, "{ALTITUDE}", formatAltitude(ac.Flight.Position.Altitude, baro.TransitionAlt, ac))
	}
	if strings.Contains(phrase, "{ALT_CLEARANCE}") {
		// TODO: call getTransitionLevel instead of using baro.TransitionAlt
		phrase = strings.ReplaceAll(phrase, "{ALT_CLEARANCE}", generateClearance(ac.Flight.Position.Altitude, baro.TransitionAlt, ac))
	}
	if strings.Contains(phrase, "{HEADING}") {
		phrase = strings.ReplaceAll(phrase, "{HEADING}", fmt.Sprintf("%03d", int(math.Round(ac.Flight.Position.Heading))))
	}
	if strings.Contains(phrase, "{BARO}") {
		phrase = strings.ReplaceAll(phrase, "{BARO}", formatBaro(ac.Flight.Comms.Controller.ICAO, baro.Sealevel))
	}
	if strings.Contains(phrase, "{WIND}") {
		phrase = strings.ReplaceAll(phrase, "{WIND}", s.formatWind())
	}
	if strings.Contains(phrase, "{SHEAR}") {
		phrase = strings.ReplaceAll(phrase, "{SHEAR}", s.formatWindShear())
	}
	if strings.Contains(phrase, "{TURBULENCE}") {
		phrase = strings.ReplaceAll(phrase, "{TURBULENCE}", s.formatTurbulence(role))
	}
	if strings.Contains(phrase, "{HANDOFF}") {
		phrase = strings.ReplaceAll(phrase, "{HANDOFF}", s.generateHandoffPhrase(ac))
	}
	if strings.Contains(phrase, "{VALEDICTION}") {
		factor := s.Config.ATC.Voices.HandoffValedictionFactor
		replace := "{VALEDICTION}"
		if strings.Contains(phrase, "{{VALEDICTION}}") {
			factor = 1
			replace = "{{VALEDICTION}}"
		}
		phrase = strings.ReplaceAll(phrase, replace, s.generateValediction(factor))
	}

	//cleanup phrase
	// 1. Decompose accents (é becomes e + ´)
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	phrase, _, _ = transform.String(t, phrase)
	phrase = strings.ReplaceAll(phrase, "[", "")
	phrase = strings.ReplaceAll(phrase, "]", "")
	phrase = strings.ReplaceAll(phrase, "{", "")
	phrase = strings.ReplaceAll(phrase, "}", "")
	phrase = strings.ReplaceAll(phrase, "(", "")
	phrase = strings.ReplaceAll(phrase, ")", "")
	re := regexp.MustCompile(`\.[\s\.]*$`)
	phrase = re.ReplaceAllString(phrase, ".")
	phrase = strings.TrimSpace(phrase)
	phrase = strings.TrimSuffix(phrase, ",")
	var reSanitize = regexp.MustCompile(`[^{a-zA-Z0-9\s\.,\-]`)
	phrase = reSanitize.ReplaceAllString(phrase, "")

	phrase = translateNumerics(phrase)

	util.LogWithLabel(ac.Registration, "sending phrase to radio queue for speech generation: %s", phrase)

	// send message to radio queue
	radioQueue <- ATCMessage{ac.Flight.Comms.Controller.ICAO, ac, role,
		phrase, ac.Flight.Comms.CountryCode, ac.Flight.Comms.Controller.Name,
	}
}

// PrepSpeech picks up text and starts the Piper process immediately
func PrepSpeech(piperPath string, vm *VoiceManager) {

	// channel queue processing loop
	for msg := range radioQueue {

		util.LogWithLabel(msg.AircraftSnap.Registration, "radio queue received phrase, processing")

		voice, onnx, rate, noise := vm.resolveVoice(msg)

		// PROTECT: If voice name is empty, we can't speak
		if voice == "" {
			util.LogWithLabel(msg.AircraftSnap.Registration, "error: voice name is empty, skipping speech generation to prevent Piper error")
			continue
		}

		// Lock this specific voice so no other Piper process touches this .onnx file
		// CRITICAL: You must pass this lock to the Player to unlock it
		vLock := vm.getVoiceLock(voice)
		if vLock == nil {
			util.LogWithLabel(msg.AircraftSnap.Registration, "ERROR: Could not retrieve lock for voice: %s", voice)
			continue
		}
		vLock.Lock()

		cmd := exec.Command(piperPath, "--model", onnx, "--output-raw", "--length_scale", "0.7")
		stdin, err := cmd.StdinPipe()
		if err != nil {
			util.LogWithLabel(msg.AircraftSnap.Registration, "Error obtaining piper stdin pipe: %v", err)
			continue
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			util.LogWithLabel(msg.AircraftSnap.Registration, "Error obtaining piper stdout pipe: %v", err)
			continue
		}

		if err := cmd.Start(); err != nil {
			util.LogWithLabel(msg.AircraftSnap.Registration, "Error starting piper: %v", err)
			continue
		}

		// Feed text immediately so Piper starts synthesizing in the background
		// Must close stdin to signal EOF to piper
		go func(s io.WriteCloser, t string) {
			defer s.Close()
			_, err := io.WriteString(s, t)
			if err != nil {
				util.LogWithLabel(msg.AircraftSnap.Registration, "Error writing to piper stdin: %v", err)
				return
			}
			// A tiny pause ensures the C++ buffer has moved the text
			// to the synthesis engine before the pipe 'disappears'
			time.Sleep(10 * time.Millisecond)
		}(stdin, msg.Text)

		util.LogWithLabel(msg.AircraftSnap.Registration, "sending message to radio player")

		// Send the running process to the player queue
		prepQueue <- PreparedAudio{
			PiperCmd:   cmd,
			PiperOut:   stdout,
			SampleRate: rate,
			NoiseType:  noise,
			Msg:        msg,
			Voice:      voice,
			VoiceLock:  vLock,
		}
	}
}

// RadioPlayer takes prepared Piper processes and pipes them to SoX sequentially
func RadioPlayer(soxPath string) {

	// channel queue processing loop
	for audio := range prepQueue {

		// PROTECT: If voice name is empty, we can't speak
		if audio.Voice == "" {
			util.LogWithLabel(audio.Msg.AircraftSnap.Registration, "error: voice name is empty, skipping speech audio playback to prevent Piper error")
			continue
		}

		// Wrap the logic in a closure so defer works per-iteration
		func(a PreparedAudio) {

			// must unlock voice at end of function regardless of outcome
			if a.VoiceLock != nil {
				defer a.VoiceLock.Unlock()
			}

			util.LogWithLabel(audio.Msg.AircraftSnap.Registration, "radio player received message, processing")

			args := []string{
				"-t", "raw", "-r", strconv.Itoa(audio.SampleRate), "-e", "signed-integer", "-b", "16", "-c", "1", "-",
			}
			if runtime.GOOS == "windows" {
				args = append(args, "-d")
			}
			args = append(args,
				// SoX effects chain
				"bandpass", "1200", "1500", "overdrive", "20", "tremolo", "5", "40",
				"pad", "0.3", "0.3", "synth", audio.NoiseType, "mix", "pad", "0", "0.2",
			)

			playCmd := exec.Command(soxPath, args...)
			playCmd.Stdin = audio.PiperOut

			util.LogWithLabel(fmt.Sprintf("%s_%s_%s", audio.Msg.AircraftSnap.Registration, strings.ToUpper(audio.Msg.Role),
				strings.ReplaceAll(audio.Msg.ControllerName, " ", "")),
				"%s (%s)", audio.Msg.Text, audio.Voice)

			if err := playCmd.Start(); err != nil {
				util.LogWithLabel(audio.Msg.AircraftSnap.Registration, "Error starting sox: %v", err)
				audio.PiperCmd.Process.Kill()
				return
			}

			// 1. Wait for SoX first.
			// When SoX finishes, it closes Stdin (audio.PiperOut).
			_ = playCmd.Wait()

			// 2. // Explicitly drop the handle to the pipe
			audio.PiperOut.Close()

			// 3. NOW wait for Piper.
			// Piper will have seen a 'broken pipe' or EOF and will be ready to exit cleanly.
			err := audio.PiperCmd.Wait()
			if err != nil {
				// Log if it's not a standard exit, but 0xc0000409 should be gone
				//if !strings.Contains(err.Error(), "exit status 1") {
				util.LogWithLabel(audio.Msg.AircraftSnap.Registration, "error on Piper exit for %s: %v", audio.Voice, err)
				//}
			}

			util.LogWithLabel(audio.Msg.AircraftSnap.Registration, "radio player finished")

			// force a small gap between transmissions
			time.Sleep(time.Duration(rand.Intn(500)+500) * time.Millisecond)

		}(audio)
	}
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

func formatBaro(icao string, pascals float64) string {

	digits := ""

	// Determine the regional "Keyword"
	prefix := "QNH"
	if strings.HasPrefix(icao, "K") || strings.HasPrefix(icao, "C") {
		prefix = "altimeter"
		inHg := pascals * 0.0002953                                     // Convert Pascals to inches of mercury
		digits = strings.ReplaceAll(fmt.Sprintf("%.2f", inHg), ".", "") // "2992"
	} else {
		hpa := int(pascals / 100)       // Convert pascals to hPa
		digits = fmt.Sprintf("%d", hpa) // "1013"
	}

	// Return the full verbal string to replace {BARO}
	return fmt.Sprintf("%s %s", prefix, digits)
}

func formatAltitude(rawAlt float64, transitionLevel int, ac *Aircraft) string {

	scaledAlt, flightLevelScale := scaleAltitude(rawAlt, transitionLevel, ac)

	if flightLevelScale {
		// Returns "flight level 330"
		return fmt.Sprintf("flight level %d", scaledAlt)
	}

	// Feet Logic (Below Transition Altitude)
	// If it's a clean thousand (e.g., 5000)
	if scaledAlt%1000 == 0 {
		return fmt.Sprintf("%d thousand", scaledAlt/1000)
	}

	// Handle split altitudes like 2400 (common in approach/missed approach)
	thousands := scaledAlt / 1000
	hundreds := (scaledAlt % 1000) / 100

	// Returns "2 thousand 4 hundred"
	return fmt.Sprintf("%d thousand %d hundred", thousands, hundreds)
}

// generateClearance builds an altitude clearance phrase
// one of "descend to", "maintain", "climb to"
func generateClearance(rawAlt float64, transitionLevel int, ac *Aircraft) string {

	instruction := ""
	phrase := ""

	if ac.Flight.AltClearance == 0 {
		return phrase
	}

	scaledClearedAlt, clearedScaleIsFlightLevel := scaleAltitude(float64(ac.Flight.AltClearance), transitionLevel, ac)
	scaledAlt, scaleIsFlightLevel := scaleAltitude(rawAlt, transitionLevel, ac)

	if scaleIsFlightLevel != clearedScaleIsFlightLevel {
		// scales are different
		if scaleIsFlightLevel {
			// current altitude is a flight level and cleared to an altitude, so we must descend
			instruction = "descend to"
		} else {
			instruction = "climb to"
		}
	} else {
		// scales are the same so we can directly compare values
		if scaledAlt >= scaledClearedAlt {
			if scaledAlt == scaledClearedAlt {
				instruction = "maintain"
			} else {
				instruction = "descend to"
			}
		} else {
			instruction = "climb to"
		}
	}

	phrase = fmt.Sprintf("%s %s", instruction, formatAltitude(float64(ac.Flight.AltClearance), transitionLevel, ac))

	return phrase
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
		// but the digits are ready for translation to words
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
			if phonetic, exists := phoneticMap[suffix]; exists {
				return fmt.Sprintf("%s %s %s", prefix, digits, phonetic)
			} else {
				return fmt.Sprintf("%s %s %s", prefix, digits, suffix)
			}
		}

		return fmt.Sprintf("%s %s", prefix, digits)
	}

	// 3. Handle Alpha-First followed by digits(e.g., "B12" -> "Gate Bravo 12")
	// Most common in US/Europe terminals
	// Use regex to verify the pattern is indeed an alpha followed by digits
	match, _ := regexp.MatchString(`^[A-Z]\d+`, parking)
	if match {
		firstChar := string(parking[0])
		if phonetic, exists := phoneticMap[firstChar]; exists {
			remaining := parking[1:]
			return fmt.Sprintf("%s %s %s", prefix, phonetic, remaining)
		} else {
			return fmt.Sprintf("%s %s", prefix, parking)

		}
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

func formatAirportName(icao string, airportNameLookup map[string]AirportCoords) string {

	apc, exists := airportNameLookup[icao]
	if !exists {
		return toPhonetics(icao)
	}

	replacer := strings.NewReplacer(
		" Intl", "",
		" Arpt", "",
		" Airport", "",
		" Regional", "",
		" Municipal", "",
	)
	return strings.TrimSpace(replacer.Replace(apc.Name))

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

// generateHandoffPhrase creates a controller handoff phrase and automatically includes valediction (based on configured factor)
func (s *Service) generateHandoffPhrase(ac *Aircraft) string {
	// Identify the 'Next Role' based on the new phase
	nextRole, exists := handoffMap[trafficglobal.FlightPhase(ac.Flight.Phase.Current)]
	if !exists {
		return ""
	}

	// Locate the "Next" controller
	searchICAO := airportICAObyPhaseClass(ac, ac.Flight.Phase.Class)
	pos := ac.Flight.Position
	label := fmt.Sprintf("%s_HANDOFF", ac.Registration)
	nextController := s.LocateController(label,
		0, nextRole, pos.Lat, pos.Long, pos.Altitude, searchICAO)

	if nextController == nil {
		util.LogWithLabel(label, "No controller found for handoff: role=%s (%d), searchICAO=%s",
			roleNameMap[nextRole], nextRole, searchICAO)
		return ""
	} else {
		util.LogWithLabel(label, "Controller found: %s %s Role ID: %s (%d)",
			nextController.Name, nextController.ICAO, roleNameMap[nextController.RoleID], nextController.RoleID)
	}

	// select controller's first listed frequency
	freqStr := fmt.Sprintf("%.3f", float64(nextController.Freqs[0])/1000.0)
	freqStr = strings.ReplaceAll(freqStr, ".", " decimal ")

	// if next role is approach or cruise, include the facility name
	facilityName := ""
	if nextRole == trafficglobal.Approach.Index() || nextRole == trafficglobal.Cruise.Index() {
		facilityName = nextController.Name
	}

	return fmt.Sprintf(" [contact] %s %s on %s %s", facilityName, roleNameMap[nextRole], freqStr, s.generateValediction(s.Config.ATC.Voices.HandoffValedictionFactor))

}

func (s *Service) generateValediction(factor int) string {

	valediction := ""
	if rand.Intn(factor) == 0 {
		currTime, err := s.DataProvider.GetSimTime()
		if err != nil {
			log.Printf("error: could not get local time: %s", err.Error())
		} else {
			localTime := currTime.LocalTimeSecs
			currHour := localTime / 3600
			if currHour < 18 {
				valediction = "good day"
			} else {
				if currHour < 23 {
					valediction = "good evening"
				} else {
					valediction = "good night"
				}
			}
		}
	}

	return fmt.Sprintf("[%s]", valediction)
}

func (s *Service) formatWind() string {

	const mpsToKnots = 1.94384
	speedKt := s.Weather.Wind.Speed * mpsToKnots

	// 2. Convert to Magnetic and Round to nearest 10
	magDir := s.Weather.Wind.Direction - float64(s.Weather.MagVar)
	if magDir <= 0 {
		magDir += 360
	}
	if magDir > 360 {
		magDir -= 360
	}

	roundedDir := int((magDir+5)/10) * 10
	if roundedDir == 0 {
		roundedDir = 360
	}

	// 3. Base Wind Phrasing
	var windPhrase string
	if speedKt < 4 {
		windPhrase = "calm"
	} else {
		windPhrase = fmt.Sprintf("%03d at %d knots", roundedDir, int(speedKt))
		gustKt := 0.0
		if s.Weather.Turbulence > 0.2 {
			// Simple heuristic: Turbulence adds a gust factor
			// A turb of 0.5 adds roughly 10-15 knots of gust
			gustKt = speedKt + (s.Weather.Turbulence * 25.0)
		}
		if gustKt > speedKt+9 {
			windPhrase += fmt.Sprintf(" gusting %d", int(gustKt))
		}
	}

	return windPhrase
}

func (s *Service) formatWindShear() string {

	var phrase string
	const mpsToKnots = 1.94384

	// Wind Shear (Converted from m/s to knots)
	shearKt := s.Weather.Wind.Shear * mpsToKnots

	if shearKt >= 15 {
		// Round to nearest 5
		shearRound := int((shearKt+2)/5) * 5
		phrase = fmt.Sprintf("[caution] wind shear [alert, loss or gain of] %d knots", shearRound)
	}

	return phrase
}

func (s *Service) formatTurbulence(role string) string {

	phrase := ""
	turbClass := ""

	// Turbulence Magnitude
	if s.Weather.Turbulence >= 0.7 {
		turbClass = "severe"
	} else if s.Weather.Turbulence >= 0.4 {
		turbClass = "moderate"
	}

	if turbClass != "" {
		if role == "PILOT" {
			phrase = fmt.Sprintf("experiencing %s turbulence", turbClass)
		} else {
			phrase = fmt.Sprintf("%s turbulence [reported]", turbClass)
		}
	}

	return phrase
}
