package xpconnect

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

	"github.com/curbz/decimal-niner/internal/atc"
	"github.com/curbz/decimal-niner/trafficglobal"

	xpapimodel "github.com/curbz/decimal-niner/internal/xplaneapi/xpapimodel"
	util "github.com/curbz/decimal-niner/pkg/util"
)

type XPConnect struct {
	config config
	conn   *websocket.Conn
	// Map to store the retrieved DataRef Index (int) using the name (string) as the key.
	dataRefIndexMap map[int]*xpapimodel.Dataref
	aircraftMap     map[string]*atc.Aircraft
	atcService      atc.ServiceInterface
	initialised     bool
}

type XPConnectInterface interface {
	Start()
	Stop()
}

type config struct {
	XPlane struct {
		RestBaseURL  string `yaml:"web_api_http_url"`
		WebSocketURL string `yaml:"web_api_websocket_url"`
	} `yaml:"xplane_api"`
}

func New(cfgPath string, atcService atc.ServiceInterface) XPConnectInterface {

	cfg, err := util.LoadConfig[config](cfgPath)
	if err != nil {
		log.Fatalf("Error reading configuration file: %v\n", err)
	}

	return &XPConnect{
		aircraftMap: make(map[string]*atc.Aircraft),
		atcService:  atcService,
		config:      *cfg,
	}

}

var requestCounter atomic.Int64

var datarefs = []xpapimodel.Dataref{

	//user position datarefs
	{Name: "sim/flightmodel/position/latitude",
		APIInfo: xpapimodel.DatarefInfo{}},
	{Name: "sim/flightmodel/position/longitude",
		APIInfo: xpapimodel.DatarefInfo{}},
	{Name: "sim/flightmodel/position/elevation",
		APIInfo: xpapimodel.DatarefInfo{}},
	{Name: "sim/flightmodel/position/psi",
		APIInfo: xpapimodel.DatarefInfo{}},

	//user tuned atc facilities and frequencies
	{Name: "sim/cockpit/radios/com1_freq_hz",
		APIInfo: xpapimodel.DatarefInfo{}},
	{Name: "sim/cockpit/radios/com2_freq_hz",
		APIInfo: xpapimodel.DatarefInfo{}},
	{Name: "sim/atc/com1_tuned_facility",
		APIInfo: xpapimodel.DatarefInfo{}},
	{Name: "sim/atc/com2_tuned_facility",
		APIInfo: xpapimodel.DatarefInfo{}},

	//traffic global datarefs
	{Name: "trafficglobal/ai/position_lat", // Float array <-- [35.145877838134766,35.145877838134766,35.145877838134766,35.145877838134766,35.145877838134766]
		APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "float_array"},
	{Name: "trafficglobal/ai/position_long", // Float array <-- [24.120702743530273,24.120702743530273,24.120702743530273,24.120702743530273,24.120702743530273]
		APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "float_array"},
	{Name: "trafficglobal/ai/position_heading", // Float array <-- failed to retrieve this one
		APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "float_array"},
	{Name: "trafficglobal/ai/position_elev", // Float array, Altitude in meters <-- [10372.2021484375,10372.2021484375,10372.2021484375,10372.2021484375,10372.2021484375]
		APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "float_array"},
	{Name: "trafficglobal/ai/aircraft_code", // Binary array of zero-terminated char strings <-- "QVQ0ADczSABBVDQAREg0AEFUNAAA" decodes to AT4,73H,AT4,DH4,AT4 (commas added for clarity)
		APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "base64_string_array"},
	{Name: "trafficglobal/ai/airline_code", // Binary array of zero-terminated char strings <-- "U0VIAE1TUgBTRUgAT0FMAFNFSAAA" decodes to SEH,MSR,SEH,OAL,SEH
		APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "base64_string_array"},
	{Name: "trafficglobal/ai/tail_number", // Binary array of zero-terminated char strings <-- "U1gtQUFFAFNVLVdGTABTWC1CWEIAU1gtWENOAFNYLVVJVAAA" decodes to SX-AAE,SU-WFL,SX-BXB,SX-XCN,SX-UIT
		APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "base64_string_array"},
	//{Name: "trafficglobal/ai/ai_type", // Int array of traffic type (TrafficType enum) <-- [0,0,0,0,0]
	//	APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "int_array"},
	//{Name: "trafficglobal/ai/ai_class", // Int array of size class (SizeClass enum) <-- [2,2,2,2,2]
	//	APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "int_array"},
	{Name: "trafficglobal/ai/flight_num", // Int array of flight numbers <-- [471,471,471,471,471]
		APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "int_array"},
	//{Name: "trafficglobal/ai/source_icao", // Binary array of zero-terminated char strings, and int array of XPLMNavRef <-- only returns int array [16803074,16803074,16803074]
	//	APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "string_array"},
	//{Name: "trafficglobal/ai/dest_icao", // Binary array of zero-terminated char strings, and int array of XPLMNavRef <-- only returns int array [16803074,16803074,16803074]
	//	APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "string_array"},
	{Name: "trafficglobal/ai/parking", // Binary array of zero-terminated char strings <-- RAMP 2,APRON A1,APRON B (commas added for clarity)
		APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "base64_string_array"},
	{Name: "trafficglobal/ai/flight_phase", // Int array of phase type (FlightPhase enum) <-- [5,5,5]
		APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "int_array"},

	// The runway is the designator at the source airport if the flight phase is one of:
	//   FP_TaxiOut, FP_Depart, FP_Climbout
	// ... and at the destination airport if the flight phase is one of:
	//   FP_Cruise, FP_Approach, FP_Final, FP_Braking, FP_TaxiIn, FP_GoAround
	{Name: "trafficglobal/ai/runway", // Int array of runway identifiers i.e. (uint32_t)'08R' <-- [538756,13107,0,0]
		APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "uint32_string_array"},

	// If the AI is taxying, this will contain the comma-separated list of taxi edge names. Consecutive duplicates and blanks are removed.
	//{Name: "trafficglobal/ai/taxi_route", // <-- "" (no aircraft was taxiing at time of query)
	//	APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "string_array"},

	// Structured data containing details of all nearby airport flows - ICAO code, active and pending flows, active runways.
	//{Name: "trafficglobal/airport_flows", // <-- decoding resulted in special character - raw data for LGIR airport flows: "CwAGAA=="
	//	APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "?"},
}

