package atc

import (
	"bufio"
	"fmt"
    "log"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/curbz/decimal-niner/pkg/geometry"
)

type aptPoint struct {
    Lat, Lon float64
}

// parseApt processes the X-Plane apt.dat file with deep fallback logic for missing coordinates.
func parseApt(path string, requiredAirports map[string]bool) ([]*Controller, map[string]*Airport, error) {
	var controllers []*Controller
	airports := make(map[string]*Airport)

	file, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open airports data file %s: %w", path, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	var curAirport *Airport
	var curICAO, curName, region string
	var curLat, curLon float64
	var transAlt int
	var isRequiredAirport bool
	var batchStartIdx int
	var airportPoints []aptPoint

	roleMap := map[string]int{
		"1050": 6, "1051": 0, "1052": 1, "1053": 2, "1054": 3, "1055": 5, "1056": 4,
	}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		p := strings.Fields(line)
		if len(p) < 2 {
			continue
		}
		code := p[0]

		// 1. HEADER RECORD (New Airport Start)
		if code == "1" || code == "16" || code == "17" {
			if curICAO != "" {
				finalizeAirport(curAirport, curLat, curLon, airportPoints, controllers, curICAO)
			}

			if len(p) >= 5 {
				curICAO = p[4]
				curName = strings.Join(p[5:], " ")
				curLat, curLon, transAlt, region = 0, 0, 0, ""
				airportPoints = []aptPoint{}

				_, isRequiredAirport = requiredAirports[curICAO]
				if strings.HasPrefix(curICAO, "[H]") || strings.HasPrefix(curICAO, "[X]") {
					isRequiredAirport = false
				}

				if isRequiredAirport {
					curAirport = &Airport{ICAO: curICAO, Name: curName, Controllers: []*Controller{}}
					airports[curICAO] = curAirport
				} else {
					curAirport = nil
				}
			}
			continue
		}

		// 2. GEOGRAPHY & METADATA (Universal Parsing)
		if code == "1302" && len(p) == 3 {
			switch p[1] {
			case "datum_lat":
				curLat, _ = strconv.ParseFloat(p[2], 64)
			case "datum_lon":
				curLon, _ = strconv.ParseFloat(p[2], 64)
			case "transition_alt":
				transAlt, _ = strconv.Atoi(p[2])
			case "region_code":
				region = p[2]
			}
			if curAirport != nil {
				curAirport.TransAlt, curAirport.Region = transAlt, region
			}
			continue
		}

		// PRIMARY FALLBACK: Runways (100/101)
		if code == "100" || code == "101" {
			if len(p) >= 19 {
				la1, _ := strconv.ParseFloat(p[9], 64)
				lo1, _ := strconv.ParseFloat(p[10], 64)
				la2, _ := strconv.ParseFloat(p[17], 64)
				lo2, _ := strconv.ParseFloat(p[18], 64)
				if la1 != 0 && la2 != 0 {
					airportPoints = append(airportPoints, aptPoint{la1, lo1}, aptPoint{la2, lo2})
				}
			}
			continue
		}

		// SECONDARY FALLBACK: Helipads (102) or Ramps (1300)
		// Only used if no official 1302 datum was provided.
		if (code == "102" || code == "1300") && curLat == 0 {
			latIdx, lonIdx := 2, 3
			if code == "1300" {
				latIdx, lonIdx = 1, 2
			}
			if len(p) > lonIdx {
				la, _ := strconv.ParseFloat(p[latIdx], 64)
				lo, _ := strconv.ParseFloat(p[lonIdx], 64)
				if la != 0 {
					airportPoints = append(airportPoints, aptPoint{la, lo})
				}
			}
			continue
		}

		// 3. FREQUENCY RECORDS
		if rID, ok := roleMap[code]; ok {
			isEnroute := rID >= 4
			if isRequiredAirport || isEnroute {
				fRaw, _ := strconv.Atoi(p[1])
				fNorm := normalizeFreq(fRaw)
				batchStartIdx = len(controllers)

				roles := []int{rID}
				if code == "1051" || code == "1054" {
					roles = []int{rID, 1, 2}
				}

				for _, r := range roles {
					c := &Controller{
						Name:    curName,
						ICAO:    curICAO,
						RoleID:  r,
						Freqs:   []int{fNorm},
						IsPoint: true,
						Lat:     curLat,
						Lon:     curLon,
					}
					controllers = append(controllers, c)
					if curAirport != nil {
						curAirport.Controllers = append(curAirport.Controllers, c)
					}
				}
			}
			continue
		}

		// 4. TRANSMITTER OVERRIDE (1100)
		if code == "1100" && len(controllers) > 0 {
			la, _ := strconv.ParseFloat(p[1], 64)
			lo, _ := strconv.ParseFloat(p[2], 64)
			if math.Abs(la) > 0.1 {
				for i := batchStartIdx; i < len(controllers); i++ {
					controllers[i].Lat, controllers[i].Lon = la, lo
				}
			}
		}
	}

	// Finalize final block
	if curICAO != "" {
		finalizeAirport(curAirport, curLat, curLon, airportPoints, controllers, curICAO)
	}

	// 5. FINAL MBB INITIALIZATION
	for i := range controllers {
		c := controllers[i]
		if c.Lat == 0 && c.Lon == 0 {
			log.Printf("WARN: No position found for: %s %s\n", c.ICAO, c.Name)
		}
		c.Airspaces = []Airspace{{
			Floor: -99999, Ceiling: 99999, Area: 0,
			MinLat: c.Lat, MaxLat: c.Lat, MinLon: c.Lon, MaxLon: c.Lon,
		}}
	}

	return controllers, airports, nil
}

