package trafficglobal

import (
	"github.com/curbz/decimal-niner/internal/atc"
	"github.com/curbz/decimal-niner/internal/flightphase"
	"github.com/curbz/decimal-niner/internal/flightplan"
	"github.com/curbz/decimal-niner/internal/logger"
	"github.com/curbz/decimal-niner/internal/simdata"
	"github.com/curbz/decimal-niner/internal/traffic"
	"github.com/curbz/decimal-niner/internal/xplaneapi/xpapimodel"
	"github.com/curbz/decimal-niner/pkg/util"
)

const (
	FP_Unknown  int = iota - 1
	FP_Cruise               // 0 - Normal cruise phase.
	FP_Approach             // 1 - Positioning from cruise to the runway.
	FP_Final                // 2 - Gear down on final approach.
	FP_TaxiIn               // 3 - Any ground movement after touchdown.
	FP_Shutdown             // 4 - Short period of spooling down engines/electrics.
	FP_Parked               // 5 - Long period parked.
	FP_Startup              // 6 - Short period of spooling up engines/electrics.
	FP_TaxiOut              // 7 - Any ground movement from the gate to the runway.
	FP_Depart               // 8 - Initial ground roll and first part of climb.
	FP_GoAround             // 9 - Unplanned transition from approach to cruise.
	FP_Climbout             // 10 - Remainder of climb, gear up.
	FP_Braking              // 11 - Short period from touchdown to when fast-taxi speed is reached.
	FP_Holding              // 12 - Holding (waiting for a flow to complete changing)
)

type TGconfig struct {
	TG struct {
		FlightPlanPath string `yaml:"plugin_directory"` // Traffic Global expects flight plan BGL files in the root of Traffic Global's plugin folder
	} `yaml:"trafficglobal"`
}

type TrafficGlobal struct {
	FlightPlanPath 	 string
	atcService       *atc.Service
}

func New(cfgPath string) (traffic.Engine, error) {

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
				d9fp = flightphase.Depart.Index()
			case FP_Climbout:
				d9fp = flightphase.Climbout.Index()
			case FP_Cruise:
				d9fp = flightphase.Cruise.Index()
			case FP_Holding:
				d9fp = flightphase.Cruise.Index()
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

	simdata.DRTrafficEngineAIPositionLat     = "trafficglobal/ai/position_lat"
	simdata.DRTrafficEngineAIPositionLong    = "trafficglobal/ai/position_long"
	simdata.DRTrafficEngineAIPositionHeading = "trafficglobal/ai/position_heading"
	simdata.DRTrafficEngineAIPositionElev    = "trafficglobal/ai/position_elev"
	simdata.DRTrafficEngineAIAircraftCode    = "trafficglobal/ai/aircraft_code"
	simdata.DRTrafficEngineAIAirlineCode     = "trafficglobal/ai/airline_code"
	simdata.DRTrafficEngineAITailNumber      = "trafficglobal/ai/tail_number"
	simdata.DRTrafficEngineAIClass           = "trafficglobal/ai/ai_class"
	simdata.DRTrafficEngineAIFlightNum       = "trafficglobal/ai/flight_num"
	simdata.DRTrafficEngineAIParking         = "trafficglobal/ai/parking"
	simdata.DRTrafficEngineAIFlightPhase     = "trafficglobal/ai/flight_phase"
	simdata.DRTrafficEngineAIRunway          = "trafficglobal/ai/runway"

	subscribeDatarefs := []xpapimodel.Dataref {
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
	}
	return te, nil
}

func (tg *TrafficGlobal) Start() {
	// no-op for Traffic Global
}

func (tg *TrafficGlobal) SetATCService(atcService *atc.Service) {
	tg.atcService = atcService
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
