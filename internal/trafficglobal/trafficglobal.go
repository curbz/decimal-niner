package trafficglobal

import (
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/curbz/decimal-niner/pkg/util"
)

type FlightPhase int

const (
	Unknown FlightPhase = iota -1
	Cruise			// Normal cruise phase.
	Approach			// Positioning from cruise to the runway.
	Final				// Gear down on final approach.
	TaxiIn				// Any ground movement after touchdown.
	Shutdown			// Short period of spooling down engines/electrics.
	Parked				// Long period parked.
	Startup				// Short period of spooling up engines/electrics.
	TaxiOut				// Any ground movement from the gate to the runway.
	Depart				// Initial ground roll and first part of climb.
	GoAround			// Unplanned transition from approach to cruise.
	Climbout			// Remainder of climb, gear up.
	Braking				// Short period from touchdown to when fast-taxi speed is reached.
	Holding				// Holding, waiting for a flow to complete changing.
)

const (
    LEG_SIZE    = 16
    ICAO_OFFSET = 9
    ICAO_LEN    = 4
    FL_OFFSET   = 13

    ALIGN_SEARCH_MAX      = 128 
    INVALID_LEG_TOLERANCE = 2
)

var icaoRe = regexp.MustCompile(`^[A-Z]{4}$`)

// ScheduledFlight is the requested output struct for each parsed leg.
type ScheduledFlight struct {
    AircraftRegistration string
    Number               int
    IcaoOrigin           string
    IcaoDest             string
    DepartureDayOfWeek   int
    DepatureHour         int
    DepartureMin         int
    ArrivalDayOfWeek     int
    ArrivalHour          int
    ArrivalMin           int
    CruiseAlt            int
}

func (fp FlightPhase) String() string {
	return [...]string{
		"Unknown",
		"Cruise",
		"Approach",
		"Final",
		"Taxi In",
		"Shutdown",
		"Parked",
		"Startup",
		"Taxi Out",
		"Depart",
		"Go Around",
		"Climbout",
		"Braking",
		"Waiting for flow change",
	}[fp+1]
}

func (fp FlightPhase) Index() int {
	return int(fp)
}

type config struct {
	TG struct {
		BGLFile string          `yaml:"bgl_file"`
	} `yaml:"trafficglobal"`
}

func LoadConfig(cfgPath string) *config {
	
	log.Println("Loading Traffic Global configurations")

	cfg, err := util.LoadConfig[config](cfgPath)
	if err != nil {
		log.Fatalf("Error reading configuration file: %v\n", err)
	}
	
	return cfg
}

