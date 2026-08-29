package d9traffic

import (
	"fmt"
	"math"
	"math/rand/v2"
	"time"

	"github.com/curbz/decimal-niner/internal/atc"
	"github.com/curbz/decimal-niner/internal/flightphase"
	"github.com/curbz/decimal-niner/internal/flightplan"
	"github.com/curbz/decimal-niner/pkg/geometry"
	"github.com/curbz/decimal-niner/pkg/util"
)

func (e *D9TrafficEngine) checkForDepartureSpawns(icao string, day, h, m int) {
	timeline, ok := e.AirportSchedules[icao]
	if !ok {
		return
	}

	nowMins := (h * 60) + m
	lookahead := 30

	// We check this day and also potentially the next day if we are near midnight
	daysToCheck := []int{day}
	if nowMins+lookahead >= 1440 {
		nextDay := (day + 1) % 7
		daysToCheck = append(daysToCheck, nextDay)
	}

	for _, targetDay := range daysToCheck {
		for _, f := range timeline.Departures {
			if f.DepartureDayOfWeek != targetDay {
				continue
			}

			fMins := (f.DepartureHour * 60) + f.DepartureMin

			// Adjust time for comparison if we are looking at 'nextDay'
			compareMins := fMins
			if targetDay != day {
				compareMins += 1440
			}

			// If the flight is in the future window [now + 10, now + 30]
			if compareMins >= nowMins+10 && compareMins <= nowMins+30 {
				if !e.isCurrentlyActive(f.AircraftRegistration, f.Number) {
					e.spawnDepartureTraffic(&f)
				}
			}

			// Optimization: Since it's sorted, if we've passed the window, stop
			if compareMins > nowMins+lookahead {
				break
			}
		}
	}
}

