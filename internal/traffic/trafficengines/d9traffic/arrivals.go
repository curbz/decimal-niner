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

func (e *D9TrafficEngine) checkForArrivalSpawns(icao string, day, h, m int) {
	timeline := e.AirportSchedules[icao]
	nowMins := (h * 60) + m

	for _, f := range timeline.Arrivals {
		if f.ArrivalDayOfWeek != day {
			continue
		}

		arrMins := (f.ArrivalHour * 60) + f.ArrivalMin
		// If arriving soon and not already active
		if arrMins > nowMins+20 && arrMins < nowMins+40 {
			if !e.isCurrentlyActive(f.AircraftRegistration, f.Number) {
				e.spawnArrivalTraffic(&f)
			}
		}
	}
}

func (e *D9TrafficEngine) spawnArrivalTraffic(f *flightplan.ScheduledFlight) {
	tta := e.timeDiffToScheduledArrival(f)

	util.LogDebugWithLabel(f.AircraftRegistration, "spawning arrival flight schedDep %d:%d schedArr %d:%d remaining time to arrival %d",
		f.DepartureHour, f.DepartureMin, f.ArrivalHour, f.ArrivalMin, tta)

	initialPhase, remainingDurSecs, fullDurationSecs := e.determineInitialArrivalPhase(tta, f)
	util.LogDebugWithLabel(f.AircraftRegistration, "initial timed-based phase is %s", flightphase.FlightPhase(initialPhase).String())
	if initialPhase == flightphase.Unknown {
		return
	}

	airport := e.AtcService.Airports[f.IcaoDest]
	originAp := e.AtcService.Airports[f.IcaoOrigin]

	// 1. KINEMATIC SETUP
	bearing := geometry.CalculateBearing(originAp.Lat, originAp.Lon, airport.Lat, airport.Lon)
	totalDistance := geometry.DistNM(originAp.Lat, originAp.Lon, airport.Lat, airport.Lon)
	speedKts := e.getPhaseGroundSpeedKts("", flightphase.Cruise)
	if speedKts <= 0 {
		speedKts = 420.0
	}

	timeRatio := float64(remainingDurSecs) / float64(fullDurationSecs)
	generatedDistToDest := totalDistance * timeRatio

	// 2. TOD CORRECTION
	cruiseAlt := float64(f.CruiseAlt * 100)
	targetArrivalAlt := atc.GetMinSafeAltitude(float64(constants.DefaultCruiseExitArrivalEntryAltFt), airport)
	vrateDescent := math.Abs(e.getPhaseVerticalRateFpm("", flightphase.Arrival))
	requiredDescentDistNM := speedKts * ((cruiseAlt - targetArrivalAlt) / vrateDescent / 60.0)

	if initialPhase == flightphase.Cruise && generatedDistToDest <= requiredDescentDistNM {
		util.LogDebugWithLabel(f.AircraftRegistration, "moving initial phase from cruise to arrival - too close to destination: %f NM", generatedDistToDest)
		initialPhase = flightphase.Arrival
		remainingDurSecs = int((generatedDistToDest / speedKts) * 3600.0)
		fullDurationSecs = int((requiredDescentDistNM / speedKts) * 3600.0)
		timeRatio = float64(remainingDurSecs) / float64(fullDurationSecs)
	}

	// =========================================================================
	// PURE SPAWN COORDINATE & ALTITUDE SEPARATION
	// =========================================================================
	var spawnLat, spawnLon, spawnHdg, spawnAlt float64
	progressRatio := 1.0 - timeRatio

	// Resolve assigned runway properties ahead of block branching
	rwy := e.AirportConfig[airport.ICAO].Arrival
	star := e.AtcService.GetMatchingSTAR(airport, rwy, originAp)

	switch initialPhase {
	case flightphase.Cruise:
		// Pure Cruise Track Spawn
		reverseBearing := geometry.NormalizeHeading(bearing + 180.0)
		spawnLat, spawnLon = geometry.Project(airport.Lat, airport.Lon, reverseBearing, generatedDistToDest)
		spawnHdg = bearing
		spawnAlt = cruiseAlt

	case flightphase.Arrival:
		var startLat, startLon, targetLat, targetLon float64

		// Replicate Step 1A track anchors from updateLinearPosition
		if star != nil && star.Entry.Fix.Lat != 0 {
			startLat, startLon = star.Entry.Fix.Lat, star.Entry.Fix.Lon
			if star.Exit.Fix.Lat != 0 {
				targetLat, targetLon = star.Exit.Fix.Lat, star.Exit.Fix.Lon
				targetArrivalAlt = float64(star.Exit.ConstraintAlt)
			}
		}

		// Fallback Fix B long-range vectoring anchors if no STAR matches
		if startLat == 0 {
			routeBearing := geometry.CalculateBearing(originAp.Lat, originAp.Lon, airport.Lat, airport.Lon)
			reverseRouteBearing := geometry.NormalizeHeading(routeBearing + 180.0)

			startLat, startLon = geometry.Project(airport.Lat, airport.Lon, reverseRouteBearing, 150.0)
			targetLat, targetLon = geometry.Project(airport.Lat, airport.Lon, reverseRouteBearing, constants.DefaultArrivalExitApproachEntryNM)
		}

		if targetArrivalAlt == 0.0 {
			targetArrivalAlt = atc.GetMinSafeAltitude(float64(constants.DefaultCruiseExitArrivalEntryAltFt), airport)
		}

		trackBearing := geometry.CalculateBearing(startLat, startLon, targetLat, targetLon)
		trackDistance := geometry.DistNM(startLat, startLon, targetLat, targetLon)

		distAlongTrack := trackDistance * progressRatio
		spawnLat, spawnLon = geometry.Project(startLat, startLon, trackBearing, distAlongTrack)
		spawnHdg = trackBearing

		phaseInitAlt := cruiseAlt
		spawnAlt = phaseInitAlt + (progressRatio * (targetArrivalAlt - phaseInitAlt))
	}

	initialPhaseIdx := initialPhase.Index()
	currSimZTime := e.AtcService.GetCurrentZuluTime()
	airline := e.resolveAirline(f)

	sizeClass := e.determineSizeClass(f, airline)
	sizeClassStr := ""
	if sizeClass == "E" || sizeClass == "F" {
		sizeClassStr = "Heavy"
	}

	newAc := &atc.Aircraft{
		Registration: f.AircraftRegistration,
		SizeClass:    sizeClass,
		Flight: atc.Flight{
			Number:      f.Number,
			Origin:      f.IcaoOrigin,
			Destination: f.IcaoDest,
			Airline:     airline,
			Position: atc.Position{
				Lat:      spawnLat,
				Long:     spawnLon,
				Heading:  spawnHdg,
				Altitude: spawnAlt,
			},
			Comms: atc.Comms{
				CountryCode: airline.CountryCode,
				Callsign:    fmt.Sprintf("%s %d %s", airline.Callsign, f.Number, sizeClassStr),
			},
			CruiseAlt: f.CruiseAlt * 100,
			Schedule:  f,
			// Squawk random number between 1200 and 6999
			Squawk:       fmt.Sprintf("%04d", 1200+rand.IntN(5800)),
			PlanAssigned: true,
			Phase: flightphase.Phase{
				Current:  initialPhaseIdx,
				Previous: flightphase.Unknown.Index(),
			},
		},
	}

	// set pre-requisite states
	e.AtcService.SetFlightPhaseClass(newAc)
	// arrival runway must be assigned BEFORE assigning runway access point
	if initialPhaseIdx <= flightphase.TaxiIn.Index() {
		newAc.Flight.AssignedRunway = e.AirportConfig[airport.ICAO].Arrival
		newAc.Flight.AssignedRunwayName = newAc.Flight.AssignedRunway.Name
	}
	if initialPhaseIdx >= flightphase.Braking.Index() && initialPhaseIdx <= flightphase.Shutdown.Index()+1 {
		// assign parking BEFORE runway exit point as this may influence the selected exit
		e.assignParking(newAc, airport)
		e.AtcService.AssignRunwayAccessPoint(newAc, airport, atc.ARRIVAL_CONTEXT)
	}

	if initialPhaseIdx >= flightphase.Cruise.Index() && initialPhaseIdx <= flightphase.Arrival.Index() {
		e.AtcService.AssignSTAR(newAc, airport, newAc.Flight.AssignedRunway)
	}

	newAc.Flight.Phase.TotalDuration = time.Duration(fullDurationSecs) * time.Second
	elapsedOffset := math.Max(0, float64(fullDurationSecs)-float64(remainingDurSecs))
	newAc.Flight.Phase.Transition = currSimZTime.Add(-time.Duration(elapsedOffset) * time.Second)
	if initialPhaseIdx <= flightphase.Startup.Index() || initialPhaseIdx >= flightphase.Shutdown.Index() {
		newAc.Flight.Phase.EstimatedNextTransition = currSimZTime.Add(time.Duration(remainingDurSecs) * time.Second)
	}

	e.assignPhaseInitialAltitude(newAc, initialPhaseIdx)
	newAc.Flight.GroundSpeed = speedKts
	newAc.Flight.Phase.LastUpdateTime = e.AtcService.GetCurrentZuluTime()

	if initialPhaseIdx >= flightphase.Shutdown.Index() {
		// Ensure the aircraft is snapped to its assigned parking spot
		e.positionAtDestParking(newAc)
	}

	// add to active aircraft map
	e.ActiveAircraft[getActiveAircraftKey(newAc)] = newAc

	util.LogWithLabel(f.AircraftRegistration, "spawned arrival %s flight %d phase %s origin %s dest %s lat %0.6f lon %0.6f alt %0.6f hdg %d - estimated next transition: %v",
		f.AirlineName, f.Number, flightphase.FlightPhase(newAc.Flight.Phase.Current).String(), f.IcaoOrigin, f.IcaoDest,
		newAc.Flight.Position.Lat, newAc.Flight.Position.Long, newAc.Flight.Position.Altitude, int(newAc.Flight.Position.Heading),
		newAc.Flight.Phase.EstimatedNextTransition.Format(time.RFC3339))
}