func finalizeAirport(a *Airport, dLat, dLon float64, pts []aptPoint, allCtrls []*Controller, icao string) {
	var fLat, fLon float64

	// Prioritize Datum, then Centroid
	if dLat != 0 {
		fLat, fLon = dLat, dLon
	} else if len(pts) > 0 {
		var sLa, sLo float64
		for _, p := range pts {
			sLa += p.Lat
			sLo += p.Lon
		}
		fLat = sLa / float64(len(pts))
		fLon = sLo / float64(len(pts))
	}

	if a != nil {
		a.Lat, a.Lon = fLat, fLon
	}

	// Retroactive update for any controllers created with 0,0
	for i := len(allCtrls) - 1; i >= 0; i-- {
		if allCtrls[i].ICAO != icao {
			if i < len(allCtrls)-100 { break } // Optimization
			continue
		}
		if allCtrls[i].Lat == 0 {
			allCtrls[i].Lat, allCtrls[i].Lon = fLat, fLon
		}
	}
}

func parseATCdatFiles(path string, isRegion bool, requiredICAOs map[string]bool) ([]*Controller, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open ATC data file %s: %w", path, err)
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
	var list []*Controller
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
			if !isRequired {
				continue
			}
			f, c := -99999.0, 99999.0
			if len(p) >= 3 {
				f, _ = strconv.ParseFloat(p[1], 64)
				c, _ = strconv.ParseFloat(p[2], 64)
			}
			curPoly = &Airspace{Floor: f, Ceiling: c}
		case "POINT":
			if !isRequired {
				continue
			}
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
					if p[0] < minLa {
						minLa = p[0]
					}
					if p[0] > maxLa {
						maxLa = p[0]
					}
				}

				// 3. Smart Longitude bounds (detect wrap-around)
				// Find the "gap" in longitude to determine if we cross the dateline
				actualMinLo, actualMaxLo := 180.0, -180.0
				hasEast := false
				hasWest := false

				for _, p := range curPoly.Points {
					lon := p[1]
					if lon > 0 {
						hasEast = true
					}
					if lon < 0 {
						hasWest = true
					}
					if lon < actualMinLo {
						actualMinLo = lon
					}
					if lon > actualMaxLo {
						actualMaxLo = lon
					}
				}

				// If a polygon has points in both East and West AND spans a huge distance,
				// it's a dateline crosser (like Anchorage)
				if hasEast && hasWest && (actualMaxLo-actualMinLo > 180) {
					// Anchorage Case: Min is the smallest positive, Max is the largest negative
					// effectively "wrapping around" the back of the map.
					minLo, maxLo = 180.0, -180.0
					for _, p := range curPoly.Points {
						if p[1] > 0 && p[1] < minLo {
							minLo = p[1]
						} // Smallest East (e.g. 165)
						if p[1] < 0 && p[1] > maxLo {
							maxLo = p[1]
						} // Largest West (e.g. -140)
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
				list = append(list, cur)
			}
			cur = nil
			isRequired = false
		}
	}
	return list, nil
}

func parseNavData(path string) (map[string]Fix, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("error opening file %s: %w", path, err)
	}
	defer f.Close()

	fixes := make(map[string]Fix)
	scan := bufio.NewScanner(f)

	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}

		rowType := fields[0]
		if rowType != "2" && rowType != "3" && rowType != "12" {
			continue
		}

		lat := parseFloat(fields[1])
		lon := parseFloat(fields[2])
		ident := fields[7]
		region := fields[9]
		fullName := strings.Join(fields[10:], " ")

		key := ident + "_" + region
		fixes[key] = Fix{
			Ident:    ident,
			Region:   region,
			FullName: cleanFixName(fullName),
			LatRad:   lat * math.Pi / 180,
			LonRad:   lon * math.Pi / 180,
		}
	}

	return fixes, scan.Err()
}

