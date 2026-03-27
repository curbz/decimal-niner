package atc

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/curbz/decimal-niner/internal/logger"
	"github.com/curbz/decimal-niner/pkg/geometry"
	"github.com/curbz/decimal-niner/pkg/util"
)

// VoiceSession stores the metadata for an active assignment
// VoiceSession now stores the specific speaker key
type VoiceSession struct {
	VoiceKey string // Combined "filename#speakerID"
	LastSeen time.Time
	Lat, Lon float64
	Type     int
}

const (
	SessionTypePilot = iota
	SessionTypeATC
)

type VoiceManager struct {
	PhraseClasses     PhraseClasses
	sessions          map[string]VoiceSession
	mu                sync.RWMutex
	voiceDir          string
	rng               *rand.Rand
	countryVoicePools map[string][]string
	regionVoicePools  map[string][]string
	globalVoicePool   []string
	voiceLocks        sync.Map // Map of string -> *sync.Mutex
	allowedSpeakerIDs map[string][]int
}

type PhraseClasses struct {
	phrases       map[string][]Exchange
	phrasesUnicom map[string][]Exchange
}

func NewVoiceManager(cfg *config) *VoiceManager {
	vm := &VoiceManager{
		sessions:          make(map[string]VoiceSession),
		voiceDir:          cfg.ATC.Voices.Piper.VoiceDirectory,
		rng:               rand.New(rand.NewSource(time.Now().UnixNano())),
		countryVoicePools: make(map[string][]string),
		regionVoicePools:  make(map[string][]string),
	}

	vm.loadPhrases(cfg)
	vm.loadSpeakerConfig(cfg.ATC.Voices.Piper)

	// loadvoice pools
	if err := vm.initialisePools(); err != nil {
		logger.Log.Fatalf("error creating voice pools: %v", err)
	}

	return vm
}

func (vm *VoiceManager) loadPhrases(cfg *config) {
	// ... [Previous binary and directory checks remain unchanged] ...

	// Helper to load and validate a phrase map
	loadAndValidate := func(filePath string) (map[string][]Exchange, error) {
		file, err := os.Open(filePath)
		if err != nil {
			return nil, err
		}
		defer file.Close()

		var data map[string][]Exchange
		if err := json.NewDecoder(file).Decode(&data); err != nil {
			return nil, err
		}

		// Validate every phrase in the file
		for category, exchanges := range data {
			for i, ex := range exchanges {
				// Validate Pilot side
				if err := validatePhrase(ex.Pilot); err != nil {
					return nil, fmt.Errorf("[%s] Exchange %d (Pilot): %v", category, i+1, err)
				}
				// Validate ATC side
				if err := validatePhrase(ex.ATC); err != nil {
					return nil, fmt.Errorf("[%s] Exchange %d (ATC): %v", category, i+1, err)
				}
				// Validate required metadata
				if ex.Initiator != "pilot" && ex.Initiator != "atc" {
					return nil, fmt.Errorf("[%s] Exchange %d: invalid initiator '%s'", category, i+1, ex.Initiator)
				}
			}
		}
		return data, nil
	}

	// Process Main Phrases
	phrases, err := loadAndValidate(cfg.ATC.Voices.PhrasesFile)
	if err != nil {
		logger.Log.Fatalf("PCL Syntax Error in %s: %v", cfg.ATC.Voices.PhrasesFile, err)
		return
	}

	// Process Unicom Phrases
	unicomPhrases, err := loadAndValidate(cfg.ATC.Voices.UnicomPhrasesFile)
	if err != nil {
		logger.Log.Fatalf("PCL Syntax Error in %s: %v", cfg.ATC.Voices.UnicomPhrasesFile, err)
		return
	}

	vm.PhraseClasses = PhraseClasses{
		phrases:       phrases,
		phrasesUnicom: unicomPhrases,
	}

	logger.Log.Info("VoiceManager: All phrase files loaded and PCL syntax validated successfully.")
}

