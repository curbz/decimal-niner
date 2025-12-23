package xpconnect

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
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	xpapimodel "github.com/curbz/decimal-niner/internal/xplane/xpapimodel"
	util "github.com/curbz/decimal-niner/pkg/util"
)

// --- Configuration ---
const (
	// X-Plane 12 default Web API port for BOTH REST and WebSocket is 8086.
	XPlaneAPIPort = "8086"

	// XPlaneRESTBaseURL is the base endpoint for retrieving indices via HTTP GET.
	XPlaneRESTBaseURL = "http://127.0.0.1:" + XPlaneAPIPort + "/api/v2/datarefs"

	// XPlaneWSURL is the endpoint for the WebSocket connection.
	XPlaneWSURL = "ws://127.0.0.1:" + XPlaneAPIPort + "/api/v2"
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

var requestCounter atomic.Int64

var datarefs = []xpapimodel.Dataref{

	{Name: "trafficglobal/ai/position_lat", // Float array <-- [35.145877838134766,35.145877838134766,35.145877838134766,35.145877838134766,35.145877838134766]
		APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "float_array"},

	{Name: "trafficglobal/ai/position_long", // Float array <-- [24.120702743530273,24.120702743530273,24.120702743530273,24.120702743530273,24.120702743530273]
		APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "float_array"},

	{Name: "trafficglobal/ai/position_heading", // Float array <-- failed to retrieve this one
		APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "float_array"},

	{Name: "trafficglobal/ai/position_elev", // Float array, Altitude in meters <-- [10372.2021484375,10372.2021484375,10372.2021484375,10372.2021484375,10372.2021484375]
		APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "float_array"},

	{Name: "trafficglobal/ai/aircraft_code", // Binary array of zero-terminated char strings <-- "QVQ0ADczSABBVDQAREg0AEFUNAAA" decodes to AT4,73H,AT4,DH4,AT4 (commas added for clarity)
		APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "string_array"},

	{Name: "trafficglobal/ai/airline_code", // Binary array of zero-terminated char strings <-- "U0VIAE1TUgBTRUgAT0FMAFNFSAAA" decodes to SEH,MSR,SEH,OAL,SEH
		APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "string_array"},

	{Name: "trafficglobal/ai/tail_number", // Binary array of zero-terminated char strings <-- "U1gtQUFFAFNVLVdGTABTWC1CWEIAU1gtWENOAFNYLVVJVAAA" decodes to SX-AAE,SU-WFL,SX-BXB,SX-XCN,SX-UIT
		APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "string_array"},

	{Name: "trafficglobal/ai/ai_type", // Int array of traffic type (TrafficType enum) <-- [0,0,0,0,0]
		APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "int_array"},

	{Name: "trafficglobal/ai/ai_class", // Int array of size class (SizeClass enum) <-- [2,2,2,2,2]
		APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "int_array"},

	{Name: "trafficglobal/ai/flight_num", // Int array of flight numbers <-- [471,471,471,471,471]
		APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "int_array"},

	{Name: "trafficglobal/ai/source_icao", // Binary array of zero-terminated char strings, and int array of XPLMNavRef <-- only returns int array [16803074,16803074,16803074]
		APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "string_array"},

	{Name: "trafficglobal/ai/dest_icao", // Binary array of zero-terminated char strings, and int array of XPLMNavRef <-- only returns int array [16803074,16803074,16803074]
		APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "string_array"},

	{Name: "trafficglobal/ai/parking", // Binary array of zero-terminated char strings <-- RAMP 2,APRON A1,APRON B (commas added for clarity)
		APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "string_array"},

	{Name: "trafficglobal/ai/flight_phase", // Int array of phase type (FlightPhase enum) <-- [5,5,5]
		APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "int_array"},

	// The runway is the designator at the source airport if the flight phase is one of:
	//   FP_TaxiOut, FP_Depart, FP_Climbout
	// ... and at the destination airport if the flight phase is one of:
	//   FP_Cruise, FP_Approach, FP_Final, FP_Braking, FP_TaxiIn, FP_GoAround
	{Name: "trafficglobal/ai/runway", // Int array of runway identifiers i.e. (uint32_t)'08R' <-- [538756,13107,0,0]
		APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "int_array"},

	// If the AI is taxying, this will contain the comma-separated list of taxi edge names. Consecutive duplicates and blanks are removed.
	{Name: "trafficglobal/ai/taxi_route", // <-- "" (no aircraft was taxiing at time of query)
		APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "string_array"},

	// Structured data containing details of all nearby airport flows - ICAO code, active and pending flows, active runways.
	{Name: "trafficglobal/airport_flows", // <-- decoding resulted in special character - raw data for LGIR airport flows: "CwAGAA=="
		APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "?"},
}

// --- Main Application ---
type XPConnect struct {
	conn *websocket.Conn
	// Map to store the retrieved DataRef Index (int) using the name (string) as the key.
	dataRefIndexMap map[int]*xpapimodel.Dataref
}

type XPConnectInterface interface {
	Start()
	Stop()
}

func New() XPConnectInterface {
	return &XPConnect{}
}

func (xpc *XPConnect) Start() {

	log.Println("--- Stage 1: Get DataRef Indices via REST (HTTP GET) ---")

	// 1. Get Indices via REST
	if err := xpc.getDataRefIndices(); err != nil {
		log.Fatalf("FATAL: Failed to retrieve Dataref Indices via REST: %v", err)
	}

	// 2. Output Results
	fmt.Println("\n==================================")
	if len(xpc.dataRefIndexMap) == len(datarefs) {
		log.Println("SUCCESS: All DataRef Indices received.")
		fmt.Println("Retrieved DataRef Indices:")
		for id, datarefInfo := range xpc.dataRefIndexMap {
			fmt.Printf("  - %-40s -> ID: %d\n", datarefInfo.Name, id)
		}
	} else if len(xpc.dataRefIndexMap) > 0 {
		log.Printf("WARNING: Only %d of %d indices were received. Some datarefs may be invalid.", len(xpc.dataRefIndexMap), len(datarefs))
	} else {
		log.Fatal("FATAL: Received no indices. Check X-Plane REST configuration (Port " + XPlaneAPIPort + ") and firewall.")
	}
	fmt.Println("==================================")

	// 3. Connect to WebSocket (Confirm successful setup)
	log.Println("--- Stage 2: Connect to WebSocket (Confirmation) ---")

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)

	u, _ := url.Parse(XPlaneWSURL)
	var err error
	xpc.conn, _, err = websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		log.Fatalf("FATAL: Could not connect to X-Plane WebSocket at %s. Ensure X-Plane 12 is running and the Web API is listening on TCP port %s.\nError: %v", XPlaneWSURL, XPlaneAPIPort, err)
	}
	defer xpc.conn.Close()
	log.Println("SUCCESS: WebSocket connection established.")

	done := make(chan struct{})

	// 2. Start listener
	go func() {
		defer close(done)
		for {
			_, message, err := xpc.conn.ReadMessage()
			if err != nil {
				if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
					log.Println("Connection closed.")
					return
				}
				log.Println("Fatal read error:", err)
				return
			}
			xpc.processMessage(message)
		}
	}()

	// 3. Send subscription requests
	log.Println("--- Sending Subscription Requests ---")
	xpc.sendDatarefSubscription()

	// 4. Keep connection alive until interrupt
	log.Println("Press Ctrl+C to disconnect.")
	<-interrupt

	// 5. Graceful Close
	log.Println("\nInterrupt received. Disconnecting...")
	xpc.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
}