func cleanFixName(name string) string {

	name = name + " " // Add trailing space to simplify parsing logic

	if idx := strings.Index(name, "("); idx != -1 {
		name = name[:idx]
	}
	for _, marker := range []string{" VOR/DME ", " VORTAC ", " NDB ", " VOR ", " DME-ILS ", " DME "} {
		if idx := strings.Index(name, marker); idx != -1 {
			name = name[:idx]
			break
		}
	}

	name = strings.ToUpper(strings.TrimSpace(name))
	// if name has the suffix "INT" or "INTL" remove it
	for _, suffix := range []string{" INT", " INTL"} {
		if strings.HasSuffix(name, suffix) {
			name = strings.TrimSuffix(name, suffix)
			break
		}
	}

	return name

}

// extract all holds from hold data file. returns two maps or an error
func parseHoldData(path string) (map[string]*Hold, map[string][]*Hold, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("error opening file %s: %w", path, err)
	}
	defer f.Close()

	globalHolds := make(map[string]*Hold)
	airportHolds := make(map[string][]*Hold)

	scan := bufio.NewScanner(f)

	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "1140 Version") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 11 {
			continue
		}

		h := &Hold{
			Ident:   fields[0],
			Region:  fields[1],
			ICAO:    fields[2], // airport ICAO or 'ENRT'
			Seq:     parseInt(fields[3]),
			Inbound: parseFloat(fields[4]),
			LegTime: parseFloat(fields[5]),
			LegDist: parseFloat(fields[6]),
			Turn:    fields[7],
			MinAlt:  parseInt(fields[8]),
			MaxAlt:  parseInt(fields[9]),
			Speed:   parseInt(fields[10]),
		}

		key := h.Ident + "_" + h.Region
		globalHolds[key] = h

		if h.ICAO != "ENRT" {
			hSlice, exists := airportHolds[h.ICAO]
			if !exists {
				hSlice = []*Hold{}
			}
			hSlice = append(hSlice, h)
			airportHolds[h.ICAO] = hSlice
		}
	}

	return globalHolds, airportHolds, scan.Err()
}

