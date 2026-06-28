package atc

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/curbz/decimal-niner/internal/constants"
	"github.com/curbz/decimal-niner/internal/flightphase"
	"github.com/curbz/decimal-niner/internal/logger"
	"github.com/curbz/decimal-niner/pkg/geometry"
)

type Hold struct {
	Ident    string
	Region   string
	FullName string
	ICAO     string // airport ICAO or 'ENRT'
	MinAlt   int
	MaxAlt   int
	Lat      float64
	Lon      float64
	X, Y, Z  float64
}

type Fix struct {
	Ident    string
	Region   string
	FullName string
	Lat      float64
	Lon      float64
	Hold     *Hold // if this fix is also a hold, this field will be populated otherwise nil
}

type ProcedureFix struct {
	Fix            *Fix
	ConstraintAlt  int
	ConstraintType int // 0 = at, 1 = at or above, 2 = at or below
}

func loadHolds(navDataFile, holdsDataFile, fixesFile string) (map[string]*Hold, map[string][]*Hold, map[string]*Fix, error) {

	allFixes, err := parseFixData(fixesFile)
	if err != nil {
		return nil, nil, nil, err
	}
	logger.Log.Infof("%d fixes read from fix data", len(allFixes))

	namedFixes, err := parseNavData(navDataFile)
	if err != nil {
		return nil, nil, nil, err
	}
	logger.Log.Infof("%d navaids read from nav data", len(namedFixes))

	// Merge maps: namedFixes takes priority over allFixes
	for key, fix := range allFixes {
		if _, exists := namedFixes[key]; !exists {
			namedFixes[key] = fix
		}
	}
	allFixes = namedFixes
	logger.Log.Infof("consolidated fix count: %d", len(allFixes))

	allHolds, airportHolds, err := parseHoldData(holdsDataFile)
	if err != nil {
		return nil, nil, nil, err
	}
	logger.Log.Infof("%d holds read from holds data", len(allHolds))

	resolveHoldCoordinates(allHolds, allFixes)

	return allHolds, airportHolds, allFixes, nil

}

func toUnit(latRad, lonRad float64) (x, y, z float64) {
	clat := math.Cos(latRad)
	return clat * math.Cos(lonRad),
		clat * math.Sin(lonRad),
		math.Sin(latRad)
}

func (h *Hold) InitUnitVector() {
	// Input is Degrees, convert to Radian for the math
	radLat := geometry.DegToRad(h.Lat)
	radLon := geometry.DegToRad(h.Lon)

	h.X, h.Y, h.Z = toUnit(radLat, radLon)
}

