package xpconnect

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gorilla/websocket"

	"github.com/curbz/decimal-niner/internal/atc"
	"github.com/curbz/decimal-niner/internal/flightclass"
	"github.com/curbz/decimal-niner/internal/flightphase"
	"github.com/curbz/decimal-niner/internal/logger"
	"github.com/curbz/decimal-niner/internal/simdata"

	xpapimodel "github.com/curbz/decimal-niner/internal/xplaneapi/xpapimodel"
	util "github.com/curbz/decimal-niner/pkg/util"
)

type XPConnect struct {
	config config
	conn   *websocket.Conn
	// Map to store the retrieved DataRef Index (int) using the name (string) as the key.
	memSubscribeDataRefIndexMap map[int]*xpapimodel.Dataref
	memDataRefIndexMap          map[int]*xpapimodel.Dataref // non-subscribed datarefs
	aircraftMap                 map[string]*atc.Aircraft
	atcService                  atc.ServiceInterface
	initialised                 bool
}

type XPConnectInterface interface {
	Start()
	Stop()
	simdata.SimDataProvider
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
		logger.Log.Errorf("Error reading configuration file: %v", err)
		return &XPConnect{
			aircraftMap:        make(map[string]*atc.Aircraft),
			atcService:         atcService,
			memDataRefIndexMap: make(map[int]*xpapimodel.Dataref),
		}
	}

	return &XPConnect{
		aircraftMap:        make(map[string]*atc.Aircraft),
		atcService:         atcService,
		memDataRefIndexMap: make(map[int]*xpapimodel.Dataref),
		config:             *cfg,
	}

}

var requestCounter atomic.Int64

func (xpc *XPConnect) Start() {

	logger.Log.Info("get sim time from x-plane web api")

	var err error
	simInitTime, err := xpc.initSimTime()
	if err != nil {
		logger.Log.Errorf("Could not get sim time: %v", err)
		return
	}
	xpc.atcService.SetSimTime(simInitTime, time.Now())

	logger.Log.Info("get dataref incides from x-plane web api")
	// Get dataref indices via Web API REST
	xpc.memSubscribeDataRefIndexMap, err = xpc.getDataRefIndices(simdata.SubscribeDatarefs)
	if err != nil {
		logger.Log.Errorf("Failed to retrieve Dataref Indices via REST: %v", err)
		return
	}

	// Log results
	logger.Log.Info("Retrieved DataRef Indices:")
	for id, datarefInfo := range xpc.memSubscribeDataRefIndexMap {
		logger.Log.Infof("  - %-40s -> ID: %d\n", datarefInfo.Name, id)
	}
	if len(xpc.memSubscribeDataRefIndexMap) == len(simdata.SubscribeDatarefs) {
		logger.Log.Info("SUCCESS: All DataRef Indices received.")
	} else if len(xpc.memSubscribeDataRefIndexMap) > 0 {
		logger.Log.Errorf("Only %d of %d dataref indices were received", len(xpc.memSubscribeDataRefIndexMap), len(simdata.SubscribeDatarefs))
		// proceed but warn; some datarefs missing may limit functionality
	} else {
		logger.Log.Error("Received no dataref indices from X-Plane web API.")
		return
	}

	// connect to X-Plane WebSocket
	logger.Log.Info("connecting to x-plane websocket")

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)

	u, _ := url.Parse(xpc.config.XPlane.WebSocketURL)

	xpc.conn, _, err = websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		logger.Log.Errorf("Could not connect to X-Plane WebSocket: %v", err)
		return
	}
	if xpc.conn != nil {
		defer func() { _ = xpc.conn.Close() }()
	}
	logger.Log.Info("WebSocket connection established.")

	done := make(chan struct{})

	// Start websocket listener
	util.GoSafe(func() {
		defer close(done)
		if xpc.conn == nil {
			logger.Log.Error("WebSocket connection is nil; listener exiting")
			return
		}
		for {
			_, message, err := xpc.conn.ReadMessage()
			if err != nil {
				if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
					logger.Log.Info("Connection closed")
					return
				}
				logger.Log.Info("Read error from X-Plane websocket:", err)
				return
			}
			xpc.processMessage(message)
		}
	})

	// Send subscription requests
	logger.Log.Info("sending dataref subscription requests")
	xpc.sendDatarefSubscription()

	// Keep connection alive until interrupt
	<-interrupt

}