func parseFixData(path string) (map[string]Fix, error) {

	fixes := make(map[string]Fix)

	file, err := os.Open(path)
	if err != nil {
		return fixes, fmt.Errorf("error opening file %s: %w", path, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.Fields(line)
		if len(parts) < 4 {
			continue
		}

		lat, _ := strconv.ParseFloat(parts[0], 64)
		lon, _ := strconv.ParseFloat(parts[1], 64)
		ident := parts[2]
		region := parts[4]

		key := ident + "_" + region
		fixes[key] = Fix{
			Ident:  ident,
			Region: region,
			LatRad: lat,
			LonRad: lon,
		}
	}
	return fixes, nil

}

func mergeRunway(existing, incoming Runway, appType string) Runway {
	// BestApproach: keep the better-ranked one
	if appType != "" {
		if rankNew, ok := approachRank[appType]; ok {
			if existing.BestApproach == "" {
				existing.BestApproach = approachString[appType]
			} else {
				// find rank of existing
				var existingType string
				for t, s := range approachString {
					if s == existing.BestApproach {
						existingType = t
						break
					}
				}
				if existingType != "" {
					if rankOld, ok2 := approachRank[existingType]; ok2 && rankNew < rankOld {
						existing.BestApproach = approachString[appType]
					}
				}
			}
		}
	}

	// FAFalt: keep lowest non-zero
	if incoming.FAFalt > 0 && (existing.FAFalt == 0 || incoming.FAFalt < existing.FAFalt) {
		existing.FAFalt = incoming.FAFalt
	}

	// MAalt: keep highest
	if incoming.MAalt > existing.MAalt {
		existing.MAalt = incoming.MAalt
	}

	// MAHeading: keep first non-zero
	if existing.MAHeading == 0 && incoming.MAHeading != 0 {
		existing.MAHeading = incoming.MAHeading
	}

	// MAFix: keep first non-empty
	if existing.MAFix == "" && incoming.MAFix != "" {
		existing.MAFix = incoming.MAFix
	}

	return existing
}

func ParseCIFP(cifpPath string) (map[string]Runway, error) {
	f, err := os.Open(cifpPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	runways := make(map[string]Runway)
	scan := bufio.NewScanner(f)

	var currentRunway string
	var rw Runway
	var inApproach bool
	var currentAppType string

	saveApproach := func() {
		if !inApproach || currentRunway == "" {
			return
		}

		existing := runways[currentRunway]

		// Only merge if this was a real approach (FAF or approach type)
		if rw.FAFalt > 0 || rw.BestApproach != "" {
			runways[currentRunway] = mergeRunway(existing, rw, currentAppType)
		} else {
			// Ensure runway entry exists, but keep it zeroed
			if _, ok := runways[currentRunway]; !ok {
				runways[currentRunway] = Runway{}
			}
		}
	}

	var sawRunwayLeg bool

	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())

		if strings.HasPrefix(line, "RWY:") {
			parts := strings.Split(line, ",")
			if len(parts) >= 1 {
				// Extract runway name from RWY:RW08
				rwyTag := strings.TrimPrefix(parts[0], "RWY:")
				rwy := strings.TrimSpace(rwyTag)
				rwy = normalizeRunway(rwy)
				if rwy != "" {
					// Ensure runway entry exists
					if _, ok := runways[rwy]; !ok {
						runways[rwy] = Runway{}
					}
				}
			}
			continue
		}

		if !strings.HasPrefix(line, "APPCH:") {
			continue
		}

		fields := strings.Split(line, ",")
		if len(fields) < 26 {
			continue
		}

		// Route/segment type: A, I, L, etc.
		routeType := strings.TrimSpace(fields[1])
		isFinal := routeType == "I" || routeType == "L" || routeType == "R" || routeType == "N"

		// Start of a new approach
		if strings.HasPrefix(fields[0], "APPCH:010") {
			// Save previous approach into runway
			saveApproach()

			// Reset for new approach
			inApproach = true
			currentRunway = ""
			rw = Runway{}
			currentAppType = ""
			sawRunwayLeg = false

			// Extract approach type from approach name
			name := strings.TrimSpace(fields[2])
			if name != "" {
				appType := string(name[0])
				if _, ok := approachRank[appType]; ok {
					currentAppType = appType
					rw.BestApproach = approachString[appType]
				}
			}
		}

		// Fix (can live in several fields)
		fix := ""
		fixCandidates := []int{4, 5, 13, 14}
		for _, idx := range fixCandidates {
			if idx < len(fields) {
				f := strings.TrimSpace(fields[idx])
				if f != "" {
					fix = f
					break
				}
			}
		}

		legType := strings.TrimSpace(fields[11])

		// Detect runway from RWxx fix
		if strings.HasPrefix(fix, "RW") {
			rwy := normalizeRunway(fix)
			if rwy != "" {
				currentRunway = rwy
				// Ensure runway entry exists even before merging
				if _, ok := runways[currentRunway]; !ok {
					runways[currentRunway] = Runway{}
				}
			}
		}

		// Altitude fields
		atOrAbove := strings.TrimSpace(fields[23])
		atAlt := strings.TrimSpace(fields[24])
		atOrBelow := strings.TrimSpace(fields[25])
		alt := lowestAltitudeOf(atAlt, atOrAbove, atOrBelow)

		// FAF detection (FFxx or FIxx)
		if (strings.HasPrefix(fix, "FF") || strings.HasPrefix(fix, "FI")) && alt >= 0 {
			if rw.FAFalt == 0 || alt < rw.FAFalt {
				rw.FAFalt = alt
			}
		}

		// Missed-approach detection (final segment only)
		isMA := isFinal && (strings.HasPrefix(fix, "MA") ||
			strings.HasPrefix(fix, "RW") ||
			strings.HasPrefix(fix, "FD") ||
			strings.HasPrefix(fix, "CI") ||
			legType == "CA" ||
			legType == "CF" ||
			legType == "HM" ||
			legType == "AF")

		// set missed approach altitude
		if isMA && alt > rw.MAalt {
			rw.MAalt = alt
		}

		// Detect RW leg
		if strings.HasPrefix(fix, "RW") {
			sawRunwayLeg = true
		}

		// Capture missed-approach heading
		if sawRunwayLeg && rw.MAHeading == 0 {
			if isMA {
				headingField := strings.TrimSpace(fields[20])
				if h, err := strconv.Atoi(headingField); err == nil {
					rw.MAHeading = h / 10
				}
			}
		}

		// MAFix: last MA leg's fix in final segment
		if isMA && !strings.HasPrefix(fix, "RW") {
			rw.MAFix = fix
		}
	}

	// Save last approach
	saveApproach()

	return runways, scan.Err()
}

func lowestAltitudeOf(at, above, below string) int {
	vals := []string{at, above, below}
	best := -1
	for _, v := range vals {
		if v == "" {
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			continue
		}
		if best == -1 || n < best {
			best = n
		}
	}
	return best
}

func normalizeRunway(rw string) string {
	// fix is like "RW27", "RW27L", "RW9", "RW09R"
	if !strings.HasPrefix(rw, "RW") {
		return ""
	}

	core := rw[2:] // strip "RW"

	// Extract digits
	i := 0
	for i < len(core) && core[i] >= '0' && core[i] <= '9' {
		i++
	}

	num := core[:i]
	suffix := core[i:] // L/R/C if present

	// Pad single-digit runways
	if len(num) == 1 {
		num = "0" + num
	}

	return num + suffix
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
