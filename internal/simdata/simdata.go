package simdata

import "github.com/curbz/decimal-niner/internal/xplaneapi/xpapimodel"

type SimDataProvider interface {
	GetSimTime() (XPlaneTime, error)
}

// XPlaneTime represents the raw values pulled from X-Plane Datarefs
type XPlaneTime struct {
	LocalDateDays int     // sim/time/local_date_days (0-indexed)
	LocalTimeSecs float64 // sim/time/local_time_sec
	ZuluTimeSecs  float64 // sim/time/zulu_time_sec
}

// Dataref name constants — use these everywhere to avoid string literals
const (
	DRSimWeatherAircraftBarometer        = "sim/weather/aircraft/barometer_current_pas"
	DRSimWeatherRegionSeaLevelPressure   = "sim/weather/region/sealevel_pressure_pas"
	DRSimFlightmodelPositionMagVariation = "sim/flightmodel/position/magnetic_variation"
	DRSimWeatherRegionTurbulence         = "sim/weather/region/turbulence"
	DRSimWeatherRegionShearSpeed         = "sim/weather/region/shear_speed_msc"
	DRSimWeatherRegionWindSpeed          = "sim/weather/region/wind_speed_msc"
	DRSimWeatherRegionWindDirection      = "sim/weather/region/wind_direction_degt"

	DRSimFlightmodelPositionLatitude = "sim/flightmodel/position/latitude"
	DRSimFlightmodelPositionLongitude = "sim/flightmodel/position/longitude"
	DRSimFlightmodelPositionElevation = "sim/flightmodel/position/elevation"
	DRSimFlightmodelPositionPsi = "sim/flightmodel/position/psi"

	DRSimCockpitRadiosCom1FreqHz = "sim/cockpit/radios/com1_freq_hz"
	DRSimCockpitRadiosCom2FreqHz = "sim/cockpit/radios/com2_freq_hz"
	DRSimATCCom1TunedFacility = "sim/atc/com1_tuned_facility"
	DRSimATCCom2TunedFacility = "sim/atc/com2_tuned_facility"
	DRSimATCCom1Active = "sim/atc/com1_active"
	DRSimATCCom2Active = "sim/atc/com2_active"

	DRTrafficGlobalAIPositionLat     = "trafficglobal/ai/position_lat"
	DRTrafficGlobalAIPositionLong    = "trafficglobal/ai/position_long"
	DRTrafficGlobalAIPositionHeading = "trafficglobal/ai/position_heading"
	DRTrafficGlobalAIPositionElev    = "trafficglobal/ai/position_elev"
	DRTrafficGlobalAIAircraftCode    = "trafficglobal/ai/aircraft_code"
	DRTrafficGlobalAIAirlineCode     = "trafficglobal/ai/airline_code"
	DRTrafficGlobalAITailNumber      = "trafficglobal/ai/tail_number"
	DRTrafficGlobalAIClass           = "trafficglobal/ai/ai_class"
	DRTrafficGlobalAIFlightNum       = "trafficglobal/ai/flight_num"
	DRTrafficGlobalAIParking         = "trafficglobal/ai/parking"
	DRTrafficGlobalAIFlightPhase     = "trafficglobal/ai/flight_phase"
	DRTrafficGlobalAIRunway          = "trafficglobal/ai/runway"

	DRSimTimeLocalDateDays = "sim/time/local_date_days"
	DRSimTimeLocalTimeSec  = "sim/time/local_time_sec"
	DRSimTimeZuluTimeSec   = "sim/time/zulu_time_sec"

)

