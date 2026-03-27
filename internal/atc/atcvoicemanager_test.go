package atc

import (
	"math/rand"
	"strings"
	"testing"
	"time"
)

// Helper to create a VoiceManager with mock data for testing
func setupMockVoiceManager() *VoiceManager {
	vm := &VoiceManager{
		sessions:          make(map[string]VoiceSession),
		countryVoicePools: make(map[string][]string),
		regionVoicePools:  make(map[string][]string),
		globalVoicePool:   []string{},
		rng:               rand.New(rand.NewSource(time.Now().UnixNano())),
	}

	// Mock Tier 1: Country Pools using the new VoiceKey format
	// Notice how "British_VCTK" now provides multiple distinct identities
	vm.countryVoicePools["GB"] = []string{"British_VCTK#0", "British_VCTK#1", "British_Solo#0"}
	vm.countryVoicePools["FR"] = []string{"French_Solo#0"}
	vm.countryVoicePools["US"] = []string{"American_Solo#0"}

	// Mock Tier 2: Region Pools
	vm.regionVoicePools["E"] = append(vm.countryVoicePools["GB"], vm.countryVoicePools["FR"]...)

	// Mock Tier 3: Global Pool
	vm.globalVoicePool = append(vm.regionVoicePools["E"], vm.countryVoicePools["US"]...)

	return vm
}

func TestResolveVoice(t *testing.T) {
	vm := setupMockVoiceManager()

	t.Run("Pilot Should Not Mimic Controller", func(t *testing.T) {
		vm.sessions = make(map[string]VoiceSession)

		msgATC := &ATCMessage{
			AircraftSnap: &Aircraft{Registration: "G-TEST1", Flight: Flight{Comms: Comms{Callsign: "EZY123"}}},
			Role:         "TOWER", ControllerICAO: "EGKK", CountryCode: "EG",
		}

		// Capture both the name AND the speaker ID
		atcName, _, _, _, atcID := vm.resolveVoice(msgATC)

		msgPilot := &ATCMessage{
			AircraftSnap: &Aircraft{Registration: "G-TEST1", Flight: Flight{Comms: Comms{Callsign: "EZY123"}}},
			Role:         "PILOT", ControllerICAO: "EGKK", CountryCode: "EG",
		}
		pilotName, _, _, _, pilotID := vm.resolveVoice(msgPilot)

		// The failure condition is: Same File AND Same Speaker ID
		if atcName == pilotName && atcID == pilotID {
			t.Errorf("CRITICAL FAIL: Pilot mimicked Controller (File: %s, ID: %s)", pilotName, pilotID)
		}

		t.Logf("Passed: Controller is %s#%s, Pilot is %s#%s", atcName, atcID, pilotName, pilotID)
	})

	t.Run("Twin Rule: Reallocate Country Voice Over Regional Fallback", func(t *testing.T) {
		vm.sessions = make(map[string]VoiceSession)

		// Fill up specific British voices
		vm.sessions["OTHER1_PILOT"] = VoiceSession{VoiceKey: "British_VCTK#0", LastSeen: time.Now().Add(-5 * time.Minute)}
		vm.sessions["OTHER2_PILOT"] = VoiceSession{VoiceKey: "British_VCTK#1", LastSeen: time.Now().Add(-1 * time.Minute)}

		msgTwin := &ATCMessage{
			AircraftSnap: &Aircraft{Registration: "G-TWIN", Flight: Flight{Comms: Comms{Callsign: "TWN1"}}},
			Role:         "PILOT", CountryCode: "EG",
		}

		// resolveVoice returns (name, path, rate, noise, speakerID)
		voiceName, _, _, _, _ := vm.resolveVoice(msgTwin)

		// FIX: The first return value of resolveVoice is the baseName (filename)
		// because that's what PrepSpeech uses for logs and locks.
		if voiceName != "British_Solo" {
			t.Errorf("Expected unused British_Solo filename, got %s", voiceName)
		}
	})
}

