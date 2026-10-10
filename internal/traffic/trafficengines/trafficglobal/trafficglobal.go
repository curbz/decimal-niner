package trafficglobal

import (
	"fmt"
	"math/rand"

	"github.com/curbz/decimal-niner/internal/atc"
	"github.com/curbz/decimal-niner/internal/flightclass"
	"github.com/curbz/decimal-niner/internal/flightphase"
	"github.com/curbz/decimal-niner/internal/flightplan"
	"github.com/curbz/decimal-niner/internal/logger"
	"github.com/curbz/decimal-niner/internal/simdata"
	"github.com/curbz/decimal-niner/internal/traffic"
	"github.com/curbz/decimal-niner/internal/xplaneapi/xpapimodel"
	"github.com/curbz/decimal-niner/pkg/util"
	"github.com/mohae/deepcopy"
)

const (
	LastCheckedPositionVerticalThreshold = 1000.0 // altitude in feet that the aircraft must have descended since the last check to trigger cruise top of descent logic

	FP_Unknown  int = iota - 1
	FP_Cruise       // 0 - Normal cruise phase.
	FP_Approach     // 1 - Positioning from cruise to the runway.
	FP_Final        // 2 - Gear down on final approach.
	FP_TaxiIn       // 3 - Any ground movement after touchdown.
	FP_Shutdown     // 4 - Short period of spooling down engines/electrics.
	FP_Parked       // 5 - Long period parked.
	FP_Startup      // 6 - Short period of spooling up engines/electrics.
	FP_TaxiOut      // 7 - Any ground movement from the gate to the runway.
	FP_Depart       // 8 - Initial ground roll and first part of climb.
	FP_GoAround     // 9 - Unplanned transition from approach to cruise.
	FP_Climbout     // 10 - Remainder of climb, gear up.
	FP_Braking      // 11 - Short period from touchdown to when fast-taxi speed is reached.
	FP_Holding      // 12 - Holding (waiting for a flow to complete changing)
)

type TGconfig struct {
	TG struct {
		FlightPlanPath string `yaml:"plugin_directory"` // Traffic Global expects flight plan BGL files in the root of Traffic Global's plugin folder
	} `yaml:"trafficglobal"`
}

type TrafficGlobal struct {
	traffic.CommonTrafficEngine
	FlightPlanPath string
	AircraftMap    map[string]*atc.Aircraft
}

