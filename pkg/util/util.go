package util

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"time"

	"github.com/curbz/decimal-niner/internal/logger"
	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
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

			// prevents terminating 0x00 from being added
			if start < len(rawBytes)-1 {
				decodedStrings = append(decodedStrings, s)
			}
			start = i + 1
		}
	}

	//Handle any remaining data (if it doesn't end with \x00)
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
	logger.Log.Printf("-> Sending: %s", string(msg))
	if err != nil {
		logger.Log.Fatalf("Error marshaling JSON: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
		logger.Log.Fatalf("Error writing message: %v", err)
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

	logger.Log.Printf("Configuration loaded from %s", filepath)

	return &config, nil
}

// PickRandomFromMap returns a random key from the given map
func PickRandomFromMap[K comparable, V any](m map[K]V) (randomKey any) {

	if len(m) == 0 {
		return nil
	}

	// Create a slice of keys (maps still require conversion for indexing)
	keys := make([]any, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	// Use rand.IntN from v2 (automatically seeded)
	randomIndex := rand.Intn(len(keys))
	randomKey = keys[randomIndex]

	return randomKey
}

func ParseHour(timeStr string) int {

	if len(timeStr) < 2 {
		return 0
	}
	hourStr := timeStr[:2]
	hour, err := strconv.Atoi(hourStr)
	if err != nil {
		return 0
	}
	return hour
}

func ParseMinute(timeStr string) int {
	if len(timeStr) < 4 {
		return 0
	}
	minuteStr := timeStr[2:4]
	minute, err := strconv.Atoi(minuteStr)
	if err != nil {
		return 0
	}
	return minute
}

// GetISOWeekday returns an int where Monday=0...Sunday=6
func GetISOWeekday(t time.Time) int {
	return (int(t.Weekday()) + 6) % 7
}

// logs as debug 
func LogDebugWithLabel(label string, msg string, args ...interface{}) {
	LogWithLabelAndLevel(label, logrus.InfoLevel, msg, args...)
}

// logs as info 
func LogWithLabel(label string, msg string, args ...interface{}) {
	LogWithLabelAndLevel(label, logrus.InfoLevel, msg, args...)
}

// logs as warning
func LogWarnWithLabel(label string, msg string, args ...interface{}) {
	LogWithLabelAndLevel(label, logrus.WarnLevel, msg, args...)
}

// logs as error
func LogErrWithLabel(label string, msg string, args ...interface{}) {
	LogWithLabelAndLevel(label, logrus.ErrorLevel, msg, args...)
}

func LogWithLabelAndLevel(label string, level logrus.Level, msg string, args ...interface{}) {
    if label == "" {
        label = "------"
    }
	msg = fmt.Sprintf("[%s] %s", label, msg)
	logger.Log.Logf(level, msg, args...)
}