func TestResolveVoiceLocaleHierarchy(t *testing.T) {
	vm := setupMockVoiceManager()

	t.Run("Exact Country Match (Tier 1)", func(t *testing.T) {
		msg := &ATCMessage{
			AircraftSnap: &Aircraft{Registration: "G-TEST3", Flight: Flight{Comms: Comms{Callsign: "BAW1"}}},
			CountryCode:  "EG", Role: "PILOT",
		}
		voiceName, _, _, _, _ := vm.resolveVoice(msg)

		if !strings.HasPrefix(voiceName, "British") {
			t.Errorf("Expected a British filename for EG, got %s", voiceName)
		}
	})

	t.Run("Region Fallback Match (Tier 2)", func(t *testing.T) {
		// Fix: Re-inserted the missing message setup
		msg := &ATCMessage{
			AircraftSnap: &Aircraft{Registration: "D-AIXA", Flight: Flight{Comms: Comms{Callsign: "DLH1"}}},
			CountryCode:  "ED", // Germany -> Fallback to Region E
			Role:         "PILOT",
		}

		voiceName, _, _, _, _ := vm.resolveVoice(msg)

		expectedPool := vm.regionVoicePools["E"]
		found := false
		for _, poolKey := range expectedPool {
			if strings.HasPrefix(poolKey, voiceName+"#") || poolKey == voiceName {
				found = true
				break
			}
		}

		if !found {
			t.Errorf("Expected voice from Region E pool, got %s", voiceName)
		}
	})

	t.Run("Global Fallback (Tier 3)", func(t *testing.T) {
		msg := &ATCMessage{
			AircraftSnap: &Aircraft{Registration: "UFO-1", Flight: Flight{Comms: Comms{Callsign: "UFO1"}}},
			CountryCode:  "ZZ", // Unknown -> Fallback to Global
			Role:         "PILOT",
		}

		voiceName, _, _, _, _ := vm.resolveVoice(msg)

		if voiceName == "" {
			t.Fatal("Expected a voice for global fallback, got empty string")
		}

		// Ensure the voice returned exists in our global pool (stripping IDs for comparison)
		found := false
		for _, poolKey := range vm.globalVoicePool {
			if strings.HasPrefix(poolKey, voiceName+"#") || poolKey == voiceName {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Voice %s not found in Global Pool", voiceName)
		}
	})
}

func TestMultiSpeakerFileSharing(t *testing.T) {
	vm := setupMockVoiceManager()
	vm.sessions = make(map[string]VoiceSession)

	// Standardize the name to "vctk" in both places
	vm.countryVoicePools["GB"] = []string{"vctk#0", "vctk#1"}
	vm.globalVoicePool = []string{"vctk#0", "vctk#1"}

	t.Run("Pilot and ATC share file but different IDs", func(t *testing.T) {
		msg := &ATCMessage{
			ControllerICAO: "EGLL",
			Role:           "TOWER",
			CountryCode:    "GB", // This will resolve to GB
			AircraftSnap:   &Aircraft{Registration: "G-A", Flight: Flight{Comms: Comms{Callsign: "EZY1"}}},
		}

		// 1. Resolve for ATC (Will likely hit Tier 1: GB)
		atcName, _, _, _, atcID := vm.resolveVoice(msg)

		// 2. Resolve for Pilot (Will likely fall back to Tier 3: Global)
		msg.Role = "PILOT"
		pilotName, _, _, _, pilotID := vm.resolveVoice(msg)

		if atcName == "" || pilotName == "" {
			t.Fatal("Voices failed to resolve")
		}

		// Now they will both be "vctk"
		if atcName != pilotName {
			t.Errorf("Failure: Expected same base file, got %s and %s", atcName, pilotName)
		}

		// This is the core logic check: IDs must be different (0 and 1)
		if atcID == pilotID {
			t.Errorf("Failure: Assigned identical Speaker ID %s to both", atcID)
		}

		t.Logf("Success: ATC used %s#%s, Pilot used %s#%s", atcName, atcID, pilotName, pilotID)
	})
}

func TestReleaseSession(t *testing.T) {
	vm := setupMockVoiceManager()

	t.Run("Graceful Release After Cooldown", func(t *testing.T) {
		aircraft := &Aircraft{
			Registration: "G-BYE",
			Flight: Flight{
				Comms: Comms{Callsign: "BYE123"},
			},
		}

		// Fix: Use VoiceKey instead of VoiceName
		vm.mu.Lock()
		vm.sessions["BYE123_PILOT"] = VoiceSession{
			VoiceKey: "British_VCTK#0",
			Type:     SessionTypePilot,
			LastSeen: time.Now(),
		}
		vm.mu.Unlock()

		vm.ReleaseSession(aircraft)

		// Session should still exist immediately because of the 15s time.Sleep
		// in the ReleaseSession goroutine.
		vm.mu.RLock()
		_, exists := vm.sessions["BYE123_PILOT"]
		vm.mu.RUnlock()

		if !exists {
			t.Error("CRITICAL FAIL: Session deleted too quickly; 15s cooldown not respected")
		}
	})
}

func TestVoiceCollisionAvoidance(t *testing.T) {
	vm := setupMockVoiceManager()

	// THE TRICK: Put the German voices in the GLOBAL pool for this test.
	// If country resolution fails (which it is), both will fall back here.
	vm.globalVoicePool = []string{"Hans#0", "Dieter#0"}
	vm.sessions = make(map[string]VoiceSession)

	t.Run("Pilot and ATC must never share a voice", func(t *testing.T) {
		// 1. Controller - Will likely find DE or fall back to Global
		msgATC := &ATCMessage{
			ControllerICAO: "EDDF",
			Role:           "TOWER",
			CountryCode:    "DE",
			AircraftSnap: &Aircraft{
				Registration: "D-AIXA",
				Flight:       Flight{Comms: Comms{Callsign: "DLH1"}},
			},
		}
		atcName, _, _, _, _ := vm.resolveVoice(msgATC)

		// 2. Pilot - Will fail country resolution and fall back to Global
		msgPilot := &ATCMessage{
			ControllerICAO: "EDDF", // CRITICAL: Same ICAO context
			Role:           "PILOT",
			AircraftSnap: &Aircraft{
				Registration: "D-AIXA",
				Flight:       Flight{Comms: Comms{Callsign: "DLH1"}},
			},
		}
		pilotName, _, _, _, _ := vm.resolveVoice(msgPilot)

		// ASSERTIONS
		if atcName == "" || pilotName == "" {
			t.Fatal("Voices failed to resolve")
		}

		// Now they are BOTH forced to pick from {"Hans", "Dieter"}
		if atcName == pilotName {
			t.Errorf("COLLISION FAIL: Both assigned '%s' from Global Pool.", atcName)
		}

		t.Logf("Success: ATC [%s] vs Pilot [%s] (Collision Avoided via Global Fallback)", atcName, pilotName)
	})
}

func TestConvertIcaoToIso(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"full ICAO EGLL", "EGLL", "GB", false},
		{"2-letter prefix EG", "EG", "GB", false},
		{"1-letter prefix KJFK", "KJFK", "US", false},
		{"empty", "", "", true},
		{"not found", "-", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := convertIcaoToIso(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("convertIcaoToIso(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("convertIcaoToIso(%q) = %q; want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestInferCommsCountryCode(t *testing.T) {
	tests := []struct {
		name        string
		class       PhaseClass
		origin      string
		dest        string
		defaultCode string
		want        string
	}{
		{"departing uses origin", Departing, "KJFK", "EGLL", "XX", "KJ"},
		{"arriving uses destination", Arriving, "KJFK", "EGLL", "ZZ", "EG"},
		{"fallback to default when short origin", Departing, "US", "EGLL", "DF", "DF"},
		{"non depart/arrive uses default", Cruising, "KJFK", "EGLL", "AA", "AA"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ac := &Aircraft{Flight: Flight{Origin: tt.origin, Destination: tt.dest, Phase: Phase{Class: tt.class}, Comms: Comms{}}}
			inferCommsCountryCode(ac, tt.defaultCode)
			if ac.Flight.Comms.CountryCode != tt.want {
				t.Fatalf("inferCommsCountryCode -> country = %q; want %q", ac.Flight.Comms.CountryCode, tt.want)
			}
		})
	}
}

func TestValidatePhrase(t *testing.T) {
	tests := []struct {
		name    string
		phrase  string
		wantErr bool
		errSub  string
	}{
		{
			name:    "Valid Simple Tags",
			phrase:  "{$CALLSIGN}, ready at {@PARKING}.",
			wantErr: false,
		},
		{
			name:    "Valid Space-Separated Logic",
			phrase:  "{WHEN $SPEED GT 250 SAY `Slow down` OTHERWISE SAY `Proceed`}",
			wantErr: false,
		},
		{
			name:    "Valid Nested Logic",
			phrase:  "{WHEN $IS_NA SAY {WHEN $SPEED GT 250 SAY `Slow`} OTHERWISE SAY `Normal`}",
			wantErr: false,
		},
		{
			name:    "Valid Complex Nested in OTHERWISE",
			phrase:  "{WHEN $IS_NA SAY `Altimeter` OTHERWISE SAY {WHEN $METRIC SAY `hPa` OTHERWISE SAY `mb` }}",
			wantErr: false,
		},
		{
			name:    "Invalid: Unclosed Brace",
			phrase:  "Hello {$CALLSIGN",
			wantErr: true,
			errSub:  "unclosed opening brace",
		},
		{
			name:    "Invalid: Unknown Tag",
			phrase:  "Report at {FAKE_TAG}.",
			wantErr: true,
			errSub:  "unknown PCL tag",
		},
		{
			name:    "Invalid Logic: Missing SAY",
			phrase:  "{WHEN $TRUE `missing say keyword`}",
			wantErr: true,
			errSub:  "missing mandatory SAY",
		},
		{
			name:    "Invalid Logic: Empty Condition",
			phrase:  "{WHEN SAY `nothing`}",
			wantErr: true,
			errSub:  "condition is empty",
		},
		{
			name: "Invalid Logic: Nested Error Recovery",
			// Parent is valid, but child is missing SAY
			phrase:  "{WHEN $TRUE SAY {WHEN $FALSE `broken child`} OTHERWISE SAY `ok`}",
			wantErr: true,
			errSub:  "missing mandatory SAY",
		},
		{
			name:    "Invalid Logic: Improper OTHERWISE",
			phrase:  "{WHEN $TRUE SAY `hi` OTHERWISE `no say`}",
			wantErr: true,
			errSub:  "missing follow-up SAY",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePhrase(tt.phrase)
			if (err != nil) != tt.wantErr {
				t.Errorf("validatePhrase() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil && tt.errSub != "" && !strings.Contains(err.Error(), tt.errSub) {
				t.Errorf("validatePhrase() error = %v, must contain %q", err, tt.errSub)
			}
		})
	}
}