func (xpc *XPConnect) Stop() {
	logger.Log.Info("\nDisconnecting...")
	xpc.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
}

// getSimTime fetches the current simulator time via HTTP GET and initialises the in-memory datarefs
func (xpc *XPConnect) initSimTime() (time.Time, error) {

	simTimeIndexMap, err := xpc.getDataRefIndices(simdata.SimTimeDatarefs)
	if err != nil {
		return time.Time{}, fmt.Errorf("error retrieving sim time and date dataref indices: %w", err)
	}
	if len(simTimeIndexMap) != len(simdata.SimTimeDatarefs) {
		return time.Time{}, fmt.Errorf("error:not all sim time dataref indices were retrieved")
	}
	// add map to non-subscribed in-memory dataref map
	maps.Copy(xpc.memDataRefIndexMap, simTimeIndexMap)

	xpTime, err := xpc.GetSimTime()
	if err != nil {
		return time.Time{}, err
	}

	zuluResult := getZuluDateTime(xpTime)

	fmt.Println("--- X-Plane Time Conversion ---")
	fmt.Printf("Sim Local Date Days: %d\n", xpTime.LocalDateDays)
	fmt.Printf("Calculated Zulu:     %s\n", zuluResult.Format("2006-01-02 15:04:05"))

	return zuluResult, nil
}