func (xpc *XPConnect) Start() {

	log.Println("--- Stage 1: Get DataRef Indices via REST (HTTP GET) ---")

	// Get dataref indices via Web API REST
	if err := xpc.getDataRefIndices(); err != nil {
		log.Fatalf("FATAL: Failed to retrieve Dataref Indices via REST: %v", err)
	}

	// Log results
	log.Println("Retrieved DataRef Indices:")
	for id, datarefInfo := range xpc.dataRefIndexMap {
		log.Printf("  - %-40s -> ID: %d\n", datarefInfo.Name, id)
	}
	if len(xpc.dataRefIndexMap) == len(datarefs) {
		log.Println("SUCCESS: All DataRef Indices received.")
	} else if len(xpc.dataRefIndexMap) > 0 {
		log.Fatalf("Only %d of %d dataref indices were received", len(xpc.dataRefIndexMap), len(datarefs))
	} else {
		log.Fatal("FATAL: Received no dataref indices from X-Plane web API.")
	}

	// connect to X-Plane WebSocket
	log.Println("connecting to x-plane websocket")

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)

	u, _ := url.Parse(xpc.config.XPlane.WebSocketURL)
	var err error
	xpc.conn, _, err = websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		log.Fatalf("FATAL: Could not connect to X-Plane WebSocket: %v", err)
	}
	defer xpc.conn.Close()
	log.Println("WebSocket connection established.")

	done := make(chan struct{})

	// Start websocket listener
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

	// Send subscription requests
	log.Println("sending dataref subscription requests")
	xpc.sendDatarefSubscription()

	// Keep connection alive until interrupt
	log.Println("Press Ctrl+C to disconnect.")
	<-interrupt

	// 5. Graceful Close
	log.Println("\nInterrupt received. Disconnecting...")
	xpc.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
}

func (xpc *XPConnect) Stop() {
	// TODO: closedown if needed
}

