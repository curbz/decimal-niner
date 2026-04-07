package atc

import (
	"testing"
)

func TestPhoneticEngine_SerialPipeline(t *testing.T) {
	// 1. Setup the Base Engine (e.g., "en")
	baseEngine := &PhoneticEngine{
		Dictionaries: map[string]string{
			"wien":   "Vienna",
			"boeing": "Bo-ing",
			"fl":     "Flight Level",
			"route":  "rowt", // Standard US/General
		},
	}

	// 2. Setup the Locale Engine (e.g., "en_GB")
	localeEngine := &PhoneticEngine{
		Dictionaries: map[string]string{
			"wien-schwechat": "Veen Shuv-ek-cat",
			"glostr":         "Gloucester",
			"rowt":           "root", // Country override
		},
	}

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Base replacement only",
			input:    "Cleared to Wien via the arrival",
			expected: "Cleared to Vienna via the arrival",
		},
		{
			name:     "Punctuation preservation",
			input:    "Report passing Wien,",
			expected: "Report passing Vienna,",
		},
		{
			name:     "Locale override beats Base",
			// Base turns 'route' to 'rowt', then Locale turns rowt' to 'root'
			// Note: this relies on the Locale having the post-processed word if the base changes it.
			input:    "Follow the assigned route.",
			expected: "Follow the assigned root.",
		},
		{
			name:     "Hyphenated word from Locale",
			input:    "Destination Wien-Schwechat",
			expected: "Destination Veen Shuv-ek-cat",
		},
		{
			name:     "Multiple replacements across both engines",
			input:    "The Boeing is on the GLOSTR SID",
			expected: "The Bo-ing is on the Gloucester SID",
		},
		{
			name:     "Case insensitivity check",
			input:    "CLEARED TO WIEN",
			expected: "CLEARED TO Vienna",
		},
		{
			name:     "Negative test: No partial matches",
			input:    "The flower is blooming at FL300.",
			expected: "The flower is blooming at FL300.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the VoiceManager pipeline: Base then Locale
			text := baseEngine.Apply(tt.input)
			actual := localeEngine.Apply(text)

			if actual != tt.expected {
				t.Errorf("%s: Got %q, want %q", tt.name, actual, tt.expected)
			}
		})
	}
}