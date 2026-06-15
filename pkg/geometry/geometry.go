package geometry

import (
	"math"
	"time"

	"github.com/curbz/decimal-niner/pkg/util"
)

const EarthRadiusNM = 3440.065 // Earth radius in Nautical Miles
const EarthRadiusFt = 20902231.0
const EarthRadiusMeter = 6371000.0

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

// Project calculates a new Lat/Lon point based on a starting point,
// heading (degrees), and distance (Nautical Miles).
func Project(lat, lon, heading, distanceNM float64) (float64, float64) {
	// 1. Convert inputs from Degrees to Radians using your wrappers
	radLat := DegToRad(lat)
	radLon := DegToRad(lon)
	radHeading := DegToRad(heading)

	// Angular distance (distance in NM / Earth's radius in NM)
	distAng := distanceNM / 3440.065

	// 2. Perform Spherical Trigonometry (Internal math must stay in Radians)
	newLatRad := math.Asin(math.Sin(radLat)*math.Cos(distAng) +
		math.Cos(radLat)*math.Sin(distAng)*math.Cos(radHeading))

	newLonRad := radLon + math.Atan2(
		math.Sin(radHeading)*math.Sin(distAng)*math.Cos(radLat),
		math.Cos(distAng)-math.Sin(radLat)*math.Sin(newLatRad),
	)

	// 3. Convert results back to Degrees using your wrappers for Path B storage
	resLat := RadToDeg(newLatRad)
	resLon := RadToDeg(newLonRad)

	// 4. Normalize Longitude to ensure it stays within [-180, 180]
	if resLon > 180 {
		resLon -= 360
	} else if resLon < -180 {
		resLon += 360
	}

	return resLat, resLon
}

// DistanceFromLine returns the shortest distance in meters from point (lat, lon)
// to a line starting at (latStart, lonStart) following a specific heading (degrees).
func DistanceFromLine(lat, lon, latStart, lonStart, heading float64) float64 {
	// Convert all to Radians
	latRad := DegToRad(lat)
	lonRad := DegToRad(lon)
	latStartRad := DegToRad(latStart)
	lonStartRad := DegToRad(lonStart)
	bearingRad := DegToRad(heading)

	// Angular distance from start point to user point
	// Using Haversine or simple spherical distance
	distStartToUser := math.Acos(math.Sin(latStartRad)*math.Sin(latRad) +
		math.Cos(latStartRad)*math.Cos(latRad)*math.Cos(lonRad-lonStartRad))

	// Bearing from start point to user point
	bearingStartToUser := math.Atan2(
		math.Sin(lonRad-lonStartRad)*math.Cos(latRad),
		math.Cos(latStartRad)*math.Sin(latRad)-math.Sin(latStartRad)*math.Cos(latRad)*math.Cos(lonRad-lonStartRad),
	)

	// Cross-track distance formula
	// dxt = asin(sin(dist_ad) * sin(bearing_ad - bearing_ab))
	xtd := math.Asin(math.Sin(distStartToUser) * math.Sin(bearingStartToUser-bearingRad))

	// Convert back to meters
	return math.Abs(xtd * EarthRadiusMeter)
}

// AlongTrackDistance returns the distance in meters along the path from the start point.
// A positive value means the user is "beyond" the threshold.
func AlongTrackDistance(lat, lon, latStart, lonStart, heading float64) float64 {
	latRad := DegToRad(lat)
	lonRad := DegToRad(lon)
	latStartRad := DegToRad(latStart)
	lonStartRad := DegToRad(lonStart)
	bearingRad := DegToRad(heading)

	// Angular distance from start to user
	distStartToUser := math.Acos(math.Sin(latStartRad)*math.Sin(latRad) +
		math.Cos(latStartRad)*math.Cos(latRad)*math.Cos(lonRad-lonStartRad))

	// Bearing from start to user
	bearingStartToUser := math.Atan2(
		math.Sin(lonRad-lonStartRad)*math.Cos(latRad),
		math.Cos(latStartRad)*math.Sin(latRad)-math.Sin(latStartRad)*math.Cos(latRad)*math.Cos(lonRad-lonStartRad),
	)

	// Along-track distance formula:
	// dat = acos(cos(dist_ad) / cos(dxt))
	// We use a simpler version for smaller distances:
	atd := math.Acos(math.Cos(distStartToUser) / math.Cos(math.Asin(math.Sin(distStartToUser)*math.Sin(bearingStartToUser-bearingRad))))

	// Check if the user is behind the threshold
	diff := bearingStartToUser - bearingRad
	for diff < -math.Pi {
		diff += 2 * math.Pi
	}
	for diff > math.Pi {
		diff -= 2 * math.Pi
	}
	if math.Abs(diff) > math.Pi/2 {
		return -(atd * EarthRadiusMeter)
	}

	return atd * EarthRadiusMeter
}

