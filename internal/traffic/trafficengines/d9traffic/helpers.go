package d9traffic

import (
	"fmt"
	"math"
	"math/rand/v2"
	"time"

	"github.com/curbz/decimal-niner/internal/atc"
	"github.com/curbz/decimal-niner/internal/constants"
	"github.com/curbz/decimal-niner/internal/flightphase"
	"github.com/curbz/decimal-niner/internal/flightplan"
	"github.com/curbz/decimal-niner/pkg/geometry"
	"github.com/curbz/decimal-niner/pkg/util"
)

func (e *D9TrafficEngine) isCurrentlyActive(registration string, flightNumber int) bool {
	_, exists := e.ActiveAircraft[fmt.Sprintf("%s_%d", registration, flightNumber)]
	return exists
}

func getLastUpdateDeltaTimeSec(ac *atc.Aircraft, currSimZTime time.Time) float64 {

	var deltaTimeSec float64 = 10.0
	if !ac.Flight.Phase.LastUpdateTime.IsZero() {
		deltaTimeSec = currSimZTime.Sub(ac.Flight.Phase.LastUpdateTime).Seconds()

		// If delta time is an unrealistic spike (e.g., > 20 seconds) due to queue delays
		// or a fresh phase initialization, treat it cleanly as a standard single frame tick.
		if deltaTimeSec <= 0 || deltaTimeSec > 20.0 {
			deltaTimeSec = 10.0
		}
	}
	return deltaTimeSec
}

func AbsDiff(a, b int) int {
	result := a - b
	if result < 0 {
		return -result
	}
	return result
}

func AbsInt(v int) int {
	return int(math.Abs(float64(v)))
}

func (e *D9TrafficEngine) groupRunwaysByOrientation(runways []*atc.Runway) map[int][]*atc.Runway {
	groups := make(map[int][]*atc.Runway)
	for _, r := range runways {
		// Use the "Tens" digit (e.g., 274 degrees becomes 27)
		orientation := int(math.Round(r.Heading / 10.0))
		groups[orientation] = append(groups[orientation], r)
	}
	return groups
}

func (e *D9TrafficEngine) determineSizeClass(f *flightplan.ScheduledFlight, info *atc.AirlineInfo) string {
	// 1. Calculate the Distance Baseline
	distNM := e.calculateFlightDistance(f.IcaoOrigin, f.IcaoDest)

	// 2. Initial estimate based on distance
	size := "C"
	switch {
	case distNM < 450:
		size = "B"
	case distNM > 2800:
		size = "E" // Heavy
	}

	// 3. Apply Tier Constraints
	if info != nil {
		switch info.Tier {
		case "international":
			// Flag carriers can be anything, keep distance estimate
		case "budget":
			// Budget airlines almost never fly Heavies (E/F)
			// Even if the distance is long, cap it at 'C'
			if size == "E" || size == "F" {
				size = "C"
			}
		case "regional":
			// Regional airlines are capped at 'B' or 'C'
			if size == "E" || size == "F" {
				size = "B"
			}
		}
	}

	// 4. Final Physical Check
	// If the origin airport doesn't even have an 'E' gate,
	// we must downgrade to the largest available.
	return e.clampSizeToAirportCapability(f.IcaoOrigin, size)
}

func (e *D9TrafficEngine) clampSizeToAirportCapability(icao string, estimatedSize string) string {
	ap, ok := e.AtcService.Airports[icao]
	if !ok {
		return estimatedSize
	}

	// Find the largest gate available at this airport
	maxClass := "A"
	for _, spot := range ap.Parking {
		if spot.WidthClass > maxClass {
			maxClass = spot.WidthClass
		}
	}

	// If our estimated size is bigger than the biggest gate, downgrade it
	if estimatedSize > maxClass {
		return maxClass
	}

	return estimatedSize
}

