package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// --- Configuration ---
const (
	// X-Plane 12 default Web API port for BOTH REST and WebSocket is 8086.
	XPlaneAPIPort = "8086"

    // XPlaneRESTBaseURL is the base endpoint for retrieving indices via HTTP GET.
	XPlaneRESTBaseURL = "http://127.0.0.1:" + XPlaneAPIPort + "/api/v2/datarefs" 
	
	// XPlaneWSURL is the endpoint for the WebSocket connection.
	XPlaneWSURL   = "ws://127.0.0.1:" + XPlaneAPIPort + "/api/v2" 
)

/*

M2NMILES(a) ((a) / 1852.0)

enum TrafficType
{
	PT_Airline = 0,
	PT_Cargo,
	PT_GA,
	PT_Military,
};

enum SizeClass
{
	Class_A = 0,
	Class_B,
	Class_C,
	Class_D,
	Class_E,
	Class_F
};

enum FlightPhase
{
	FP_Unknown = -1,
	FP_Cruise = 0,
	FP_Approach,			// Positioning from cruise to the runway.
	FP_Final,				// Gear down on final approach.
	FP_TaxiIn,				// Any ground movement after touchdown.
	FP_Shutdown,			// Short period of spooling down engines/electrics.
	FP_Parked,				// Long period parked.
	FP_Startup,				// Short period of spooling up engines/electrics.
	FP_TaxiOut,				// Any ground movement from the gate to the runway.
	FP_Depart,				// Initial ground roll and first part of climb.
	FP_GoAround,			// Unplanned transition from approach to cruise.
	FP_Climbout,			// Remainder of climb, gear up.
	FP_Braking,				// Short period from touchdown to when fast-taxi speed is reached.
	FP_Holding,				// Holding, waiting for a flow to complete changing.
};
*/

// List of datarefs we want to look up indices for.
var datarefsToLookup = []string{
	"trafficglobal/ai/position_lat", 	// Float array
	"trafficglobal/ai/position_long", 	// Float array
	"trafficglobal/ai/position_heading", // Float array
	"trafficglobal/ai/position_elev", // Float array, Altitude in meters
	"trafficglobal/ai/aircraft_code",  		// Binary array of zero-terminated char strings
	"trafficglobal/ai/airline_code", 		// Binary array of zero-terminated char strings
	"trafficglobal/ai/tail_number", 		// Binary array of zero-terminated char strings
	"trafficglobal/ai/ai_type", 			// Int array of traffic type (TrafficType enum)
	 "trafficglobal/ai/ai_class",			// Int array of size class (SizeClass enum)
	"trafficglobal/ai/flight_num" ,		// Int array of flight numbers
	"trafficglobal/ai/source_icao",	// Binary array of zero-terminated char strings, and int array of XPLMNavRef
 	"trafficglobal/ai/dest_icao",		// Binary array of zero-terminated char strings, and int array of XPLMNavRef
	"trafficglobal/ai/parking" ,			// Binary array of zero-terminated char strings
	"trafficglobal/ai/flight_phase" ,	// Int array of phase type (FlightPhase enum)
	// The runway is the designator at the source airport if the flight phase is one of:
	//   FP_TaxiOut, FP_Depart, FP_Climbout
	// ... and at the destination airport if the flight phase is one of:
	//   FP_Cruise, FP_Approach, FP_Final, FP_Braking, FP_TaxiIn, FP_GoAround
 	"trafficglobal/ai/runway",	// Int array of runway identifiers i.e. (uint32_t)'08R'
	// If the AI is taxying, this will contain the comma-separated list of taxi edge names. Consecutive duplicates and blanks are removed.
 	"trafficglobal/ai/taxi_route",
 	// Structured data containing details of all nearby airport flows - ICAO code, active and pending flows, active runways.
	"trafficglobal/airport_flows",
	// Set or get the user's parking allocation as a string formatted as ICAO/Parking Slot i.e. "EGLL/555" or "KORD/Terminal 1 Gate C15".
	// The ICAO code should be uppercase and the parking slot name should match the one in the apt.dat EXACTLY, case sensitive.
	// If you want to check it was reserved OK, get the data after you set it.
	// Note that any AI in the user's slot OR ANY THAT OVERLAP IT are immediately deleted.
 	"trafficglobal/user_parking",
}


// Map to store the retrieved DataRef Index (int) using the name (string) as the key.
var dataRefIndexMap = make(map[string]int)

// --- Data Structures ---

type APIResponseDatarefs struct {
	Data []DatarefInfo `json:"data"`
}

