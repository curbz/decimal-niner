package atc

import (
	"fmt"
	"strings"
	"testing"
)

func BenchmarkPhoneticEngine_Apply(b *testing.B) {
	// 1. Setup Layered Dictionaries
	// Base layer with 1000 entries
	baseDict := make(map[string]string)
	for i := 0; i < 1000; i++ {
		baseDict[fmt.Sprintf("word%d", i)] = fmt.Sprintf("Replacement%d", i)
	}
	baseDict["wien"] = "Vienna"

	// Specific Locale layer with 50 overrides
	localeDict := make(map[string]string)
	for i := 0; i < 50; i++ {
		localeDict[fmt.Sprintf("localword%d", i)] = fmt.Sprintf("LocalReplacement%d", i)
	}
	// Add an override to force the engine to find it in the "top" layer
	localeDict["word10"] = "Custom-Replacement-10"

	pe := &PhoneticEngine{
		Dictionaries: []map[string]string{baseDict, localeDict},
	}

	// 2. Realistic ATC sentence
	input := "Lufthansa 123, cleared to WIEN via the standard arrival, maintain FL300 and report passing WORD10."

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = pe.Apply(input)
	}
}

func BenchmarkPhoneticEngine_LongText(b *testing.B) {
	// Simulate a 2-layer engine
	pe := &PhoneticEngine{
		Dictionaries: []map[string]string{
			{"wien": "Vienna"}, // Base
			{},                 // Empty locale to test fallback overhead
		},
	}

	// Simulate a very long ATIS broadcast (~100-120 words)
	longInput := strings.Repeat("This is a test sentence for WIEN. ", 20)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = pe.Apply(longInput)
	}
}
