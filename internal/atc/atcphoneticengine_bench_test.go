package atc

import (
	"fmt"
	"strings"
	"testing"
)

func BenchmarkPhoneticEngine_Apply(b *testing.B) {
	// 1. Setup a large dictionary size of 1000 entries
	dict := make(map[string]string)
	for i := 0; i < 1000; i++ {
		dict[fmt.Sprintf("WORD%d", i)] = fmt.Sprintf("REPLACEMENT%d", i)
	}
	// Add our specific test case
	dict["WIEN"] = "VIENNA"
	
	pe := &PhoneticEngine{Replacements: dict}

	// 2. Create a realistic ATC sentence
	input := "Lufthansa 123, cleared to WIEN via the standard arrival, maintain FL300 and report passing WORD10."

	// 3. Reset timer to exclude setup time
	b.ResetTimer()
	b.ReportAllocs() // Tracks memory pressure

	for i := 0; i < b.N; i++ {
		_ = pe.Apply(input)
	}
}

func BenchmarkPhoneticEngine_LongText(b *testing.B) {
	pe := &PhoneticEngine{
		Replacements: map[string]string{"WIEN": "VIENNA"},
	}
	
	// Simulate a very long ATIS broadcast or complex clearance (~100 words)
	longInput := strings.Repeat("This is a test sentence for WIEN. ", 20)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = pe.Apply(longInput)
	}
}