func (vm *VoiceManager) initialisePools() error {
	vm.countryVoicePools = make(map[string][]string)
	vm.regionVoicePools = make(map[string][]string)
	vm.globalVoicePool = []string{}

	files, err := os.ReadDir(vm.voiceDir)
	if err != nil {
		return err
	}

	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".onnx") {
			continue
		}

		fileName := file.Name()
		baseName := strings.TrimSuffix(fileName, ".onnx")

		// 1. Get Speaker Count from onnx.json
		numSpeakers := vm.getSpeakerCount(filepath.Join(vm.voiceDir, fileName+".json"))

		// 2. Extract country (e.g., "US" from "en_US...")
		var code string
		if len(baseName) >= 5 {
			code = strings.ToUpper(baseName[3:5])
		}

		// 3. Register every speaker as a unique person in our pools
		for i := range numSpeakers {
			// Unique VoiceKey format: "filename#id"
			voiceKey := fmt.Sprintf("%s#%d", baseName, i)
			if code != "" {
				vm.countryVoicePools[code] = append(vm.countryVoicePools[code], voiceKey)
			}
		}

		// filter pools based on config include list (if provided)
		for country, pool := range vm.countryVoicePools {
			vm.countryVoicePools[country] = vm.filterByIncludeList(pool)

			// Safety Check: Did we filter the pool into non-existence?
			if len(vm.countryVoicePools[country]) == 0 {
				logger.Log.Warnf("Pool for %s is empty after filtering", country)
			}
		}

		for region, pool := range vm.regionVoicePools {
			vm.regionVoicePools[region] = vm.filterByIncludeList(pool)

			// Safety Check: Did we filter the pool into non-existence?
			if len(vm.regionVoicePools[region]) == 0 {
				logger.Log.Warnf("Pool for %s is empty after filtering", region)
			}
		}

		// We use a map to track unique keys so we don't add the same
		// voice twice if it exists in both a Country and Region pool.
		seen := make(map[string]bool)

		// Add from Country pools
		for _, pool := range vm.countryVoicePools {
			for _, key := range pool {
				if !seen[key] {
					vm.globalVoicePool = append(vm.globalVoicePool, key)
					seen[key] = true
				}
			}
		}

		if len(vm.globalVoicePool) == 0 {
			logger.Log.Warn("global voice pool for is empty after filtering")
		}

	}

	return nil
}

func (vm *VoiceManager) loadSpeakerConfig(cfg Piper) {
	// Initialize the map
	vm.allowedSpeakerIDs = make(map[string][]int)

	for _, s := range cfg.Speakers {
		// Clean the filename (e.g., remove .onnx if the user included it)
		cleanName := strings.TrimSuffix(s.FileName, ".onnx")

		// Store the allowed IDs for this specific file
		vm.allowedSpeakerIDs[cleanName] = s.IDs

		logger.Log.Infof("filtering %s to speaker IDs: %v", cleanName, s.IDs)
	}
}

// Helper to check for multi-speaker models
func (vm *VoiceManager) getSpeakerCount(jsonPath string) int {
	f, err := os.Open(jsonPath)
	if err != nil {
		return 1 // Assume 1 voice if no JSON exists
	}
	defer f.Close()

	var cfg struct {
		NumSpeakers int `json:"num_speakers"`
	}
	if err := json.NewDecoder(f).Decode(&cfg); err != nil {
		return 1
	}
	return util.Max(1, cfg.NumSpeakers)
}

// resolveVoice is the main entry point
func (vm *VoiceManager) resolveVoice(msg *ATCMessage) (string, string, int, string, string) {
	vm.mu.Lock()
	defer vm.mu.Unlock()

	key, partnerKey := vm.getSymmetricKeys(msg)

	// check for existing session
	if s, exists := vm.sessions[key]; exists {
		s.LastSeen = time.Now()
		s.Lat, s.Lon = msg.AircraftSnap.Flight.Position.Lat, msg.AircraftSnap.Flight.Position.Long
		vm.sessions[key] = s
		return vm.getVoiceMetadata(s.VoiceKey, msg)
	}

	partnerVoiceKey := ""
	if ps, ok := vm.sessions[partnerKey]; ok {
		partnerVoiceKey = ps.VoiceKey
	}

	selectedVoiceKey := vm.selectVoice(msg, partnerVoiceKey)

	// save session
	vm.sessions[key] = VoiceSession{
		VoiceKey: selectedVoiceKey,
		LastSeen: time.Now(),
		Lat:      msg.AircraftSnap.Flight.Position.Lat,
		Lon:      msg.AircraftSnap.Flight.Position.Long,
		Type:     vm.getSessionType(msg.Role),
	}

	return vm.getVoiceMetadata(selectedVoiceKey, msg)
}