func parseFloat(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

func parseInt(s string) int {
	i, _ := strconv.Atoi(s)
	return i
}

// enrich holds with lat/lon from fixes, and precompute unit vectors for nearest-hold search
func resolveHoldCoordinates(allHolds map[string]*Hold, allFixes map[string]*Fix) {

	namedCnt := 0
	enrichedCnt := 0

	for _, h := range allHolds {

		key := h.Ident + "_" + h.Region

		namedFix, found := allFixes[key]
		if found {
			h.FullName = namedFix.FullName
			h.Lat = namedFix.Lat
			h.Lon = namedFix.Lon
			namedFix.Hold = h
			if h.FullName != "" {
				namedCnt++
			}
			enrichedCnt++
		} else {
			logger.Log.Warn("hold not found in fix map for key ", key)
			continue
		}

		h.InitUnitVector()
	}
	logger.Log.Infof("%d holds were enriched with full names", namedCnt)
	logger.Log.Infof("%d holds were enriched with coordinates", enrichedCnt)
}

// AssignHold will return the most appropriate hold based on flight phase.
// For go around phase, the first attempt is to assign the assigned runway's missed approach fix.
// For the arrival phase, a check is performed to see if a STAR is assigned and if the STAR exit
// is a defined hold, this will be the assigned hold.
// For all other phases, and as a backup to the go around phase, the nearest hold for the airport is assigned.
func (s *Service) AssignHold(ac *Aircraft, icao string) {
    holding := &Holding{}
    ac.Flight.Holding = holding

    airport := s.GetAirportByICAO(icao)
    originAp := s.GetAirportByICAO(ac.Flight.Origin)

    // 1. Resolve Target Approach Constraints
    if ac.Flight.AssignedSTAR != nil && ac.Flight.AssignedSTAR.Exit != nil {
        holding.TargetApproachFix = ac.Flight.AssignedSTAR.Exit.Fix
        holding.TargetApproachAlt = float64(ac.Flight.AssignedSTAR.Exit.ConstraintAlt)
    }

    if holding.TargetApproachFix == nil {
        var star *Procedure
        // Only look for a STAR if the destination airport object actually exists
        if airport != nil {
            star = s.GetMatchingSTAR(airport, ac.Flight.AssignedRunway, originAp)
        }
        
        if star != nil && star.Exit != nil {
            holding.TargetApproachFix = star.Exit.Fix
            holding.TargetApproachAlt = float64(star.Exit.ConstraintAlt)
        } else {
			// Reciprocal runway heading projection straight down the extended centerline
			var projectHeading float64
			if ac.Flight.AssignedRunway != nil {
				projectHeading = math.Mod(ac.Flight.AssignedRunway.Heading+180.0, 360.0)
			} else if originAp != nil && airport != nil {
				routeBearing := geometry.CalculateBearing(originAp.Lat, originAp.Lon, airport.Lat, airport.Lon)
				projectHeading = geometry.NormalizeHeading(routeBearing + 180.0)
			} else {
				projectHeading = 0.0 // absolute fallback if completely blind
			}

			// Establish a safe anchor reference coordinate block
			refLat, refLon := 51.470, -0.454 // Default to global center baseline if missing
			if airport != nil {
				refLat, refLon = airport.Lat, airport.Lon
			}

			targetLat, targetLon := geometry.Project(refLat, refLon, projectHeading, constants.DefaultArrivalExitApproachEntryNM)
			holding.TargetApproachFix = &Fix{
				Lat:      targetLat,
				Lon:      targetLon,
				Ident:    "SYN-APPR",
				FullName: "Synthetic Approach Transition Gate",
			}
		}
	}

	// Safety fallback for vertical profiling floor
	if holding.TargetApproachAlt == 0.0 {
		var airportElev float64
		if airport != nil {
			airportElev = GetElevation(airport, ac.Flight.AssignedRunway)
		}
		holding.TargetApproachAlt = airportElev + float64(constants.DefaultArrivalExitApproachEntryAltFt)
	}

	// 2. Resolve Active Hold Spatial Point Location
	lat := ac.Flight.Position.Lat
	lng := ac.Flight.Position.Long
	runway := ac.Flight.AssignedRunwayName
	phase := ac.Flight.Phase.Current

	latRad := geometry.DegToRad(lat)
	lonRad := geometry.DegToRad(lng)
	ux, uy, uz := toUnit(latRad, lonRad)

	// DEFENSIVE GUARD: Verify the airport pointer isn't nil before checking its local holds
	if airport != nil && len(airport.Holds) > 0 {
		// A. GO-AROUND: Specifically look for the MAFix
		if phase == flightphase.GoAround.Index() {
			var targetFix string
			if r, ok := airport.Runways[runway]; ok {
				targetFix = r.MAFix
			}

			if targetFix != "" {
				for _, h := range airport.Holds {
					if h.Ident == targetFix {
						holding.AssignedHold = h
						return
					}
				}
			}
		}

		// B. Arrival phase - check if defined STAR holding point exists
		if phase == flightphase.Arrival.Index() {
			if ac.Flight.AssignedSTAR != nil && ac.Flight.AssignedSTAR.Exit.Fix.Hold != nil {
				holding.AssignedHold = ac.Flight.AssignedSTAR.Exit.Fix.Hold
				return
			}
		}

		// C. FALLBACK: Nearest airport terminal hold
		var bestAirportHold *Hold
		bestDot := -2.0
		for _, h := range airport.Holds {
			dot := ux*h.X + uy*h.Y + uz*h.Z
			if dot > bestDot {
				bestDot = dot
				bestAirportHold = h
			}
		}
		if bestAirportHold != nil {
			holding.AssignedHold = bestAirportHold
			return
		}
	}

	// 3. GLOBAL FALLBACK: Find nearest regional en-route fix structure hold
	var bestGlobalHold *Hold
	bestDotGlobal := -2.0
	for _, h := range s.Holds {
		dot := ux*h.X + uy*h.Y + uz*h.Z
		if dot > bestDotGlobal {
			bestDotGlobal = dot
			bestGlobalHold = h
		}
	}

	holding.AssignedHold = bestGlobalHold
	holding.PatternEntryTime = s.GetCurrentZuluTime() // Set once here!
    holding.ArrivedAtHoldFix = false
    holding.ExitingHold = false
}

// extract all holds from hold data file. returns two maps or an error
func parseHoldData(path string) (map[string]*Hold, map[string][]*Hold, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("error opening file %s: %w", path, err)
	}
	defer f.Close()

	allHolds := make(map[string]*Hold)
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
			Ident:  fields[0],
			Region: fields[1],
			ICAO:   fields[2], // airport ICAO or 'ENRT'
			MinAlt: parseInt(fields[8]),
			MaxAlt: parseInt(fields[9]),
		}

		key := h.Ident + "_" + h.Region
		allHolds[key] = h

		if h.ICAO != "ENRT" {
			hSlice, exists := airportHolds[h.ICAO]
			if !exists {
				hSlice = []*Hold{}
			}
			hSlice = append(hSlice, h)
			airportHolds[h.ICAO] = hSlice
		}
	}

	return allHolds, airportHolds, scan.Err()
}

func parseNavData(path string) (map[string]*Fix, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("error opening file %s: %w", path, err)
	}
	defer f.Close()

	fixes := make(map[string]*Fix)
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
		fixes[key] = &Fix{
			Ident:    ident,
			Region:   region,
			FullName: cleanFixName(fullName),
			Lat:      lat,
			Lon:      lon,
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

func parseFixData(path string) (map[string]*Fix, error) {

	fixes := make(map[string]*Fix)

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

		if lat < -90 || lat > 90 {
			continue
		}

		ident := parts[2]
		region := parts[4]

		key := ident + "_" + region
		fixes[key] = &Fix{
			Ident:  ident,
			Region: region,
			Lat:    lat,
			Lon:    lon,
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scanner error: %w", err)
	}
	return fixes, nil

}
