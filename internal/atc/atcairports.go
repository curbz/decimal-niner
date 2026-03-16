package atc

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/curbz/decimal-niner/pkg/geometry"
)

type Airport struct {
	ICAO     string
	Name        string
	Lat         float64
	Lon         float64
	TransAlt    int
	Region      string
	Runways     map[string]*Runway // keyed by "09L", "27R"
	Holds       []*Hold
    Controllers []*Controller
}

type Runway struct {
	FAFalt       int    // Final approach fix altitude
	MAalt        int    // highest missed approach altitude
	MAHeading    int    // initial MA course (degrees)
	MAFix        string // only if HM leg exists
	BestApproach string // highest precision approach type
}

type Fix struct {
	Ident    string
	Region   string
	FullName string
	LatRad   float64
	LonRad   float64
}

type aptPoint struct {
    Lat, Lon float64
}

func (s *Service) GetClosestAirport(aiLat, aiLon float64) string {
	var closestICAO string
	minDist := 4.0 // 4 Nautical Miles threshold

	for icao, coords := range s.Airports {

		dist := geometry.DistNM(aiLat, aiLon, coords.Lat, coords.Lon)

		if dist < minDist {
			minDist = dist
			closestICAO = icao
		}
	}

	return closestICAO
}

func loadAirports(dir string, airports map[string]*Airport, requiredAirports map[string]bool,
	airportHolds map[string][]*Hold, globalHolds map[string]*Hold) error {

	for icao := range requiredAirports {

		// Parse airport CIFP data for runway, approach and fixes data
		path := filepath.Join(dir, icao+".dat")
		rwyMap, err := parseCIFP(path)
		var pathErr *fs.PathError
		if err != nil {
			if errors.As(err, &pathErr) {
				// if error is io/fs.PathError then prefix log message with WARN: otherwise report as error
				log.Println("WARN: CIFP file not found for airport", icao, ": ", err)
			} else {
				log.Println("error parsing CIFP file for airport", icao, ": ", err)
			}
			continue
		}

		ap, exists := airports[icao]
		if !exists {
			ap = &Airport{
				ICAO: icao,
			}
			airports[icao] = ap
		}

		ap.Runways = make(map[string]*Runway)
		ap.Holds = []*Hold{}

		// Add runways
		for rwy, data := range rwyMap {
			ap.Runways[rwy] = &data
		}

		// Add airport holds
		if hSlice, ok := airportHolds[icao]; ok {
			ap.Holds = append(ap.Holds, hSlice...)
		}

		// Ensure missed-approach holds are added - these can be defined as ENRT holds which is why they are not present in the
		// airportHolds map
		for _, rw := range ap.Runways {
			if rw.MAFix != "" {
				key := rw.MAFix + "_" + ap.Region
				if h, ok := globalHolds[key]; ok {
					// check hold not already present in array
					present := false
					if len(ap.Holds) > 0 {
						for _, aph := range ap.Holds {
							if aph.Ident == h.Ident && aph.Region == h.Region {
								// hold already present
								present = true
								break
							}
						}
					}
					if !present {
						ap.Holds = append(ap.Holds, h)
					}
				}
			}
		}
	}

	return nil
}

func parseCIFP(cifpPath string) (map[string]Runway, error) {
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

// returns nil if not found
func (s *Service) getAirportRunway(icao, rwy string) *Runway {
	var r *Runway
	if icao != "" && rwy != "" {
		ap, found := s.Airports[icao]
		if found {
			r, _ = ap.Runways[rwy]
		}
	}
	return r
}

func getAirportICAObyPhaseClass(ac *Aircraft) string {
	switch ac.Flight.Phase.Class {
	case PreflightParked, Departing:
		return ac.Flight.Origin
	case Cruising:
		return ""
	case Arriving, PostflightParked:
		return ac.Flight.Destination
	default:
		return ""
	}
}