// --- Internal Logic Helpers ---

func (vm *VoiceManager) getSymmetricKeys(msg *ATCMessage) (string, string) {
	// Determine the ID of the aircraft (Callsign is preferred, Reg as fallback)
	planeID := msg.AircraftSnap.Flight.Comms.Callsign
	if planeID == "" {
		planeID = msg.AircraftSnap.Registration
	}

	// The ATC ICAO comes from the message context, not the aircraft's permanent stats
	atcID := msg.ControllerICAO + "_" + msg.Role

	var key, partnerKey string

	if msg.Role == "PILOT" {
		// I am the Pilot
		key = planeID + "_PILOT"
		// My partner is the Controller I'm talking to
		partnerKey = atcID
	} else {
		// I am the Controller (GROUND, TOWER, etc.)
		key = atcID
		// My partner is the Pilot I'm talking to
		partnerKey = planeID + "_PILOT"
	}

	return key, partnerKey
}

func (vm *VoiceManager) getSessionType(role string) int {
	if role == "PILOT" {
		return SessionTypePilot
	}
	return SessionTypeATC
}

func (vm *VoiceManager) selectVoice(msg *ATCMessage, partnerVoice string) string {

	countryCode := msg.CountryCode
	if msg.Role != "PILOT" {
		countryCode = msg.ControllerICAO[:2] // For ATC, derive country from ICAO
	}

	targetISO, _ := convertIcaoToIso(countryCode)
	logLabel := fmt.Sprintf("%s_%s", msg.AircraftSnap.Registration, msg.Role)
	util.LogWithLabel(logLabel, "voice selection started - ISO code: %s (country code %s)", targetISO, countryCode)

	// 1. TIER 1: Primary Country Match
	if pool, ok := vm.countryVoicePools[targetISO]; ok {
		if voice := vm.findBestInPool(pool, partnerVoice); voice != "" {
			util.LogWithLabel(logLabel, "voice selection on country code successful: %s", voice)
			return voice
		}
	}

	util.LogWithLabel(logLabel, "voice selection did not find match for country code: %s", countryCode)

	// 2. TIER 2: Regional Fallback
	if len(countryCode) > 0 {
		regionCode := countryCode[:1] // e.g., 'K' for USA, 'E' for Europe
		util.LogWithLabel(logLabel, "voice selection falling back to region code: %s", regionCode)
		if pool, ok := vm.regionVoicePools[regionCode]; ok {
			if voice := vm.findBestInPool(pool, partnerVoice); voice != "" {
				util.LogWithLabel(logLabel, "voice selection on region code %s successful: %s", regionCode, voice)
				return voice
			}
		}
	}

	util.LogWithLabel(logLabel, "voice selection falling back to global voice pool")

	// 3. TIER 3: Global Fallback
	// Uses the pre-calculated pool to find ANY voice that isn't the partner.
	voice := vm.findBestInPool(vm.globalVoicePool, partnerVoice)

	// If Global pool only had the partnerVoice, findBestInPool returned ""
	if voice == "" {
		util.LogWarnWithLabel(logLabel, "voice pools are currently drained, reluctant reuse of exchange partner voice")
		return vm.globalVoicePool[0]
	}

	return voice
}

func (vm *VoiceManager) findBestInPool(pool []string, partnerVoice string) string {

	if len(pool) == 0 {
		return ""
	}

	// Shuffle to maintain randomness within the pool
	shuffled := make([]string, len(pool))
	copy(shuffled, pool)
	vm.rng.Shuffle(len(shuffled), func(i, j int) {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	})

	// STAGE A: Seek a unique voice (Not partner, not globally used)
	for _, v := range shuffled {
		if v == partnerVoice {
			continue
		}
		if !vm.isVoiceGloballyUsed(v) {
			return v
		}
	}

	// STAGE B: Reallocate (The "Twin" Rule)
	// Pick the voice that was updated (LastSeen) furthest in the past.
	var bestDuplicate string
	var oldestSeen time.Time

	for _, v := range shuffled {
		if v == partnerVoice {
			continue
		}

		lastUsed := vm.getLastUsedTime(v)
		if bestDuplicate == "" || lastUsed.Before(oldestSeen) {
			bestDuplicate = v
			oldestSeen = lastUsed
		}
	}

	return bestDuplicate
}

