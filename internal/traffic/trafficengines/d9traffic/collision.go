package d9traffic

import (
	"math"
	"time"

	"github.com/curbz/decimal-niner/internal/atc"
	"github.com/curbz/decimal-niner/internal/flightphase"
	"github.com/curbz/decimal-niner/pkg/geometry"
	"github.com/curbz/decimal-niner/pkg/util"
)

const (
	collisionMaxDistanceNM         = 2.0
	collisionMaxVerticalSeparation = 999.0
	collisionFunnelHalfAngleDeg    = 85.0
	collisionAirportBufferNM       = 3.0
	collisionBaseTurnRateDegPerSec = 3.0
	collisionReferenceGroundSpeed  = 200.0
	collisionMinTurnRateDegPerSec  = 1.5
	collisionMaxTurnRateDegPerSec  = 4.5
	collisionManeuverHeadingOffset = 45.0
)

func isAirbornePhase(phase int) bool {
	switch flightphase.FlightPhase(phase) {
	case flightphase.Climbout,
		flightphase.Departure,
		flightphase.Cruise,
		flightphase.Arrival,
		flightphase.Holding,
		flightphase.Approach,
		flightphase.Final,
		flightphase.GoAround:
		return true
	default:
		return false
	}
}

func (e *D9TrafficEngine) detectCollisionThreat(ac *atc.Aircraft) *atc.Aircraft {
	if !isAirbornePhase(ac.Flight.Phase.Current) {
		return nil
	}

	for _, other := range e.ActiveAircraft {
		if other == nil || other == ac || other.Flight.Schedule == nil {
			continue
		}
		if !isAirbornePhase(other.Flight.Phase.Current) {
			continue
		}
		verticalSeparation := math.Abs(ac.Flight.Position.Altitude - other.Flight.Position.Altitude)
		if verticalSeparation > collisionMaxVerticalSeparation {
			continue
		}
		distanceToOther := geometry.DistNM(
			ac.Flight.Position.Lat,
			ac.Flight.Position.Long,
			other.Flight.Position.Lat,
			other.Flight.Position.Long,
		)
		if distanceToOther > collisionMaxDistanceNM {
			continue
		}
		if !isWithinFunnel(ac, other, collisionFunnelHalfAngleDeg) {
			continue
		}
		if other.Flight.ActiveManeuver != nil {
			// If the other aircraft is already maneuvering, we will not initiate a new maneuver for this aircraft.
			continue
		}
		return other
	}

	return nil
}

func isWithinFunnel(ac, other *atc.Aircraft, funnelHalfAngle float64) bool {
	bearingToOther := geometry.CalculateBearing(
		ac.Flight.Position.Lat,
		ac.Flight.Position.Long,
		other.Flight.Position.Lat,
		other.Flight.Position.Long,
	)
	relativeBearing := math.Abs(geometry.BearingDiff(ac.Flight.Position.Heading, bearingToOther))
	return relativeBearing <= funnelHalfAngle
}

func collisionTurnRateDegPerSec(groundSpeed float64) float64 {
	if groundSpeed <= 0 {
		groundSpeed = collisionReferenceGroundSpeed
	}
	rate := collisionBaseTurnRateDegPerSec * (groundSpeed / (collisionReferenceGroundSpeed - 20.0))
	if rate < collisionMinTurnRateDegPerSec {
		rate = collisionMinTurnRateDegPerSec
	}
	if rate > collisionMaxTurnRateDegPerSec {
		rate = collisionMaxTurnRateDegPerSec
	}
	return rate
}

func collisionTurnRadiusNM(groundSpeed, turnRateDegPerSec float64) float64 {
	if turnRateDegPerSec <= 0 {
		return 0
	}
	groundSpeedNmPerSec := groundSpeed / 3600.0
	angularRateRadPerSec := turnRateDegPerSec * math.Pi / 180.0
	return groundSpeedNmPerSec / angularRateRadPerSec
}

func (e *D9TrafficEngine) turnDirectionConflictsWithAirport(ac *atc.Aircraft, direction atc.ManeuverDirection) bool {
	if ac == nil || ac.Flight.Schedule == nil {
		return false
	}
	originAp := e.AtcService.Airports[ac.Flight.Schedule.IcaoOrigin]
	destAp := e.AtcService.Airports[ac.Flight.Schedule.IcaoDest]
	if originAp == nil && destAp == nil {
		return false
	}
	turnRate := collisionTurnRateDegPerSec(ac.Flight.GroundSpeed - 20.0)
	radiusNM := collisionTurnRadiusNM(ac.Flight.GroundSpeed-20.0, turnRate)
	centerBearing := ac.Flight.Position.Heading
	if direction == atc.ManeuverDirectionRight {
		centerBearing += 90.0
	} else {
		centerBearing -= 90.0
	}
	centerLat, centerLon := geometry.Project(ac.Flight.Position.Lat, ac.Flight.Position.Long, centerBearing, radiusNM)

	for _, airport := range []*atc.Airport{originAp, destAp} {
		if airport == nil {
			continue
		}
		distToAirport := geometry.DistNM(centerLat, centerLon, airport.Lat, airport.Lon)
		if math.Abs(distToAirport-radiusNM) < collisionAirportBufferNM {
			return true
		}
	}
	return false
}