func (e *D9TrafficEngine) spawnDepartureTraffic(f *flightplan.ScheduledFlight) {

	ttd := e.timeDiffToScheduledDeparture(f)
	initialPhase, remainingDurSecs, fullDurationSecs, delay := e.determineInitialDeparturePhase(ttd, f)
	if initialPhase == flightphase.Unknown {
		return
	}
	ip := initialPhase.Index()

	airport := e.AtcService.Airports[f.IcaoOrigin]
	destApt := e.AtcService.Airports[f.IcaoDest]
	currSimZTime := e.AtcService.GetCurrentZuluTime()

	airline := e.resolveAirline(f)
	if airline == nil {
		util.LogWarnWithLabel(f.AircraftRegistration, "unable to resolve airline for flight %s %d - aircraft will not be spawned", f.AirlineName, f.Number)
		return
	}
	if airline.AirlineName != f.AirlineName {
		util.LogWarnWithLabel(f.AircraftRegistration, "airline %s reallocated to %s", f.AirlineName, airline.AirlineName)
	}

	sizeClass := e.determineSizeClass(f, airline)
	sizeClassStr := ""
	if sizeClass == "E" || sizeClass == "F" {
		sizeClassStr = "Heavy"
	}

	// =========================================================================
	// UPFRONT GEOMETRIC COORDINATE GENERATION
	// =========================================================================
	var spawnLat, spawnLon, spawnHdg float64
	timeRatio := float64(remainingDurSecs) / float64(fullDurationSecs)
	progressRatio := 1.0 - timeRatio

	// Resolve the SID exit fix geometry early for airborne phase positioning
	assignedRwyName := e.AirportConfig[airport.ICAO].Departure.Name
	assignedRwy := e.AtcService.GetAirportRunway(airport, assignedRwyName)

	var sidExitLat, sidExitLon float64
	var sidTotalDistNM float64 = 30.0

	if sid := e.AtcService.GetMatchingSID(airport, assignedRwy, destApt); sid != nil {
		sidExitLat, sidExitLon = sid.Exit.Fix.Lat, sid.Exit.Fix.Lon
		sidTotalDistNM = geometry.DistNM(airport.Lat, airport.Lon, sidExitLat, sidExitLon)
	}

	switch {
	// ZONE A: GROUND PHASES & TAKEOFF SAFETY OVERRIDE
	// Defensively sets coordinates to 0,0 so downstream taxi positioning engines handle the assets safely.
	case ip <= flightphase.TaxiOut.Index() || ip == flightphase.Takeoff.Index():
		spawnLat, spawnLon, spawnHdg = 0.0, 0.0, 0.0

	// ZONE B: EARLY AIRBORNE CORRIDORS (Climbout & Departure)
	case ip == flightphase.Climbout.Index() || ip == flightphase.Departure.Index():
		if sidExitLat != 0 {
			// Measure along the exact track line that the tracking engine expects
			bearingToSidExit := geometry.CalculateBearing(airport.Lat, airport.Lon, sidExitLat, sidExitLon)
			distAlongSid := sidTotalDistNM * progressRatio // Use the calculated progressRatio!

			spawnLat, spawnLon = geometry.Project(airport.Lat, airport.Lon, bearingToSidExit, distAlongSid)
			spawnHdg = bearingToSidExit
		} else {
			bearingToDest := geometry.CalculateBearing(airport.Lat, airport.Lon, destApt.Lat, destApt.Lon)
			// Use fallback total distance for the phase context if no SID exists
			fallbackPhaseDist := 30.0 * progressRatio
			spawnLat, spawnLon = geometry.Project(airport.Lat, airport.Lon, bearingToDest, fallbackPhaseDist)
			spawnHdg = bearingToDest
		}

	// ZONE C: HIGH-ALTITUDE AIRWAY (Cruise)
	case ip == flightphase.Cruise.Index():
		var cruiseStartLat, cruiseStartLon float64
		if sidExitLat != 0 {
			cruiseStartLat, cruiseStartLon = sidExitLat, sidExitLon
		} else {
			bearingToDest := geometry.CalculateBearing(airport.Lat, airport.Lon, destApt.Lat, destApt.Lon)
			cruiseStartLat, cruiseStartLon = geometry.Project(airport.Lat, airport.Lon, bearingToDest, sidTotalDistNM)
		}

		cruiseDistance := geometry.DistNM(cruiseStartLat, cruiseStartLon, destApt.Lat, destApt.Lon)
		distAlongCruise := cruiseDistance * progressRatio
		bearingAlongAirway := geometry.CalculateBearing(cruiseStartLat, cruiseStartLon, destApt.Lat, destApt.Lon)

		spawnLat, spawnLon = geometry.Project(cruiseStartLat, cruiseStartLon, bearingAlongAirway, distAlongCruise)
		spawnHdg = bearingAlongAirway
	}
	// =========================================================================

	// If the phase usually takes 600s (full) and we have 200s left (dur), we've been in it for 400s.
	elapsedOffset := math.Max(0, float64(fullDurationSecs)-float64(remainingDurSecs))
	// backdate transition time
	transitionTime := currSimZTime.Add(-time.Duration(elapsedOffset) * time.Second)

	newAc := &atc.Aircraft{
		Registration: f.AircraftRegistration,
		SizeClass:    sizeClass,
		Flight: atc.Flight{
			Number:      f.Number,
			Origin:      f.IcaoOrigin,
			Destination: f.IcaoDest,
			Airline:     airline,
			Comms: atc.Comms{
				CountryCode: airline.CountryCode,
				Callsign:    fmt.Sprintf("%s %d %s", airline.Callsign, f.Number, sizeClassStr),
			},
			Position: atc.Position{
				Lat:     spawnLat,
				Long:    spawnLon,
				Heading: spawnHdg,
			},
			CruiseAlt: f.CruiseAlt * 100,
			Schedule:  f,
			// Squawk random number between 1200 and 6999
			Squawk:       fmt.Sprintf("%04d", 1200+rand.IntN(5800)),
			PlanAssigned: true,
			Phase: flightphase.Phase{
				Current:  ip,
				Previous: flightphase.Unknown.Index(),
			},
			DepartureDelay: delay,
		},
	}

	// Set all pre-requisite states - strict order is important
	e.AtcService.SetFlightPhaseClass(newAc)
	if ip < flightphase.Takeoff.Index() {
		// assign departure gate - do this BEFORE assigning the departure runway access as this may influence the selected access point
		e.assignParking(newAc, airport)
	}

	// assign departure runway
	newAc.Flight.AssignedRunway = e.AirportConfig[airport.ICAO].Departure
	newAc.Flight.AssignedRunwayName = newAc.Flight.AssignedRunway.Name

	// assign SID for departure
	e.AtcService.AssignSID(newAc, airport, newAc.Flight.AssignedRunway)

	if ip < flightphase.Takeoff.Index() {
		// assign departure runway access - must be done after parking assignment
		e.AtcService.AssignRunwayAccessPoint(newAc, airport, atc.DEPARTURE_CONTEXT)
	}

	newAc.Flight.Phase.Transition = transitionTime // BACKDATED
	newAc.Flight.Phase.EstimatedNextTransition = currSimZTime.Add(time.Duration(remainingDurSecs) * time.Second)
	newAc.Flight.Phase.TotalDuration = time.Duration(fullDurationSecs) * time.Second
	// set initial altitude
	e.assignPhaseInitialAltitude(newAc, ip)

	// initial placement
	if ip >= flightphase.Climbout.Index() {
		// If Cruise, flip to destination (arrival) runway BEFORE initializing
		if ip == flightphase.Cruise.Index() {
			rwy := e.getActiveRunway(f.IcaoDest, atc.ARRIVAL_CONTEXT)
			destApt := e.AtcService.Airports[f.IcaoDest]
			newAc.Flight.AssignedRunway = rwy
			newAc.Flight.AssignedRunwayName = rwy.Name
			// assign destination procedure
			e.AtcService.AssignSTAR(newAc, destApt, rwy)
		}
	} else {
		if ip <= flightphase.Startup.Index() {
			// For Parked/Startup, use the static parking logic
			e.positionAtOriginParking(newAc)
		}
	}

	newAc.Flight.Phase.LastUpdateTime = e.AtcService.GetCurrentZuluTime()

	// add to active aircraft map
	e.ActiveAircraft[getActiveAircraftKey(newAc)] = newAc

	util.LogWithLabel(f.AircraftRegistration, "spawned departure %s flight %d phase %s origin %s dest %s lat %0.6f lon %0.6f alt %0.6f hdg %d - estimated next transition: %v",
		f.AirlineName, f.Number, flightphase.FlightPhase(newAc.Flight.Phase.Current).String(), f.IcaoOrigin, f.IcaoDest,
		newAc.Flight.Position.Lat, newAc.Flight.Position.Long, newAc.Flight.Position.Altitude, int(newAc.Flight.Position.Heading),
		newAc.Flight.Phase.EstimatedNextTransition.Format(time.RFC3339))
}