func (xpc *XPConnect) Stop() {
	//
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
	for _, dataref := range datarefs {
		// The spec requires filter[name] for each dataref
		q.Add("filter[name]", dataref.Name)
	}
	u.RawQuery = q.Encode()

	return u.String(), nil
}

// getDataRefIndices fetches the integer indices for the named datarefs via HTTP GET.
func (xpc *XPConnect) getDataRefIndices() error {
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
	var response xpapimodel.APIResponseDatarefs
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return fmt.Errorf("error decoding response body: %w", err)
	}

	// E. Store the received indices in the map
	xpc.dataRefIndexMap = make(map[int]*xpapimodel.Dataref)
	for _, dataref := range response.Data {
		//xpc.dataRefIndexMap[dataref.ID] = &dataref
		// find the corresponding dataref by name
		for _, dr := range datarefs {
			if dr.Name == dataref.Name {
				// store in map
				xpc.dataRefIndexMap[dataref.ID] = &xpapimodel.Dataref{
					Name:            dr.Name,
					APIInfo:         dataref,
					Value:           nil,
					DecodedDataType: dr.DecodedDataType,
				}
				break
			}
		}
	}

	return nil
}

// --- WebSocket Utility (Stage 2) ---

// sendDatarefSubscription sends a request to subscribe to a dataref.
func (xpc *XPConnect) sendDatarefSubscription() {
	reqID := requestCounter.Add(1)

	// loop through each dataref in map and create a SubDataref for each

	paramDatarefs := make([]xpapimodel.SubDataref, 0, len(xpc.dataRefIndexMap))
	for index := range xpc.dataRefIndexMap {
		subDataref := xpapimodel.SubDataref{
			Id: index,
		}
		paramDatarefs = append(paramDatarefs, subDataref)
	}

	params := xpapimodel.ParamDatarefs{
		Datarefs: paramDatarefs,
	}

	request := xpapimodel.DatarefSubscriptionRequest{
		RequestID: reqID,
		Type:      "dataref_subscribe_values",
		Params:    params,
	}

	util.SendJSON(xpc.conn, request)
	log.Printf("-> Sent Request ID %d: Subscribing to datarefs", reqID)
}

