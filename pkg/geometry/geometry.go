package geometry

import "math"

const EarthRadiusNM = 3440.065 // Earth radius in Nautical Miles

// IsPointInPolygon uses a dateline-aware ray-casting algorithm.
func IsPointInPolygon(lat, lon float64, points [][2]float64) bool {
	if len(points) < 3 {
		return false
	}
	inside := false
	j := len(points) - 1

	for i := 0; i < len(points); i++ {
		xi, yi := points[i][0], points[i][1]
		xj, yj := points[j][0], points[j][1]

		// DATELINE FIX: Detect segments that cross the 180/-180 line
		if math.Abs(yi-yj) > 180 {
			// Shift coordinates to a 0-360 range for this segment calculation
			if yi < 0 {
				yi += 360
			}
			if yj < 0 {
				yj += 360
			}

			testLon := lon
			if lon < 0 {
				testLon += 360
			}

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
	if len(points) < 3 {
		return 0
	}
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
	if dist > 1 {
		dist = 1
	}
	dist = math.Acos(dist)
	dist = dist * 180 / math.Pi
	return dist * 60
}

// Project calculates a new Lat/Lon point based on a starting point,
// heading (degrees), and distance (Nautical Miles).
func Project(lat, lon, heading, distanceNM float64) (float64, float64) {
	radLat := lat * math.Pi / 180
	radLon := lon * math.Pi / 180
	radHeading := heading * math.Pi / 180

	distAng := distanceNM / EarthRadiusNM

	newLat := math.Asin(math.Sin(radLat)*math.Cos(distAng) +
		math.Cos(radLat)*math.Sin(distAng)*math.Cos(radHeading))

	newLon := radLon + math.Atan2(math.Sin(radHeading)*math.Sin(distAng)*math.Cos(radLat),
		math.Cos(distAng)-math.Sin(radLat)*math.Sin(newLat))

	return newLat * 180 / math.Pi, newLon * 180 / math.Pi
}

// CalculateBearing returns the true bearing from point 1 to point 2
func CalculateBearing(lat1, lon1, lat2, lon2 float64) float64 {
	radLat1 := lat1 * math.Pi / 180
	radLat2 := lat2 * math.Pi / 180
	diffLon := (lon2 - lon1) * math.Pi / 180

	y := math.Sin(diffLon) * math.Cos(radLat2)
	x := math.Cos(radLat1)*math.Sin(radLat2) -
		math.Sin(radLat1)*math.Cos(radLat2)*math.Cos(diffLon)

	bearing := math.Atan2(y, x)
	return math.Mod((bearing*180/math.Pi)+360, 360)
}

// CalculateDistanceFeet returns the distance between two points in Feet
func CalculateDistanceFeet(lat1, lon1, lat2, lon2 float64) float64 {
	const EarthRadiusFt = 20902231.0
	radLat1 := lat1 * math.Pi / 180
	radLat2 := lat2 * math.Pi / 180
	diffLat := (lat2 - lat1) * math.Pi / 180
	diffLon := (lon2 - lon1) * math.Pi / 180

	a := math.Sin(diffLat/2)*math.Sin(diffLat/2) +
		math.Cos(radLat1)*math.Cos(radLat2)*
			math.Sin(diffLon/2)*math.Sin(diffLon/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))

	return EarthRadiusFt * c
}
