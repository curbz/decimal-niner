package atc

import (
	"log"
	"math"
	"strconv"

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
				log.Println("WARN: hold not found in fix map for key", key)
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
