package atc

import (
	"math"
	"strconv"
)

func loadHolds(navDataFile, holdsDataFile string) ([]Hold, error) {

	fixes, err := parseNavData(navDataFile)
	if err != nil {
		return nil, err
	}
	holds, err := parseHoldData(holdsDataFile)
	if err != nil {
		return nil, err
	}
	holds, err = resolveHoldCoordinates(holds, fixes)
	if err != nil {
		return nil, err
	}
	
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

func resolveHoldCoordinates(holds []Hold, fixes map[string]Fix) ([]Hold, error) {
    out := make([]Hold, 0, len(holds))

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

        out = append(out, h)
    }

    return out, nil
}

func (s *Service) findNearestHold(lat, lng float64) *Hold {

	latRad := lat * math.Pi / 180
	lonRad := lng * math.Pi / 180

    ux, uy, uz := toUnit(latRad, lonRad)

    var best *Hold
    bestDot := -2.0

    for i := range s.Holds {
        h := &s.Holds[i]
        dot := ux*h.X + uy*h.Y + uz*h.Z
        if dot > bestDot {
            bestDot = dot
            best = h
        }
    }

    return best
}