// --- Message Processing ---

// processMessage handles and dispatches the incoming JSON data from X-Plane.
func (xpc *XPConnect) processMessage(message []byte) {
	var response xpapimodel.SubscriptionResponse
	if err := json.Unmarshal(message, &response); err != nil {
		log.Printf("Error unmarshaling top-level response: %v. Raw: %s", err, string(message))
		return
	}

	// A diagram showing the structure of an incoming WebSocket message for the X-Plane 12 API
	// would make this section clearer.

	switch response.Type {
	case "dataref_update_values":
		xpc.handleDatarefUpdate(response.Data)
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

func (xpc *XPConnect) handleDatarefUpdate(datarefs map[string]any) {

	for id, value := range datarefs {

		// convert id from string to int
		idInt, err := strconv.Atoi(id)
		if err != nil {
			log.Printf("Error converting dataref ID %s to int: %v", id, err)
			continue
		}

		// get the stored dataref from the xpconnect map
		dr, exists := xpc.dataRefIndexMap[idInt]
		if !exists {
			log.Printf("Received update for unknown DataRef ID %d", idInt)
			continue
		}

		// umarshal the value into the native golang type
		switch dr.DecodedDataType {
		case "string_array":
			// Attempt to decode as base64-null-terminated string blob
			if decoded, err := util.DecodeNullTerminatedString(value.(string)); err == nil && len(decoded) > 0 {
				fmt.Printf("DataRef %s: decoded strings: %v\n", id, decoded)
				// get the stored dataref from the xpconnect map and update the value
				dr := xpc.dataRefIndexMap[idInt]
				dr.Value = decoded
				continue
			}
			// Otherwise, print raw string
			fmt.Printf("DataRef %s: string: %s\n", id, value.(string))
		case "float_array":
			// Float array
			floatArray := make([]float64, len(value.([]any)))
			for i, elem := range value.([]any) {
				floatArray[i] = elem.(float64)
			}
			fmt.Printf("DataRef %s: floats: %v\n", id, floatArray)
		case "int_array":
			// Int array
			intArray := make([]int, len(value.([]any)))
			for i, elem := range value.([]any) {
				intArray[i] = int(elem.(float64))
			}
			fmt.Printf("DataRef %s: ints: %v\n", id, intArray)
		default:
			// Unknown type — print raw
			fmt.Printf("DataRef %s: raw payload: %v\n", id, value)
		}
	}

}
