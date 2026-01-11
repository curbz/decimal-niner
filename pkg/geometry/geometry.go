package geometry

import (
	"math"
)

// --- Geometry Helpers ---

func DistNM(lat1, lon1, lat2, lon2 float64) float64 {
	const R = 3440.06
	r1, r2 := lat1*math.Pi/180, lat2*math.Pi/180

	dLat := (lat2 - lat1) * math.Pi / 180
	dLon := (lon2 - lon1) * math.Pi / 180

	// --- handle dateline crossing ---
	for dLon > math.Pi {
		dLon -= 2 * math.Pi
	}
	for dLon < -math.Pi {
		dLon += 2 * math.Pi
	}
	// --------------------------------

	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(r1)*math.Cos(r2)*math.Sin(dLon/2)*math.Sin(dLon/2)

	return R * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

func IsPointInPolygon(lat, lon float64, polygon [][2]float64) bool {
	if len(polygon) < 3 {
		return false
	}
	inside, j := false, len(polygon)-1
	for i := 0; i < len(polygon); i++ {
		if ((polygon[i][1] > lon) != (polygon[j][1] > lon)) &&
			(lat < (polygon[j][0]-polygon[i][0])*(lon-polygon[i][1])/(polygon[j][1]-polygon[i][1])+polygon[i][0]) {
			inside = !inside
		}
		j = i
	}
	return inside
}

func CalculateRoughArea(pts [][2]float64) float64 {
	minLat, maxLat := 90.0, -90.0
	minLon, maxLon := 180.0, -180.0
	for _, p := range pts {
		if p[0] < minLat {
			minLat = p[0]
		}
		if p[0] > maxLat {
			maxLat = p[0]
		}
		if p[1] < minLon {
			minLon = p[1]
		}
		if p[1] > maxLon {
			maxLon = p[1]
		}
	}
	return (maxLat - minLat) * (maxLon - minLon)
}

