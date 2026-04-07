package atc

import (
	"fmt"
	"strings"
	"testing"
)

func BenchmarkPhoneticEngine_Pipeline(b *testing.B) {
	// 1. Setup Base Engine (1000 entries)
	baseReplacements := make(map[string]string)
	for i := 0; i < 1000; i++ {
		baseReplacements[fmt.Sprintf("word%d", i)] = fmt.Sprintf("Replacement%d", i)
	}
	baseReplacements["wien"] = "Vienna"
	baseEngine := &PhoneticEngine{Dictionaries: baseReplacements}

	// 2. Setup Locale Engine (50 entries)
	localeReplacements := make(map[string]string)
	for i := 0; i < 50; i++ {
		localeReplacements[fmt.Sprintf("localword%d", i)] = fmt.Sprintf("LocalReplacement%d", i)
	}
	// Note: In serial mode, if base changes "word10" to "Replacement10", 
	// the locale must target "replacement10" to override it.
	localeReplacements["replacement10"] = "Custom-Override-10"
	localeEngine := &PhoneticEngine{Dictionaries: localeReplacements}

	// 3. Realistic ATC sentence
	input := "Lufthansa 123, cleared to WIEN via the standard arrival, maintain FL300 and report passing WORD10."

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		// Simulate the VoiceManager.PrepareSpeech pipeline
		text := baseEngine.Apply(input)
		_ = localeEngine.Apply(text)
	}
}

func BenchmarkPhoneticEngine_LongText_Pipeline(b *testing.B) {
	// Simulate a typical English setup
	baseEngine := &PhoneticEngine{Dictionaries: map[string]string{"wien": "Vienna"}}
	localeEngine := &PhoneticEngine{Dictionaries: map[string]string{}} // Empty regional override

	// Simulate a very long ATIS broadcast (~120 words)
	longInput := strings.Repeat("This is a test sentence for WIEN. ", 20)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		text := baseEngine.Apply(longInput)
		_ = localeEngine.Apply(text)
	}
}