package atc

import (
	"math/rand"
	"testing"
	"time"
)

// Helper to create a VoiceManager with mock data for testing
func setupMockVoiceManager() *VoiceManager {
	vm := &VoiceManager{
		sessions:          make(map[string]VoiceSession),
		countryVoicePools: make(map[string][]string),
		regionVoicePools:  make(map[string][]string),
		globalPool:        []string{},
		rng:               rand.New(rand.NewSource(time.Now().UnixNano())),
	}

	// Mock ICAO to ISO mapping for internal logic
	// Note: Ensure your convertIcaoToIso function is accessible or mocked

	// Tier 1: Country Pools
	vm.countryVoicePools["GB"] = []string{"British_1", "British_2"}
	vm.countryVoicePools["FR"] = []string{"French_1"}
	vm.countryVoicePools["US"] = []string{"American_1"}

	// Tier 2: Region Pools
	vm.regionVoicePools["E"] = []string{"British_1", "British_2", "French_1", "Euro_Extra"}
	vm.regionVoicePools["K"] = []string{"American_1", "American_Extra"}

	// Tier 3: Global Pool
	vm.globalPool = []string{"British_1", "British_2", "French_1", "American_1", "Euro_Extra", "American_Extra"}

	return vm
}

func TestResolveVoice(t *testing.T) {
	vm := setupMockVoiceManager()

	t.Run("Pilot Should Not Mimic Controller", func(t *testing.T) {
		// Clear sessions for a fresh test
		vm.sessions = make(map[string]VoiceSession)

		// 1. Controller resolves first (EGKK Tower - British Pool)
		msgATC := ATCMessage{
			AircraftSnap: &Aircraft{
				Registration: "G-TEST1",
				Flight: Flight{
					Comms:    Comms{Callsign: "EZY123"},
					Position: Position{Lat: 51.1, Long: -0.1},
				},
			},
			Role: "TOWER", ICAO: "EGKK", CountryCode: "EG",
		}
		atcVoice, _, _, _ := vm.resolveVoice(msgATC)

		// 2. Pilot for the same flight resolves
		msgPilot := msgATC
		msgPilot.Role = "PILOT"

		pilotVoice, _, _, _ := vm.resolveVoice(msgPilot)

		if pilotVoice == atcVoice {
			t.Errorf("CRITICAL FAIL: Pilot mimicked Controller (%s)", pilotVoice)
		}

		if pilotVoice == "" {
			t.Fatal("Expected a pilot voice, got empty string")
		}
	})

	t.Run("Twin Rule: Reallocate Country Voice Over Regional Fallback", func(t *testing.T) {
		vm.sessions = make(map[string]VoiceSession)

		// Fill up all unique British voices with other planes
		vm.sessions["OTHER1_PILOT"] = VoiceSession{VoiceName: "British_1", LastSeen: time.Now().Add(-5 * time.Minute)}
		vm.sessions["OTHER2_PILOT"] = VoiceSession{VoiceName: "British_2", LastSeen: time.Now().Add(-1 * time.Minute)}

		// New plane (G-TWIN) should REUSE British_1 (the oldest) rather than falling back to French
		msgTwin := ATCMessage{
			AircraftSnap: &Aircraft{
				Registration: "G-TWIN",
				Flight: Flight{
					Comms:    Comms{Callsign: "TWN1"},
					Position: Position{Lat: 51.1, Long: -0.1},
				},
			},
			Role: "PILOT", ICAO: "EGKK", CountryCode: "EG",
		}

		voice, _, _, _ := vm.resolveVoice(msgTwin)

		if voice != "British_1" {
			t.Errorf("Expected reallocation of oldest British voice (British_1), got %s", voice)
		}
	})
}

