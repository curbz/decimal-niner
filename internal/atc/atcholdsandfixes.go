package atc

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

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

	lat := ac.Flight.Position.Lat
	lng := ac.Flight.Position.Long
	runway := ac.Flight.AssignedRunway
	phase := ac.Flight.Phase.Current

	latRad := lat * math.Pi / 180
	lonRad := lng * math.Pi / 180
	ux, uy, uz := toUnit(latRad, lonRad)

	// 1. Get the Airport from the Service
	airport, airportExists := s.Airports[icao]

	// 2. Handle Prioritization Logic
	if airportExists && len(airport.Holds) > 0 {

		// A. GO-AROUND: Specifically look for the MAFix
		if phase == flightphase.GoAround.Index() {
			// Find the MAFix name for the specific runway
			// runway is normalized (e.g., "27R")
			var targetFix string
			if r, ok := airport.Runways[runway]; ok {
				targetFix = r.MAFix
			}

			if targetFix != "" {
				// Search the airport's local holds for this name
				for _, h := range airport.Holds {
					if h.Ident == targetFix {
						ac.Flight.AssignedHold = h
					}
				}
			}
			// If no MAFix match found, fall back to nearest airport hold
		}

		// B. Arrival phase - if STAR is assigned return exit fix if this is a defined holding point
		if phase == flightphase.Arrival.Index() {
			if ac.Flight.AssignedSTAR != nil {
				if ac.Flight.AssignedSTAR.Exit.Fix.Hold != nil {
					ac.Flight.AssignedHold = ac.Flight.AssignedSTAR.Exit.Fix.Hold
				}
			}
		}

		// C. OTHER PHASES (or fallback): Find nearest hold in Airport.Holds
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
			ac.Flight.AssignedHold = bestAirportHold
		}
	}

	// 3. GLOBAL FALLBACK: Find nearest in Service.Holds
	var bestGlobalHold *Hold
	bestDotGlobal := -2.0
	for _, h := range s.Holds {
		dot := ux*h.X + uy*h.Y + uz*h.Z
		if dot > bestDotGlobal {
			bestDotGlobal = dot
			bestGlobalHold = h
		}
	}

	ac.Flight.AssignedHold = bestGlobalHold
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
	return fixes, nil

}