func (e *D9TrafficEngine) getArrivalSaturationStats(ac *atc.Aircraft, airport *atc.Airport) (approachCount, holdingCount, runwayQueueCount int) {

	for _, other := range e.ActiveAircraft {
		if other == nil || other.Flight.Schedule == nil {
			continue
		}

		// Match flights targeting the exact same runway asset
		if other.Flight.AssignedRunwayName == ac.Flight.AssignedRunwayName {
			otherPhase := flightphase.FlightPhase(other.Flight.Phase.Current)

			// Count active approaches
			if otherPhase == flightphase.Approach {
				approachCount++
			}

			// Count flights tied to a hold for this runway
			if otherPhase == flightphase.Holding && other.Flight.Holding != nil {
				holdingCount++
			}
		}
	}

	qKey := normalizeRunwayKey(airport.ICAO, ac.Flight.AssignedRunway)
	qLength := len(e.RunwayQueues[qKey])

	return approachCount, holdingCount, qLength

}

func (e *D9TrafficEngine) determineInitialArrivalPhase(minsToSchedArr int, f *flightplan.ScheduledFlight) (flightphase.FlightPhase, int, int) {

	switch {
	// ARRIVAL
	case minsToSchedArr > AMINUS_APPROACH_MINS && minsToSchedArr <= AMINUS_ARRIVAL_MINS:
		jitter := rand.IntN((ARRIVAL_JITTER_SECONDS*2)+1) - ARRIVAL_JITTER_SECONDS
		remainingDur := (AbsDiff(minsToSchedArr, AMINUS_APPROACH_MINS) * 60) + jitter
		return flightphase.Arrival, remainingDur, AbsInt(((AMINUS_ARRIVAL_MINS - AMINUS_APPROACH_MINS) * 60) + jitter)

	// // APPROACH:
	// case minsToSchedArr > AMINUS_FINAL_MINS && minsToSchedArr <= AMINUS_APPROACH_MINS:
	// 	jitter := rand.IntN((APPROACH_JITTER_SECONDS*2)+1) - APPROACH_JITTER_SECONDS
	// 	remainingDur := (AbsDiff(minsToSchedArr, AMINUS_FINAL_MINS) * 60) + jitter
	// 	return flightphase.Approach, remainingDur, AbsInt(((AMINUS_APPROACH_MINS - AMINUS_FINAL_MINS) * 60) + jitter)

	// // FINAL: Redirect to TaxIn
	// case minsToSchedArr > AMINUS_LAND_MINS && minsToSchedArr <= AMINUS_FINAL_MINS:
	// 	// Calculate the complete standard duration it takes to taxi to the gate
	// 	fullTaxiInWindow := AbsInt(AMINUS_TAXIIN_MINS-AMINUS_SHUTDOWN_MINS) * 60
	// 	// Because the aircraft is resetting to the runway exit to start a fresh taxi,
	// 	// we grant it the full duration to execute the path realistically.
	// 	remainingDur := fullTaxiInWindow
	// 	return flightphase.TaxiIn, remainingDur, fullTaxiInWindow

	// // BRAKING OVERRIDE: Redirect to TaxiIn
	// // This clears the runway immediately and feeds the ground network a realistic timeline.
	// case minsToSchedArr > AMINUS_BRAKING && minsToSchedArr <= AMINUS_LAND_MINS:
	// 	// Calculate the complete standard duration it takes to taxi to the gate
	// 	fullTaxiInWindow := AbsInt(AMINUS_TAXIIN_MINS-AMINUS_SHUTDOWN_MINS) * 60
	// 	// Because the aircraft is resetting to the runway exit to start a fresh taxi,
	// 	// we grant it the full duration to execute the path realistically.
	// 	remainingDur := fullTaxiInWindow
	// 	return flightphase.TaxiIn, remainingDur, fullTaxiInWindow

	// TAXI IN:
	case minsToSchedArr > AMINUS_TAXIIN_MINS && minsToSchedArr <= AMINUS_BRAKING:
		remainingDur := AbsInt(minsToSchedArr-AMINUS_TAXIIN_MINS) * 60
		return flightphase.TaxiIn, remainingDur, AbsInt(AMINUS_BRAKING-AMINUS_TAXIIN_MINS) * 60

	// SHUTDOWN:
	case minsToSchedArr > AMINUS_SHUTDOWN_MINS && minsToSchedArr <= AMINUS_TAXIIN_MINS:
		jitter := rand.IntN((SHUTDOWN_JITTER_SECONDS*2)+1) - SHUTDOWN_JITTER_SECONDS
		remainingDur := (AbsDiff(minsToSchedArr, AMINUS_SHUTDOWN_MINS) * 60) + jitter
		return flightphase.Shutdown, remainingDur, AbsInt(AMINUS_TAXIIN_MINS-AMINUS_SHUTDOWN_MINS) * 60

	// PARKED:
	case minsToSchedArr >= AMINUS_PARKED_MINS && minsToSchedArr <= AMINUS_SHUTDOWN_MINS:
		jitter := rand.IntN((PARKED_JITTER_SECONDS*2)+1) - PARKED_JITTER_SECONDS
		remainingDur := (AbsDiff(minsToSchedArr, AMINUS_PARKED_MINS) * 60) + jitter
		return flightphase.Parked, remainingDur, AbsInt(AMINUS_SHUTDOWN_MINS-AMINUS_PARKED_MINS) * 60

	// CRUISE EXPLICIT CASE:
	case minsToSchedArr > AMINUS_ARRIVAL_MINS:
		jitter := rand.IntN((CRUISE_JITTER_SECONDS*2)+1) - CRUISE_JITTER_SECONDS

		remainingCruiseMins := minsToSchedArr - AMINUS_ARRIVAL_MINS
		remainingCruiseSecs := (remainingCruiseMins * 60) + jitter

		totalCruiseMins := AbsDiff(f.ArrivalHour*60+f.ArrivalMin, f.DepartureHour*60+f.DepartureMin) - DMINUS_DEPARTURE_MINS - AMINUS_ARRIVAL_MINS
		if totalCruiseMins <= remainingCruiseMins {
			totalCruiseMins = remainingCruiseMins + 15
		}
		totalCruiseSecs := totalCruiseMins * 60

		return flightphase.Cruise, AbsInt(remainingCruiseSecs), AbsInt(totalCruiseSecs)

	default:
		return flightphase.Unknown, 0, 0
	}
}

