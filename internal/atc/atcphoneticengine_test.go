package atc

import (
	"testing"
)

func TestPhoneticEngine_Apply(t *testing.T) {
    // IMPORTANT: Keys MUST be lowercase here to match the 
    // strings.ToLower() lookup in the Apply function.
    pe := &PhoneticEngine{
        Replacements: map[string]string{
            "wien":   "Vienna",
			"wien-schwechat": "Veen Shuv-ek-cat",
            "boeing": "Bo-ing",
            "glostr": "Gloucester",
            "fl":     "Flight Level",
        },
    }

    tests := []struct {
        name     string
        input    string
        expected string
    }{
        {
            name:     "Simple replacement",
            input:    "Cleared to Wien via the arrival",
            expected: "Cleared to Vienna via the arrival",
        },
        {
            name:     "Case insensitivity",
            input:    "cleared to wien",
            expected: "cleared to Vienna",
        },
        {
            name:     "Punctuation preservation",
            input:    "Report passing Wien,",
            expected: "Report passing Vienna,",
        },
        {
            name:     "Hyphenated word replacement",
            input:    "Destination Wien-Schwechat",
            expected: "Destination Veen Shuv-ek-cat",
        },
		{
            name:     "Multiple replacements",
            input:    "Cleared IFR to Wien via the GLOSTR SID",
            expected: "Cleared IFR to Vienna via the Gloucester SID",
        },
		{
			name:     "Negative test: No partial matches in longer words",
			input:    "The flower is blooming at FL300.",
			expected: "The flower is blooming at FL300.",
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