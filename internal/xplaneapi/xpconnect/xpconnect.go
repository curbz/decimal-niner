package xpconnect

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/curbz/decimal-niner/internal/atc"
	"github.com/curbz/decimal-niner/internal/trafficglobal"

	xpapimodel "github.com/curbz/decimal-niner/internal/xplaneapi/xpapimodel"
	util "github.com/curbz/decimal-niner/pkg/util"
)

type XPConnect struct {
	config config
	conn   *websocket.Conn
	// Map to store the retrieved DataRef Index (int) using the name (string) as the key.
	memDataRefIndexMap map[int]*xpapimodel.Dataref
	memDataRefs        []xpapimodel.Dataref
	aircraftMap        map[string]*atc.Aircraft
	atcService         atc.ServiceInterface
	initialised        bool
	simInitTime        time.Time
	sessionInitTime    time.Time
}

type XPConnectInterface interface {
	Start()
	Stop()
}

// XPlaneTime represents the raw values pulled from X-Plane Datarefs
type XPlaneTime struct {
	LocalDateDays int     // sim/time/local_date_days (0-indexed)
	LocalTimeSecs float64 // sim/time/local_time_sec
	ZuluTimeSecs  float64 // sim/time/zulu_time_sec
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

	datarefs := []xpapimodel.Dataref{

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
	}

	return &XPConnect{
		aircraftMap: make(map[string]*atc.Aircraft),
		atcService:  atcService,
		config:      *cfg,
		memDataRefs: datarefs,
	}

}

var requestCounter atomic.Int64

func (xpc *XPConnect) Start() {

	log.Println("get sim time from x-plane web api")

	var err error
	xpc.simInitTime, err = xpc.getSimTime()
	if err != nil {
		log.Fatalf("FATAL: Could not get sim time: %v", err)
	}
	xpc.sessionInitTime = time.Now()

	log.Println("get traffic global dataref incides from x-plane web api")
	// Get dataref indices via Web API REST
	xpc.memDataRefIndexMap, err = xpc.getDataRefIndices(xpc.memDataRefs)
	if err != nil {
		log.Fatalf("FATAL: Failed to retrieve Dataref Indices via REST: %v", err)
	}

	// Log results
	log.Println("Retrieved DataRef Indices:")
	for id, datarefInfo := range xpc.memDataRefIndexMap {
		log.Printf("  - %-40s -> ID: %d\n", datarefInfo.Name, id)
	}
	if len(xpc.memDataRefIndexMap) == len(xpc.memDataRefs) {
		log.Println("SUCCESS: All DataRef Indices received.")
	} else if len(xpc.memDataRefIndexMap) > 0 {
		log.Fatalf("Only %d of %d dataref indices were received", len(xpc.memDataRefIndexMap), len(xpc.memDataRefs))
	} else {
		log.Fatal("FATAL: Received no dataref indices from X-Plane web API.")
	}

	// connect to X-Plane WebSocket
	log.Println("connecting to x-plane websocket")

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)

	u, _ := url.Parse(xpc.config.XPlane.WebSocketURL)

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

// getSimTime fetches the current simulator time via HTTP GET.
func (xpc *XPConnect) getSimTime() (time.Time, error) {

	simTimeDatarefs := []xpapimodel.Dataref{
		//sim time
		{Name: "sim/time/local_date_days",
			APIInfo: xpapimodel.DatarefInfo{}},
		{Name: "sim/time/local_time_sec",
			APIInfo: xpapimodel.DatarefInfo{}},
		{Name: "sim/time/zulu_time_sec",
			APIInfo: xpapimodel.DatarefInfo{}},
	}

	simTimeIndexMap, err := xpc.getDataRefIndices(simTimeDatarefs)
	if err != nil {
		return time.Time{}, fmt.Errorf("error retrieving sim time and date dataref indices: %w", err)
	}
	if len(simTimeIndexMap) != len(simTimeDatarefs) {
		return time.Time{}, fmt.Errorf("error:not all sim time dataref indices were retrieved")
	}

	// Example:
	// simData := XPlaneTime{
	// 	LocalDateDays: 0,       // Jan 1st
	// 	LocalTimeSecs: 70200.0, // 19:30:00
	// 	ZuluTimeSecs:  1800.0,  // 00:30:00
	// }

	simData := XPlaneTime{}
	// fetch each dataref value
	for _, dr := range simTimeDatarefs {
		memDref := xpc.getMemDataRefByName(simTimeIndexMap, dr.Name)
		drefId := memDref.APIInfo.ID
		value, err := xpc.webGetDataRefValue(drefId)
		if err != nil {
			return time.Time{}, fmt.Errorf("error retrieving sim time dataref %s value: %w", dr.Name, err)
		}
		//update the value of dataref in memory
		err = xpc.updateMemDatarefValue(memDref, value)
		if err != nil {
			return time.Time{}, fmt.Errorf("error updating sim time dataref %s value: %w", dr.Name, err)
		}

		switch dr.Name {
		case "sim/time/local_date_days":
			simData.LocalDateDays = int(value.(float64))
		case "sim/time/local_time_sec":
			simData.LocalTimeSecs = value.(float64)
		case "sim/time/zulu_time_sec":
			simData.ZuluTimeSecs = value.(float64)
		}
	}

	zuluResult := getZuluDateTime(simData)

	fmt.Println("--- X-Plane Time Conversion ---")
	fmt.Printf("Sim Local Date Days: %d\n", simData.LocalDateDays)
	fmt.Printf("Calculated Zulu:     %s\n", zuluResult.Format("2006-01-02 15:04:05"))

	return zuluResult, nil
}

