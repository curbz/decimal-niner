package atc

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/curbz/decimal-niner/internal/logger"
	"github.com/curbz/decimal-niner/internal/trafficglobal"
)

type Hold struct {
	Ident    string
	Region   string
	FullName string
	ICAO     string // airport ICAO or 'ENRT'
	Seq      int
	Inbound  float64
	LegTime  float64
	LegDist  float64
	Turn     string
	MinAlt   int
	MaxAlt   int
	Speed    int
	LatRad   float64
	LonRad   float64
	X, Y, Z  float64
}

func loadHolds(navDataFile, holdsDataFile, fixesFile string) (map[string]*Hold, map[string][]*Hold, error) {

	allFixes, err := parseFixData(fixesFile)
	if err != nil {
		return nil, nil, err
	}

	namedFixes, err := parseNavData(navDataFile)
	if err != nil {
		return nil, nil, err
	}

	globalHolds, airportHolds, err := parseHoldData(holdsDataFile)
	if err != nil {
		return nil, nil, err
	}
	resolveHoldCoordinates(globalHolds, namedFixes, allFixes)

	return globalHolds, airportHolds, nil

}

func toUnit(latRad, lonRad float64) (x, y, z float64) {
	clat := math.Cos(latRad)
	return clat * math.Cos(lonRad),
		clat * math.Sin(lonRad),
		math.Sin(latRad)
}

func (h *Hold) InitUnitVector() {
	h.X, h.Y, h.Z = toUnit(h.LatRad, h.LonRad)
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
func resolveHoldCoordinates(holds map[string]*Hold, namedFixes map[string]Fix, allFixes map[string]Fix) {

	for _, h := range holds {

		key := h.Ident + "_" + h.Region

		namedFix, found := namedFixes[key]
		if found {
			h.FullName = namedFix.FullName
			h.LatRad = namedFix.LatRad
			h.LonRad = namedFix.LonRad
		}

		if h.LatRad == 0 && h.LonRad == 0 {
			fix, found := allFixes[key]
			if found {
				h.LatRad = fix.LatRad
				h.LonRad = fix.LonRad
			} else {
				logger.Log.Println("WARN: hold not found in fix map for key", key)
				continue
			}
		}

		h.InitUnitVector()
	}

}

func (s *Service) findNearestHold(ac *Aircraft, icao string) *Hold {

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
		if phase == trafficglobal.GoAround.Index() {
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
						return h
					}
				}
			}
			// If no MAFix match found, fall back to nearest airport hold
		}

		// B. OTHER PHASES (or Go-Around fallback): Find nearest hold in Airport.Holds
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
			return bestAirportHold
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

	return bestGlobalHold
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