func (e *D9TrafficEngine) timeDiffToScheduledDeparture(f *flightplan.ScheduledFlight) int {
	// Calculate diff at spawn time
	currSimZTime := e.AtcService.GetCurrentZuluTime()
	h, m, _ := currSimZTime.Clock()
	nowMins := h*60 + m
	depMins := (f.DepartureHour * 60) + f.DepartureMin
	diff := depMins - nowMins
	return diff
}

func (e *D9TrafficEngine) determineInitialDeparturePhase(minsToSchedDep int, f *flightplan.ScheduledFlight) (flightphase.FlightPhase, int, int, int) {
	delay := 0
	switch {
	// 1. LONG TERM PARKED (Way out before the tracking window)
	case minsToSchedDep > DMINUS_PARKED_MINS:
		flow, found := e.AirportConfig[f.IcaoOrigin]
		if found {
			qKey := normalizeRunwayKey(f.IcaoOrigin, flow.Departure)
			if len(e.RunwayQueues[qKey]) >= TRAFFIC_MANAGEMENT_RUNWAY_QUEUE_THRESHOLD {
				delay = len(e.RunwayQueues[qKey]) * TRAFFIC_MANAGEMENT_PER_AIRCRAFT_DELAY_SECONDS
				util.LogWithLabel(f.AircraftRegistration, "initial departure delay of %d seconds applied based on current traffic queue of %d for runway %s at %s",
					delay, len(e.RunwayQueues[qKey]), flow.Departure.Name, f.IcaoOrigin)
			}
		} else {
			util.LogWarnWithLabel(f.AircraftRegistration, "unable to determine initial departure phase due to missing airport flow for %s", f.IcaoOrigin)
		}
		jitter := rand.IntN((PARKED_JITTER_SECONDS*2)+1) - PARKED_JITTER_SECONDS
		remainingDur := (AbsDiff(minsToSchedDep, DMINUS_STARTUP_MINS) * 60) + jitter
		// Keep total clamped to a realistic standard baseline if it's way out
		totalDur := AbsInt((DMINUS_PARKED_MINS-DMINUS_STARTUP_MINS)*60) + jitter
		if remainingDur > totalDur {
			totalDur = remainingDur
		}
		return flightphase.Parked, remainingDur, totalDur, delay

	// 2. ACTIVE PRE-STARTUP PARKING
	case minsToSchedDep > DMINUS_STARTUP_MINS && minsToSchedDep <= DMINUS_PARKED_MINS:
		jitter := rand.IntN((PARKED_JITTER_SECONDS*2)+1) - PARKED_JITTER_SECONDS
		remainingDur := (AbsDiff(minsToSchedDep, DMINUS_STARTUP_MINS) * 60) + jitter
		return flightphase.Parked, remainingDur, AbsInt(((DMINUS_PARKED_MINS - DMINUS_STARTUP_MINS) * 60) + jitter), delay

	// 3. STARTUP
	case minsToSchedDep > DMINUS_TAXIOUT_MINS && minsToSchedDep <= DMINUS_STARTUP_MINS:
		jitter := rand.IntN((STARTUP_JITTER_SECONDS*2)+1) - STARTUP_JITTER_SECONDS
		remainingDur := (AbsDiff(minsToSchedDep, DMINUS_TAXIOUT_MINS) * 60) + jitter
		return flightphase.Startup, remainingDur, AbsInt(((DMINUS_STARTUP_MINS - DMINUS_TAXIOUT_MINS) * 60) + jitter), delay

	// 4. TAXI OUT
	case minsToSchedDep > DMINUS_TAKEOFF_MINS && minsToSchedDep <= DMINUS_TAXIOUT_MINS:
		remainingDur := (AbsDiff(minsToSchedDep, DMINUS_TAKEOFF_MINS) * 60)
		totalDur := (DMINUS_TAXIOUT_MINS - DMINUS_TAKEOFF_MINS) * 60
		return flightphase.TaxiOut, remainingDur, totalDur, delay

	// 5. TAKEOFF OVERRIDE: Align to full taxi parameters
	case minsToSchedDep >= DMINUS_CLIMBOUT_MINS && minsToSchedDep <= DMINUS_TAKEOFF_MINS:
		// Calculate the maximum standard duration a full taxi takes at this airport
		fullTaxiDurationSecs := AbsInt(DMINUS_TAXIOUT_MINS-DMINUS_TAKEOFF_MINS) * 60
		// Because we are resetting the aircraft to the gate to start a fresh taxi,
		// the remaining time in this phase is the FULL taxi time.
		remainingDur := fullTaxiDurationSecs
		return flightphase.TaxiOut, remainingDur, fullTaxiDurationSecs, delay

	// 6. CLIMBOUT
	case minsToSchedDep >= DMINUS_DEPARTURE_MINS && minsToSchedDep < DMINUS_CLIMBOUT_MINS:
		jitter := rand.IntN((CLIMBOUT_JITTER_SECONDS*2)+1) - CLIMBOUT_JITTER_SECONDS
		remainingDur := (AbsDiff(minsToSchedDep, DMINUS_DEPARTURE_MINS) * 60) + jitter
		return flightphase.Climbout, remainingDur, AbsInt(((DMINUS_CLIMBOUT_MINS - DMINUS_DEPARTURE_MINS) * 60) + jitter), delay

	// 7. DEPARTURE (En-route transition segment)
	case minsToSchedDep >= DMINUS_CRUISE_MINS && minsToSchedDep <= DMINUS_DEPARTURE_MINS:
		jitter := rand.IntN((DEPARTURE_JITTER_SECONDS*2)+1) - DEPARTURE_JITTER_SECONDS
		remainingDur := (AbsDiff(minsToSchedDep, DMINUS_CRUISE_MINS) * 60) + jitter
		return flightphase.Departure, remainingDur, AbsInt(((DMINUS_DEPARTURE_MINS - DMINUS_CRUISE_MINS) * 60) + jitter), delay

	// 8. CRUISE EXPLICIT BOUNDARY
	case minsToSchedDep < DMINUS_CRUISE_MINS:
		jitter := rand.IntN((CRUISE_JITTER_SECONDS*2)+1) - CRUISE_JITTER_SECONDS

		// Remaining time in cruise uses your timeDiffToScheduledArrival helper
		tta := e.timeDiffToScheduledArrival(f)
		remainingCruiseSecs := (AbsDiff(tta, AMINUS_ARRIVAL_MINS) * 60) + jitter

		// Total Cruise Duration extraction — compute duration between departure
		// and arrival while handling midnight wrap-around (e.g., dep 23:57 -> arr 00:27).
		depMins := f.DepartureHour*60 + f.DepartureMin
		arrMins := f.ArrivalHour*60 + f.ArrivalMin

		diff := arrMins - depMins
		if diff < -720 {
			diff += 1440
		} else if diff > 720 {
			diff -= 1440
		}

		totalCruiseMins := AbsInt(diff) - AbsInt(DMINUS_DEPARTURE_MINS) - AMINUS_ARRIVAL_MINS
		if totalCruiseMins*60 <= remainingCruiseSecs {
			totalCruiseMins = (remainingCruiseSecs / 60) + 15
		}
		if totalCruiseMins < 0 {
			totalCruiseMins = 0
		}

		totalCruiseSecs := totalCruiseMins * 60

		return flightphase.Cruise, AbsInt(remainingCruiseSecs), AbsInt(totalCruiseSecs), delay

	default:
		// STALE/HISTORICAL FALLBACK:
		// Catches any orphan tracking frames safely
		return flightphase.Parked, 0, 0, delay
	}
}

