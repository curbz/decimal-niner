package atc

import (
	"testing"
)

func TestPhoneticEngine_Apply(t *testing.T) {
	// Mocking the layered structure:
	// Index 0: Base Language (General)
	// Index 1: Specific Locale (Override)
	pe := &PhoneticEngine{
		Dictionaries: []map[string]string{
			{
				"wien":   "Vienna",
				"boeing": "Bo-ing",
				"fl":     "Flight Level",
				"route":  "Rowt", // Standard US/General pronunciation
			},
			{
				"wien-schwechat": "Veen Shuv-ek-cat",
				"glostr":         "Gloucester",
				"route":          "Root", // Override for GB/Specific locale
			},
		},
	}

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Simple replacement from Base layer",
			input:    "Cleared to Wien via the arrival",
			expected: "Cleared to Vienna via the arrival",
		},
		{
			name:     "Punctuation preservation",
			input:    "Report passing Wien,",
			expected: "Report passing Vienna,",
		},
		{
			name:     "Hyphenated word replacement from Locale layer",
			input:    "Destination Wien-Schwechat",
			expected: "Destination Veen Shuv-ek-cat",
		},
		{
			name:     "Layered Override: Locale beats Base",
			// 'route' is in both, but we want the 'en-GB' version (Rowt)
			input:    "Follow the assigned route.",
			expected: "Follow the assigned Root.",
		},
		{
			name:     "Multiple replacements across both layers",
			input:    "The Boeing is on the GLOSTR SID",
			expected: "The Bo-ing is on the Gloucester SID",
		},
		{
			name:     "Negative test: No partial matches in longer words",
			input:    "The flower is blooming at FL300.",
			expected: "The flower is blooming at FL300.",
		},
		{
			name:     "Case insensitivity check",
			input:    "CLEARED TO WIEN",
			expected: "CLEARED TO Vienna",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := pe.Apply(tt.input)
			if actual != tt.expected {
				t.Errorf("%s: Got %q, want %q", tt.name, actual, tt.expected)
			}
		})
	}
}
