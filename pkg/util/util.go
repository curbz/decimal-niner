package util

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"

	"github.com/gorilla/websocket"
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

// DecodeUint32 decodes a uint32 value into a string by interpreting its bytes. Useful for decoding runway identifiers.
func DecodeUint32(val uint32) {
	fmt.Printf("Int: %d -> String: \"", val)

	// Extract 4 bytes in Little Endian order (Low byte first)
	// This simulates the behavior of reinterpret_cast<char*> on a standard PC
	bytes := []byte{
		byte(val & 0xFF),         // Byte 0
		byte((val >> 8) & 0xFF),  // Byte 1
		byte((val >> 16) & 0xFF), // Byte 2
		byte((val >> 24) & 0xFF), // Byte 3
	}

	for _, b := range bytes {
		if b == 0 {
			break // Stop at null terminator
		}

		// Check if the byte is a printable ASCII character
		if b >= 32 && b <= 126 {
			fmt.Printf("%c", b)
		} else {
			// Print non-printable bytes as Hex [xNN]
			fmt.Printf("[x%x]", b)
		}
	}
	fmt.Printf("\"\n")
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