// buildURLWithFilters constructs the complete URL with filter[name]=... parameters.
func buildURLWithFilters(urlStr string) (string, error) {
	// 1. Parse the base URL
	u, err := url.Parse(urlStr)
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
	// Build the full URL with GET parameters
	fullURL, err := buildURLWithFilters(xpc.config.XPlane.RestBaseURL + "/datarefs")
	if err != nil {
		return err
	}
	log.Printf("Querying: %s", fullURL)

	// Create the HTTP Request object
	req, err := http.NewRequest(http.MethodGet, fullURL, nil)
	if err != nil {
		return fmt.Errorf("error creating HTTP request: %w", err)
	}

	// Set required header
	req.Header.Set("Accept", "application/json")

	// Send the HTTP GET request
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

	// read the response body
	var response xpapimodel.APIResponseDatarefs
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return fmt.Errorf("error decoding response body: %w", err)
	}

	// Store the received indices in a map
	xpc.dataRefIndexMap = make(map[int]*xpapimodel.Dataref)
	for _, dataref := range response.Data {
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

		// Decode based on expected type
		switch dr.DecodedDataType {
		case "base64_string_array":
			// Attempt to decode as base64-null-terminated string blob
			if decoded, err := util.DecodeNullTerminatedString(value.(string)); err == nil && len(decoded) > 0 {
				log.Printf("DataRef %s: decoded strings: %v\n", id, decoded)
				dr.Value = decoded
				continue
			}
			// Otherwise, print raw string
			log.Printf("error decoding null terminated string: DataRef %s: raw value: %v error: %v\n", id, value, err)
		case "uint32_string_array":
			strArray := make([]string, len(value.([]any)))
			for i, elem := range value.([]any) {
				strArray[i] = util.DecodeUint32(uint32(elem.(float64)))
			}
			dr.Value = strArray
			log.Printf("DataRef %s: uint32 decoded: %v\n", id, strArray)			
		case "float_array":
			floatArray := make([]float64, len(value.([]any)))
			for i, elem := range value.([]any) {
				floatArray[i] = elem.(float64)
			}
			dr.Value = floatArray
			log.Printf("DataRef %s: floats: %v\n", id, floatArray)
		case "int_array":
			intArray := make([]int, len(value.([]any)))
			for i, elem := range value.([]any) {
				intArray[i] = int(elem.(float64))
			}
			dr.Value = intArray
			log.Printf("DataRef %s: ints: %v\n", id, intArray)
		default:
			// Unknown or unspecified type — print raw
			log.Printf("DataRef %s: raw payload: %v\n", id, value)
			dr.Value = value
		}
	}

	xpc.updateUserData()
	xpc.updateAircraftData()
}

// determine if user has changed tuned frequencies and inform the ATC service if they have
func (xpc *XPConnect) updateUserData() {

	com1FreqVal := xpc.getDataRefValue("sim/cockpit/radios/com1_freq_hz", 0)
	com2FreqVal := xpc.getDataRefValue("sim/cockpit/radios/com2_freq_hz", 0)
	com1FacilityVal := xpc.getDataRefValue("sim/atc/com1_tuned_facility", 0)
	com2FacilityVal := xpc.getDataRefValue("sim/atc/com2_tuned_facility", 0)

	if com1FreqVal == nil || com2FreqVal == nil ||
		com1FacilityVal == nil || com2FacilityVal == nil {
		log.Println("WARNING: Couldn't update user state as com1 or com2 dataref values are not available")
		return
	}

	com1Freq := int(com1FreqVal.(float64))
	com2Freq := int(com2FreqVal.(float64))
	com1Facility := int(com1FacilityVal.(float64))
	com2Facility := int(com2FacilityVal.(float64))

	userState := xpc.atcService.GetUserState()
	lastTunedFreqs := userState.TunedFreqs
	lastTunedFacilities := userState.TunedFacilities

	// if no change to tuned frequencies, no need to update user state
	if com1Freq == lastTunedFreqs[1] && com2Freq == lastTunedFreqs[2] &&
		com1Facility == lastTunedFacilities[1] && com2Facility == lastTunedFacilities[2] {
		return
	}

	xpc.atcService.NotifyUserChange(atc.Position{
		Lat:      xpc.getDataRefValue("sim/flightmodel/position/latitude", 0).(float64),
		Long:     xpc.getDataRefValue("sim/flightmodel/position/longitude", 0).(float64),
		Altitude: xpc.getDataRefValue("sim/flightmodel/position/elevation", 0).(float64) * 3.28084,
	}, map[int]int{1: com1Freq, 2: com2Freq}, map[int]int{1: com1Facility, 2: com2Facility})

}