func (e *D9TrafficEngine) calculateFlightDistance(originICAO, destICAO string) float64 {
	origin, okO := e.AtcService.Airports[originICAO]
	dest, okD := e.AtcService.Airports[destICAO]

	// If we don't have coordinate data for both airports,
	// return a medium distance as a safe fallback for the size heuristic.
	if !okO || !okD {
		return 500.0
	}

	// Convert degrees to radians
	lat1 := geometry.DegToRad(origin.Lat)
	lon1 := geometry.DegToRad(origin.Lon)
	lat2 := geometry.DegToRad(dest.Lat)
	lon2 := geometry.DegToRad(dest.Lon)

	// Haversine formula
	diffLat := lat2 - lat1
	diffLon := lon2 - lon1

	a := math.Sin(diffLat/2)*math.Sin(diffLat/2) +
		math.Cos(lat1)*math.Cos(lat2)*
			math.Sin(diffLon/2)*math.Sin(diffLon/2)

	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))

	return geometry.EarthRadiusNM * c
}

func (e *D9TrafficEngine) getRunwayUtilityScore(rwy *atc.Runway, windDir float64, windSpeed float64) float64 {
	// 1. Start with the "Static" score (Length and Procedures)
	score := float64(len(rwy.SIDs)*10 + len(rwy.STARs)*10)
	score += rwy.Length / 1000.0

	// 2. Add the "Dynamic" Weather Component
	// Calculate the angular difference between wind and runway heading
	diff := windDir - rwy.Heading
	radDiff := geometry.DegToRad(diff)

	// headwindComponent: 1.0 = Direct Headwind, -1.0 = Direct Tailwind
	headwindComponent := math.Cos(radDiff)

	// 3. Weight the wind heavily
	// We multiply the component by wind speed.
	// A 20kt tailwind should almost always disqualify a runway (-20 points)
	// A 20kt headwind should make it very attractive (+20 points)
	score += (headwindComponent * windSpeed)

	// 4. Bonus for Precision (ILS/RNAV)
	if rwy.HighestPrecisionApproach != "" {
		score += 50.0
	}

	return score
}

func getWeightedCommonAirline(origin, dest *atc.Airport) string {
	// 1. Find airlines that exist in BOTH hub weight maps
	commonWeights := make(map[string]float64)

	for code, originWeight := range origin.HubWeights {
		if destWeight, exists := dest.HubWeights[code]; exists {
			// We average the weights to find a "mutual" probability.
			// If BA is 80% at LHR and 10% at JFK, their mutual weight is 45%.
			commonWeights[code] = (originWeight + destWeight) / 2.0
		}
	}

	// 2. If no common airlines found, return empty so the cascade continues
	if len(commonWeights) == 0 {
		return ""
	}

	// 3. Use the Weighted Random selector we wrote previously
	return getWeightedRandomAirline(commonWeights)
}

func getWeightedRandomAirline(weights map[string]float64) string {
	if len(weights) == 0 {
		return ""
	}

	// 1. Calculate the total sum of weights
	var totalWeight float64
	for _, w := range weights {
		totalWeight += w
	}

	// 2. Pick a random number in the range [0.0, totalWeight)
	r := rand.Float64() * totalWeight

	// 3. Iterate and subtract until we find the winner
	var cumulative float64
	for code, weight := range weights {
		cumulative += weight
		if r <= cumulative {
			return code
		}
	}

	// Fallback to a random key if logic fails
	for code := range weights {
		return code
	}
	return ""
}

func getReciprocalName(name string) string {
	// 1. Separate numbers from letters
	var numPart int
	var letterPart string
	fmt.Sscanf(name, "%d%s", &numPart, &letterPart)

	// 2. Flip the number
	recipNum := numPart + 18
	if recipNum > 36 {
		recipNum -= 18
	}

	// 3. Flip the letter
	recipLetter := letterPart
	switch letterPart {
	case "L":
		recipLetter = "R"
	case "R":
		recipLetter = "L"
	}

	return fmt.Sprintf("%02d%s", recipNum, recipLetter)
}

