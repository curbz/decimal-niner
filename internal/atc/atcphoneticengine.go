package atc

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type PhoneticEngine struct {
	Replacements map[string]string
}

func NewPhoneticEngine(path string) (*PhoneticEngine, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("error reading phonetic dictionary: %w", err)
	}

	var dict map[string]string
	if err := json.Unmarshal(data, &dict); err != nil {
		return nil,fmt.Errorf("error: malformed dictionary JSON: %w", err)
	}

	// create a new map for normalized keys
    normalized := make(map[string]string)
    for key, value := range dict {
        normalized[strings.ToLower(key)] = value
    }

    return &PhoneticEngine{Replacements: normalized}, nil
}

func (pe *PhoneticEngine) Apply(text string) string {
    words := strings.Fields(text)
    modified := false

    for i, word := range words {
        // 1. Clean punctuation: "Wien," -> "Wien"
        clean := strings.Trim(word, ".,!?;:\"()")
        
        // 2. Lookup using LOWERCASE: "Wien" -> "wien"
        // This ensures O(1) speed regardless of dictionary size
        if replacement, ok := pe.Replacements[strings.ToLower(clean)]; ok {
            
            // 3. Replace the EXACT 'clean' string found in the original 'word'
            // This preserves the comma/period/casing of the original sentence
            words[i] = strings.Replace(word, clean, replacement, 1)
            modified = true
        }
    }

    if !modified {
        return text
    } 
    return strings.Join(words, " ")
}

