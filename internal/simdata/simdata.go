package simdata

import (
	"time"

	"github.com/curbz/decimal-niner/internal/xplaneapi/xpapimodel"
)

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

	DRSimFlightmodelPositionLatitude  = "sim/flightmodel/position/latitude"
	DRSimFlightmodelPositionLongitude = "sim/flightmodel/position/longitude"
	DRSimFlightmodelPositionElevation = "sim/flightmodel/position/elevation"
	DRSimFlightmodelPositionPsi       = "sim/flightmodel/position/psi"

	DRSimCockpitRadiosCom1FreqHz = "sim/cockpit/radios/com1_freq_hz"
	DRSimCockpitRadiosCom2FreqHz = "sim/cockpit/radios/com2_freq_hz"
	DRSimATCCom1TunedFacility    = "sim/atc/com1_tuned_facility"
	DRSimATCCom2TunedFacility    = "sim/atc/com2_tuned_facility"
	DRSimATCCom1Active           = "sim/atc/com1_active"
	DRSimATCCom2Active           = "sim/atc/com2_active"

	DRSimTimeLocalDateDays = "sim/time/local_date_days"
	DRSimTimeLocalTimeSec  = "sim/time/local_time_sec"
	DRSimTimeZuluTimeSec   = "sim/time/zulu_time_sec"
)

var (
	DRTrafficEngineAIPositionLat     string
	DRTrafficEngineAIPositionLong    string
	DRTrafficEngineAIPositionHeading string
	DRTrafficEngineAIPositionElev    string
	DRTrafficEngineAIAircraftCode    string
	DRTrafficEngineAIAirlineCode     string
	DRTrafficEngineAITailNumber      string
	DRTrafficEngineAIClass           string
	DRTrafficEngineAIFlightNum       string
	DRTrafficEngineAIParking         string
	DRTrafficEngineAIFlightPhase     string
	DRTrafficEngineAIRunway          string
)

var SimTimeDatarefs = []xpapimodel.Dataref{
	{Name: DRSimTimeLocalDateDays,
		APIInfo: xpapimodel.DatarefInfo{}},
	{Name: DRSimTimeLocalTimeSec,
		APIInfo: xpapimodel.DatarefInfo{}},
	{Name: DRSimTimeZuluTimeSec,
		APIInfo: xpapimodel.DatarefInfo{}},
}

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
}

// GetZuluDateTime converts sim datarefs into a standard Go time.Time object
func GetZuluDateTime(xpt XPlaneTime) time.Time {
	// 1. Establish the Year. XP doesn't provide this, so we use current system year.
	currentYear := time.Now().Year()

	// 2. Create the Local Date.
	// Jan 1st of current year + local_date_days.
	// We use 00:00:00 as the starting point for this date.
	localDate := time.Date(currentYear, time.January, 1, 0, 0, 0, 0, time.UTC).
		AddDate(0, 0, xpt.LocalDateDays)

	// 3. Combine Local Date with Local Time to get a full "Local Timestamp"
	localFull := localDate.Add(time.Duration(xpt.LocalTimeSecs) * time.Second)

	// 4. Calculate the Offset (Local - Zulu)
	// We handle the midnight rollover by checking if the diff exceeds 12 hours.
	diff := xpt.LocalTimeSecs - xpt.ZuluTimeSecs
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
