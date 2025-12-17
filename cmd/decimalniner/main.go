package main

/*

Issue 1: websocket does not return the correct values for numberic trafficglobal datarefs. Element 0 is repeated for all elements.
Issue 2: When subscribed, if the number of elements changes, this is not reflected in the updates.
Issue 3: When traffic global AI aircraft count is greater than 255, X-Plane crashes
Issue 4: source and dest icao datarefs do not return string values as stated in the c++ sample, only int array.
Issue 5: Not an issue, but the TG UI has a setting "RELEASE_AIRCRAFT Disabled" for which there is no documentation. Presumably this stops AI from spawning/despawning which would affect the number of elements in the datarefs.

To replicate:
1. Run this program subscribing to multiple trafficglobal/ai/position_lat dataref
2. Note the dataref id and use it to manually query the dataref via REST API:
   	e.g. http://localhost:8086/api/v2/datarefs/1988818324744/value
3. Compare values returned via REST and WebSocket.

*/

import (
	"encoding/base64"
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

	"trafficglobal/ai/position_lat", 	   // Float array <-- [35.145877838134766,35.145877838134766,35.145877838134766,35.145877838134766,35.145877838134766]
	"trafficglobal/ai/position_long", 	   // Float array <-- [24.120702743530273,24.120702743530273,24.120702743530273,24.120702743530273,24.120702743530273]
	"trafficglobal/ai/position_heading",   // Float array <-- failed to retrieve this one
	"trafficglobal/ai/position_elev",      // Float array, Altitude in meters <-- [10372.2021484375,10372.2021484375,10372.2021484375,10372.2021484375,10372.2021484375]

	"trafficglobal/ai/aircraft_code",  		// Binary array of zero-terminated char strings <-- "QVQ0ADczSABBVDQAREg0AEFUNAAA" decodes to AT4,73H,AT4,DH4,AT4 (commas added for clarity)
	"trafficglobal/ai/airline_code", 		// Binary array of zero-terminated char strings <-- "U0VIAE1TUgBTRUgAT0FMAFNFSAAA" decodes to SEH,MSR,SEH,OAL,SEH
	"trafficglobal/ai/tail_number", 		// Binary array of zero-terminated char strings <-- "U1gtQUFFAFNVLVdGTABTWC1CWEIAU1gtWENOAFNYLVVJVAAA" decodes to SX-AAE,SU-WFL,SX-BXB,SX-XCN,SX-UIT

	"trafficglobal/ai/ai_type", 			// Int array of traffic type (TrafficType enum) <-- [0,0,0,0,0]
	"trafficglobal/ai/ai_class",			// Int array of size class (SizeClass enum) <-- [2,2,2,2,2]

	"trafficglobal/ai/flight_num" ,		    // Int array of flight numbers <-- [471,471,471,471,471]

	"trafficglobal/ai/source_icao",	        // Binary array of zero-terminated char strings, and int array of XPLMNavRef <-- only returns int array [16803074,16803074,16803074]
 	"trafficglobal/ai/dest_icao",		    // Binary array of zero-terminated char strings, and int array of XPLMNavRef <-- only returns int array [16803074,16803074,16803074]

	"trafficglobal/ai/parking" ,			// Binary array of zero-terminated char strings <-- RAMP 2,APRON A1,APRON B (commas added for clarity)

	"trafficglobal/ai/flight_phase" ,	    // Int array of phase type (FlightPhase enum) <-- [5,5,5]

	// The runway is the designator at the source airport if the flight phase is one of:
	//   FP_TaxiOut, FP_Depart, FP_Climbout
	// ... and at the destination airport if the flight phase is one of:
	//   FP_Cruise, FP_Approach, FP_Final, FP_Braking, FP_TaxiIn, FP_GoAround

 	"trafficglobal/ai/runway",	// Int array of runway identifiers i.e. (uint32_t)'08R' <-- [538756,13107,0,0]

	// If the AI is taxying, this will contain the comma-separated list of taxi edge names. Consecutive duplicates and blanks are removed.

 	"trafficglobal/ai/taxi_route",   // <-- "" (no aircraft was taxiing at time of query)

 	// Structured data containing details of all nearby airport flows - ICAO code, active and pending flows, active runways.

	"trafficglobal/airport_flows",  // <-- decoding resulted in special character - raw data for LGIR airport flows: "CwAGAA=="

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
type DatarefSubscriptionRequest struct {
	RequestID int64       `json:"req_id"`
	Type      string      `json:"type"` 
	Params    ParamDatarefs `json:"params"`
}

type ParamDatarefs struct {
	Datarefs []SubDataref `json:"datarefs"`
}

type SubDataref struct {
	Id    int `json:"id"`
}

type SubscriptionResponse struct {
	RequestID int64           `json:"req_id"`
	Type      string          `json:"type"`
	Data      json.RawMessage `json:"data,omitempty"`
	Success   bool            `json:"success,omitempty"`
}

// ErrorPayload is used if Type is "error".
type ErrorPayload struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
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
	fmt.Println("==================================")

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
	sendDatarefSubscription(conn, dataRefIndexMap)

	// 4. Keep connection alive until interrupt
	log.Println("Press Ctrl+C to disconnect.")
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
	log.Printf("-> Sending: %s", string(msg))
	if err != nil {
		log.Fatalf("Error marshaling JSON: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
		log.Fatalf("Error writing message: %v", err)
	}
}


// sendDatarefSubscription sends a request to subscribe to a dataref.
func sendDatarefSubscription(conn *websocket.Conn, datarefMap map[string]int) {
	reqID := requestCounter.Add(1)

// loop through each dataref in map and create a SubDataref for each

	paramDatarefs := make([]SubDataref, 0, len(datarefMap))
	for _, index := range datarefMap {
		subDataref := SubDataref{
			Id:   index,
		}
		paramDatarefs = append(paramDatarefs, subDataref)
	}

	params := ParamDatarefs{
		Datarefs: paramDatarefs,
	}

	request := DatarefSubscriptionRequest{
		RequestID: reqID,
		Type:      "dataref_subscribe_values",
		Params:    params,
	}

	sendJSON(conn, request)
	log.Printf("-> Sent Request ID %d: Subscribing to datarefs", reqID)
}

// --- Message Processing ---

// processMessage handles and dispatches the incoming JSON data from X-Plane.
func processMessage(message []byte) {
	var response SubscriptionResponse
	if err := json.Unmarshal(message, &response); err != nil {
		log.Printf("Error unmarshaling top-level response: %v. Raw: %s", err, string(message))
		return
	}
	
	// A diagram showing the structure of an incoming WebSocket message for the X-Plane 12 API 
	// would make this section clearer. 

	switch response.Type {
	case "dataref_update_values":
		handleDatarefUpdate(response.Data)
	case "result":
		if response.Success {
			log.Printf("<- Received Response ID %d: Success", response.RequestID)
		} else {
			log.Printf("<- Received Response ID %d: Failure", response.RequestID)
		}
	default:
		// Catch all other messages
		log.Printf("[UNKNOWN] Req ID %d, Type: %s, Payload: %s", response.RequestID, response.Type, string(message))
	}
}

func handleDatarefUpdate(rawPayload json.RawMessage) {

	log.Printf("TODO: Handle dataref update payload %v", string(rawPayload) )
	
	// TODO: need to determine the payload content and which dataref(s) this update is for, then decode accordingly

	// for now assume payload is: {"<dateref_id>": "<base64_encoded_null_terminated_strings>"}

	//get the base64 encoded string from the payload
	var payloadMap map[string]string
	if err := json.Unmarshal(rawPayload, &payloadMap); err != nil {
		log.Printf("Error unmarshaling dataref update payload: %v. Raw: %s", err, string(rawPayload))
		return
	}

	// For this example, just process the first entry in the map
	var base64EncodedDataString string
	for _, v := range payloadMap {
		base64EncodedDataString = v
		break
	}
	
	// Decode the null-terminated strings from the base64 encoded data
	aircraftCodes, err := decodeNullTerminatedString(base64EncodedDataString)
	if err != nil {
		fmt.Println("Error:", err)
		return
	}

	fmt.Printf("Decoded %d Aircraft Codes:\n", len(aircraftCodes))
	for i, code := range aircraftCodes {
		fmt.Printf("%d: %s\n", i+1, code)
	}


}

// decodeNullTerminatedString decodes the base64 string and splits the resulting
// binary data into a slice of strings using the null byte (\x00) as a delimiter.
func decodeNullTerminatedString(encodedData string) ([]string, error) {
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

// decodeUint32 decodes a uint32 value into a string by interpreting its bytes. Useful for decoding runway identifiers.
func decodeUint32(val uint32) {
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