// Selects best departure runway based on SID count, wind utility, and inboard preference
func (e *D9TrafficEngine) selectBestDepartureRunway(ap *atc.Airport, candidates []*atc.Runway, weather *atc.Weather) *atc.Runway {
	var best *atc.Runway
	bestScore := -10000.0

	for _, rwy := range candidates {
		score := e.getRunwayUtilityScore(rwy, weather.Wind.Direction, weather.Wind.Speed)

		// Heavy penalty if no SIDs exist; bonus for published SIDs
		if len(rwy.SIDs) == 0 {
			score -= 500.0
		} else {
			score += 100.0 + float64(len(rwy.SIDs))*10.0
		}

		// Real-World Inboard Preference: Departures prefer runways closer to airport center
		distToCenter := geometry.DistNM(ap.Lat, ap.Lon, rwy.Lat, rwy.Lon)
		score -= distToCenter * 5.0 // Slightly favor inner runways

		if score > bestScore {
			bestScore = score
			best = rwy
		}
	}
	return best
}

func (e *D9TrafficEngine) positionAtOriginParking(ac *atc.Aircraft) {
	airport := e.AtcService.Airports[ac.Flight.Origin]
	if ac.Flight.AssignedParkingSpot == nil {
		e.assignParking(ac, airport)
		if ac.Flight.AssignedParkingSpot == nil {
			util.LogWarnWithLabel(ac.Registration, "no suitable parking found at origin airport %s - terminating flight", airport.ICAO)
			delete(e.ActiveAircraft, getActiveAircraftKey(ac))
			//TODO consider strategy to prevent spawn re-selection, potentially delete schedule
			return
		} else {
			util.LogWithLabel(ac.Registration, "assigning parking at airport %s to spot %s", airport.ICAO, ac.Flight.AssignedParkingSpot.Name)
		}
	}
	ac.Flight.Position = atc.Position{
		Lat:      ac.Flight.AssignedParkingSpot.Lat,
		Long:     ac.Flight.AssignedParkingSpot.Lon,
		Heading:  geometry.NormalizeHeading(ac.Flight.AssignedParkingSpot.Heading),
		Altitude: airport.Elevation,
	}
}