func (vm *VoiceManager) filterByIncludeList(keys []string) []string {
	if len(vm.allowedSpeakerIDs) == 0 {
		return keys // No restrictions
	}

	var filtered []string
	for _, key := range keys {
		baseName, id := splitVoiceKey(key)

		allowedIDs, exists := vm.allowedSpeakerIDs[baseName]
		if !exists {
			filtered = append(filtered, key)
			continue
		}

		// Check if this specific ID is in the include list
		for _, allowedID := range allowedIDs {
			if id == allowedID {
				filtered = append(filtered, key)
				break
			}
		}
	}
	return filtered
}

func splitVoiceKey(key string) (string, int) {
	parts := strings.Split(key, "#")
	if len(parts) < 2 {
		return key, 0 // Default for solo voice files
	}
	id, _ := strconv.Atoi(parts[1])
	return parts[0], id
}

func (vm *VoiceManager) getVoiceMetadata(voiceKey string, msg *ATCMessage) (string, string, int, string, string) {
	parts := strings.Split(voiceKey, "#")
	baseName := parts[0]
	speakerID := "0"
	if len(parts) > 1 {
		speakerID = parts[1]
	}

	path := filepath.Join(vm.voiceDir, baseName+".onnx")
	rate := 22050

	if f, err := os.Open(path + ".json"); err == nil {
		var cfg struct {
			Audio struct {
				SampleRate int `json:"sample_rate"`
			} `json:"audio"`
		}
		if err := json.NewDecoder(f).Decode(&cfg); err == nil && cfg.Audio.SampleRate > 0 {
			rate = cfg.Audio.SampleRate
		}
		f.Close()
	}

	envNoise := noiseType(msg.Role, msg.AircraftSnap.Flight.Phase.Current)

	// Returns: Name, Path, Rate, Noise, SpeakerID
	return baseName, path, rate, envNoise, speakerID
}

func (vm *VoiceManager) ReleaseSession(aircraftSnap *Aircraft) {
	if aircraftSnap == nil {
		return
	}

	// Identify the ID using the exact same logic as ResolveVoice
	id := aircraftSnap.Flight.Comms.Callsign
	if id == "" {
		id = aircraftSnap.Registration
	}

	// We only ever release Pilots on shutdown; ATC stays static.
	key := id + "_PILOT"

	// Use a goroutine for a "Graceful Cooldown"
	// This prevents the race condition with prepAndQueuePhrase
	target := key
	util.GoSafe(func() {
		// 15s is usually enough for the 'Engine Shutdown' audio to finish
		time.Sleep(15 * time.Second)

		vm.mu.Lock()
		defer vm.mu.Unlock()

		if _, exists := vm.sessions[target]; exists {
			delete(vm.sessions, target)
			logger.Log.Infof("VoiceManager: Successfully released %s\n", target)
		}
	})
}

func (vm *VoiceManager) startCleaner(interval time.Duration, getUserPos func() (float64, float64)) {
	ticker := time.NewTicker(interval)
	for range ticker.C {
		vm.mu.Lock()
		logger.Log.Infof("VoiceManager: Running cleanup, current sessions: %d", len(vm.sessions))
		pLat, pLon := getUserPos()
		now := time.Now()
		evicted := 0

		for key, s := range vm.sessions {
			dist := geometry.DistNM(pLat, pLon, s.Lat, s.Lon)
			shouldEvict := false

			if s.Type == SessionTypePilot {
				// Pilots: 150nm or 20 mins silence
				if dist > 150.0 || now.Sub(s.LastSeen) > 20*time.Minute {
					shouldEvict = true
				}
			} else {
				// ATC: 400nm or 20 mins silence
				if dist > 400.0 || now.Sub(s.LastSeen) > 20*time.Minute {
					shouldEvict = true
				}
			}

			if shouldEvict {
				delete(vm.sessions, key)
				evicted++
			}
		}

		if evicted > 0 {
			logger.Log.Info("VoiceManager: Evicted", evicted, "stale sessions")
		}
		logger.Log.Infof("VoiceManager: Cleanup complete, current sessions: %d", len(vm.sessions))
		vm.mu.Unlock()
	}
}