func New(cfgPath string) (atc.TrafficEngine, error) {

	// trafficglobal uses different flight phase values to those d9 uses internally, so we
	// need to translate them when writing to the simdata.DRTrafficEngineAIFlightPhase dataref.
	// The setFlightPhaseValue function does this translation and is assigned as the SetValue function
	// for the DRTrafficEngineAIFlightPhase dataref below.
	var setFlightPhaseValue = func(dr *xpapimodel.Dataref, newValue any) {

		values := newValue.([]int)
		intArray := make([]int, len(values))

		for i, v := range values {
			var d9fp int
			switch v {
			case FP_Unknown:
				d9fp = flightphase.Unknown.Index()
			case FP_Parked:
				d9fp = flightphase.Parked.Index()
			case FP_Startup:
				d9fp = flightphase.Startup.Index()
			case FP_TaxiOut:
				d9fp = flightphase.TaxiOut.Index()
			case FP_Depart:
				d9fp = flightphase.Takeoff.Index()
			case FP_Climbout:
				d9fp = flightphase.Climbout.Index()
			case FP_Cruise:
				d9fp = flightphase.Cruise.Index()
			case FP_Holding:
				d9fp = flightphase.Holding.Index()
			case FP_Approach:
				d9fp = flightphase.Approach.Index()
			case FP_Final:
				d9fp = flightphase.Final.Index()
			case FP_GoAround:
				d9fp = flightphase.GoAround.Index()
			case FP_Braking:
				d9fp = flightphase.Braking.Index()
			case FP_TaxiIn:
				d9fp = flightphase.TaxiIn.Index()
			case FP_Shutdown:
				d9fp = flightphase.Shutdown.Index()
			default:
				d9fp = flightphase.Unknown.Index()
			}
			intArray[i] = d9fp
		}

		dr.Value = intArray
	}

	cfg, err := util.LoadConfig[TGconfig](cfgPath)
	if err != nil {
		logger.Log.Errorf("Error reading configuration file: %v", err)
		return nil, err
	}

	simdata.DRTrafficEngineAIPositionLat = "trafficglobal/ai/position_lat"
	simdata.DRTrafficEngineAIPositionLong = "trafficglobal/ai/position_long"
	simdata.DRTrafficEngineAIPositionHeading = "trafficglobal/ai/position_heading"
	simdata.DRTrafficEngineAIPositionElev = "trafficglobal/ai/position_elev"
	simdata.DRTrafficEngineAIAircraftCode = "trafficglobal/ai/aircraft_code"
	simdata.DRTrafficEngineAIAirlineCode = "trafficglobal/ai/airline_code"
	simdata.DRTrafficEngineAITailNumber = "trafficglobal/ai/tail_number"
	simdata.DRTrafficEngineAIClass = "trafficglobal/ai/ai_class"
	simdata.DRTrafficEngineAIFlightNum = "trafficglobal/ai/flight_num"
	simdata.DRTrafficEngineAIParking = "trafficglobal/ai/parking"
	simdata.DRTrafficEngineAIFlightPhase = "trafficglobal/ai/flight_phase"
	simdata.DRTrafficEngineAIRunway = "trafficglobal/ai/runway"

	subscribeDatarefs := []xpapimodel.Dataref{
		{Name: simdata.DRTrafficEngineAIPositionLat, // Float array <-- [35.145877838134766,35.145877838134766,35.145877838134766,35.145877838134766,35.145877838134766]
			APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "float_array"},
		{Name: simdata.DRTrafficEngineAIPositionLong, // Float array <-- [24.120702743530273,24.120702743530273,24.120702743530273,24.120702743530273,24.120702743530273]
			APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "float_array"},
		{Name: simdata.DRTrafficEngineAIPositionHeading, // Float array <-- failed to retrieve this one
			APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "float_array"},
		{Name: simdata.DRTrafficEngineAIPositionElev, // Float array, Altitude in meters <-- [10372.2021484375,10372.2021484375,10372.2021484375,10372.2021484375,10372.2021484375]
			APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "float_array"},
		{Name: simdata.DRTrafficEngineAIAircraftCode, // Binary array of zero-terminated char strings <-- "QVQ0ADczSABBVDQAREg0AEFUNAAA" decodes to AT4,73H,AT4,DH4,AT4 (commas added for clarity)
			APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "base64_string_array"},
		{Name: simdata.DRTrafficEngineAIAirlineCode, // Binary array of zero-terminated char strings <-- "U0VIAE1TUgBTRUgAT0FMAFNFSAAA" decodes to SEH,MSR,SEH,OAL,SEH
			APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "base64_string_array"},
		{Name: simdata.DRTrafficEngineAITailNumber, // Binary array of zero-terminated char strings <-- "U1gtQUFFAFNVLVdGTABTWC1CWEIAU1gtWENOAFNYLVVJVAAA" decodes to SX-AAE,SU-WFL,SX-BXB,SX-XCN,SX-UIT
			APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "base64_string_array"},
		{Name: simdata.DRTrafficEngineAIClass, // Int array of size class (SizeClass enum) <-- [2,2,2,2,2]
			APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "int_array"},
		{Name: simdata.DRTrafficEngineAIFlightNum, // Int array of flight numbers <-- [471,471,471,471,471]
			APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "int_array"},
		{Name: simdata.DRTrafficEngineAIParking, // Binary array of zero-terminated char strings <-- RAMP 2,APRON A1,APRON B (commas added for clarity)
			APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "base64_string_array"},
		{Name: simdata.DRTrafficEngineAIFlightPhase, // Int array of phase type (FlightPhase enum) <-- [5,5,5]
			APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "int_array",
			SetValue: setFlightPhaseValue},
		// The runway is the designator at the source airport if the flight phase is one of:
		//   FP_TaxiOut, FP_Depart, FP_Climbout
		// ... and at the destination airport if the flight phase is one of:
		//   FP_Cruise, FP_Approach, FP_Final, FP_Braking, FP_TaxiIn, FP_GoAround
		{Name: simdata.DRTrafficEngineAIRunway, // Int array of runway identifiers i.e. (uint32_t)'08R' <-- [538756,13107,0,0]
			APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "uint32_string_array"},
	}
	simdata.SubscribeDatarefs = append(simdata.SubscribeDatarefs, subscribeDatarefs...)

	te := &TrafficGlobal{
		FlightPlanPath: cfg.TG.FlightPlanPath,
		AircraftMap:    make(map[string]*atc.Aircraft),
	}
	return te, nil
}

func (tg *TrafficGlobal) Start() {
	// no-op for Traffic Global
}