// getSimTime fetches the current simulator time via HTTP GET.
// Example:
//
//	XPlaneTime{
//		  LocalDateDays: 0,       // Jan 1st
//		  LocalTimeSecs: 70200.0, // 19:30:00
//		  ZuluTimeSecs:  1800.0,  // 00:30:00
//	}
func (xpc *XPConnect) GetSimTime() (simdata.XPlaneTime, error) {

	xplaneTime := simdata.XPlaneTime{}
	// fetch each dataref value
	for _, dr := range simdata.SimTimeDatarefs {
		memDref := xpc.getMemDataRefByName(xpc.memDataRefIndexMap, dr.Name)
		drefId := memDref.APIInfo.ID
		value, err := xpc.webGetDataRefValue(drefId)
		if err != nil {
			return xplaneTime, fmt.Errorf("error retrieving sim time dataref %s value: %w", dr.Name, err)
		}
		//update the value of dataref in memory
		err = xpc.updateMemDatarefValue(memDref, value)
		if err != nil {
			return xplaneTime, fmt.Errorf("error updating sim time dataref %s value: %w", dr.Name, err)
		}

		switch dr.Name {
		case simdata.DRSimTimeLocalDateDays:
			if v, ok := value.(float64); ok {
				xplaneTime.LocalDateDays = int(v)
			} else {
				return xplaneTime, fmt.Errorf("unexpected type for %s: %T", dr.Name, value)
			}
		case simdata.DRSimTimeLocalTimeSec:
			if v, ok := value.(float64); ok {
				xplaneTime.LocalTimeSecs = v
			} else {
				return xplaneTime, fmt.Errorf("unexpected type for %s: %T", dr.Name, value)
			}
		case simdata.DRSimTimeZuluTimeSec:
			if v, ok := value.(float64); ok {
				xplaneTime.ZuluTimeSecs = v
			} else {
				return xplaneTime, fmt.Errorf("unexpected type for %s: %T", dr.Name, value)
			}
		}
	}

	return xplaneTime, nil
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
					SetValue: dr.SetValue,
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
	logger.Log.Infof("Querying web api: %s", fullURL)
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
		var returnError error
		if errors.Is(err, syscall.Errno(10061)) {
			logger.Log.Errorf("error performing HTTP GET to %s: %v\n", fullURL, err)
			returnError = errors.New("Connection refused - ensure X-Plane is running, in an active flight situation and the traffic plugin is also running")
		} else {
			returnError = fmt.Errorf("error performing HTTP GET to %s: %w", fullURL, err)
		}
		return response, returnError
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Read body for detailed X-Plane error message
		body, _ := io.ReadAll(resp.Body)
		strBody := string(body)
		if strings.Contains(strBody, "invalid_dataref_name") {
			logger.Log.Infof("received non-OK status code %d from X-Plane REST API. Response: %s", resp.StatusCode, strBody)
			return nil, errors.New("invalid_dataref_name error - ensure X-Plane is in an active flight situation and the traffic plugin is running")
		} else {
			return nil, fmt.Errorf("received non-OK status code %d from X-Plane REST API. Response: %s", resp.StatusCode, strBody)
		}
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

	logger.Log.Infof("Querying web api: %s", fullURL)

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
		var returnError error
		if errors.Is(err, syscall.Errno(10061)) {
			logger.Log.Errorf("error performing HTTP GET to %s: %v\n", fullURL, err)
			returnError = errors.New("Connection refused - ensure X-Plane is running, in an active flight situation and the traffic plugin is also running")
		} else {
			returnError = fmt.Errorf("error performing HTTP GET to %s: %w", fullURL, err)
		}
		return response, returnError
	}

	if resp.StatusCode != http.StatusOK {
		// Read body for detailed X-Plane error message
		body, _ := io.ReadAll(resp.Body)
		strBody := string(body)
		if strings.Contains(strBody, "invalid_dataref_name") {
			logger.Log.Infof("received non-OK status code %d from X-Plane REST API. Response: %s", resp.StatusCode, strBody)
			return response, errors.New("invalid_dataref_name error - ensure X-Plane is in an active flight situation and the traffic plugin is running")
		} else {
			return response, fmt.Errorf("received non-OK status code %d from X-Plane REST API. Response: %s", resp.StatusCode, strBody)
		}
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
	paramDatarefs := make([]xpapimodel.SubDataref, 0, len(xpc.memSubscribeDataRefIndexMap))
	for index := range xpc.memSubscribeDataRefIndexMap {
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
	logger.Log.Infof("-> Sent Request ID %d: Subscribing to datarefs", reqID)
}

// --- Message Processing ---

// processMessage handles and dispatches the incoming JSON data from X-Plane.
func (xpc *XPConnect) processMessage(message []byte) {
	var response xpapimodel.SubscriptionResponse
	if err := json.Unmarshal(message, &response); err != nil {
		logger.Log.Errorf("error unmarshaling top-level response: %v. Raw: %s", err, string(message))
		return
	}

	switch response.Type {
	case "dataref_update_values":
		xpc.handleSubscribedDatarefUpdate(response.Data)
	case "result":
		if response.Success {
			logger.Log.Infof("<- Received Response ID %d: Success", response.RequestID)
		} else {
			logger.Log.Infof("<- Received Response ID %d: Failure", response.RequestID)
		}
	default:
		// Catch all other messages
		logger.Log.Warnf("unrecognised response type: Req ID %d, Type: %s, Payload: %s", response.RequestID, response.Type, string(message))
	}
}

// handleSubscribedDatarefUpdate updates the in-memory dataref values and triggers downstream updates
func (xpc *XPConnect) handleSubscribedDatarefUpdate(datarefs map[string]any) {

	for id, value := range datarefs {

		// convert id from string to int
		idInt, err := strconv.Atoi(id)
		if err != nil {
			logger.Log.Errorf("error converting dataref ID %s to int: %v", id, err)
			continue
		}

		err = xpc.updateMemDatarefValueInMap(xpc.memSubscribeDataRefIndexMap, idInt, value)
		if err != nil {
			logger.Log.Errorf("error updating dataref ID %d value: %v", idInt, err)
			continue
		}

	}

	xpc.updateUserData()
	xpc.updateAircraftData()
	xpc.updateWeatherData()
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
		s, ok := value.(string)
		if !ok {
			return fmt.Errorf("error decoding null terminated string: DataRef %s id: %d raw value has wrong type: %T", dr.APIInfo.Name, dr.APIInfo.ID, value)
		}
		if decoded, err := util.DecodeNullTerminatedString(s); err == nil && len(decoded) > 0 {
			if dr.SetValue != nil {
				dr.SetValue(dr, decoded)
			} else {
				dr.Value = decoded
			}
		} else {
			return fmt.Errorf("error decoding null terminated string: DataRef %s id: %d raw value: %v error: %v", dr.APIInfo.Name, dr.APIInfo.ID, value, err)
		}
	case "uint32_string_array":
		arr, ok := value.([]any)
		if !ok {
			return fmt.Errorf("dataref %s id %d expected []any for uint32_string_array, got %T", dr.APIInfo.Name, dr.APIInfo.ID, value)
		}
		strArray := make([]string, len(arr))
		for i, elem := range arr {
			f, ok := elem.(float64)
			if !ok {
				return fmt.Errorf("dataref %s id %d element index %d expected float64, got %T", dr.APIInfo.Name, dr.APIInfo.ID, i, elem)
			}
			strArray[i] = util.DecodeUint32(uint32(f))
		}
		if dr.SetValue != nil {
			dr.SetValue(dr, strArray)
		} else {
			dr.Value = strArray
		}
	case "float_array":
		arr, ok := value.([]any)
		if !ok {
			return fmt.Errorf("dataref %s id %d expected []any for float_array, got %T", dr.APIInfo.Name, dr.APIInfo.ID, value)
		}
		floatArray := make([]float64, len(arr))
		for i, elem := range arr {
			f, ok := elem.(float64)
			if !ok {
				return fmt.Errorf("dataref %s id %d element index %d expected float64, got %T", dr.APIInfo.Name, dr.APIInfo.ID, i, elem)
			}
			floatArray[i] = f
		}
		if dr.SetValue != nil {
			dr.SetValue(dr, floatArray)
		} else {
			dr.Value = floatArray
		}
	case "int_array":
		arr, ok := value.([]any)
		if !ok {
			return fmt.Errorf("dataref %s id %d expected []any for int_array, got %T", dr.APIInfo.Name, dr.APIInfo.ID, value)
		}
		intArray := make([]int, len(arr))
		for i, elem := range arr {
			f, ok := elem.(float64)
			if !ok {
				return fmt.Errorf("dataref %s id %d element index %d expected float64, got %T", dr.APIInfo.Name, dr.APIInfo.ID, i, elem)
			}
			intArray[i] = int(f)
		}
		if dr.SetValue != nil {
			dr.SetValue(dr, intArray)
		} else {
			dr.Value = intArray
		}
	default:
		if dr.SetValue != nil {
			dr.SetValue(dr, value)
		} else {
			dr.Value = value
		}
	}

	return nil
}