// Backup helper to select a distinct parallel runway if primary selection produced a collision
func (e *D9TrafficEngine) selectSecondaryRunway(ap *atc.Airport, candidates []*atc.Runway, primary *atc.Runway, isDeparture bool) *atc.Runway {
	var secondary *atc.Runway
	bestScore := -10000.0

	for _, rwy := range candidates {
		if rwy.Name == primary.Name {
			continue
		}

		var count int
		if isDeparture {
			count = len(rwy.SIDs)
		} else {
			count = len(rwy.STARs)
		}

		score := float64(count) * 20.0
		distToCenter := geometry.DistNM(ap.Lat, ap.Lon, rwy.Lat, rwy.Lon)

		if isDeparture {
			score -= distToCenter * 5.0 // Favor inboard
		} else {
			score += distToCenter * 5.0 // Favor outboard
		}

		if score > bestScore {
			bestScore = score
			secondary = rwy
		}
	}

	if secondary == nil {
		return primary
	}
	return secondary
}

func (e *D9TrafficEngine) getViableRunways(ap *atc.Airport) []*atc.Runway {
	viable := []*atc.Runway{}
	for _, rwy := range ap.Runways {
		// Only consider runways longer than configured minimum (meters)
		if rwy.Length >= constants.RunwayLengthNM*constants.MetersToNM {
			viable = append(viable, rwy)
		}
	}
	return viable
}

func (e *D9TrafficEngine) resolveAirline(f *flightplan.ScheduledFlight) *atc.AirlineInfo {
	// 1. Direct Match: The most efficient path.
	if info := e.AtcService.GetAirlineByName(f.AirlineName); info != nil {
		return info
	}

	// --- FALLBACKS ---
	// At this point, we know we don't have a name match.
	// We will now find a code and immediately return its full info struct.
	// 2. Matching Pairs (Airlines at both ends)
	util.LogWarnWithLabel(f.AircraftRegistration, "airline %s not found - allocating by orign/destination gate pairing logic", f.AirlineName)
	origin := e.AtcService.Airports[f.IcaoOrigin]
	dest := e.AtcService.Airports[f.IcaoDest]
	if origin != nil && dest != nil {
		if code := getWeightedCommonAirline(origin, dest); code != "" {
			airline := e.AtcService.GetAirlineByCode(code)
			if airline != nil {
				return airline
			}
		}
	}

	// 3. Origin Hub Weighted Selection
	util.LogWarnWithLabel(f.AircraftRegistration, "allocating airline by origin gate logic")
	if origin != nil && len(origin.HubWeights) > 0 {
		if code := getWeightedRandomAirline(origin.HubWeights); code != "" {
			airline := e.AtcService.GetAirlineByCode(code)
			if airline != nil {
				return airline
			}
		}
	}

	// 4. Registration Country Fallback
	util.LogWarnWithLabel(f.AircraftRegistration, "allocating airline by country of registration logic")
	countryCode := e.AtcService.GetCountryFromRegistration(f.AircraftRegistration)
	if countryCode == "" {
		countryCode = e.AtcService.Config.ATC.AirlineCountryCodeFallback
	}

	if countryCode != "" {
		code := e.AtcService.GetRandomAirlineByCountry(countryCode)
		if code != "" {
			return e.AtcService.GetAirlineByCode(code)
		}
	}

	util.LogWarnWithLabel(f.AircraftRegistration, "unable to resolve airline, defaulting to BAW")
	return &atc.AirlineInfo{
		ICAO:        "UNK",
		AirlineName: "British Airways",
		Callsign:    "SPEEDBIRD",
		CountryCode: "GB",
		Tier:        "international",
	}
}