func (tg *TrafficGlobal) SetATCService(atcService *atc.Service) {
	tg.AtcService = atcService
	if atcService != nil {
		atcService.RegisterTrafficEngine(tg)
	}
}

// Enrich will apply additional data to the aircraft and should be called by the ATC Service on aircraft phase change
func (e *TrafficGlobal) Enrich(ac *atc.Aircraft, ap *atc.Airport) {

	switch flightphase.FlightPhase(ac.Flight.Phase.Current) {
	case flightphase.Final, flightphase.Braking, flightphase.TaxiIn:
		if ac.Flight.AssignedRunway.ArrivalAccess == nil {
			e.AtcService.AssignRunwayAccessPoint(ac, ap, atc.ARRIVAL_CONTEXT)
		}
	}

	if ac.Flight.Phase.Current <= flightphase.TaxiOut.Index() {
		if ac.Flight.AssignedRunway.DepartureAccess == nil {
			e.AtcService.AssignRunwayAccessPoint(ac, ap, atc.DEPARTURE_CONTEXT)
		}
	}

	if ac.Flight.Phase.Current <= flightphase.Departure.Index() {
		if ac.Flight.AssignedSID == nil {
			e.AtcService.AssignSID(ac, ap, ac.Flight.AssignedRunway)
		}
	}

	if ac.Flight.Phase.Current >= flightphase.Cruise.Index() && ac.Flight.Phase.Current <= flightphase.Approach.Index() {
		if ac.Flight.AssignedSTAR == nil {
			e.AtcService.AssignSTAR(ac, ap, ac.Flight.AssignedRunway)
		}
	}
}

func (e *TrafficGlobal) CheckForSubPhaseChange(ac *atc.Aircraft) {

	// if last check position has not yet been set, set it now so that we have something to compare against and return
	if ac.Flight.LastCheckedPosition.Lat == 0 && ac.Flight.LastCheckedPosition.Long == 0 {
		ac.Flight.LastCheckedPosition = ac.Flight.Position
		return
	}

	switch flightphase.FlightPhase(ac.Flight.Phase.Current) {
	case flightphase.Cruise:
		// check for possible sector change
		e.CheckForCruiseSectorChange(ac)
		// check for TOD
		e.CheckForTOD(ac)
	}
}

func (e *TrafficGlobal) CheckForTOD(ac *atc.Aircraft) {

	if ac.Flight.ClearedTOD {
		return
	}

	descent := ac.Flight.LastCheckedPosition.Altitude - ac.Flight.Position.Altitude
	// Only notify if descent is more than threshold
	if descent >= LastCheckedPositionVerticalThreshold {
		// set new state to trigger subphase
		ac.Flight.ClearedTOD = true
		// deep copy and transmit
		util.LogWithLabel(ac.Registration, "TOD detected, aircraft descending")
		// creat snapshot of aircraft state for phrase generation
		v := deepcopy.Copy(ac)
		acSnap, ok := v.(*atc.Aircraft)
		if !ok {
			util.LogWarnWithLabel(ac.Registration, "failed to deepcopy aircraft snapshot for TOD subphase; skipping phrase generation")
		} else {
			util.GoSafe(func() {
				// +-----------------------------------------------------------------+
				// | Only use acSnap to reference the aircraft within the go routine |
				// +-----------------------------------------------------------------+
				e.AtcService.Transmit(e.AtcService.UserState, acSnap)
			})
		}
		// update position
		ac.Flight.LastCheckedPosition = ac.Flight.Position
	}

}