// getDataRefIndices fetches the integer indices for the named datarefs via HTTP GET.
func (xpc *XPConnect) getDataRefIndices(drefs []xpapimodel.Dataref) (map[int]*xpapimodel.Dataref, error) {

	// Store the received indices in a map
	m := make(map[int]*xpapimodel.Dataref)

	response, err := xpc.webGetDatarefIndices(drefs)
	if err != nil {
		return nil, fmt.Errorf("error retrieving dataref indices from web api: %w", err)
	}

	for _, dataref := range response.Data {
		// find the corresponding dataref by name
		for _, dr := range drefs {
			if dr.Name == dataref.Name {
				// store in map
				m[dataref.ID] = &xpapimodel.Dataref{
					Name:            dr.Name,
					APIInfo:         dataref,
					Value:           nil,
					DecodedDataType: dr.DecodedDataType,
				}
				break
			}
		}
	}

	return m, nil
}

func (xpc *XPConnect) webGetDataRefValue(datarefId int) (any, error) {

	var response xpapimodel.APIResponseDatarefValue
	fullURL := fmt.Sprintf("%s/datarefs/%d/value", xpc.config.XPlane.RestBaseURL, datarefId)
	log.Printf("Querying web api: %s", fullURL)
	// Create the HTTP Request object
	req, err := http.NewRequest(http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("error creating HTTP request: %w", err)
	}
	// Set required header
	req.Header.Set("Accept", "application/json")
	// Send the HTTP GET request
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error performing HTTP GET to %s: %w", fullURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Read body for detailed X-Plane error message
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("received non-OK status code %d from X-Plane REST API. Response: %s", resp.StatusCode, string(body))
	}

	// read the response body
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf("error decoding response body: %w", err)
	}

	return response.Data, nil
}

func (xpc *XPConnect) webGetDatarefIndices(drefs []xpapimodel.Dataref) (xpapimodel.APIResponseDatarefs, error) {

	var response xpapimodel.APIResponseDatarefs

	// Build the full URL with GET parameters
	fullURL, err := buildURLWithFilters(xpc.config.XPlane.RestBaseURL+"/datarefs", drefs)
	if err != nil {
		return response, err
	}

	log.Printf("Querying web api: %s", fullURL)

	// Create the HTTP Request object
	req, err := http.NewRequest(http.MethodGet, fullURL, nil)
	if err != nil {
		return response, fmt.Errorf("error creating HTTP request: %w", err)
	}

	// Set required header
	req.Header.Set("Accept", "application/json")

	// Send the HTTP GET request
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return response, fmt.Errorf("error performing HTTP GET to %s: %w", fullURL, err)
	}

	if resp.StatusCode != http.StatusOK {
		// Read body for detailed X-Plane error message
		body, _ := io.ReadAll(resp.Body)
		return response, fmt.Errorf("received non-OK status code %d from X-Plane REST API. Response: %s", resp.StatusCode, string(body))
	}

	// read the response body
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return response, fmt.Errorf("error decoding response body: %w", err)
	}

	return response, nil
}