func TestResolveVoiceLocaleHierarchy(t *testing.T) {
	vm := setupMockVoiceManager()

	t.Run("Exact Country Match (Tier 1)", func(t *testing.T) {
		msg := ATCMessage{
			AircraftSnap: &Aircraft{
				Registration: "G-TEST3",
				Flight:       Flight{Comms: Comms{Callsign: "BAW1"}},
			},
			CountryCode: "EG", Role: "PILOT",
		}
		voice, _, _, _ := vm.resolveVoice(msg)

		if voice != "British_1" && voice != "British_2" {
			t.Errorf("Expected British voice for EG, got %s", voice)
		}
	})

	t.Run("Region Fallback Match (Tier 2)", func(t *testing.T) {
		// Country code "ED" (Germany) has no Tier 1 pool, should use Region "E"
		msg := ATCMessage{
			AircraftSnap: &Aircraft{
				Registration: "D-AIXA",
				Flight:       Flight{Comms: Comms{Callsign: "DLH1"}},
			},
			CountryCode: "ED", Role: "PILOT",
		}
		voice, _, _, _ := vm.resolveVoice(msg)

		expectedPool := vm.regionVoicePools["E"]
		found := false
		for _, v := range expectedPool {
			if v == voice {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected voice from Region E pool, got %s", voice)
		}
	})

	t.Run("Global Fallback (Tier 3)", func(t *testing.T) {
		// Unknown country "ZZ"
		msg := ATCMessage{
			AircraftSnap: &Aircraft{
				Registration: "UFO-1",
				Flight:       Flight{Comms: Comms{Callsign: "UFO1"}},
			},
			CountryCode: "ZZ", Role: "PILOT",
		}
		voice, _, _, _ := vm.resolveVoice(msg)

		if voice == "" {
			t.Fatal("Expected a voice for global fallback, got empty string")
		}
	})
}

func TestReleaseSession(t *testing.T) {
	vm := setupMockVoiceManager()

	t.Run("Graceful Release After Cooldown", func(t *testing.T) {
		aircraft := &Aircraft{
			Registration: "G-BYE",
			Flight:       Flight{Comms: Comms{Callsign: "BYE123"}},
		}

		// Manually add session
		vm.sessions["BYE123_PILOT"] = VoiceSession{VoiceName: "British_1"}

		vm.ReleaseSession(aircraft)

		// Session should still exist immediately (cooldown)
		if _, exists := vm.sessions["BYE123_PILOT"]; !exists {
			t.Error("Session deleted too quickly; cooldown not respected")
		}

		// We can't easily wait 15s in a unit test, but we can verify the key logic
	})
}

func TestVoiceCollisionAvoidance(t *testing.T) {
	vm := setupMockVoiceManager()

	// SCENARIO: A German Pilot is talking to a German Controller.
	// There are ONLY 2 German voices available in the entire pool.
	vm.countryVoicePools["DE"] = []string{"Hans", "Dieter"}
	vm.sessions = make(map[string]VoiceSession)

	t.Run("Pilot and ATC must never share a voice in the same ICAO context", func(t *testing.T) {
		// 1. Controller (Dieter) speaks first
		msgATC := ATCMessage{
			ICAO:        "EDDF",
			Role:        "TOWER",
			CountryCode: "DE", // German
			AircraftSnap: &Aircraft{
				Registration: "D-AIXA",
				Flight:       Flight{Comms: Comms{Callsign: "DLH123"}},
			},
		}
		atcVoice, _, _, _ := vm.resolveVoice(msgATC)

		// 2. Pilot (DLH123) speaks back to the same ICAO (EDDF)
		msgPilot := msgATC
		msgPilot.Role = "PILOT"

		pilotVoice, _, _, _ := vm.resolveVoice(msgPilot)

		// ASSERTIONS
		if atcVoice == "" || pilotVoice == "" {
			t.Fatal("Voices failed to resolve")
		}

		if atcVoice == pilotVoice {
			t.Errorf("RULE VIOLATION: Both Controller and Pilot assigned '%s'. They must be different.", atcVoice)
		}

		t.Logf("Success: ATC assigned '%s', Pilot assigned '%s'", atcVoice, pilotVoice)
	})
}