// Selects best arrival runway based on STAR count, wind utility, and outboard preference
func (e *D9TrafficEngine) selectBestArrivalRunway(ap *atc.Airport, candidates []*atc.Runway, weather *atc.Weather) *atc.Runway {
	var best *atc.Runway
	bestScore := -10000.0

	for _, rwy := range candidates {
		score := e.getRunwayUtilityScore(rwy, weather.Wind.Direction, weather.Wind.Speed)

		// Heavy penalty if no STARs exist; bonus for published STARs
		if len(rwy.STARs) == 0 {
			score -= 500.0
		} else {
			score += 100.0 + float64(len(rwy.STARs))*10.0
		}

		// Real-World Outboard Preference: Arrivals prefer runways further from airport center
		distToCenter := geometry.DistNM(ap.Lat, ap.Lon, rwy.Lat, rwy.Lon)
		score += distToCenter * 5.0 // Favor outer runways

		if score > bestScore {
			bestScore = score
			best = rwy
		}
	}
	return best
}

func (e *D9TrafficEngine) isRunwayPendingArrivals(ap *atc.Airport, rwyName string) bool {
	// loop through all active aircraft and check if any are in the Approach, final or braking phase and assigned to this runway
	for _, ac := range e.ActiveAircraft {
		if ap.ICAO == ac.Flight.Destination && ac.Flight.AssignedRunway.Name == rwyName && (flightphase.FlightPhase(ac.Flight.Phase.Current) == flightphase.Approach ||
			flightphase.FlightPhase(ac.Flight.Phase.Current) == flightphase.Final ||
			flightphase.FlightPhase(ac.Flight.Phase.Current) == flightphase.Braking) {
			return true
		}
	}
	return false
}