func (xpc *XPConnect) updateWeatherData() {

	flightBaro, errFb := xpc.getMemDataRefValue(xpc.memSubscribeDataRefIndexMap, simdata.DRSimWeatherAircraftBarometer, 0)
	sealevelBaro, errSb := xpc.getMemDataRefValue(xpc.memSubscribeDataRefIndexMap, simdata.DRSimWeatherRegionSeaLevelPressure, 0)
	magVar, errMv := xpc.getMemDataRefValue(xpc.memSubscribeDataRefIndexMap, simdata.DRSimFlightmodelPositionMagVariation, 0)
	// request index 0 for the following datarefs as this is surface/sea-level layer which is most applicable for flight phases
	// where this will be utilised
	turbMag, errTm := xpc.getMemDataRefValue(xpc.memSubscribeDataRefIndexMap, simdata.DRSimWeatherRegionTurbulence, 0)
	wsMag, errWs := xpc.getMemDataRefValue(xpc.memSubscribeDataRefIndexMap, simdata.DRSimWeatherRegionShearSpeed, 0)
	speed, errSp := xpc.getMemDataRefValue(xpc.memSubscribeDataRefIndexMap, simdata.DRSimWeatherRegionWindSpeed, 0)
	dir, errDr := xpc.getMemDataRefValue(xpc.memSubscribeDataRefIndexMap, simdata.DRSimWeatherRegionWindDirection, 0)

	if errFb != nil || errSb != nil || errMv != nil || errTm != nil || errWs != nil || errSp != nil || errDr != nil {
		logErrors(errFb, errSb)
		return
	}

	w := xpc.atcService.GetWeatherState()
	if v, ok := flightBaro.(float64); ok {
		w.Baro.Flight = v
	} else {
		logger.Log.Error("weather flight baro has unexpected type", flightBaro)
		return
	}
	if v, ok := sealevelBaro.(float64); ok {
		w.Baro.Sealevel = v
	} else {
		logger.Log.Error("weather sealevel baro has unexpected type", sealevelBaro)
		return
	}
	if v, ok := magVar.(float64); ok {
		w.MagVar = v
	} else {
		logger.Log.Error("weather magVar has unexpected type", magVar)
		return
	}
	if v, ok := turbMag.(float64); ok {
		w.Turbulence = v
	} else {
		logger.Log.Error("weather turbulence has unexpected type", turbMag)
		return
	}
	if v, ok := wsMag.(float64); ok {
		w.Wind.Shear = v
	} else {
		logger.Log.Error("weather shear has unexpected type", wsMag)
		return
	}
	if v, ok := speed.(float64); ok {
		w.Wind.Speed = v
	} else {
		logger.Log.Error("weather speed has unexpected type", speed)
		return
	}
	if v, ok := dir.(float64); ok {
		w.Wind.Direction = v
	} else {
		logger.Log.Error("weather direction has unexpected type", dir)
		return
	}

}

