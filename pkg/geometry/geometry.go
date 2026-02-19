package geometry

import "math"

// IsPointInPolygon uses a dateline-aware ray-casting algorithm.
func IsPointInPolygon(lat, lon float64, points [][2]float64) bool {
    if len(points) < 3 { return false }
    inside := false
    j := len(points) - 1

    for i := 0; i < len(points); i++ {
        xi, yi := points[i][0], points[i][1]
        xj, yj := points[j][0], points[j][1]

        // DATELINE FIX: Detect segments that cross the 180/-180 line
        if math.Abs(yi-yj) > 180 {
            // Shift coordinates to a 0-360 range for this segment calculation
            if yi < 0 { yi += 360 } 
            if yj < 0 { yj += 360 }
            
            testLon := lon
            if lon < 0 { testLon += 360 }
            
            if ((yi > testLon) != (yj > testLon)) &&
                (lat < (xj-xi)*(testLon-yi)/(yj-yi)+xi) {
                inside = !inside
            }
        } else {
            // STANDARD Ray-Casting
            if ((yi > lon) != (yj > lon)) &&
                (lat < (xj-xi)*(lon-yi)/(yj-yi)+xi) {
                inside = !inside
            }
        }
        j = i
    }
    return inside
}

// CalculateRoughArea provides a simple tie-breaker value.
// It is not strictly accurate in square miles but perfect for sorting.
func CalculateRoughArea(points [][2]float64) float64 {
    if len(points) < 3 { return 0 }
    area := 0.0
    j := len(points) - 1
    for i := 0; i < len(points); i++ {
        area += (points[j][1] + points[i][1]) * (points[j][0] - points[i][0])
        j = i
    }
    return math.Abs(area / 2.0)
}

// DistNM calculates the great-circle distance between two points in Nautical Miles.
func DistNM(lat1, lon1, lat2, lon2 float64) float64 {
    radlat1 := lat1 * math.Pi / 180
    radlat2 := lat2 * math.Pi / 180
    theta := lon1 - lon2
    radtheta := theta * math.Pi / 180
    dist := math.Sin(radlat1)*math.Sin(radlat2) + math.Cos(radlat1)*math.Cos(radlat2)*math.Cos(radtheta)
    if dist > 1 { dist = 1 }
    dist = math.Acos(dist)
    dist = dist * 180 / math.Pi
    return dist * 60
}