func (vm *VoiceManager) isVoiceGloballyUsed(voiceKey string) bool {
	// We check every active session to see if this specific
	// Speaker ID + File combo is already "on the radio"
	for _, s := range vm.sessions {
		if s.VoiceKey == voiceKey {
			return true
		}
	}
	return false
}

func (vm *VoiceManager) getLastUsedTime(voiceKey string) time.Time {
	var latest time.Time
	for _, s := range vm.sessions {
		if s.VoiceKey == voiceKey {
			if s.LastSeen.After(latest) {
				latest = s.LastSeen
			}
		}
	}
	// If never seen (ideal for selection), return an empty time.Time (0001-01-01)
	// findBestInPool will see this as the "oldest" and pick it first.
	return latest
}

func (vm *VoiceManager) getVoiceLock(voiceName string) *sync.Mutex {
	// If for some reason voiceName is empty, return a 'global' fallback lock
	if voiceName == "" {
		voiceName = "default_fallback_voice"
	}

	lock, _ := vm.voiceLocks.LoadOrStore(voiceName, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

// convertIcaoToIso takes a full ICAO airport code (e.g., "EGLL") or
// a country prefix (e.g., "EG") and returns the ISO country code.
func convertIcaoToIso(icao string) (string, error) {
	icao = strings.ToUpper(strings.TrimSpace(icao))
	if len(icao) < 1 {
		return "", fmt.Errorf("invalid ICAO code")
	}

	// 1. Check for 2-letter prefix match (most common)
	if len(icao) >= 2 {
		prefix2 := icao[:2]
		if iso, ok := icaoToIsoMap[prefix2]; ok {
			return iso, nil
		}
	}

	// 2. Check for 1-letter prefix match (Major countries)
	prefix1 := icao[:1]
	if iso, ok := icaoToIsoMap[prefix1]; ok {
		return iso, nil
	}

	return "", fmt.Errorf("no ISO mapping found for ICAO code: %s", icao)
}

// infer the comms country code from flight data for use when other methods of setting the comms country code have failed
func inferCommsCountryCode(ac *Aircraft, defaultCode string) {
	countrySource := ""
	if ac.Flight.Phase.Class == Departing {
		countrySource = ac.Flight.Origin
	}
	if ac.Flight.Phase.Class == Arriving {
		countrySource = ac.Flight.Destination
	}
	if len(countrySource) > 2 {
		ac.Flight.Comms.CountryCode = countrySource[:2]
		util.LogWithLabel(ac.Registration, "flight data used to set comms country code %s", ac.Flight.Comms.CountryCode)
	} else {
		// we absolutely must have a country code to work with at this point
		ac.Flight.Comms.CountryCode = defaultCode
		util.LogWarnWithLabel(ac.Registration, "no comms country code - last resort setting to default of  %s", ac.Flight.Comms.CountryCode)
	}
}

var (
	// validPCLTags includes both Raw ($) and Formatted (@) tags found in newPCLContext
	validPCLTags = map[string]bool{
		"$ALTITUDE": true, "$CALLSIGN": true, "$FACILITY": true, "$SQUAWK": true,
		"$HEADING": true, "$RUNWAY": true, "$DESTINATION": true, "$BARO_SEALEVEL": true,
		"$BARO_AIRCRAFT": true, "$WIND_SPEED": true, "$WIND_SHEAR": true, "$TURBULENCE": true,
		"$PARKING": true, "$APPROACH_TYPE": true, "$HOLD_FIX_NAME": true, "$HOLD_FIX_IDENT": true,
		"$MA_HEADING": true, "$MA_ALTITUDE": true, "$MA_FIX": true, "$FA_ALTITUDE": true,
		"@RUNWAY": true, "@PARKING": true, "@DESTINATION": true, "@APPROACH_TYPE": true,
		"@MA_HEADING": true, "@MA_ALTITUDE": true, "@MA_FIX": true, "@ALTITUDE": true,
		"@ALT_CLEARANCE": true, "@BARO": true, "@WIND": true, "@SHEAR": true,
		"@TURBULENCE": true, "@HANDOFF": true, "@VALEDICTION": true, "@HOLD_FIX": true,
	}
)

func validatePhrase(phrase string) error {
	if phrase == "" {
		return nil
	}

	// 1. Check for balanced braces
	stack := 0
	for _, char := range phrase {
		switch char {
		case '{':
			stack++
		case '}':
			stack--
		}
		if stack < 0 {
			return fmt.Errorf("unexpected closing brace '}'")
		}
	}
	if stack != 0 {
		return fmt.Errorf("unclosed opening brace '{'")
	}

	// 2. Validate Tags and Logic
	start := -1
	for i, char := range phrase {
		if char == '{' {
			start = i
		} else if char == '}' && start != -1 {
			tagContent := phrase[start+1 : i]
			start = -1

			// Handle Functional PCL Logic (WHEN/SAY/OTHERWISE)
			if strings.HasPrefix(tagContent, "WHEN") {
				if err := validateLogicBlock(tagContent); err != nil {
					return err
				}
				continue
			}

			// Ignore simple functional markers
			if tagContent == "NOREADBACK" {
				continue
			}

			// Standard Variable Validation ($ or @)
			baseTag := tagContent
			if idx := strings.Index(tagContent, "("); idx != -1 {
				baseTag = tagContent[:idx]
			}

			if !validPCLTags[baseTag] {
				return fmt.Errorf("unknown PCL tag: %s", baseTag)
			}
		}
	}
	return nil
}

func validateLogicBlock(content string) error {
	if !strings.HasPrefix(content, "WHEN") {
		return fmt.Errorf("PCL logic error: logic block must start with WHEN")
	}

	// Find SAY and OTHERWISE at the CURRENT level only
	idxSay := findKeywordAtLevel(content, "SAY")
	if idxSay == -1 {
		return fmt.Errorf("PCL logic error: WHEN block missing mandatory SAY statement")
	}

	idxOtherwise := findKeywordAtLevel(content, "OTHERWISE")

	// Validate the condition (between WHEN and the first SAY)
	condition := strings.TrimSpace(content[4:idxSay])
	if condition == "" {
		return fmt.Errorf("PCL logic error: WHEN condition is empty")
	}

	// Validate the 'SAY' branch (recursively check if it contains more PCL)
	sayBranch := strings.TrimSpace(content[idxSay+3 : func() int {
		if idxOtherwise != -1 {
			return idxOtherwise
		}
		return len(content)
	}()])

	if err := validatePhrase(sayBranch); err != nil {
		return fmt.Errorf("error in SAY branch: %v", err)
	}

	// Validate the 'OTHERWISE' branch if it exists
	if idxOtherwise != -1 {
		remaining := content[idxOtherwise+9:] // after "OTHERWISE"
		idxSecondSay := findKeywordAtLevel(remaining, "SAY")
		if idxSecondSay == -1 {
			return fmt.Errorf("PCL logic error: OTHERWISE block missing follow-up SAY statement")
		}

		otherwiseBranch := strings.TrimSpace(remaining[idxSecondSay+3:])
		if err := validatePhrase(otherwiseBranch); err != nil {
			return fmt.Errorf("error in OTHERWISE branch: %v", err)
		}
	}

	return nil
}

// findKeywordAtLevel locates a keyword (SAY, OTHERWISE) only if it's not inside another { }
func findKeywordAtLevel(content, keyword string) int {
	stack := 0
	// We iterate by byte to find the exact starting index of the keyword
	for i := 0; i < len(content); i++ {
		if content[i] == '{' {
			stack++
		} else if content[i] == '}' {
			stack--
		} else if stack == 0 && strings.HasPrefix(content[i:], keyword) {
			// Ensure it's a "whole word" match by checking surrounding characters
			endIdx := i + len(keyword)

			// Check character before keyword (must be start of string or whitespace)
			isStart := i == 0 || content[i-1] == ' '

			// Check character after keyword (must be end of string or whitespace)
			isEnd := endIdx == len(content) || content[endIdx] == ' '

			if isStart && isEnd {
				return i
			}
		}
	}
	return -1
}