func (e *D9TrafficEngine) chooseCollisionTurnDirection(ac *atc.Aircraft) atc.ManeuverDirection {

	// first check if either direction conflicts with the threat aircraft's heading to avoid turning into the threat's path.
	// we must avoid at all costs, so if one direction is clear and the other is not, we will choose the clear direction.
	threat := ac.Flight.ActiveManeuver.Threat

	threatBearing := geometry.CalculateBearing(
		ac.Flight.Position.Lat,
		ac.Flight.Position.Long,
		threat.Flight.Position.Lat,
		threat.Flight.Position.Long,
	)

	// calculate the relative bearing of the threat aircraft from the current aircraft's heading - this can be negative or positive
	// and will be used to determine if the threat is on the left or right side of the current aircraft's heading.
	ac.Flight.ActiveManeuver.ThreatRelativeBearing = geometry.BearingDiff(ac.Flight.Position.Heading, threatBearing)

	leftTurnBearing := geometry.NormalizeHeading(ac.Flight.Position.Heading - 90.0)
	rightTurnBearing := geometry.NormalizeHeading(ac.Flight.Position.Heading + 90.0)

	leftTurnConflict := math.Abs(geometry.BearingDiff(leftTurnBearing, threatBearing)) < collisionFunnelHalfAngleDeg
	rightTurnConflict := math.Abs(geometry.BearingDiff(rightTurnBearing, threatBearing)) < collisionFunnelHalfAngleDeg

	if leftTurnConflict && !rightTurnConflict {
		return atc.ManeuverDirectionRight
	} else if rightTurnConflict && !leftTurnConflict {
		return atc.ManeuverDirectionLeft
	}

	// next check if either direction conflicts with airport proximity
	airportConflictLeft := e.turnDirectionConflictsWithAirport(ac, atc.ManeuverDirectionLeft)
	airportConflictRight := e.turnDirectionConflictsWithAirport(ac, atc.ManeuverDirectionRight)

	if airportConflictLeft && !airportConflictRight {
		return atc.ManeuverDirectionRight
	} else if airportConflictRight && !airportConflictLeft {
		return atc.ManeuverDirectionLeft
	}

	// if both directions are clear, choose the direction that is furthest from the threat aircraft's heading.
	leftTurnDiff := math.Abs(geometry.BearingDiff(leftTurnBearing, threatBearing))
	rightTurnDiff := math.Abs(geometry.BearingDiff(rightTurnBearing, threatBearing))

	if leftTurnDiff > rightTurnDiff {
		return atc.ManeuverDirectionLeft
	} else {
		return atc.ManeuverDirectionRight
	}
}

func (e *D9TrafficEngine) startCollisionManeuver(ac *atc.Aircraft, threat *atc.Aircraft) {
	if ac == nil {
		return
	}
	ac.Flight.ActiveManeuver = &atc.ManeuverState{
		Threat:            threat,
		RemainingDegrees:  360.0,
		TurnRateDegPerSec: collisionTurnRateDegPerSec(ac.Flight.GroundSpeed - 20.0),
	}
	ac.Flight.ActiveManeuver.Direction = e.chooseCollisionTurnDirection(ac)
}

func (e *D9TrafficEngine) advanceCollisionManeuver(ac *atc.Aircraft, currSimZTime time.Time) {
	if ac == nil || ac.Flight.ActiveManeuver == nil {
		return
	}
	state := ac.Flight.ActiveManeuver
	deltaSec := currSimZTime.Sub(ac.Flight.Phase.LastUpdateTime).Seconds()
	// Align collision maneuver timing with frame-based position updates.
	// Treat extremely small elapsed times as a single standard tick so repeated
	// immediate test loops and low-resolution updates still make progress.
	if deltaSec <= 0 || deltaSec < 1.0 || deltaSec > 20.0 {
		deltaSec = 10.0
	}
	headingDelta := state.TurnRateDegPerSec * deltaSec
	if headingDelta > state.RemainingDegrees {
		headingDelta = state.RemainingDegrees
	}
	sign := 1.0
	if state.Direction == atc.ManeuverDirectionLeft {
		sign = -1.0
	}
	ac.Flight.Position.Heading = geometry.NormalizeHeading(ac.Flight.Position.Heading + sign*headingDelta)
	util.LogDebugWithLabel(ac.Registration, "changed heading to %f", ac.Flight.Position.Heading)

	state.RemainingDegrees -= headingDelta
	remainingThreshold := 360 - collisionManeuverHeadingOffset
	if ac.Flight.Phase.Current == flightphase.Approach.Index() {
		remainingThreshold = 0.0
	}
	if state.RemainingDegrees <= remainingThreshold {
		ac.Flight.ActiveManeuver.Resolved = true
		e.triggerPhrase(ac)
		util.LogDebugWithLabel(ac.Registration, "avoidance action complete")
		ac.Flight.ActiveManeuver = nil
	}

	distanceMovedThisTick := (ac.Flight.GroundSpeed - 20.0) * (deltaSec / 3600.0)
	ac.Flight.Position.Lat, ac.Flight.Position.Long = geometry.Project(ac.Flight.Position.Lat, ac.Flight.Position.Long, ac.Flight.Position.Heading, distanceMovedThisTick)
	ac.Flight.Phase.LastUpdateTime = currSimZTime
}
