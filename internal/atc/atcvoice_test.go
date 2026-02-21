package atc

import (
	"testing"
)

func setupLocaleMockPools() {
    countryVoicePools = make(map[string][]string)
    regionVoicePools = make(map[string][]string)
    icaoToIsoMap = map[string]string{
        "EG": "GB", // UK
        "LF": "FR", // France
        "K":  "US", // USA
    }
    
    // Tier 1: Country Pools
    countryVoicePools["GB"] = []string{"British_1", "British_2"}
    countryVoicePools["FR"] = []string{"French_1"}
    
    // Tier 2: Region Pools (Region "K" for North America, "E" for Northern Europe)
    regionVoicePools["K"] = []string{"American_Region_1", "American_Region_2"}
    regionVoicePools["E"] = []string{"Euro_Region_1"}
}

func TestResolveVoiceAirtight(t *testing.T) {
    setupLocaleMockPools()
    voiceDir := "./test_voices"

    // TEST 1: The "Pilot Blindspot"
    t.Run("Pilot Should Not Mimic Controller", func(t *testing.T) {
        sessionVoices = make(map[string]string)
        
        // 1. Controller gets assigned first
        msgATC := ATCMessage{
            Callsign: "EZY123", Role: "TOWER", ICAO: "EGKK", CountryCode: "EG",
        }
        atcVoice, _, _, _ := resolveVoice(msgATC, voiceDir)

        // 2. Pilot for the same flight resolves
        msgPilot := ATCMessage{
            Callsign: "EZY123", Role: "PILOT", ICAO: "EGKK", CountryCode: "EG",
        }
        pilotVoice, _, _, _ := resolveVoice(msgPilot, voiceDir)

        if pilotVoice == atcVoice {
            t.Errorf("CRITICAL FAIL: Pilot mimicked Controller (%s). Symmetry check failed.", pilotVoice)
        }
    })

    // TEST 2: The "Pool Exhaustion" Trap
    t.Run("Pool Exhaustion Safety", func(t *testing.T) {
        sessionVoices = make(map[string]string)
        
        // Populate global session so all voices in pool are "used"
        sessionVoices["PLANE_A_PILOT"] = "Voice_Alpha"
        sessionVoices["PLANE_B_PILOT"] = "Voice_Beta"

        // New conversation: Pilot gets Voice_Alpha (reused duplicate)
        msgP := ATCMessage{Callsign: "NEW1", Role: "PILOT", ICAO: "EGSS", CountryCode: "EG"}
        pVoice, _, _, _ := resolveVoice(msgP, voiceDir)

        // Controller for NEW1 MUST NOT be Voice_Alpha, even though Voice_Beta is also used.
        msgC := ATCMessage{Callsign: "NEW1", Role: "TOWER", ICAO: "EGSS", CountryCode: "EG"}
        cVoice, _, _, _ := resolveVoice(msgC, voiceDir)

        if pVoice == cVoice {
            t.Errorf("FAIL: Exhausted pool caused duplicate within conversation (%s)", pVoice)
        }
    })
}

func TestResolveVoiceLocaleHierarchy(t *testing.T) {
    setupLocaleMockPools()
    voiceDir := "./test_voices"

    // Country Match (Tier 1)
    t.Run("Exact Country Match", func(t *testing.T) {
        msg := ATCMessage{CountryCode: "EG", Callsign: "BAW1", Role: "PILOT"}
        voice, _, _, _ := resolveVoice(msg, voiceDir)
        
        // Should be British
        if voice != "British_1" && voice != "British_2" {
            t.Errorf("Expected British voice for EG (GB), got %s", voice)
        }
    })

    // Region Fallback (Tier 2)
    // Scenario: We have no specific pool for "K" (USA) country code, 
    // but we have a "K" Region pool.
    t.Run("Region Fallback Match", func(t *testing.T) {
        // "K" is the country code for USA. icaoToIsoMap has it, but countryVoicePools does NOT.
        msg := ATCMessage{CountryCode: "K", Callsign: "AAL1", Role: "PILOT"}
        voice, _, _, _ := resolveVoice(msg, voiceDir)
        
        if voice != "American_Region_1" && voice != "American_Region_2" {
            t.Errorf("Expected American Region voice for Country K fallback, got %s", voice)
        }
    })

    // Total Fallback (Tier 3)
    // Scenario: An ICAO code we've never heard of (e.g., "ZZ"). 
    // Should pick a random country pool.
    t.Run("Total Fallback to Random", func(t *testing.T) {
        msg := ATCMessage{CountryCode: "ZZ", Callsign: "UFO1", Role: "PILOT"}
        voice, _, _, _ := resolveVoice(msg, voiceDir)
        
        if voice == "" {
            t.Error("Expected a random voice for unknown country, got empty string")
        }
        
        // Verify it picked from one of our existing pools
        found := false
        allPossibleVoices := []string{"British_1", "British_2", "French_1", "American_Region_1", "American_Region_2", "Euro_Region_1"}
        for _, v := range allPossibleVoices {
            if voice == v {
                found = true
                break
            }
        }
        if !found {
            t.Errorf("Random fallback picked a voice outside of known pools: %s", voice)
        }
    })
}
