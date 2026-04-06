package trafficglobal

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/curbz/decimal-niner/internal/flightphase"
	"github.com/curbz/decimal-niner/internal/flightplan"
	"github.com/curbz/decimal-niner/internal/logger"
	"github.com/curbz/decimal-niner/internal/simdata"
	"github.com/curbz/decimal-niner/internal/traffic"
	"github.com/curbz/decimal-niner/internal/xplaneapi/xpapimodel"
	"github.com/curbz/decimal-niner/pkg/util"
)

const (
	LEG_SIZE    = 16
	ICAO_OFFSET = 9
	ICAO_LEN    = 4
	FL_OFFSET   = 13

	ALIGN_SEARCH_MAX      = 128
	INVALID_LEG_TOLERANCE = 2

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

var icaoRe = regexp.MustCompile(`^[A-Z]{4}$`)

type TGconfig struct {
	TG struct {
		FlightPlanPath string `yaml:"plugin_directory"` // Traffic Global expects flight plan BGL files in the root of Traffic Global's plugin folder
	} `yaml:"trafficglobal"`
}

type TrafficGlobal struct {
	FlightPlanPath string
}

var (
	// Regex breakdown:
	// 1. Matches everything from the start
	// 2. Lookahead: Stops before a space/underscore followed by:
	//    - A 4-digit year (e.g., 2022)
	//    - A Season code (e.g., S24, W25, Su24)
	airlineRegex = regexp.MustCompile(`^(.*?)(?:[ _]+(?:\d{4}|[SW][u]?\d{2}))?\.bgl$`)
)

func New(cfgPath string) (traffic.Engine, error) {

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

func (tg *TrafficGlobal) GetFlightPlanPath() string {
	return tg.FlightPlanPath
}

func (tg *TrafficGlobal) LoadFlightPlans(dirPath string) (map[string][]flightplan.ScheduledFlight, map[string]bool) {
	start := time.Now()

	// Initialize the master storage once
	masterSchedules := make(map[string][]flightplan.ScheduledFlight)
	masterAirports := make(map[string]bool)

	files, err := os.ReadDir(dirPath)
	if err != nil {
		logger.Log.Errorf("Could not open traffic directory: %v", err)
		return nil, nil
	}

	var fileCount int
	for _, file := range files {
		if !file.IsDir() && strings.HasSuffix(strings.ToLower(file.Name()), ".bgl") {
			fullPath := filepath.Join(dirPath, file.Name())
			airlineName := cleanAirlineName(file.Name())
			err := bglReader(fullPath, airlineName, masterSchedules, masterAirports)
			if err != nil {
				logger.Log.Warnf("Skipping %s: %v", file.Name(), err)
				continue
			}
			fileCount++
		}
	}

	logger.Log.Infof("Loaded %d BGL flight plan file(s). Total: %d schedules, %d airports in %v",
		fileCount, len(masterSchedules), len(masterAirports), time.Since(start))

	return masterSchedules, masterAirports
}

func bglReader(filePath, airline string, masterSchedules map[string][]flightplan.ScheduledFlight, masterAirports map[string]bool) error {
	logger.Log.Debugf("Parsing BGL: %s", filepath.Base(filePath))

	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("read error: %v", err)
	}

	legs, airportICAOlist := collectLegs(data)
	if len(legs) == 0 {
		return fmt.Errorf("no legs found in %s", filePath)
	}

	// Directly update the master maps
	for _, l := range legs {
		l.AirlineName = airline
		key := fmt.Sprintf("%s_%d_%d", l.AircraftRegistration, l.Number, l.DepartureDayOfWeek)
		// No need to check 'found'; appending to a nil slice in a map works
		masterSchedules[key] = append(masterSchedules[key], l)
	}

	for icao := range airportICAOlist {
		masterAirports[icao] = true
	}

	return nil
}

func cleanAirlineName(fileName string) string {
	// 1. Handle case sensitivity
	name := strings.TrimSpace(fileName)

	// 2. Execute Regex
	matches := airlineRegex.FindStringSubmatch(name)
	if len(matches) < 2 {
		// Fallback: just strip .bgl if regex fails
		return strings.TrimSuffix(name, ".bgl")
	}

	result := matches[1]

	// 3. Clean up delimiters for a "human readable" look
	// Replaces underscores with spaces (e.g., Aeromexico_Connect -> Aeromexico Connect)
	result = strings.ReplaceAll(result, "_", " ")

	// 4. Final trim for safety
	return strings.TrimSpace(result)
}

func isRegCharUpper(b byte) bool {
	if (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '-' {
		return true
	}
	return false
}

func isICAO(b []byte) bool {
	if len(b) < 4 {
		return false
	}
	for i := 0; i < 4; i++ {
		if b[i] < 'A' || b[i] > 'Z' {
			return false
		}
	}
	return true
}

func decodeBGLTime24(b1, b2, b3 byte) (int, string) {
	val := int64(uint32(b1) | uint32(b2)<<8 | uint32(b3)<<16)
	// 1664.4 == 8322 / 5  -> val / 1664.4 == val * 5 / 8322
	num := val * 5
	denom := int64(8322)
	totalMinutes := int((num + denom/2) / denom) // rounded
	day := totalMinutes / 1440
	minsOfDay := totalMinutes % 1440
	if minsOfDay < 0 {
		minsOfDay += 1440
	}
	h := minsOfDay / 60
	m := minsOfDay % 60
	return day, fmt.Sprintf("%02d:%02d:00", h, m)
}

func decodeTime3(b []byte) (string, bool) {
	if len(b) < 3 {
		return "", false
	}
	h, m, s := int(b[0]), int(b[1]), int(b[2])
	if h >= 0 && h < 24 && m >= 0 && m < 60 && s >= 0 && s < 60 {
		return fmt.Sprintf("%02d:%02d:%02d", h, m, s), true
	}
	return "", false
}

func decodeFlightNum(block []byte) (int, string, int) {
	// returns (number int, rawHex string, rawVal int)
	if len(block) >= 16 {
		val := int(block[14]) | int(block[15])<<8
		rawHex := fmt.Sprintf("%02X%02X", block[15], block[14])
		if val > 0 {
			printed := val / 4
			if printed > 0 && printed < 100000 {
				return printed, rawHex, val
			}
		}
		if len(block) > 1 {
			fn := int(block[1])
			if fn > 0 && fn < 100000 {
				return fn, rawHex, val
			}
		}
		return 0, rawHex, val
	}
	if len(block) > 1 {
		fn := int(block[1])
		if fn > 0 && fn < 100000 {
			return fn, "----", 0
		}
	}
	return 0, "----", 0
}

func validateLeg(block []byte) bool {
	if ICAO_OFFSET+ICAO_LEN > len(block) {
		return false
	}
	cand := block[ICAO_OFFSET : ICAO_OFFSET+ICAO_LEN]
	if !isPrintableASCII(cand) || !icaoRe.Match(cand) {
		return false
	}
	if _, ok := decodeTime3(block[2:5]); ok {
		return true
	}
	if _, ok := decodeTime3(block[6:9]); ok {
		return true
	}
	// accept if ICAO valid even if times not strictly valid
	return true
}

func isPrintableASCII(b []byte) bool {
	for _, c := range b {
		if c < 0x20 || c > 0x7E {
			return false
		}
	}
	return true
}

// decodeFlightLevel decodes the flight level
func decodeFlightLevel(block []byte) int {
	if len(block) <= FL_OFFSET {
		if len(block) > FL_OFFSET {
			return int(block[FL_OFFSET])
		}
		return 0
	}
	primary := int(block[FL_OFFSET])
	if primary >= 0 && primary <= 128 {
		return 255 + primary
	}
	return primary
}

func collectLegs(data []byte) ([]flightplan.ScheduledFlight, map[string]bool) {
	const firstICAOOffset = 18
	n := len(data)
	var out []flightplan.ScheduledFlight
	airportICAOlist := make(map[string]bool)

	i := 0
	for i < n {

		if !isRegCharUpper(data[i]) || i == 0 || data[i-1] != 0x00 {
			i++
			continue
		}

		j := i
		for j < n && isRegCharUpper(data[j]) {
			j++
		}
		if j == i || j-i > 8 || j-i < 2 {
			i = j
			continue
		}
		if j >= n || !(data[j] == 0x00 || data[j] == 0x20) {
			i = j
			continue
		}
		// end of reg identifier
		if data[i+9] != 0x07 && data[i+9] != 0x17 && data[i+9] != 0x18 && data[i+9] != 0x19 && data[i+9] != 0x1A {
			i = j
			continue
		}

		regStr := string(data[i:j])

		icao1Pos := i + firstICAOOffset
		if icao1Pos+ICAO_LEN >= n || !isICAO(data[icao1Pos:icao1Pos+ICAO_LEN]) {
			i = j
			continue
		}

		regEnd := j
		foundAlign := -1
		for shift := 0; shift <= ALIGN_SEARCH_MAX && regEnd+shift+LEG_SIZE <= n; shift++ {
			block := data[regEnd+shift : regEnd+shift+LEG_SIZE]
			if validateLeg(block) {
				foundAlign = regEnd + shift
				break
			}
		}
		if foundAlign == -1 {
			i = j
			continue
		}

		cursor := foundAlign
		invalidCount := 0

		var blockLegs []flightplan.ScheduledFlight
		var rawFlightNums []int

		for cursor+LEG_SIZE <= n {

			block := data[cursor : cursor+LEG_SIZE]
			if !validateLeg(block) {
				invalidCount++
				if invalidCount >= INVALID_LEG_TOLERANCE {
					break
				}
				cursor += LEG_SIZE
				continue
			}
			invalidCount = 0

			icaoDest := string(block[ICAO_OFFSET : ICAO_OFFSET+ICAO_LEN])
			fn, _, _ := decodeFlightNum(block)

			rawFlightNums = append(rawFlightNums, fn)

			dd, dt := decodeBGLTime24(block[1], block[2], block[3])
			ad, at := decodeBGLTime24(block[5], block[6], block[7])

			if dd > ad && dd != 6 {
				logger.Log.Warn("invalid leg ", regStr, " ", icaoDest)
			}

			depHour, depMin := 0, 0
			arrHour, arrMin := 0, 0

			if len(dt) >= 5 {
				parts := strings.Split(dt, ":")
				if len(parts) >= 2 {
					depHour, _ = strconv.Atoi(parts[0])
					depMin, _ = strconv.Atoi(parts[1])
				}
			}
			if len(at) >= 5 {
				parts := strings.Split(at, ":")
				if len(parts) >= 2 {
					arrHour, _ = strconv.Atoi(parts[0])
					arrMin, _ = strconv.Atoi(parts[1])
				}
			}

			cruise := decodeFlightLevel(block)

			sf := flightplan.ScheduledFlight{
				AircraftRegistration: regStr,
				Number:               0, // assign later
				IcaoOrigin:           "",
				IcaoDest:             icaoDest,
				DepartureDayOfWeek:   dd,
				DepatureHour:         depHour,
				DepartureMin:         depMin,
				ArrivalDayOfWeek:     ad,
				ArrivalHour:          arrHour,
				ArrivalMin:           arrMin,
				CruiseAlt:            cruise,
			}

			blockLegs = append(blockLegs, sf)
			cursor += LEG_SIZE
		}

		if len(blockLegs) > 0 {

			// Flight number in each leg applies to NEXT leg
			if len(rawFlightNums) == len(blockLegs) {

				for idx := range blockLegs {
					prev := idx - 1
					if prev < 0 {
						prev = len(rawFlightNums) - 1
					}
					blockLegs[idx].Number = rawFlightNums[prev]
				}
			}

			// --- ORIGIN ASSIGNMENT ---
			for k := 1; k < len(blockLegs); k++ {
				blockLegs[k].IcaoOrigin = blockLegs[k-1].IcaoDest
			}
			blockLegs[0].IcaoOrigin = blockLegs[len(blockLegs)-1].IcaoDest

			for _, s := range blockLegs {
				airportICAOlist[s.IcaoOrigin] = true
				airportICAOlist[s.IcaoDest] = true
				out = append(out, s)
			}
		}

		i = regEnd + 1
	}

	return out, airportICAOlist
}