func (e *D9TrafficEngine) timeDiffToScheduledArrival(f *flightplan.ScheduledFlight) int {
	currSimZTime := e.AtcService.GetCurrentZuluTime()
	h, m, _ := currSimZTime.Clock()

	nowMins := (h * 60) + m
	arrMins := (f.ArrivalHour * 60) + f.ArrivalMin

	diff := arrMins - nowMins

	// Handle midnight wrap-around:
	// If it's 23:55 (1435 mins) and arrival is 00:05 (5 mins)
	// diff is -1430. Adding 1440 makes it a 10-minute TTA.
	if diff < -720 {
		diff += 1440
	} else if diff > 720 {
		diff -= 1440
	}

	return diff
}

func (e *D9TrafficEngine) positionAtDestParking(ac *atc.Aircraft) {
	airport := e.AtcService.Airports[ac.Flight.Destination]
	if ac.Flight.AssignedParkingSpot == nil {
		e.assignParking(ac, airport)
		if ac.Flight.AssignedParkingSpot == nil {
			util.LogWarnWithLabel(ac.Registration, "no suitable parking found at airport %s - ending flight", airport.ICAO)
			e.endFlight(ac)
			return
		} else {
			util.LogWithLabel(ac.Registration, "assigning parking at airport %s to spot %s", airport.ICAO, ac.Flight.AssignedParkingSpot.Name)
		}
	}
	ac.Flight.Position.Lat = ac.Flight.AssignedParkingSpot.Lat
	ac.Flight.Position.Long = ac.Flight.AssignedParkingSpot.Lon
	ac.Flight.Position.Heading = geometry.NormalizeHeading(ac.Flight.AssignedParkingSpot.Heading)
	ac.Flight.Position.Altitude = airport.Elevation

	util.LogWithLabel(ac.Registration, "positioned at gate %s", ac.Flight.AssignedParkingSpot.Name)
}
