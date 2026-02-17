package atc

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"bufio"
	"math"
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
				curLat, curLon = 0, 0
			}
			continue
		}

		// 2. Runway Record (Capture Coordinates for Airport Center)
		if isRequiredAirport && (code == "100" || code == "101" || code == "102") {
			if len(p) >= 11 {
				la, _ := strconv.ParseFloat(p[9], 64)
				lo, _ := strconv.ParseFloat(p[10], 64)
				// Avoid 0,0 or placeholder phantom points
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
			// LOGIC: Keep if it's a scheduled airport OR a high-level Enroute facility (4, 5, 6)
			isEnroute := rID >= 4

			if isRequiredAirport || isEnroute {
				fRaw, _ := strconv.Atoi(p[1])
				fNorm := normalizeFreq(fRaw)

				// Aliasing for small/major airports
				roles := []int{rID}
				if code == "1051" || code == "1054" {
					roles = []int{rID, 1, 2} // Include Del/Gnd
				}

				for _, r := range roles {
					controllers = append(controllers, Controller{
						Name:    curName,
						ICAO:    curICAO,
						RoleID:  r,
						Freqs:   []int{fNorm},
						IsPoint: true,
						// Initial coords; airport-specific ones are updated in fixup pass
						Lat: curLat,
						Lon: curLon,
					})
				}
			}
		}

		// 4. Specific Transmitter Location (Optional but good for Center/TRACON)
		if code == "1100" && len(controllers) > 0 {
			la, _ := strconv.ParseFloat(p[1], 64)
			lo, _ := strconv.ParseFloat(p[2], 64)
			if math.Abs(la) > 0.1 {
				// Update the coordinates of the controller we just added
				controllers[len(controllers)-1].Lat = la
				controllers[len(controllers)-1].Lon = lo
			}
		}
	}

	// --- FIXUP STEP ---
	// Ensures airport-specific controllers get the runway center coordinates
	for i := range controllers {
		// Only update if coords are 0 (means they haven't been set by an 1100 record)
		if controllers[i].Lat == 0 {
			if loc, exists := airportLocations[controllers[i].ICAO]; exists {
				controllers[i].Lat = loc.Lat
				controllers[i].Lon = loc.Lon
			}
		}
	}

	return controllers, airportLocations, nil
}

func parseGeneric(path string, isRegion bool, requiredICAOs map[string]bool) ([]Controller, error) {
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
        "ctr":    6, // Standardized to 6 to match your Center logic
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
            if cur != nil && curPoly != nil && isRequired {
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

func normalizeFreq(fRaw int) int {
	if fRaw == 0 {
		return 0
	}
	
	f := fRaw
	// X-Plane frequencies in apt.dat are often missing the trailing zero 
	// or decimal precision. We want to scale everything to 1xx.xxx format 
	// represented as an integer (e.g., 118050).
	
	for f < 100000 {
		f *= 10
	}
	
	// If the frequency ended up like 1180000 (too large), 
	// we trim it back down.
	for f > 999999 {
		f /= 10
	}
	
	return f
}