// getPhaseGroundSpeedKts returns a nominal ground speed (knots) appropriate for the phase and aircraft size class.
func (e *D9TrafficEngine) getPhaseGroundSpeedKts(sizeClass string, phase flightphase.FlightPhase) float64 {
	// Default conservative speeds
	switch phase {
	case flightphase.TaxiOut, flightphase.TaxiIn:
		return 18.0
	case flightphase.Takeoff:
		switch sizeClass {
		case "E", "F":
			return 150.0
		case "C", "D":
			return 160.0
		default:
			return 155.0
		}
	case flightphase.Climbout:
		switch sizeClass {
		case "E", "F":
			return 190.0
		case "C", "D":
			return 190.0
		default:
			return 190.0
		}
	case flightphase.Departure:
		switch sizeClass {
		case "E", "F":
			return 220.0
		case "C", "D":
			return 220.0
		default:
			return 220.0
		}
	case flightphase.Cruise:
		// Use a high nominal speed but Cruise uses its own interpolation logic elsewhere
		return 420.0
	case flightphase.Arrival:
		switch sizeClass {
		case "E", "F":
			return 240.0
		case "C", "D":
			return 240.0
		default:
			return 230.0
		}
	case flightphase.Approach:
		switch sizeClass {
		case "E", "F":
			return 180.0
		case "C", "D":
			return 180.0
		default:
			return 180.0
		}
	case flightphase.Final:
		switch sizeClass {
		case "E", "F":
			return 140.0
		case "C", "D":
			return 140.0
		default:
			return 140.0
		}
	case flightphase.Holding:
		switch sizeClass {
		case "E", "F":
			return 220.0
		case "C", "D":
			return 220.0
		default:
			return 200.0
		}
	case flightphase.Braking:
		return 100.0
	default:
		return 120.0
	}
}

// getPhaseVerticalRateFpm returns a nominal vertical rate (feet per minute) for the given phase and aircraft.
func (e *D9TrafficEngine) getPhaseVerticalRateFpm(sizeClass string, phase flightphase.FlightPhase) float64 {
	switch phase {
	case flightphase.Takeoff:
		switch sizeClass {
		case "E", "F":
			return 2500.0
		case "C", "D":
			return 3000.0
		default:
			return 3500.0
		}
	case flightphase.Climbout:
		switch sizeClass {
		case "E", "F":
			return 1500.0
		case "C", "D":
			return 2000.0
		default:
			return 2500.0
		}
	case flightphase.Departure:
		switch sizeClass {
		case "E", "F":
			return 1200.0
		case "C", "D":
			return 1500.0
		default:
			return 1800.0
		}
	case flightphase.Cruise:
		return 0.0
	case flightphase.Arrival:
		// High-altitude descent down the airway (Post-TOD down to terminal area)
		// Standard airline descent profiles typically target 1800 - 2400 FPM
		switch sizeClass {
		case "E", "F": // Heavy/Super (e.g., A380, B777)
			return -2000.0
		case "C", "D": // Mainline Jets (e.g., A320, B737)
			return -2200.0
		default: // Light/Props
			return -1500.0
		}

	case flightphase.Approach:
		// Terminal radar vectoring area (Below 10,000 ft down to FAF)
		// Stepping down through terminal altitudes
		switch sizeClass {
		case "C", "D", "E", "F":
			return -1500.0
		default:
			return -1000.0
		}

	case flightphase.Final:
		// Stabilized on the Instrument Landing System (ILS) Glideslope
		// Standard 3° glideslope down to the runway
		switch sizeClass {
		case "C", "D", "E", "F":
			return -750.0 // Roughly matches a 140kt approach on a 3-degree slope
		default:
			return -500.0 // Slower props need less FPM to stay on the same slope
		}

	case flightphase.Braking, flightphase.TaxiIn, flightphase.TaxiOut:
		return 0.0
	default:
		return 0.0
	}
}

// NormalizeRunwayKey creates a consistent ID for the physical concrete
func normalizeRunwayKey(icao string, rwy *atc.Runway) string {
	recip := getReciprocalName(rwy.Name)
	// Sort them so the key is always the same regardless of which end we use
	if rwy.Name < recip {
		return fmt.Sprintf("%s-%s-%s", icao, rwy.Name, recip)
	}
	return fmt.Sprintf("%s-%s-%s", icao, recip, rwy.Name)
}
