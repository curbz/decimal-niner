package atc

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// PhoneticEngine handles the transformation of text into phonetic equivalents.
type PhoneticEngine struct {
	Dictionaries map[string]string
}

// NewPhoneticEngine loads a JSON dictionary and normalizes keys to lowercase.
func NewPhoneticEngine(path string) (*PhoneticEngine, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("error reading phonetic dictionary: %w", err)
	}

	var rawDict map[string]string
	if err := json.Unmarshal(data, &rawDict); err != nil {
		return nil, fmt.Errorf("error: malformed dictionary JSON: %w", err)
	}

	// Create a new map for normalized keys (lowercase)
	normalized := make(map[string]string)
	for key, value := range rawDict {
		normalized[strings.ToLower(key)] = value
	}

	return &PhoneticEngine{Dictionaries: normalized}, nil
}

// Apply performs a case-insensitive lookup of words and replaces them while
// preserving surrounding punctuation and the original string's relative position.
func (pe *PhoneticEngine) Apply(text string) string {
	words := strings.Fields(text)
	modified := false

	for i, word := range words {
		// 1. Clean punctuation: "Wien," -> "Wien"
		clean := strings.Trim(word, ".,!?;:\"()")
		lower := strings.ToLower(clean)

		// 2. Lookup using LOWERCASE
		if replacement, ok := pe.Dictionaries[lower]; ok {
			// 3. Replace the 'clean' part of the original 'word' 
			// to preserve the commas/periods.
			words[i] = strings.Replace(word, clean, replacement, 1)
			modified = true
		}
	}

	if !modified {
		return text
	}
	return strings.Join(words, " ")
}