// sendDatarefSubscription sends a request to subscribe to a dataref.
func (xpc *XPConnect) sendDatarefSubscription() {
	reqID := requestCounter.Add(1)

	// loop through each dataref in map and create a SubDataref for each
	paramDatarefs := make([]xpapimodel.SubDataref, 0, len(xpc.memDataRefIndexMap))
	for index := range xpc.memDataRefIndexMap {
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
		xpc.handleSubscribedDatarefUpdate(response.Data)
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

func (xpc *XPConnect) handleSubscribedDatarefUpdate(datarefs map[string]any) {

	for id, value := range datarefs {

		// convert id from string to int
		idInt, err := strconv.Atoi(id)
		if err != nil {
			log.Printf("Error converting dataref ID %s to int: %v", id, err)
			continue
		}

		err = xpc.updateMemDatarefValueInMap(xpc.memDataRefIndexMap, idInt, value)
		if err != nil {
			log.Printf("Error updating dataref ID %d value: %v", idInt, err)
			continue
		}

	}
	xpc.updateUserData()
	xpc.updateAircraftData()
}

func (xpc *XPConnect) updateMemDatarefValueInMap(datarefIndicesMap map[int]*xpapimodel.Dataref, id int, value any) error {

	// get the stored dataref from the map
	dr, exists := datarefIndicesMap[id]
	if !exists {
		return fmt.Errorf("unable to update dataref id %d - not found in map", id)
	}

	err := xpc.updateMemDatarefValue(dr, value)
	if err != nil {
		return err
	}

	return nil
}

func (xpc *XPConnect) updateMemDatarefValue(dr *xpapimodel.Dataref, value any) error {

	// Decode based on expected type
	switch dr.DecodedDataType {
	case "base64_string_array":
		// Attempt to decode as base64-null-terminated string blob
		if decoded, err := util.DecodeNullTerminatedString(value.(string)); err == nil && len(decoded) > 0 {
			//log.Printf("DataRef %s id: %d decoded strings: %v\n", dr.APIInfo.Name, dr.APIInfo.ID, decoded)
			dr.Value = decoded
		} else {
			// Otherwise, print raw string
			return fmt.Errorf("error decoding null terminated string: DataRef %s id: %d raw value: %v error: %v\n", dr.APIInfo.Name, dr.APIInfo.ID, value, err)
		}
	case "uint32_string_array":
		strArray := make([]string, len(value.([]any)))
		for i, elem := range value.([]any) {
			strArray[i] = util.DecodeUint32(uint32(elem.(float64)))
		}
		dr.Value = strArray
		//log.Printf("DataRef %s id: %d uint32 decoded: %v\n", dr.APIInfo.Name, dr.APIInfo.ID, strArray)
	case "float_array":
		floatArray := make([]float64, len(value.([]any)))
		for i, elem := range value.([]any) {
			floatArray[i] = elem.(float64)
		}
		dr.Value = floatArray
		//log.Printf("DataRef %s id: %d floats: %v\n", dr.APIInfo.Name, dr.APIInfo.ID, floatArray)
	case "int_array":
		intArray := make([]int, len(value.([]any)))
		for i, elem := range value.([]any) {
			intArray[i] = int(elem.(float64))
		}
		dr.Value = intArray
		//log.Printf("DataRef %s id: %d ints: %v\n", dr.APIInfo.Name, dr.APIInfo.ID, intArray)
	default:
		// Unknown or unspecified type — print raw
		//log.Printf("DataRef %s id: %d raw payload: %v\n", dr.APIInfo.Name, dr.APIInfo.ID, value)
		dr.Value = value
	}

	return nil
}

// determine if user has changed tuned frequencies and inform the ATC service if they have
func (xpc *XPConnect) updateUserData() {

	com1FreqVal, errC1 := xpc.getMemDataRefValue(xpc.memDataRefIndexMap, "sim/cockpit/radios/com1_freq_hz", 0)
	com2FreqVal, errC2 := xpc.getMemDataRefValue(xpc.memDataRefIndexMap, "sim/cockpit/radios/com2_freq_hz", 0)
	com1FacilityVal, errF1 := xpc.getMemDataRefValue(xpc.memDataRefIndexMap, "sim/atc/com1_tuned_facility", 0)
	com2FacilityVal, errF2 := xpc.getMemDataRefValue(xpc.memDataRefIndexMap, "sim/atc/com2_tuned_facility", 0)
		if errC1 != nil || errC2 != nil || errF1 != nil || errF2 != nil {
		logErrors(errC1, errC2, errF1, errF2)
		return
	}

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

	lat, errLat := xpc.getMemDataRefValue(xpc.memDataRefIndexMap, "sim/flightmodel/position/latitude", 0)
	lng, errLng := xpc.getMemDataRefValue(xpc.memDataRefIndexMap, "sim/flightmodel/position/longitude", 0)
	alt, errAlt := xpc.getMemDataRefValue(xpc.memDataRefIndexMap, "sim/flightmodel/position/elevation", 0)
	if errLat != nil || errLng != nil || errAlt != nil {
		logErrors(errLat, errLng, errAlt)
		return
	}

	xpc.atcService.NotifyUserChange(atc.Position{
		Lat:      lat.(float64),
		Long:     lng.(float64),
		Altitude: alt.(float64) * 3.28084,
	}, map[int]int{1: com1Freq, 2: com2Freq}, map[int]int{1: com1Facility, 2: com2Facility})

}

// updateAircraftData processes the latest aircraft data using the stored datarefs
func (xpc *XPConnect) updateAircraftData() {

	// get tail numbers/registrations
	tailNumbersDR := xpc.getMemDataRefByName(xpc.memDataRefIndexMap, "trafficglobal/ai/tail_number")
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
	airlineCodesDR := xpc.getMemDataRefByName(xpc.memDataRefIndexMap, "trafficglobal/ai/airline_code")
	flightNumsDR := xpc.getMemDataRefByName(xpc.memDataRefIndexMap, "trafficglobal/ai/flight_num")
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
			// set flight phase to unknown initially
			fpUnknown := trafficglobal.FlightPhase(trafficglobal.Unknown.Index())
			aircraft = &atc.Aircraft{
				Registration: tailNumber,
				Flight: atc.Flight{
					// Squawk random number between 1200 and 6999
					Squawk: fmt.Sprintf("%04d", 1200+rand.Intn(5800)),
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
		flightPhase, err := xpc.getMemDataRefValue(xpc.memDataRefIndexMap, "trafficglobal/ai/flight_phase", index)
		if err != nil {
			log.Println(err)
			return
		}

		updatedFlightPhase := flightPhase.(int)
		aircraft.Flight.Phase.Previous = aircraft.Flight.Phase.Current
		aircraft.Flight.Phase.Current = updatedFlightPhase

		// Update position
		lat, errLat := xpc.getMemDataRefValue(xpc.memDataRefIndexMap, "trafficglobal/ai/position_lat", index)
		lng, errLng := xpc.getMemDataRefValue(xpc.memDataRefIndexMap, "trafficglobal/ai/position_long", index)
		alt, errAlt := xpc.getMemDataRefValue(xpc.memDataRefIndexMap, "trafficglobal/ai/position_elev", index)
		hdg, errHdg := xpc.getMemDataRefValue(xpc.memDataRefIndexMap, "trafficglobal/ai/position_heading", index)
		if errLat != nil || errLng != nil || errAlt != nil || errHdg != nil {
			logErrors(errLat, errLng, errAlt, errHdg)
			return
		}		

		aircraft.Flight.Position = atc.Position{
			Lat:      lat.(float64),
			Long:     lng.(float64),
			Altitude: alt.(float64) * 3.28084, // Ensure AI altitude is also in feet
			Heading:  hdg.(float64),
		}

		// get flight number
		previousFlightNum := aircraft.Flight.Number
		flightNum := 0
		if index < len(flightNums) {
			flightNum = flightNums[index]
		}
		aircraft.Flight.Number = flightNum

		// Add flight plan - only need to do this when adding as a new aircraft or  if flight number has changed
		if newAircraft || (!newAircraft && previousFlightNum != flightNum) {
			// use sim init time + time since session started
			simTime := xpc.simInitTime.Add(time.Since(xpc.sessionInitTime))
			xpc.atcService.AddFlightPlan(aircraft, simTime)
		}

		// update airline code
		airlineCode := "unknown"
		if index < len(airlineCodes) {
			airlineCode = airlineCodes[index]
		}

		// lookup callsign for airline code, default to airline code value if not found in map
		callsign := airlineCode
		airlineInfo := xpc.atcService.GetAirline(airlineCode)
		if airlineInfo != nil {
			callsign = airlineInfo.Callsign
			aircraft.Flight.Comms.CountryCode = airlineInfo.CountryCode
		}
		aircraft.Flight.Comms.Callsign = fmt.Sprintf("%s %d", callsign, flightNum)

		// get parking
		parking, err := xpc.getMemDataRefValue(xpc.memDataRefIndexMap, "trafficglobal/ai/parking", index)
		if err != nil {
			//TODO: reinstate once parking dataref issue is resolved
			//log.Println(err)
			//return
			parking = "stand"		
		}
		aircraft.Flight.AssignedParking = parking.(string)

		// get assigned runway
		runway, err := xpc.getMemDataRefValue(xpc.memDataRefIndexMap, "trafficglobal/ai/runway", index)
		if err != nil {
			log.Println(err)
			return
		}
		aircraft.Flight.AssignedRunway = runway.(string)

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

	//log.Printf("Total tracked aircraft: %d", len(xpc.aircraftMap))

	//xpc.printAircraftData()

}

// getDataRefValue retrieves the value of a dataref by name and index (for array types).
// If the dataref is not an array type, index is ignored.
func (xpc *XPConnect) getMemDataRefValue(datarefIndicesMap map[int]*xpapimodel.Dataref, s string, index int) (any, error) {

	dr := xpc.getMemDataRefByName(datarefIndicesMap, s)
	if dr == nil {
		return nil, fmt.Errorf("error: dataref %s not found in map", s) 
	}

	// if the decoded value type is array, get the element at index
	switch dr.DecodedDataType {
	case "base64_string_array", "uint32_string_array":
		values, ok := dr.Value.([]string)
		if !ok {
			return nil, fmt.Errorf("error: dataref %s is not of expected type []string", s) 
		}
		if index >= len(values) {
			return nil, fmt.Errorf("error: requested index %d is greater than length %d of for dataref %s ", index, len(values), s) 
		}
		return values[index], nil
	case "float_array":
		values, ok := dr.Value.([]float64)
		if !ok {
			return nil, fmt.Errorf("error: dataref %s is not of expected type []float64", s) 
		}
		if index >= len(values) {
			return nil, fmt.Errorf("error: requested index %d is greater than length %d of for dataref %s ", index, len(values), s) 
		}
		return values[index], nil
	case "int_array":
		values, ok := dr.Value.([]int)
		if !ok {
			return nil, fmt.Errorf("error: dataref %s is not of expected type []int", s) 
		}
		if index >= len(values) {
			return nil, fmt.Errorf("error: requested index %d is greater than length %d of for dataref %s ", index, len(values), s) 
		}
		return values[index], nil
	default:
		// return raw value
		return dr.Value, nil
	}
}

// getDataRefByName retrieves the Dataref struct by its name.
func (xpc *XPConnect) getMemDataRefByName(datarefIndicesMap map[int]*xpapimodel.Dataref, s string) *xpapimodel.Dataref {

	for _, dr := range datarefIndicesMap {
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

// --- Helper functions ---

// buildURLWithFilters constructs the complete URL with filter[name]=... parameters.
func buildURLWithFilters(urlStr string, drefs []xpapimodel.Dataref) (string, error) {
	// 1. Parse the base URL
	u, err := url.Parse(urlStr)
	if err != nil {
		return "", fmt.Errorf("error parsing base URL: %w", err)
	}

	// 2. Add filter parameters
	q := u.Query()
	for _, dataref := range drefs {
		// The spec requires filter[name] for each dataref
		q.Add("filter[name]", dataref.Name)
	}
	u.RawQuery = q.Encode()

	return u.String(), nil
}

// GetZuluDateTime converts sim datarefs into a standard Go time.Time object
func getZuluDateTime(xp XPlaneTime) time.Time {
	// 1. Establish the Year. XP doesn't provide this, so we use current system year.
	currentYear := time.Now().Year()

	// 2. Create the Local Date.
	// Jan 1st of current year + local_date_days.
	// We use 00:00:00 as the starting point for this date.
	localDate := time.Date(currentYear, time.January, 1, 0, 0, 0, 0, time.UTC).
		AddDate(0, 0, xp.LocalDateDays)

	// 3. Combine Local Date with Local Time to get a full "Local Timestamp"
	localFull := localDate.Add(time.Duration(xp.LocalTimeSecs) * time.Second)

	// 4. Calculate the Offset (Local - Zulu)
	// We handle the midnight rollover by checking if the diff exceeds 12 hours.
	diff := xp.LocalTimeSecs - xp.ZuluTimeSecs
	if diff > 43200 {
		diff -= 86400
	} else if diff < -43200 {
		diff += 86400
	}

	// 5. Subtract the offset from the Local Timestamp to get the Zulu Timestamp
	// If Local is 5 hours ahead of Zulu, subtracting 5 hours gives us Zulu.
	zuluDateTime := localFull.Add(time.Duration(-diff) * time.Second)

	return zuluDateTime
}

func logErrors(errors ...error) {
	for _, e := range errors {
		if e != nil {
			log.Println(e)
		}
	}
}