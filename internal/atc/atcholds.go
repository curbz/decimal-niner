package atc

import (
	"math"
	"strconv"
)

func loadHolds(navDataFile, holdsDataFile string) (map[string]*Hold, error) {

	fixes, err := parseNavData(navDataFile)
	if err != nil {
		return nil, err
	}
	holds, err := parseHoldData(holdsDataFile)
	if err != nil {
		return nil, err
	}
	resolveHoldCoordinates(holds, fixes)
	
	return holds, nil

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
func resolveHoldCoordinates(holds map[string]*Hold, fixes map[string]Fix) {

    for _, h := range holds {
        key := h.Name + "_" + h.Region
        fix, ok := fixes[key]
        if !ok {
            continue
        }

		h.FullName = fix.FullName
        h.LatRad = fix.LatRad
        h.LonRad = fix.LonRad
        h.InitUnitVector()
    }

}

func (s *Service) findNearestHold(lat, lng float64) *Hold {

	latRad := lat * math.Pi / 180
	lonRad := lng * math.Pi / 180

    ux, uy, uz := toUnit(latRad, lonRad)

    var best *Hold
    bestDot := -2.0

    for _, h := range s.Holds {
        dot := ux * h.X + uy * h.Y + uz * h.Z
        if dot > bestDot {
            bestDot = dot
            best = h
        }
    }

    return best
}