// updateAircraftData processes the latest aircraft data using the stored datarefs
func (xpc *XPConnect) updateAircraftData() {

	// get tail numbers/registrations
	tailNumbersDR := xpc.getDataRefByName("trafficglobal/ai/tail_number")
	if tailNumbersDR == nil {
		log.Println("Error: tail number dataref not found")
		return
	}
	tailNumbers, ok := tailNumbersDR.Value.([]string)
	if !ok {
		log.Println("Error: tail number dataref has invalid type")
		return
	}

	airlineCodes := []string{}
	flightNums := []int{}
	airlineCodesDR := xpc.getDataRefByName("trafficglobal/ai/airline_code")
	flightNumsDR := xpc.getDataRefByName("trafficglobal/ai/flight_num")
	if airlineCodesDR == nil || flightNumsDR == nil {
		log.Println("Error: airline code or flight number dataref not found")
	} else {
		airlineCodes, ok = airlineCodesDR.Value.([]string)
		if !ok {
			log.Println("Error: airline code dataref has invalid type")
		}
		flightNums, ok = flightNumsDR.Value.([]int)
		if !ok {
			log.Println("Error: flight number dataref has invalid type")
		}
	}

	// for each tail number, get or create aircraft object
	for index, tailNumber := range tailNumbers {
		aircraft, exists := xpc.aircraftMap[tailNumber]
		newAircraft := !exists
		if newAircraft {
			fpUnknown := trafficglobal.FlightPhase(trafficglobal.Unknown.Index())
			aircraft = &atc.Aircraft{
				Registration: tailNumber,
				Flight: atc.Flight{
					Phase: atc.Phase{
						Current:    fpUnknown.Index(),
						Previous:   fpUnknown.Index(),
						Transition: time.Now()},
				},
			}
			xpc.aircraftMap[tailNumber] = aircraft
			log.Printf("New aircraft detected: %s", tailNumber)
		}

		// Update aircraft flight phase
		flightPhase := xpc.getDataRefValue("trafficglobal/ai/flight_phase", index)
		if flightPhase != nil {
			updatedFlightPhase := flightPhase.(int)
			aircraft.Flight.Phase.Previous = aircraft.Flight.Phase.Current
			aircraft.Flight.Phase.Current = updatedFlightPhase
		}

		// --- UPDATE POSITION DATA ---
		// We use the 'index' from the loop to pull the correct element from the AI arrays
		lat := xpc.getDataRefValue("trafficglobal/ai/position_lat", index)
		lon := xpc.getDataRefValue("trafficglobal/ai/position_long", index)
		alt := xpc.getDataRefValue("trafficglobal/ai/position_elev", index)
		hdg := xpc.getDataRefValue("trafficglobal/ai/position_heading", index)

		if lat != nil && lon != nil && alt != nil && hdg != nil {
			aircraft.Flight.Position = atc.Position{
				Lat:      lat.(float64),
				Long:     lon.(float64),
				Altitude: alt.(float64) * 3.28084, // Ensure AI altitude is also in feet
				Heading:  hdg.(float64),
			}
		}

		airlineCode := "unknown"
		if index < len(airlineCodes) {
			airlineCode = airlineCodes[index]
		}
		// lookup callsign for airline code, default to airline code value if not found in map
		callsign := airlineCode
		airlineInfo := xpc.atcService.GetAirline(airlineCode)
		if airlineInfo != nil {
			callsign = airlineInfo.Callsign
		}

		// get flight number
		flightNum := 0
		if index < len(flightNums) {
			flightNum = flightNums[index]
		}
		aircraft.Flight.Comms.Callsign = fmt.Sprintf("%s %d", callsign, flightNum)
		aircraft.Flight.Number = flightNum

		// get parking
		aircraft.Flight.AssignedParking = xpc.getDataRefValue("trafficglobal/ai/parking", index).(string)

		// get assigned runway
		aircraft.Flight.AssignedRunway = xpc.getDataRefValue("trafficglobal/ai/runway", index).(string)

	}

	if !xpc.initialised {
		xpc.initialised = true
		log.Println("Initial aircraft data loaded.")
	} else {
		// check for flight phase changes
		for _, ac := range xpc.aircraftMap {
			if ac.Flight.Phase.Current != ac.Flight.Phase.Previous {
				log.Printf("Aircraft %s changed phase from %d to %d", ac.Registration, ac.Flight.Phase.Previous, ac.Flight.Phase.Current)
				ac.Flight.Phase.Transition = time.Now()
				// Notify ATC service of flight phase change
				xpc.atcService.NotifyAircraftChange(*ac)
			}
		}
	}

	log.Printf("Total tracked aircraft: %d", len(xpc.aircraftMap))

	xpc.printAircraftData()

}

// getDataRefValue retrieves the value of a dataref by name and index (for array types).
// If the dataref is not found, returns nil.
// If the dataref is not an array type, index is ignored.
func (xpc *XPConnect) getDataRefValue(s string, index int) any {
	dr := xpc.getDataRefByName(s)
	if dr == nil {
		return nil
	}

	// if the decoded value type is array, get the element at index
	switch dr.DecodedDataType {
	case "base64_string_array", "uint32_string_array" :
		values, ok := dr.Value.([]string)
		if !ok || index >= len(values) {
			return nil
		}
		return values[index]
	case "float_array":
		values, ok := dr.Value.([]float64)
		if !ok || index >= len(values) {
			return nil
		}
		return values[index]
	case "int_array":
		values, ok := dr.Value.([]int)
		if !ok || index >= len(values) {
			return nil
		}
		return values[index]
	default:
		// return raw value
		return dr.Value
	}
}

// getDataRefByName retrieves the Dataref struct by its name.
func (xpc *XPConnect) getDataRefByName(s string) *xpapimodel.Dataref {

	for _, dr := range xpc.dataRefIndexMap {
		if dr.Name == s {
			return dr
		}
	}
	return nil
}

// printAircraftData prints the current aircraft data
func (xpc *XPConnect) printAircraftData() {
	for _, ac := range xpc.aircraftMap {
		log.Printf("Aircraft: %s, Flight Phase: %d", ac.Registration, ac.Flight.Phase.Current)
	}
}
