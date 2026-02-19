package atc

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"bufio"
	"math"

	"github.com/curbz/decimal-niner/pkg/geometry"
)

func parseApt(path string, requiredICAOs map[string]bool) ([]Controller, map[string]AirportCoords, error) {
    airportLocations := make(map[string]AirportCoords)
    var controllers []Controller

    file, err := os.Open(path)
    if err != nil {
        return nil, airportLocations, fmt.Errorf("failed to open airports data file: %w", err)
    }
    defer file.Close()

    scanner := bufio.NewScanner(file)
    var curICAO, curName string
    var curLat, curLon float64
    var isRequiredAirport bool
    var batchStartIdx int // Tracks start of controllers for the current frequency

    roleMap := map[string]int{
        "1051": 0, // Unicom / CTAF
        "1052": 1, // Delivery
        "1053": 2, // Ground
        "1054": 3, // Tower
        "1056": 4, // Departure (TRACON)
        "1055": 5, // Approach (TRACON)
        "1050": 6, // Center (Enroute)
    }

    for scanner.Scan() {
        line := strings.TrimSpace(scanner.Text())
        p := strings.Fields(line)
        if len(p) < 2 {
            continue
        }
        code := p[0]

        // 1. Header Record (Airport ICAO and Name)
        if code == "1" || code == "16" || code == "17" {
            if len(p) >= 5 {
                curICAO = p[4]
                
                // FILTER: Check if this airport is in our schedules
                _, isRequiredAirport = requiredICAOs[curICAO]

                // Global Exclusion: Never store helipads or closed strips
                if strings.HasPrefix(curICAO, "[H]") || strings.HasPrefix(curICAO, "[X]") {
                    isRequiredAirport = false
                }

                curName = strings.Join(p[5:], " ")
                curLat, curLon = 0, 0 // Reset for new airport
            }
            continue
        }

        // 2. Runway Record (Capture Coordinates for Airport Center)
        if isRequiredAirport && (code == "100" || code == "101" || code == "102") {
            if len(p) >= 11 {
                la, _ := strconv.ParseFloat(p[9], 64)
                lo, _ := strconv.ParseFloat(p[10], 64)
                
                // Use first valid runway point as airport center reference
                if math.Abs(la) > 0.1 && curLat == 0 {
                    curLat, curLon = la, lo
                    airportLocations[curICAO] = AirportCoords{
                        Lat:  curLat,
                        Lon:  curLon,
                        Name: curName,
                    }
                }
            }
        }

        // 3. Frequency Records
        if rID, ok := roleMap[code]; ok {
            isEnroute := rID >= 4

            if isRequiredAirport || isEnroute {
                fRaw, _ := strconv.Atoi(p[1])
                fNorm := normalizeFreq(fRaw)

                // Track the beginning of this specific frequency batch
                batchStartIdx = len(controllers)

                // Aliasing: Unicom (1051) and Tower (1054) imply Ground/Delivery availability
                roles := []int{rID}
                if code == "1051" || code == "1054" {
                    roles = []int{rID, 1, 2} 
                }

                for _, r := range roles {
                    controllers = append(controllers, Controller{
                        Name:    curName,
                        ICAO:    curICAO,
                        RoleID:  r,
                        Freqs:   []int{fNorm},
                        IsPoint: true,
                        // Initial coords (will be refined by 1100 or Fixup Step)
                        Lat: curLat,
                        Lon: curLon,
                    })
                }
            }
        }

        // 4. Specific Transmitter Location (The 1100 Record)
        // This applies to the controllers added immediately before it
        if code == "1100" && len(controllers) > 0 {
            la, _ := strconv.ParseFloat(p[1], 64)
            lo, _ := strconv.ParseFloat(p[2], 64)
            if math.Abs(la) > 0.1 {
                for i := batchStartIdx; i < len(controllers); i++ {
                    controllers[i].Lat = la
                    controllers[i].Lon = lo
                }
            }
        }
    }

    // --- FIXUP & MBB INITIALIZATION ---
    for i := range controllers {
        c := &controllers[i]
        
        // If Lat/Lon is still 0 (Freq appeared before Runway and no 1100 record found)
        if c.Lat == 0 {
            if loc, exists := airportLocations[c.ICAO]; exists {
                c.Lat = loc.Lat
                c.Lon = loc.Lon
            }
        }

        // Initialize Point-Controller as a tiny MBB (Min == Max)
        // This makes points compatible with the polygon search logic in LocateController
        c.Airspaces = []Airspace{
            {
                Floor:   -99999,
                Ceiling: 99999,
                Area:    0, // Most specific possible area
                MinLat:  c.Lat,
                MaxLat:  c.Lat,
                MinLon:  c.Lon,
                MaxLon:  c.Lon,
            },
        }
    }

    return controllers, airportLocations, nil
}

