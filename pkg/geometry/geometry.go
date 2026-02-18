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

	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(r1)*math.Cos(r2)*math.Sin(dLon/2)*math.Sin(dLon/2)

	return R * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

func IsPointInPolygon(lat, lon float64, polygon [][2]float64) bool {
    if len(polygon) < 3 {
        return false
    }

    inside := false
    j := len(polygon) - 1

    for i := 0; i < len(polygon); i++ {
        xi, yi := polygon[i][0], polygon[i][1]
        xj, yj := polygon[j][0], polygon[j][1]

        // --- Handle Dateline Crossing ---
        // If the segment crosses the 180/-180 line, 
        // we shift the points so they are continuous relative to 'lon'
        if yi-lon > 180 {
            yi -= 360
        } else if yi-lon < -180 {
            yi += 360
        }
        
        if yj-lon > 180 {
            yj -= 360
        } else if yj-lon < -180 {
            yj += 360
        }

        // Standard Ray Casting logic using the (potentially) shifted coordinates
        // We check if the 'lon' is between the two points' longitudes
        if ((yi > lon) != (yj > lon)) &&
            (lat < (xj-xi)*(lon-yi)/(yj-yi)+xi) {
            inside = !inside
        }
        j = i
    }

    return inside
}

func CalculateRoughArea(polygon [][2]float64) float64 {
	if len(polygon) < 3 {
		return 0
	}

	var area float64
	j := len(polygon) - 1

	for i := 0; i < len(polygon); i++ {
		latI, lonI := polygon[i][0], polygon[i][1]
		latJ, lonJ := polygon[j][0], polygon[j][1]

		// --- Handle Dateline Crossing ---
		// We normalize point J relative to point I.
		// If the gap is > 180 degrees, it's a wrap-around.
		dLon := lonJ - lonI
		if dLon > 180 {
			lonJ -= 360
		} else if dLon < -180 {
			lonJ += 360
		}

		// Shoelace formula: (x1*y2 - x2*y1)
		// Note: Using Lat as X and Lon as Y for a "rough" area 
		area += (latI * lonJ) - (latJ * lonI)
		j = i
	}

	return math.Abs(area / 2.0)
}