// determine if user has changed tuned frequencies and inform the ATC service if they have
func (xpc *XPConnect) updateUserData() {

	// check for radio activity
	com1ActivityVal, errC1a := xpc.getMemDataRefValue(xpc.memSubscribeDataRefIndexMap, simdata.DRSimATCCom1Active, 0)
	com2ActivityVal, errC2a := xpc.getMemDataRefValue(xpc.memSubscribeDataRefIndexMap, simdata.DRSimATCCom2Active, 0)
	if errC1a != nil || errC2a != nil {
		// log but don't return as we can still update user state with frequencies/facilities if we have those
		logErrors(errC1a, errC2a)
	}

	// check we got values
	if com1ActivityVal == nil || com2ActivityVal == nil {
		logger.Log.Warn("couldn't validate radio activity - com1 or com2 dataref values are not available")
		return
	}

	// convert to target types
	ca1, caOk1 := com1ActivityVal.(float64)
	ca2, caOk2 := com2ActivityVal.(float64)
	if !caOk1 || !caOk2 {
		logger.Log.Error("unexpected types for com radio activity datarefs")
		return
	}

	transmitting := ca1 == 1 || ca2 == 1
	xpc.atcService.SetRadioMute(transmitting)

	// get updated comms
	com1FreqVal, errC1 := xpc.getMemDataRefValue(xpc.memSubscribeDataRefIndexMap, simdata.DRSimCockpitRadiosCom1FreqHz, 0)
	com2FreqVal, errC2 := xpc.getMemDataRefValue(xpc.memSubscribeDataRefIndexMap, simdata.DRSimCockpitRadiosCom2FreqHz, 0)
	com1FacilityVal, errF1 := xpc.getMemDataRefValue(xpc.memSubscribeDataRefIndexMap, simdata.DRSimATCCom1TunedFacility, 0)
	com2FacilityVal, errF2 := xpc.getMemDataRefValue(xpc.memSubscribeDataRefIndexMap, simdata.DRSimATCCom2TunedFacility, 0)

	// check for errors
	if errC1 != nil || errC2 != nil || errF1 != nil || errF2 != nil {
		logErrors(errC1, errC2, errF1, errF2)
		return
	}

	// check we got values
	if com1FreqVal == nil || com2FreqVal == nil ||
		com1FacilityVal == nil || com2FacilityVal == nil {
		logger.Log.Warn("couldn't update user state as com1 or com2 dataref values are not available")
		return
	}

	// convert to target types
	cf1, ok1 := com1FreqVal.(float64)
	cf2, ok2 := com2FreqVal.(float64)
	cfac1, ok3 := com1FacilityVal.(float64)
	cfac2, ok4 := com2FacilityVal.(float64)
	if !ok1 || !ok2 || !ok3 || !ok4 {
		logger.Log.Error("unexpected types for coms/facility datarefs")
		return
	}
	com1Freq := int(cf1)
	com2Freq := int(cf2)
	com1Facility := int(cfac1)
	com2Facility := int(cfac2)

	userState := xpc.atcService.GetUserState()
	// check for changes
	commsChanged := false
	if com1Freq != userState.TunedFreqs[1] || com2Freq != userState.TunedFreqs[2] ||
		com1Facility != userState.TunedFacilityRoles[1] || com2Facility != userState.TunedFacilityRoles[2] {
		commsChanged = true
	}

	// get updated position
	latVal, errLat := xpc.getMemDataRefValue(xpc.memSubscribeDataRefIndexMap, simdata.DRSimFlightmodelPositionLatitude, 0)
	lngVal, errLng := xpc.getMemDataRefValue(xpc.memSubscribeDataRefIndexMap, simdata.DRSimFlightmodelPositionLongitude, 0)
	altVal, errAlt := xpc.getMemDataRefValue(xpc.memSubscribeDataRefIndexMap, simdata.DRSimFlightmodelPositionElevation, 0)

	// check for errors
	if errLat != nil || errLng != nil || errAlt != nil {
		logErrors(errLat, errLng, errAlt)
		return
	}

	// check we got values
	if latVal == nil || lngVal == nil || altVal == nil {
		logger.Log.Warn("couldn't update user state as positional dataref values are not available")
		return
	}

	// convert to target value types
	latF, lok := latVal.(float64)
	lngF, lok2 := lngVal.(float64)
	altF, aok := altVal.(float64)
	if !lok || !lok2 || !aok {
		logger.Log.Error("unexpected types for position datarefs")
		return
	}
	lat := latF
	lng := lngF
	alt := altF * 3.28084

	// check for changes
	posChanged := false
	// no need to include altitude in change detection
	if math.Abs(lat-userState.Position.Lat) > 0.0001 || math.Abs(lng-userState.Position.Long) > 0.0001 {
		posChanged = true
	}

	//only notify if change has occurred
	if commsChanged || posChanged {
		xpc.atcService.NotifyUserStateChange(atc.Position{
			Lat:      lat,
			Long:     lng,
			Altitude: alt,
		}, map[int]int{1: com1Freq, 2: com2Freq}, map[int]int{1: com1Facility, 2: com2Facility})
	}

}