func parseATCdatFiles(path string, isRegion bool, requiredICAOs map[string]bool) ([]Controller, error) {
    file, err := os.Open(path)
    if err != nil {
        return nil, fmt.Errorf("failed to open generic data file: %w", err)
    }
    defer file.Close()

    roleMap := map[string]int{
        "del":    1,
        "gnd":    2,
        "twr":    3,
        "tracon": 4, // Approach/Departure
        "ctr":    6, // Standardized to 6 to match Center logic in parseApt function
    }

    scanner := bufio.NewScanner(file)
    var list []Controller
    var cur *Controller
    var curPoly *Airspace
    var isRequired bool // Track if the current controller block should be kept

    for scanner.Scan() {
        line := strings.TrimSpace(scanner.Text())
        if line == "" || line[0] == '#' {
            continue
        }
        p := strings.Fields(line)

        switch strings.ToUpper(p[0]) {
        case "CONTROLLER":
            cur = &Controller{IsRegion: isRegion, IsPoint: false}
            isRequired = true // Default to true until we check the ICAO/Role
        case "NAME":
            if cur != nil {
                cur.Name = strings.Join(p[1:], " ")
            }
        case "FACILITY_ID", "ICAO":
            if cur != nil {
                cur.ICAO = p[1]
            }
        case "ROLE":
            if cur != nil {
                roleStr := strings.ToLower(p[1])
                cur.RoleID = roleMap[roleStr]
                
                // FILTER LOGIC:
                // If it's a local airport role (DEL, GND, TWR), check requiredICAOs.
                // If it's a wide-area role (TRACON, CTR), always keep it.
                if roleStr == "del" || roleStr == "gnd" || roleStr == "twr" {
                    if _, found := requiredICAOs[cur.ICAO]; !found {
                        isRequired = false
                    }
                }
            }
        case "FREQ", "CHAN":
            if cur != nil && isRequired {
                fRaw, _ := strconv.Atoi(p[1])
                cur.Freqs = append(cur.Freqs, normalizeFreq(fRaw))
            }
        case "AIRSPACE_POLYGON_BEGIN":
            if !isRequired { continue }
            f, c := -99999.0, 99999.0
            if len(p) >= 3 {
                f, _ = strconv.ParseFloat(p[1], 64)
                c, _ = strconv.ParseFloat(p[2], 64)
            }
            curPoly = &Airspace{Floor: f, Ceiling: c}
        case "POINT":
            if !isRequired { continue }
            la, _ := strconv.ParseFloat(p[1], 64)
            lo, _ := strconv.ParseFloat(p[2], 64)
            if curPoly != nil {
                curPoly.Points = append(curPoly.Points, [2]float64{la, lo})
            }
            if cur != nil && cur.Lat == 0 {
                cur.Lat, cur.Lon = la, lo
            }
        case "AIRSPACE_POLYGON_END":
            if cur != nil && curPoly != nil {
                curPoly.Area = geometry.CalculateRoughArea(curPoly.Points)
                
                // 1. Initialize bounds
                minLa, maxLa := 90.0, -90.0
                minLo, maxLo := 180.0, -180.0
                
                // 2. Standard Lat bounds
                for _, p := range curPoly.Points {
                    if p[0] < minLa { minLa = p[0] }
                    if p[0] > maxLa { maxLa = p[0] }
                }
                
                // 3. Smart Longitude bounds (detect wrap-around)
                // Find the "gap" in longitude to determine if we cross the dateline
                actualMinLo, actualMaxLo := 180.0, -180.0
                hasEast := false
                hasWest := false
                
                for _, p := range curPoly.Points {
                    lon := p[1]
                    if lon > 0 { hasEast = true }
                    if lon < 0 { hasWest = true }
                    if lon < actualMinLo { actualMinLo = lon }
                    if lon > actualMaxLo { actualMaxLo = lon }
                }

                // If a polygon has points in both East and West AND spans a huge distance,
                // it's a dateline crosser (like Anchorage)
                if hasEast && hasWest && (actualMaxLo - actualMinLo > 180) {
                    // Anchorage Case: Min is the smallest positive, Max is the largest negative
                    // effectively "wrapping around" the back of the map.
                    minLo, maxLo = 180.0, -180.0
                    for _, p := range curPoly.Points {
                        if p[1] > 0 && p[1] < minLo { minLo = p[1] } // Smallest East (e.g. 165)
                        if p[1] < 0 && p[1] > maxLo { maxLo = p[1] } // Largest West (e.g. -140)
                    }
                } else {
                    // Standard case
                    minLo, maxLo = actualMinLo, actualMaxLo
                }

                curPoly.MinLat, curPoly.MaxLat = minLa, maxLa
                curPoly.MinLon, curPoly.MaxLon = minLo, maxLo
                
                cur.Airspaces = append(cur.Airspaces, *curPoly)
            }
            curPoly = nil
        case "CONTROLLER_END":
            if cur != nil && isRequired {
                list = append(list, *cur)
            }
            cur = nil
            isRequired = false
        }
    }
    return list, nil
}

// convertIcaoToIso takes a full ICAO airport code (e.g., "EGLL") or
// a country prefix (e.g., "EG") and returns the ISO country code.
func convertIcaoToIso(icao string) (string, error) {
	icao = strings.ToUpper(strings.TrimSpace(icao))
	if len(icao) < 1 {
		return "", fmt.Errorf("invalid ICAO code")
	}

	// 1. Check for 2-letter prefix match (most common)
	if len(icao) >= 2 {
		prefix2 := icao[:2]
		if iso, ok := icaoToIsoMap[prefix2]; ok {
			return iso, nil
		}
	}

	// 2. Check for 1-letter prefix match (Major countries)
	prefix1 := icao[:1]
	if iso, ok := icaoToIsoMap[prefix1]; ok {
		return iso, nil
	}

	return "", fmt.Errorf("no ISO mapping found for ICAO code: %s", icao)
}
