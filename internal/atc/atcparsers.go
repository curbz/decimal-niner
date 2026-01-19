package atc

import (

	"fmt"
	"os"
	"strconv"
	"strings"

	"bufio"
	"math"
)

func parseApt(path string) []Controller {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	var list []Controller
	var curICAO, curName string
	var curLat, curLon float64

	roleMap := map[string]int{
		"1051": 0, // Unicom / CTAF
		"1052": 1, // Delivery
		"1053": 2, // Ground
		"1054": 3, // Tower
		"1056": 4, // Departure
		"1055": 5, // Approach
	}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		p := strings.Fields(line)
		if len(p) < 2 {
			continue
		}
		code := p[0]

		if code == "1" || code == "16" || code == "17" {
			curLat, curLon = 0, 0
			if len(p) >= 5 {
				curICAO = p[4]
				curName = strings.Join(p[5:], " ")
			}
			continue
		}

		// Use Runway (100) to find the airport center
		if (code == "100" || code == "101" || code == "102") && curLat == 0 {
			if len(p) >= 11 {
				la, _ := strconv.ParseFloat(p[9], 64)
				lo, _ := strconv.ParseFloat(p[10], 64)
				if math.Abs(la) <= 90 {
					curLat, curLon = la, lo
				}
			}
		}

		fRaw, _ := strconv.Atoi(p[1])
		fNorm := fRaw
		for fNorm > 0 && fNorm < 100000 {
			fNorm *= 10
		}

		// ALIASSING LOGIC: If an airport has Unicom (1051) or Tower (1054),
		// it likely handles Ground/Delivery duties too.
		if code == "1051" || code == "1054" {
			roles := []int{3} // Tower
			if code == "1051" || code == "1054" {
				roles = append(roles, 1, 2)
			}
			for _, r := range roles {
				list = append(list, Controller{
					Name: curName, ICAO: curICAO, RoleID: r,
					Freqs: []int{fNorm}, Lat: curLat, Lon: curLon, IsPoint: true,
				})
			}
		} else if rID, ok := roleMap[code]; ok {
			list = append(list, Controller{
				Name: curName, ICAO: curICAO, RoleID: rID,
				Freqs: []int{fNorm}, Lat: curLat, Lon: curLon, IsPoint: true,
			})
		}
	}
	return list
}

func parseGeneric(path string, isRegion bool) []Controller {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	var list []Controller
	var cur *Controller
	var curPoly *Airspace
	roleMap := map[string]int{"del": 1, "gnd": 2, "twr": 3, "tracon": 4, "ctr": 5}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		p := strings.Fields(line)

		switch strings.ToUpper(p[0]) {
		case "CONTROLLER":
			cur = &Controller{IsRegion: isRegion, IsPoint: false}
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
				cur.RoleID = roleMap[strings.ToLower(p[1])]
			}
		case "FREQ", "CHAN":
			if cur != nil {
				f, _ := strconv.Atoi(p[1])
				for f > 0 && f < 100000 {
					f *= 10
				}
				cur.Freqs = append(cur.Freqs, f)
			}
		case "AIRSPACE_POLYGON_BEGIN":
			f, c := -99999.0, 99999.0
			if len(p) >= 3 {
				f, _ = strconv.ParseFloat(p[1], 64)
				c, _ = strconv.ParseFloat(p[2], 64)
			}
			curPoly = &Airspace{Floor: f, Ceiling: c}
		case "POINT":
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
				cur.Airspaces = append(cur.Airspaces, *curPoly)
			}
			curPoly = nil
		case "CONTROLLER_END":
			if cur != nil {
				list = append(list, *cur)
			}
			cur = nil
		}
	}
	return list
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