func (tg *TrafficGlobal) HandleAircraftData(datarefs map[int]*xpapimodel.Dataref) {
	if datarefs == nil {
		return
	}
	if tg.AircraftMap == nil {
		tg.AircraftMap = make(map[string]*atc.Aircraft)
	}

	tailNumbersDR := getDataRefByName(datarefs, simdata.DRTrafficEngineAITailNumber)
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
	airlineCodesDR := getDataRefByName(datarefs, simdata.DRTrafficEngineAIAirlineCode)
	flightNumsDR := getDataRefByName(datarefs, simdata.DRTrafficEngineAIFlightNum)
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

	for index, tailNumber := range tailNumbers {
		flightNum := 0
		if index < len(flightNums) {
			flightNum = flightNums[index]
		}
		acKey := fmt.Sprintf("%s_%d", tailNumber, flightNum)
		aircraft, exists := tg.AircraftMap[acKey]
		if !exists {
			airlineCode := "unknown"
			if index < len(airlineCodes) {
				airlineCode = airlineCodes[index]
			}
			aircraft = tg.createNewAircraft(datarefs, index, flightNum, acKey, tailNumber, airlineCode)
		}

		flightPhase, err := getDataRefValue(datarefs, simdata.DRTrafficEngineAIFlightPhase, index)
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

		lat, errLat := getDataRefValue(datarefs, simdata.DRTrafficEngineAIPositionLat, index)
		lng, errLng := getDataRefValue(datarefs, simdata.DRTrafficEngineAIPositionLong, index)
		alt, errAlt := getDataRefValue(datarefs, simdata.DRTrafficEngineAIPositionElev, index)
		hdg, errHdg := getDataRefValue(datarefs, simdata.DRTrafficEngineAIPositionHeading, index)
		if errLat != nil || errLng != nil || errAlt != nil || errHdg != nil {
			logger.Log.Error(errLat)
			logger.Log.Error(errLng)
			logger.Log.Error(errAlt)
			logger.Log.Error(errHdg)
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
		aircraft.Flight.Position = atc.Position{Lat: latF, Long: lngF, Altitude: altF * 3.28084, Heading: hdgF}
	}
}

func (tg *TrafficGlobal) createNewAircraft(datarefs map[int]*xpapimodel.Dataref, index, flightNumber int, acKey, registration, airlineCode string) *atc.Aircraft {
	fpUnknown := flightphase.FlightPhase(flightphase.Unknown.Index())
	aircraft := &atc.Aircraft{
		Registration: registration,
		Flight: atc.Flight{
			Number: flightNumber,
			Squawk: fmt.Sprintf("%04d", 1200+rand.Intn(5800)),
			Phase: flightphase.Phase{
				Class:      flightclass.Unknown,
				Current:    fpUnknown.Index(),
				Previous:   fpUnknown.Index(),
				Transition: tg.AtcService.GetCurrentZuluTime(),
			},
		},
	}
	tg.AircraftMap[acKey] = aircraft
	util.LogWithLabel(registration, "New aircraft detected registration %s flight number %d", registration, flightNumber)

	classVal, err := getDataRefValue(datarefs, simdata.DRTrafficEngineAIClass, index)
	if err != nil {
		logger.Log.Error(err)
		return aircraft
	}
	sizeClass := 3
	if v, ok := classVal.(int); ok {
		sizeClass = v
	} else if v, ok := classVal.(float64); ok {
		sizeClass = int(v)
	}
	if sizeClass > 5 {
		sizeClass = 3
	}
	aircraft.SizeClass = atc.SizeClass[sizeClass]

	callsign := airlineCode
	if aircraft.Flight.Comms.Callsign == "" {
		airlineInfo := tg.AtcService.GetAirlineByCode(airlineCode)
		if airlineInfo != nil {
			callsign = airlineInfo.Callsign
			aircraft.Flight.Comms.CountryCode = airlineInfo.CountryCode
			aircraft.Flight.Airline = airlineInfo
		} else {
			util.LogWarnWithLabel(aircraft.Registration, "no airline information found for code %s", airlineCode)
			if ccode := tg.AtcService.GetCountryFromRegistration(aircraft.Registration); ccode != "" {
				aircraft.Flight.Comms.CountryCode = ccode
				util.LogWithLabel(aircraft.Registration, "aircraft registration used to set country code %s", ccode)
			} else {
				util.LogWarnWithLabel(aircraft.Registration, "no country code information found for registration %s - using fallback", aircraft.Registration)
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

func getDataRefByName(datarefIndicesMap map[int]*xpapimodel.Dataref, s string) *xpapimodel.Dataref {
	for _, dr := range datarefIndicesMap {
		if dr.Name == s {
			return dr
		}
	}
	return nil
}

func getDataRefValue(datarefIndicesMap map[int]*xpapimodel.Dataref, s string, index int) (any, error) {
	dr := getDataRefByName(datarefIndicesMap, s)
	if dr == nil {
		return nil, fmt.Errorf("error: dataref %s not found in map", s)
	}

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
		return dr.Value, nil
	}
}

func (tg *TrafficGlobal) GetFlightPlanPath() string {
	return tg.FlightPlanPath
}

func (tg *TrafficGlobal) LoadFlightPlans(dirPath string) (map[string][]flightplan.ScheduledFlight, map[string]bool) {
	return flightplan.LoadFlightPlans(dirPath)
}

func (tg *TrafficGlobal) RequiresAircraftData() bool {
	return true
}
