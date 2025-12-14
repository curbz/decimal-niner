package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/signal"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// --- Configuration ---
const (
	XPlaneWSURL      = "ws://127.0.0.1:8086/api/v2" 
	
	// Datarefs for monitoring
	ContinuousDataref = "sim/flightmodel/forces/indicated_airspeed"
	EventDataref      = "sim/cockpit2/radios/actuators/com1_standby_frequency_hz"
	
	// Frequencies (in Hz)
	ContinuousFreq = 10.0 
	EventFreq      = 1.0  
)

// Atomic counter to generate unique, sequential request IDs (req_id)
var requestCounter atomic.Int64 

// --- Data Structures for Official JSON RPC Protocol (OUTGOING) ---

// APIRequest is the top-level structure for all outgoing requests.
type APIRequest struct {
	RequestID int64       `json:"req_id"`
	Type      string      `json:"type"` // e.g., "sub_dataref", "sub_command"
	Params    interface{} `json:"params"`
}

// SubDatarefParams defines the structure for subscribing to Datarefs (used in "params").
type SubDatarefParams struct {
	Dataref   string  `json:"dataref"`
	Frequency float64 `json:"frequency"`
}

// SubCommandParams defines the structure for subscribing to Command feedback (used in "params").
// This is empty, as "sub_command" does not require specific parameters.
type SubCommandParams struct{}

// --- Data Structures for Official JSON RPC Protocol (INCOMING) ---

// APIResponse is the top-level structure for all incoming messages.
type APIResponse struct {
	RequestID int64             `json:"req_id"` 
	Type      string            `json:"type"` // e.g., "dataref_update", "command_update", "error"
	Payload   json.RawMessage `json:"payload"` // Use RawMessage to defer parsing
}

// ErrorPayload is used if Type is "error".
type ErrorPayload struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// DatarefUpdatePayload is used when Type is "dataref_update".
type DatarefUpdatePayload struct {
	Value float64 `json:"value"`
	Dataref string `json:"dataref"`
}

// CommandUpdatePayload is used when Type is "command_update".
type CommandUpdatePayload struct {
	CommandName string `json:"command_name"`
	Status      string `json:"status"` // e.g., "executed"
}


// --- Main Application ---

func main() {
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)

	// 1. Establish connection
	u, _ := url.Parse(XPlaneWSURL)
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		log.Fatalf("\nFATAL CONNECTION ERROR: Could not connect to X-Plane at %s.\n"+
			"Please ensure X-Plane 12 is running and the Web API is listening on TCP port 8086.\nError: %v", XPlaneWSURL, err)
	}
	defer conn.Close()
    log.Printf("Successfully connected to X-Plane Web API at %s.", XPlaneWSURL)

	done := make(chan struct{})

	// 2. Start listener
	go func() {
		defer close(done)
		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
					log.Println("Connection closed.")
					return
				}
				log.Println("Fatal read error:", err)
				return
			}
			processMessage(message)
		}
	}()

	// 3. Send subscription requests
	log.Println("--- Sending Subscription Requests ---")
	sendDatarefSubscription(conn, ContinuousDataref, ContinuousFreq)
	sendDatarefSubscription(conn, EventDataref, EventFreq)
	sendCommandSubscription(conn)

	// 4. Block until interrupt signal
	<-interrupt
	log.Println("\nInterrupt received. Closing connection...")

	err = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	if err != nil {
		log.Println("Write close error:", err)
	}
	<-time.After(time.Second) 
}

// --- Subscription Functions ---

// sendDatarefSubscription sends a request to subscribe to a dataref.
func sendDatarefSubscription(conn *websocket.Conn, dataref string, freq float64) {
	reqID := requestCounter.Add(1)
	params := SubDatarefParams{
		Dataref:   dataref,
		Frequency: freq,
	}

	request := APIRequest{
		RequestID: reqID,
		Type:      "sub_dataref",
		Params:    params,
	}

	sendJSON(conn, request)
	log.Printf("-> Sent Request ID %d: Subscribing to %s (%.0f Hz)", reqID, dataref, freq)
}

// sendCommandSubscription sends a request to subscribe to all command execution events.
func sendCommandSubscription(conn *websocket.Conn) {
	reqID := requestCounter.Add(1)
	params := SubCommandParams{}

	request := APIRequest{
		RequestID: reqID,
		Type:      "sub_command",
		Params:    params,
	}

	sendJSON(conn, request)
	log.Printf("-> Sent Request ID %d: Subscribing to Command Events", reqID)
}

// sendJSON is a utility function to marshal and write the JSON message.
func sendJSON(conn *websocket.Conn, data interface{}) {
	msg, err := json.Marshal(data)
	if err != nil {
		log.Fatalf("Error marshaling JSON: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
		log.Fatalf("Error writing message: %v", err)
	}
}

// --- Message Processing ---

// processMessage handles and dispatches the incoming JSON data from X-Plane.
func processMessage(message []byte) {
	var response APIResponse
	if err := json.Unmarshal(message, &response); err != nil {
		log.Printf("Error unmarshaling top-level response: %v. Raw: %s", err, string(message))
		return
	}
	
	// A diagram showing the structure of an incoming WebSocket message for the X-Plane 12 API 
	// would make this section clearer. 

	switch response.Type {
	case "dataref_update":
		handleDatarefUpdate(response.Payload)
	case "command_update":
		handleCommandUpdate(response.Payload)
	case "error":
		handleAPIError(response.RequestID, response.Payload)
	case "message":
		// This is a general confirmation message, usually harmless.
		// log.Printf("[API MESSAGE] Req ID: %d, Payload: %s", response.RequestID, string(response.Payload))
	default:
		// Catch all other messages
		log.Printf("[UNKNOWN] Req ID %d, Type: %s, Payload: %s", response.RequestID, response.Type, string(response.Payload))
	}
}

func handleDatarefUpdate(rawPayload json.RawMessage) {
	var update DatarefUpdatePayload
	if err := json.Unmarshal(rawPayload, &update); err != nil {
		log.Printf("Error unmarshaling dataref update: %v", err)
		return
	}

	if update.Dataref == ContinuousDataref {
		// Fast-changing data
		fmt.Printf("[DATA: %.1f Hz] Airspeed: %.2f kts\n", ContinuousFreq, update.Value)
	} else if update.Dataref == EventDataref {
		// Event-driven data (pushed only when the value changes)
		fmt.Printf(">>> EVENT DETECTED: COM1 Standby Freq Changed to %.0f Hz\n", update.Value)
	}
}

func handleCommandUpdate(rawPayload json.RawMessage) {
	var update CommandUpdatePayload
	if err := json.Unmarshal(rawPayload, &update); err != nil {
		log.Printf("Error unmarshaling command update: %v", err)
		return
	}
	
	// Log command executions
	if update.Status == "executed" {
		fmt.Printf("--- COMMAND EVENT: Command '%s' was executed.\n", update.CommandName)
	}
}

func handleAPIError(reqID int64, rawPayload json.RawMessage) {
	var errPayload ErrorPayload
	if err := json.Unmarshal(rawPayload, &errPayload); err != nil {
		log.Printf("[API ERROR] Req ID: %d, Unmarshal failed: %v", reqID, err)
		return
	}
	log.Printf("[API ERROR] Req ID: %d, Code: %d, Message: %s", reqID, errPayload.Code, errPayload.Message)
}