// updateAircraftData processes the latest aircraft data using the stored datarefs
func (xpc *XPConnect) updateAircraftData() {

	// get tail numbers/registrations
	tailNumbersDR := xpc.getMemDataRefByName(xpc.memSubscribeDataRefIndexMap, simdata.DRTrafficEngineAITailNumber)
	if tailNumbersDR == nil {
		logger.Log.Error("error: tail number dataref not found")
		return
	}
	tailNumbers, ok := tailNumbersDR.Value.([]string)
	if !ok {
		logger.Log.Error("error: tail number dataref has invalid type")
		return
	}

	airlineCodes := []string{}
	flightNums := []int{}
	airlineCodesDR := xpc.getMemDataRefByName(xpc.memSubscribeDataRefIndexMap, simdata.DRTrafficEngineAIAirlineCode)
	flightNumsDR := xpc.getMemDataRefByName(xpc.memSubscribeDataRefIndexMap, simdata.DRTrafficEngineAIFlightNum)
	if airlineCodesDR == nil || flightNumsDR == nil {
		logger.Log.Error("error: airline code or flight number dataref not found")
	} else {
		airlineCodes, ok = airlineCodesDR.Value.([]string)
		if !ok {
			logger.Log.Error("error: airline code dataref has invalid type")
		}
		flightNums, ok = flightNumsDR.Value.([]int)
		if !ok {
			logger.Log.Error("error: flight number dataref has invalid type")
		}
	}

	// for each tail number and flight number combination, get or create aircraft object
	for index, tailNumber := range tailNumbers {
		// safe flight number access
		flightNum := 0
		if index < len(flightNums) {
			flightNum = flightNums[index]
		}
		acKey := fmt.Sprintf("%s_%d", tailNumber, flightNum)
		aircraft, exists := xpc.aircraftMap[acKey]
		if !exists {
			airlineCode := "unknown"
			if index < len(airlineCodes) {
				airlineCode = airlineCodes[index]
			}
			aircraft = xpc.createNewAircraft(index, flightNum, acKey, tailNumber, airlineCode)
		}

		// Update aircraft flight phase
		flightPhase, err := xpc.getMemDataRefValue(xpc.memSubscribeDataRefIndexMap, simdata.DRTrafficEngineAIFlightPhase, index)
		if err != nil {
			logger.Log.Error(err)
			return
		}
		if v, ok := flightPhase.(int); ok {
			aircraft.Flight.Phase.Current = v
		} else if vf, okf := flightPhase.(float64); okf {
			aircraft.Flight.Phase.Current = int(vf)
		} else {
			logger.Log.Errorf("unexpected type for flight_phase at index %d: %T", index, flightPhase)
		}

		// Update position
		lat, errLat := xpc.getMemDataRefValue(xpc.memSubscribeDataRefIndexMap, simdata.DRTrafficEngineAIPositionLat, index)
		lng, errLng := xpc.getMemDataRefValue(xpc.memSubscribeDataRefIndexMap, simdata.DRTrafficEngineAIPositionLong, index)
		alt, errAlt := xpc.getMemDataRefValue(xpc.memSubscribeDataRefIndexMap, simdata.DRTrafficEngineAIPositionElev, index)
		hdg, errHdg := xpc.getMemDataRefValue(xpc.memSubscribeDataRefIndexMap, simdata.DRTrafficEngineAIPositionHeading, index)
		if errLat != nil || errLng != nil || errAlt != nil || errHdg != nil {
			logErrors(errLat, errLng, errAlt, errHdg)
			return
		}
		latF, lok := lat.(float64)
		lngF, lok2 := lng.(float64)
		altF, aok := alt.(float64)
		hdgF, hok := hdg.(float64)
		if !lok || !lok2 || !aok || !hok {
			logger.Log.Errorf("unexpected position data types for aircraft %s at index %d", tailNumber, index)
			continue
		}
		aircraft.Flight.Position = atc.Position{
			Lat:      latF,
			Long:     lngF,
			Altitude: altF * 3.28084, // Ensure AI altitude is also in feet
			Heading:  hdgF,
		}

		// update parking
		parking, err := xpc.getMemDataRefValue(xpc.memSubscribeDataRefIndexMap, simdata.DRTrafficEngineAIParking, index)
		if err != nil {
			logger.Log.Error(err)
			return
		}
		if p, ok := parking.(string); ok {
			aircraft.Flight.AssignedParking = p
		} else {
			logger.Log.Errorf("unexpected parking type for aircraft %s at index %d: %T", tailNumber, index, parking)
		}

		// update assigned runway
		runway, err := xpc.getMemDataRefValue(xpc.memSubscribeDataRefIndexMap, simdata.DRTrafficEngineAIRunway, index)
		if err != nil {
			logger.Log.Error(err)
			return
		}
		if r, ok := runway.(string); ok {
			aircraft.Flight.AssignedRunway = r
		} else {
			logger.Log.Errorf("unexpected runway type for aircraft %s at index %d: %T", tailNumber, index, runway)
		}

	}

	// now go through all aircraft looking for flight phase changes
	for _, ac := range xpc.aircraftMap {
		if ac.Flight.Phase.Current != ac.Flight.Phase.Previous {

			// If we are already initialised, this is a REAL mid-session change.
			// We notify the service.
			if xpc.initialised {

				// Notify ATC service of flight phase change
				xpc.atcService.NotifyFlightPhaseChange(ac)

				util.LogWithLabel(ac.Registration,
					"flight %d changed phase from %s to %s. Position is lat: %0.6f, lng: %0.6f, alt: %0.6f, hdg: %d",
					ac.Flight.Number,
					flightphase.FlightPhase(ac.Flight.Phase.Previous).String(),
					flightphase.FlightPhase(ac.Flight.Phase.Current).String(),
					ac.Flight.Position.Lat,
					ac.Flight.Position.Long,
					ac.Flight.Position.Altitude,
					int(ac.Flight.Position.Heading))
			}

			// ALWAYS commit the state.
			// If initialised is false, this "silently" syncs the starting state.
			ac.Flight.Phase.Previous = ac.Flight.Phase.Current
			ac.Flight.Phase.Transition = time.Now()
		} else {
			// check for possible sector change
			xpc.atcService.CheckForCruiseSectorChange(ac)
		}
	}

	if !xpc.initialised {
		xpc.initialised = true
		logger.Log.Infof("Initial aircraft data loaded. Total tracked aircraft: %d", len(xpc.aircraftMap))
	}
}