//TODO: pass in current sim time and only load flights that are either in progress
// or due to depart within 12 hours
func BGLReader(filePath string) map[string][]ScheduledFlight {

	log.Printf("Loading Traffic Global BGL file: %s\n", filePath)

	data, err := os.ReadFile(filePath)
	if err != nil {
		log.Fatalf("error reading bgl file: %v\n", err)
	}

	legs := collectAllLegsSequential(data)
    if len(legs) == 0 {
        log.Fatalf("no legs extracted from bgl file %s", filePath)
    }
	log.Printf("total legs extracted from bgl file: %d\n", len(legs))

	schedules := make(map[string][]ScheduledFlight)
	for _, l := range legs {
		key := fmt.Sprintf("%s_%d_%d", l.AircraftRegistration,l.Number,l.DepartureDayOfWeek)
		existingLegs, found := schedules[key]
		if found {
			schedules[key] = append(existingLegs, l)
			continue
		} else {
			schedules[key] = []ScheduledFlight{l}
		}

	}

	return schedules
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

func tryFlightNum(block []byte) (int, string, int) {
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

// looksLikeRegistrationAt detect if a registration starts at pos 
// requires the registration to be preceded by 0x00 and rejects 0x07 prefix (ICAO).
func looksLikeRegistrationAt(data []byte, pos int) bool {
    n := len(data)
    if pos <= 0 || pos >= n {
        return false
    }
    // registration must be preceded by 0x00 (not 0x07 which marks ICAO)
    if data[pos-1] != 0x00 {
        return false
    }
    // must start with reg char
    if !isRegCharUpper(data[pos]) {
        return false
    }
    // find end within 1..8 chars
    j := pos
    for j < n && isRegCharUpper(data[j]) {
        j++
    }
    if j == pos || j-pos > 8 || j-pos < 2 {
        return false
    }
    // terminator must be NUL or space
    if j >= n || !(data[j] == 0x00 || data[j] == 0x20) {
        return false
    }
    // quick ICAO sanity at expected offset
    firstICAOOffset := 18
    icaoPos := pos + firstICAOOffset
    if icaoPos+ICAO_LEN >= n {
        return false
    }
    if !isICAO(data[icaoPos : icaoPos+ICAO_LEN]) {
        return false
    }
    return true
}


// collectAllLegsSequential scans the file sequentially. When it finds a registration
// it attempts to align to the first leg within ALIGN_SEARCH_MAX bytes and then
// parses contiguous 16-byte legs until INVALID_LEG_TOLERANCE consecutive invalid legs
// or until another registration is encountered.
func collectAllLegsSequential(data []byte) []ScheduledFlight {
    const firstICAOOffset = 18
    n := len(data)
    var out []ScheduledFlight

    i := 0
    for i < n {
		// look for registration start: must be reg char and preceded by 0x00
		if !isRegCharUpper(data[i]) || i == 0 || data[i-1] != 0x00 {
			i++
			continue
		}
        // parse registration
        j := i
        for j < n && isRegCharUpper(data[j]) {
            j++
        }
        // must be reasonable length
        if j == i || j-i > 8 || j-i < 2 {
            i = j
            continue
        }
        // terminator must be NUL or space
        if j >= n || !(data[j] == 0x00 || data[j] == 0x20) {
            i = j
            continue
        }
        regStr := string(data[i:j])
        // validate that an ICAO exists at expected offset (quick sanity)
        icao1Pos := i + firstICAOOffset
        if icao1Pos+ICAO_LEN >= n || !isICAO(data[icao1Pos:icao1Pos+ICAO_LEN]) {
            // not a registration block we understand; skip ahead
            i = j
            continue
        }

        // attempt to find alignment for first leg starting at regEnd + shift
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
            // no leg block found nearby; continue scanning after registration
            i = j
            continue
        }

        // parse contiguous legs
        cursor := foundAlign
        invalidCount := 0
        var blockLegs []ScheduledFlight
        for cursor+LEG_SIZE <= n {
            // if a new registration starts here, stop parsing legs so outer loop can handle it
            if looksLikeRegistrationAt(data, cursor) {
                break
            }

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

            // extract fields
            icaoDest := string(block[ICAO_OFFSET : ICAO_OFFSET+ICAO_LEN])
            fn, _, _ := tryFlightNum(block)
            // departure bytes 1..3
            dd, dt := decodeBGLTime24(block[1], block[2], block[3])
            // arrival bytes 5..7
            ad, at := decodeBGLTime24(block[5], block[6], block[7])

            // parse hours/mins from dt and at ("HH:MM:00")
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

            sf := ScheduledFlight{
                AircraftRegistration: regStr,
                Number:               fn,
                IcaoOrigin:           "", // will set later (previous leg's dest)
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

        // if we collected legs, set IcaoOrigin for each leg as previous leg's dest,
        // and for the first leg set origin to last leg's dest (wrap-around) as requested.
        if len(blockLegs) > 0 {
            // set previous dest as origin
            for k := 1; k < len(blockLegs); k++ {
                blockLegs[k].IcaoOrigin = blockLegs[k-1].IcaoDest
            }
            // first leg origin = last leg dest
            blockLegs[0].IcaoOrigin = blockLegs[len(blockLegs)-1].IcaoDest

            // append to global out
            for _, s := range blockLegs {
                out = append(out, s)
            }
        }

        // advance i to just after the registration string (regEnd+1) so we don't skip
        // any registrations that may start inside or immediately after the parsed block.
        i = regEnd + 1
    }

    return out
}