// CalculateBearing takes latitude and longitude pairs and returns the initial bearing in degrees ($0^\circ$ to $360^\circ$).
func CalculateBearing(lat1, lon1, lat2, lon2 float64) float64 {
	// Convert degrees to radians
	phi1 := DegToRad(lat1)
	phi2 := DegToRad(lat2)
	lambda1 := DegToRad(lon1)
	lambda2 := DegToRad(lon2)

	y := math.Sin(lambda2-lambda1) * math.Cos(phi2)
	x := math.Cos(phi1)*math.Sin(phi2) -
		math.Sin(phi1)*math.Cos(phi2)*math.Cos(lambda2-lambda1)

	theta := math.Atan2(y, x)

	// Convert back to degrees and normalize to 0-360
	bearing := math.Mod(RadToDeg(theta)+360, 360)

	return bearing
}

// Helper to find the smallest difference between two headings (0-180)
func BearingDiff(b1, b2 float64) float64 {
	diff := math.Mod(b2-b1+180, 360) - 180
	if diff < -180 {
		return diff + 360
	}
	return diff
}

func CrossTrackDistance(lat1, lon1, lat2, lon2, lat3, lon3 float64) float64 {

	dist13 := DistNM(lat1, lon1, lat3, lon3)

	// Convert bearings to radians for the math
	brng12 := DegToRad(CalculateBearing(lat1, lon1, lat2, lon2))
	brng13 := DegToRad(CalculateBearing(lat1, lon1, lat3, lon3))

	// The angular distance
	d13Ang := dist13 / EarthRadiusNM

	// Cross-track distance in radians
	xtdAng := math.Asin(math.Sin(d13Ang) * math.Sin(brng13-brng12))

	// Return absolute distance in Nautical Miles
	return math.Abs(xtdAng * EarthRadiusNM)
}

// RadToDeg converts radians to decimal degrees.
// Useful for converting SID/STAR Radian coordinates to X-Plane degrees.
func RadToDeg(rad float64) float64 {
	deg := rad * 180 / math.Pi
	if deg > 1000 || deg < -1000 {
		util.LogWarnWithLabel("D9TRAFFIC", "probable double conversion attempted in geometry.RadToDeg function - possible bug")
		return rad
	}
	return deg
}

// DegToRad converts decimal degrees to radians.
// Useful for passing degrees into trigonometric functions like math.Sin or math.Cos.
func DegToRad(deg float64) float64 {
	return deg * math.Pi / 180
}

// NormalizeHeading prevents headings ever exceeding 360 or going below 0
func NormalizeHeading(heading float64) float64 {

	// If the incoming heading is broken math (NaN), safety fallback to North
	if math.IsNaN(heading) {
		util.LogErrWithLabel("D9TRAFFIC", "NormalizeHeading received NaN input, defaulting to 360 - possible bug")
		return 360.0
	}

	h := math.Mod(heading, 360)
	if h <= 0 {
		h += 360
	}
	// Now, even if h was 0, it becomes 360.
	// If it was -10, it becomes 350.
	// If it was 360, math.Mod makes it 0, then we add 360.
	return h
}

// Internal helper calculating the central angle arc between two points using the stable Haversine formula
func greatCircleArc(lat1, lon1, lat2, lon2 float64) float64 {
	radLat1 := DegToRad(lat1)
	radLat2 := DegToRad(lat2)
	dLat := DegToRad(lat2 - lat1)
	dLon := DegToRad(lon2 - lon1)

	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(radLat1)*math.Cos(radLat2)*
			math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

// DistanceInMeters calculates the distance between two coordinates in meters
func DistanceInMeters(lat1, lon1, lat2, lon2 float64) float64 {
	return EarthRadiusMeter * greatCircleArc(lat1, lon1, lat2, lon2)
}

// CalculateDistanceFeet returns the distance between two points in Feet
func CalculateDistanceFeet(lat1, lon1, lat2, lon2 float64) float64 {
	return EarthRadiusFt * greatCircleArc(lat1, lon1, lat2, lon2)
}

// DistNM calculates the great-circle distance between two points in Nautical Miles
func DistNM(lat1, lon1, lat2, lon2 float64) float64 {
	// Uses the package global constant instead of the arbitrary literal * 60
	return EarthRadiusNM * greatCircleArc(lat1, lon1, lat2, lon2)
}

// Interpolate calculates a point along a linear track between two coordinates based on a ratio (0.0 to 1.0)
func InterpolateCoords(startLat, startLon, targetLat, targetLon, ratio float64) (float64, float64) {
    if ratio <= 0.0 {
        return startLat, startLon
    }
    if ratio >= 1.0 {
        return targetLat, targetLon
    }
    
    lat := startLat + (targetLat-startLat)*ratio
    lon := startLon + (targetLon-startLon)*ratio
    return lat, lon
}

// CalculateKinematicDuration estimates how long it will take an aircraft to travel
// a given distance in NM based on its current groundspeed in knots.
func CalculateKinematicDuration(distNM float64, groundSpeedKnots float64) time.Duration {
    // Safety floor: If groundspeed is zero or invalid, fall back to a reasonable default
    // to avoid an infinite time duration or a divide-by-zero crash.
    if groundSpeedKnots <= 10.0 {
        return 5 * time.Minute 
    }

    // Ground speed in NM per second
    nmPerSecond := groundSpeedKnots / 3600.0

    // Total seconds required to close the spatial gap
    secondsRequired := distNM / nmPerSecond

    return time.Duration(secondsRequired) * time.Second
}