type DatarefInfo struct {
	ID         int64  `json:"id"`
	IsWritable bool   `json:"is_writable"`
	Name       string `json:"name"`
	ValueType  string `json:"value_type"`
}

// Placeholder for WebSocket request structure (only used for confirmation)
type APIRequest struct {
	RequestID int64       `json:"req_id"`
	Type      string      `json:"type"` 
	Params    interface{} `json:"params"`
}

var requestCounter atomic.Int64 

// --- Main Application ---

func main() {
	log.Println("--- Stage 1: Get DataRef Indices via REST (HTTP GET) ---")
	
	// 1. Get Indices via REST
	if err := getDataRefIndices(); err != nil {
		log.Fatalf("FATAL: Failed to retrieve Dataref Indices via REST: %v", err)
	}

	// 2. Output Results
	fmt.Println("\n==================================")
	if len(dataRefIndexMap) == len(datarefsToLookup) {
		log.Println("SUCCESS: All DataRef Indices received.")
		fmt.Println("Retrieved DataRef Indices:")
		for name, id := range dataRefIndexMap {
			fmt.Printf("  - %-40s -> ID: %d\n", name, id)
		}
	} else if len(dataRefIndexMap) > 0 {
		log.Printf("WARNING: Only %d of %d indices were received. Some datarefs may be invalid.", len(dataRefIndexMap), len(datarefsToLookup))
	} else {
		log.Fatal("FATAL: Received no indices. Check X-Plane REST configuration (Port " + XPlaneAPIPort + ") and firewall.")
	}
	fmt.Println("==================================\n")

	// 3. Connect to WebSocket (Confirm successful setup)
	log.Println("--- Stage 2: Connect to WebSocket (Confirmation) ---")
	
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)
	
	u, _ := url.Parse(XPlaneWSURL)
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		log.Fatalf("FATAL: Could not connect to X-Plane WebSocket at %s. Ensure X-Plane 12 is running and the Web API is listening on TCP port %s.\nError: %v", XPlaneWSURL, XPlaneAPIPort, err)
	}
	defer conn.Close()
	log.Println("SUCCESS: WebSocket connection established.")
    
	// 4. Keep connection alive until interrupt
	log.Println("Application paused. Press Ctrl+C to disconnect.")
	<-interrupt

	// 5. Graceful Close
	log.Println("\nInterrupt received. Disconnecting...")
	conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
}

// --- REST API Functions (Stage 1) ---

// buildURLWithFilters constructs the complete URL with filter[name]=... parameters.
func buildURLWithFilters() (string, error) {
    // 1. Parse the base URL
    u, err := url.Parse(XPlaneRESTBaseURL)
    if err != nil {
        return "", fmt.Errorf("error parsing base URL: %w", err)
    }

    // 2. Add filter parameters
    q := u.Query()
    for _, dataref := range datarefsToLookup {
        // The spec requires filter[name] for each dataref
        q.Add("filter[name]", dataref) 
    }
    u.RawQuery = q.Encode()

    return u.String(), nil
}

// getDataRefIndices fetches the integer indices for the named datarefs via HTTP GET.
func getDataRefIndices() error {
    // A. Build the full URL with GET parameters
    fullURL, err := buildURLWithFilters()
    if err != nil {
        return err
    }
    log.Printf("Querying: %s", fullURL)

    // B. Create the HTTP Request object
    req, err := http.NewRequest(http.MethodGet, fullURL, nil)
    if err != nil {
        return fmt.Errorf("error creating HTTP request: %w", err)
    }

	// Set required header
    req.Header.Set("Accept", "application/json") 

	// C. Send the HTTP GET request
    client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("error performing HTTP GET to %s: %w", fullURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Read body for detailed X-Plane error message
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("received non-OK status code %d from X-Plane REST API. Response: %s", resp.StatusCode, string(body))
	}

	// D. Decode the response body
    // The response body structure is expected to be {"indices": {"dataref/name": id, ...}}
	var response APIResponseDatarefs
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return fmt.Errorf("error decoding response body: %w", err)
	}

	// E. Store the received indices
	for _, dataref := range response.Data {
		dataRefIndexMap[dataref.Name] = int(dataref.ID)
	}

	return err
}

// --- WebSocket Utility (Stage 2) ---

// sendJSON is a utility function for the WebSocket connection (not used for REST).
func sendJSON(conn *websocket.Conn, data interface{}) {
	msg, err := json.Marshal(data)
	if err != nil {
		log.Fatalf("Error marshaling JSON: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
		log.Fatalf("Error writing message: %v", err)
	}
}