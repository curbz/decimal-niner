package atc

import (
	"encoding/json"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/curbz/decimal-niner/pkg/geometry"
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
	sessions map[string]VoiceSession
	mu       sync.RWMutex
	voiceDir string
	rng      *rand.Rand
}

func NewVoiceManager(dir string) *VoiceManager {
	vm := &VoiceManager{
		sessions: make(map[string]VoiceSession),
		voiceDir: dir,
		// Using a local RNG to avoid global state contention
		rng: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	return vm
}

// ResolveVoice is the main entry point
func (vm *VoiceManager) ResolveVoice(msg ATCMessage) (string, string, int, string) {

	if msg.AircraftSnap == nil {
        return "", "", 0, ""
    }

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
	// TIER 1: Country Match
	isoCountry, err := convertIcaoToIso(msg.CountryCode)
	if err == nil {
		if voice := vm.findNonPartner(countryVoicePools[isoCountry], partnerVoice); voice != "" {
			return voice
		}
	}

	// TIER 2: Region Match
	if len(msg.CountryCode) > 0 {
		regionCode := msg.CountryCode[:1]
		if voice := vm.findNonPartner(regionVoicePools[regionCode], partnerVoice); voice != "" {
			return voice
		}
	}

	// TIER 3: Global Random Fallback (Guarantees no collision)
	allCountries := make([]string, 0, len(countryVoicePools))
	for k := range countryVoicePools {
		allCountries = append(allCountries, k)
	}
	vm.rng.Shuffle(len(allCountries), func(i, j int) { allCountries[i], allCountries[j] = allCountries[j], allCountries[i] })

	for _, k := range allCountries {
		if voice := vm.findNonPartner(countryVoicePools[k], partnerVoice); voice != "" {
			return voice
		}
	}

	return "" // Should be impossible if pools are > 2
}

func (vm *VoiceManager) findNonPartner(pool []string, partnerVoice string) string {
	if len(pool) == 0 {
		return ""
	}

	// Shuffle a copy to maintain randomness without mutating the global pool order
	shuffled := make([]string, len(pool))
	copy(shuffled, pool)
	vm.rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })

	var fallback string
	for _, v := range shuffled {
		if partnerVoice != "" && v == partnerVoice {
			continue
		}

		// Check global usage
		used := false
		for _, s := range vm.sessions {
			if s.VoiceName == v {
				used = true
				break
			}
		}

		if !used {
			return v
		}
		if fallback == "" {
			fallback = v
		}
	}
	return fallback
}

func (vm *VoiceManager) getVoiceMetadata(name string, msg ATCMessage) (string, string, int, string) {
	path := filepath.Join(vm.voiceDir, name+".onnx")
	rate := 22050 // Default

	// Try to get sample rate from Piper JSON
	if f, err := os.Open(path + ".json"); err == nil {
		var cfg struct {
			Audio struct{ SampleRate int `json:"sample_rate"` } `json:"audio"`
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
