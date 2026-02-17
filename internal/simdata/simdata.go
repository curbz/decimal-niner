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

var SubscribeDatarefs = []xpapimodel.Dataref{
		//TODO: use constants throughout application for all dataref names
		
		//weather
		{Name: "sim/weather/aircraft/barometer_current_pas",  // float <-- 97878.51
			APIInfo: xpapimodel.DatarefInfo{}},
		{Name: "sim/weather/region/sealevel_pressure_pas",  // float <-- 98220.164
			APIInfo: xpapimodel.DatarefInfo{}},
		{Name: "sim/flightmodel/position/magnetic_variation",
			APIInfo: xpapimodel.DatarefInfo{}},	
		{Name: "sim/weather/region/turbulence",
			APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "float_array"},
		{Name: "sim/weather/region/shear_speed_msc",
			APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "float_array"},
		{Name: "sim/weather/region/wind_speed_msc",
			APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "float_array"},
		{Name: "sim/weather/region/wind_direction_degt",
			APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "float_array"},

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
		{Name: "trafficglobal/ai/ai_class", // Int array of size class (SizeClass enum) <-- [2,2,2,2,2]
			APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "int_array"},
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

var SimTimeDatarefs = []xpapimodel.Dataref{
		{Name: "sim/time/local_date_days",
			APIInfo: xpapimodel.DatarefInfo{}},
		{Name: "sim/time/local_time_sec",
			APIInfo: xpapimodel.DatarefInfo{}},
		{Name: "sim/time/zulu_time_sec",
			APIInfo: xpapimodel.DatarefInfo{}},
	}