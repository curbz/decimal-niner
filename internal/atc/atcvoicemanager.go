package atc

import (
	"encoding/json"
	"io"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/curbz/decimal-niner/pkg/geometry"
	"github.com/curbz/decimal-niner/pkg/util"
)

// VoiceSession stores the metadata for an active assignment
type VoiceSession struct {
	VoiceName string
	LastSeen  time.Time
	Lat, Lon  float64
	Type      int
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
	globalPool        []string
	voiceLocks sync.Map // Map of string -> *sync.Mutex
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

	return vm
}

func (vm *VoiceManager) loadPhrases(cfg *config) {

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
	err := vm.initialisePools()
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

	vm.PhraseClasses = PhraseClasses{
		phrases:       phrases,
		phrasesUnicom: unicomPhrases,
	}
}

func (vm *VoiceManager) initialisePools() error {

	// Initialize the map
	vm.countryVoicePools = make(map[string][]string)
	vm.regionVoicePools = make(map[string][]string)

	files, err := os.ReadDir(vm.voiceDir)
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
			// Extract the country for the key
			if len(fileName) >= 5 {
				code := strings.ToUpper(fileName[3:5])

				// Remove the extension for the value
				// filepath.Ext(fileName) returns ".onnx"
				cleanName := strings.TrimSuffix(fileName, filepath.Ext(fileName))

				// populate global pool
        		vm.globalPool = append(vm.globalPool, cleanName)

				// Populate map
				vm.countryVoicePools[code] = append(vm.countryVoicePools[code], cleanName)
			}
		}
	}

	if len(vm.globalPool) < 2 {
		log.Fatalf("a minimum of 2 voice files are required in folder %s", vm.voiceDir)
	}

	if len(vm.countryVoicePools) == 0 {
		log.Fatalf("no voice files found in folder %s", vm.voiceDir)
	}

	// create region voice pools
	for k, v := range icaoToIsoMap {
		cvp, cvpfound := vm.countryVoicePools[v]
		if !cvpfound {
			continue
		}
		regionCode := k[:1]
		vm.regionVoicePools[regionCode] = append(vm.regionVoicePools[regionCode], cvp...)
	}

	return nil
}

// resolveVoice is the main entry point
func (vm *VoiceManager) resolveVoice(msg ATCMessage) (string, string, int, string) {

	vm.mu.Lock()
	defer vm.mu.Unlock()

	key, partnerKey := vm.getSymmetricKeys(msg)

	// 1. Check for existing session
	if s, exists := vm.sessions[key]; exists {
		s.LastSeen = time.Now()
		s.Lat, s.Lon = msg.AircraftSnap.Flight.Position.Lat, msg.AircraftSnap.Flight.Position.Long
		vm.sessions[key] = s
		return vm.getVoiceMetadata(s.VoiceName, msg)
	}

	// 2. Assign New Voice
	partnerVoice := vm.sessions[partnerKey].VoiceName
	selectedVoice := vm.performTieredSearch(msg, partnerVoice)

	// 3. Save Session
	vm.sessions[key] = VoiceSession{
		VoiceName: selectedVoice,
		LastSeen:  time.Now(),
		Lat:       msg.AircraftSnap.Flight.Position.Lat,
		Lon:       msg.AircraftSnap.Flight.Position.Long,
		Type:      vm.getSessionType(msg.Role),
	}

	return vm.getVoiceMetadata(selectedVoice, msg)
}

// --- Internal Logic Helpers ---

func (vm *VoiceManager) getSymmetricKeys(msg ATCMessage) (string, string) {
	// Determine the ID of the aircraft (Callsign is preferred, Reg as fallback)
	planeID := msg.AircraftSnap.Flight.Comms.Callsign
	if planeID == "" {
		planeID = msg.AircraftSnap.Registration
	}

	// The ATC ICAO comes from the message context, not the aircraft's permanent stats
	atcID := msg.ICAO + "_" + msg.Role

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

func (vm *VoiceManager) performTieredSearch(msg ATCMessage, partnerVoice string) string {

	util.LogWithLabel(msg.AircraftSnap.Registration, "voice selection started - target country code: %s", msg.CountryCode)

	// 1. TIER 1: Primary Country Match
	targetISO, _ := convertIcaoToIso(msg.CountryCode)
	if voice := vm.findBestInPool(vm.countryVoicePools[targetISO], partnerVoice); voice != "" {
		util.LogWithLabel(msg.AircraftSnap.Registration, "voice selection on country code successful: %s", voice)
		return voice
	}

	util.LogWithLabel(msg.AircraftSnap.Registration, "voice selection did not find match for country code: %s", msg.CountryCode)

	// 2. TIER 2: Regional Fallback
	if len(msg.CountryCode) > 0 {
		regionCode := msg.CountryCode[:1] // e.g., 'K' for USA, 'E' for Europe
		util.LogWithLabel(msg.AircraftSnap.Registration, "voice selection falling back to region code: %s", regionCode)
		if voice := vm.findBestInPool(vm.regionVoicePools[regionCode], partnerVoice); voice != "" {
			util.LogWithLabel(msg.AircraftSnap.Registration, "voice selection on region code successful: %s", voice)
			return voice
		}
	}

	util.LogWithLabel(msg.AircraftSnap.Registration, "voice selection falling back to global voice pool")

	// 3. TIER 3: Global Fallback
	// Uses the pre-calculated pool to find ANY voice that isn't the partner.
	voice :=  vm.findBestInPool(vm.globalPool, partnerVoice)

	// If Global pool only had the partnerVoice, findBestInPool returned ""
    if voice == "" {
        util.LogWithLabel(msg.AircraftSnap.Registration, "WARN: voice pools are currently drained, reluctant reuse of partner voice")
        return vm.globalPool[0] 
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

func (vm *VoiceManager) getVoiceMetadata(name string, msg ATCMessage) (string, string, int, string) {
	path := filepath.Join(vm.voiceDir, name+".onnx")
	rate := 22050 // Default

	// Try to get sample rate from Piper JSON
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

	return name, path, rate, noiseType(msg.Role, msg.AircraftSnap.Flight.Phase.Current)
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
	go func(targetKey string) {
		// 15s is usually enough for the 'Engine Shutdown' audio to finish
		time.Sleep(15 * time.Second)

		vm.mu.Lock()
		defer vm.mu.Unlock()

		if _, exists := vm.sessions[targetKey]; exists {
			delete(vm.sessions, targetKey)
			log.Printf("VoiceManager: Successfully released %s\n", targetKey)
		}
	}(key)
}

func (vm *VoiceManager) startCleaner(interval time.Duration, getUserPos func() (float64, float64)) {
	ticker := time.NewTicker(interval)
	for range ticker.C {
		vm.mu.Lock()
		log.Printf("VoiceManager: Running cleanup, current sessions: %d", len(vm.sessions))
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
			log.Println("VoiceManager: Evicted", evicted, "stale sessions")
		}
		log.Printf("VoiceManager: Cleanup complete, current sessions: %d", len(vm.sessions))
		vm.mu.Unlock()
	}
}

func (vm *VoiceManager) isVoiceGloballyUsed(voiceName string) bool {
	for _, s := range vm.sessions {
		if s.VoiceName == voiceName {
			return true
		}
	}
	return false
}

func (vm *VoiceManager) getLastUsedTime(voiceName string) time.Time {
	var latest time.Time
	for _, s := range vm.sessions {
		if s.VoiceName == voiceName {
			if s.LastSeen.After(latest) {
				latest = s.LastSeen
			}
		}
	}
	// If never seen (shouldn't happen), return ancient time so it's picked first
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