func (xpc *XPConnect) createNewAircraft(index, flightNumber int, acKey, registration, airlineCode string) *atc.Aircraft {

	// set flight phase to unknown initially
	fpUnknown := flightphase.FlightPhase(flightphase.Unknown.Index())
	aircraft := &atc.Aircraft{
		Registration: registration,
		Flight: atc.Flight{
			Number: flightNumber,
			// Squawk random number between 1200 and 6999
			Squawk: fmt.Sprintf("%04d", 1200+rand.Intn(5800)),
			Phase: flightphase.Phase{
				Class:      flightclass.Unknown,
				Current:    fpUnknown.Index(),
				Previous:   fpUnknown.Index(),
				Transition: time.Now()},
		},
	}
	xpc.aircraftMap[acKey] = aircraft
	util.LogWithLabel(registration, "New aircraft detected registration %s flight number %d", registration, flightNumber)

	// get aircraft class
	class, err := xpc.getMemDataRefValue(xpc.memSubscribeDataRefIndexMap, simdata.DRTrafficEngineAIClass, index)
	sizeClass := class.(int)
	if err != nil || sizeClass > 5 {
		logger.Log.Error(err)
		sizeClass = 3 // size class 'D'
	}
	aircraft.SizeClass = atc.SizeClass[sizeClass]

	// lookup callsign for airline code, default to airline code value if not found in map
	callsign := airlineCode
	if aircraft.Flight.Comms.Callsign == "" {
		airlineInfo := xpc.atcService.GetAirline(airlineCode)
		if airlineInfo != nil {
			callsign = airlineInfo.Callsign
			aircraft.Flight.Comms.CountryCode = airlineInfo.CountryCode
			aircraft.Flight.AirlineName = airlineInfo.AirlineName
		} else {
			util.LogWarnWithLabel(aircraft.Registration, "no airline information found for code %s", airlineCode)
			// if we don't have airline info, we also won't have country code, so use tail number as fallback
			if ccode := atc.GetCountryFromRegistration(aircraft.Registration); ccode != "" {
				aircraft.Flight.Comms.CountryCode = ccode
				util.LogWithLabel(aircraft.Registration, "aircraft registration used to set country code %s", ccode)
			}
		}
	}

	sizeClassStr := ""
	if sizeClass > 3 {
		sizeClassStr = "Heavy"
	}
	aircraft.Flight.Comms.Callsign = fmt.Sprintf("%s %d %s", callsign, aircraft.Flight.Number, sizeClassStr)

	return aircraft
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
func getZuluDateTime(xp simdata.XPlaneTime) time.Time {
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
			logger.Log.Error(e)
		}
	}
}
