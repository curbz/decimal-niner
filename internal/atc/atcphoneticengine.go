package atc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type PhoneticEngine struct {
    // Slice of maps: [0] is Language (en), [1] is Locale (en-GB)
    Dictionaries []map[string]string
}

func NewPhoneticEngine(baseDir string, fullFileName string) (*PhoneticEngine, error) {
    pe := &PhoneticEngine{}

    // 1. Identify the layers
    // Example: "en_GB-dictionary.json"
    // Base: "en-dictionary.json"
    
    var layers []string
    
    // Check if there is an underscore indicating a locale
    if strings.Contains(fullFileName, "_") {
        parts := strings.Split(fullFileName, "_")
        langCode := parts[0] // "en"
        
        // Find the suffix after the locale part (e.g., "-dictionary.json")
        // We look for the first dash or dot after the locale
        suffixIndex := strings.Index(fullFileName, "-")
        if suffixIndex == -1 {
            suffixIndex = strings.Index(fullFileName, ".")
        }
        
        if suffixIndex != -1 {
            baseFile := langCode + fullFileName[suffixIndex:]
            layers = append(layers, baseFile) // Layer 0: en-dictionary.json
        }
    }
    
    // Always add the requested file as the top layer
    layers = append(layers, fullFileName) // Layer 1: en_GB-dictionary.json

    // 2. Load layers in order
    for _, fileName := range layers {
        fullPath := filepath.Join(baseDir, fileName)
        
        dict, err := loadAndNormalize(fullPath)
        if err != nil {
            // It's okay if the base language file doesn't exist, 
            // but we should probably log it.
            continue
        }
        
        pe.Dictionaries = append(pe.Dictionaries, dict)
    }

    if len(pe.Dictionaries) == 0 {
        return nil, fmt.Errorf("could not load any dictionary layers from %s", fullFileName)
    }

    return pe, nil
}

func loadAndNormalize(path string) (map[string]string, error) {
    data, err := os.ReadFile(path)
    if err != nil {
        return nil, err
    }

    var rawDict map[string]string
    if err := json.Unmarshal(data, &rawDict); err != nil {
        return nil, err
    }

    normalized := make(map[string]string)
    for key, value := range rawDict {
        normalized[strings.ToLower(key)] = value
    }
    return normalized, nil
}

func (pe *PhoneticEngine) Apply(text string) string {
    words := strings.Fields(text)
    modified := false

    for i, word := range words {
        clean := strings.Trim(word, ".,!?;:\"()")
        lower := strings.ToLower(clean)

        // Specific (end of slice) to General (start of slice)
        for d := len(pe.Dictionaries) - 1; d >= 0; d-- {
            if replacement, ok := pe.Dictionaries[d][lower]; ok {
                // Your existing replacement logic
                words[i] = strings.Replace(word, clean, replacement, 1)
                modified = true
                break // Match found in specific layer, skip base layer
            }
        }
    }

    if !modified {
        return text
    }
    return strings.Join(words, " ")
}

