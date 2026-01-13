package util

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/gorilla/websocket"
	"gopkg.in/yaml.v3"
)

// DecodeNullTerminatedString decodes the base64 string and splits the resulting
// binary data into a slice of strings using the null byte (\x00) as a delimiter.
func DecodeNullTerminatedString(encodedData string) ([]string, error) {
	// 1. Base64 Decode
	rawBytes, err := base64.StdEncoding.DecodeString(encodedData)
	if err != nil {
		return nil, fmt.Errorf("error decoding base64: %w", err)
	}

	var decodedStrings []string
	start := 0

	for i, b := range rawBytes {
		if b == 0x00 {
			// Extract the string
			s := string(rawBytes[start:i])

			// FIX: Only append if the string is NOT empty.
			// This prevents adding empty elements caused by double nulls
			// (\x00\x00) or trailing padding at the end of the buffer.
			if len(s) > 0 {
				decodedStrings = append(decodedStrings, s)
			}

			start = i + 1
		}
	}

	// Handle any remaining data (if it doesn't end with \x00)
	if start < len(rawBytes) {
		s := string(rawBytes[start:])
		if len(s) > 0 {
			decodedStrings = append(decodedStrings, s)
		}
	}

	return decodedStrings, nil
}

// DecodeRunway converts a uint32 packed ASCII value into a string.
func DecodeUint32(val uint32) string {
	// Extract bytes using bit shifting and masking
	char1 := byte(val & 0xFF)         // Rightmost byte
	char2 := byte((val >> 8) & 0xFF)  // Second byte
	char3 := byte((val >> 16) & 0xFF) // Third byte

	// Return as a concatenated string
	return string([]byte{char1, char2, char3})
}

// SendJSON is a utility function for the WebSocket connection (not used for REST).
func SendJSON(conn *websocket.Conn, data interface{}) {
	msg, err := json.Marshal(data)
	log.Printf("-> Sending: %s", string(msg))
	if err != nil {
		log.Fatalf("Error marshaling JSON: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
		log.Fatalf("Error writing message: %v", err)
	}
}

// LoadConfig reads a YAML file and unmarshals it into a struct of type T.
func LoadConfig[T any](filepath string) (*T, error) {
	// 1. Read the file
	data, err := os.ReadFile(filepath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	// 2. Initialize an empty instance of T
	var config T

	// 3. Unmarshal the YAML data into the struct
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal yaml: %w", err)
	}

	return &config, nil
}