var SubscribeDatarefs = []xpapimodel.Dataref{

	//weather
	{Name: DRSimWeatherAircraftBarometer, // float <-- 97878.51
		APIInfo: xpapimodel.DatarefInfo{}},
	{Name: DRSimWeatherRegionSeaLevelPressure, // float <-- 98220.164
		APIInfo: xpapimodel.DatarefInfo{}},
	{Name: DRSimFlightmodelPositionMagVariation,
		APIInfo: xpapimodel.DatarefInfo{}},
	{Name: DRSimWeatherRegionTurbulence,
		APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "float_array"},
	{Name: DRSimWeatherRegionShearSpeed,
		APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "float_array"},
	{Name: DRSimWeatherRegionWindSpeed,
		APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "float_array"},
	{Name: DRSimWeatherRegionWindDirection,
		APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "float_array"},

	//user position datarefs
	{Name: DRSimFlightmodelPositionLatitude,
		APIInfo: xpapimodel.DatarefInfo{}},
	{Name: DRSimFlightmodelPositionLongitude,
		APIInfo: xpapimodel.DatarefInfo{}},
	{Name: DRSimFlightmodelPositionElevation,
		APIInfo: xpapimodel.DatarefInfo{}},
	{Name: DRSimFlightmodelPositionPsi, // heading
		APIInfo: xpapimodel.DatarefInfo{}},

	//user tuned atc facilities and frequencies
	{Name: DRSimCockpitRadiosCom1FreqHz,
		APIInfo: xpapimodel.DatarefInfo{}},
	{Name: DRSimCockpitRadiosCom2FreqHz,
		APIInfo: xpapimodel.DatarefInfo{}},
	{Name: DRSimATCCom1TunedFacility,
		APIInfo: xpapimodel.DatarefInfo{}},
	{Name: DRSimATCCom2TunedFacility,
		APIInfo: xpapimodel.DatarefInfo{}},
	{Name: DRSimATCCom1Active,
		APIInfo: xpapimodel.DatarefInfo{}},
	{Name: DRSimATCCom2Active,
		APIInfo: xpapimodel.DatarefInfo{}},		

	//traffic global datarefs
	{Name: DRTrafficGlobalAIPositionLat, // Float array <-- [35.145877838134766,35.145877838134766,35.145877838134766,35.145877838134766,35.145877838134766]
		APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "float_array"},
	{Name: DRTrafficGlobalAIPositionLong, // Float array <-- [24.120702743530273,24.120702743530273,24.120702743530273,24.120702743530273,24.120702743530273]
		APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "float_array"},
	{Name: DRTrafficGlobalAIPositionHeading, // Float array <-- failed to retrieve this one
		APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "float_array"},
	{Name: DRTrafficGlobalAIPositionElev, // Float array, Altitude in meters <-- [10372.2021484375,10372.2021484375,10372.2021484375,10372.2021484375,10372.2021484375]
		APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "float_array"},
	{Name: DRTrafficGlobalAIAircraftCode, // Binary array of zero-terminated char strings <-- "QVQ0ADczSABBVDQAREg0AEFUNAAA" decodes to AT4,73H,AT4,DH4,AT4 (commas added for clarity)
		APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "base64_string_array"},
	{Name: DRTrafficGlobalAIAirlineCode, // Binary array of zero-terminated char strings <-- "U0VIAE1TUgBTRUgAT0FMAFNFSAAA" decodes to SEH,MSR,SEH,OAL,SEH
		APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "base64_string_array"},
	{Name: DRTrafficGlobalAITailNumber, // Binary array of zero-terminated char strings <-- "U1gtQUFFAFNVLVdGTABTWC1CWEIAU1gtWENOAFNYLVVJVAAA" decodes to SX-AAE,SU-WFL,SX-BXB,SX-XCN,SX-UIT
		APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "base64_string_array"},
	{Name: DRTrafficGlobalAIClass, // Int array of size class (SizeClass enum) <-- [2,2,2,2,2]
		APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "int_array"},
	{Name: DRTrafficGlobalAIFlightNum, // Int array of flight numbers <-- [471,471,471,471,471]
		APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "int_array"},
	{Name: DRTrafficGlobalAIParking, // Binary array of zero-terminated char strings <-- RAMP 2,APRON A1,APRON B (commas added for clarity)
		APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "base64_string_array"},
	{Name: DRTrafficGlobalAIFlightPhase, // Int array of phase type (FlightPhase enum) <-- [5,5,5]
		APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "int_array"},

	// The runway is the designator at the source airport if the flight phase is one of:
	//   FP_TaxiOut, FP_Depart, FP_Climbout
	// ... and at the destination airport if the flight phase is one of:
	//   FP_Cruise, FP_Approach, FP_Final, FP_Braking, FP_TaxiIn, FP_GoAround
	{Name: DRTrafficGlobalAIRunway, // Int array of runway identifiers i.e. (uint32_t)'08R' <-- [538756,13107,0,0]
		APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "uint32_string_array"},
}

var SimTimeDatarefs = []xpapimodel.Dataref{
	{Name: DRSimTimeLocalDateDays,
		APIInfo: xpapimodel.DatarefInfo{}},
	{Name: DRSimTimeLocalTimeSec,
		APIInfo: xpapimodel.DatarefInfo{}},
	{Name: DRSimTimeZuluTimeSec,
		APIInfo: xpapimodel.DatarefInfo{}},
}
