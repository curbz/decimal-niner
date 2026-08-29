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
	collisionManeuverHeadingOffset = 40.0
	collisionStraightLegSec        = 40.0 // Straight flight duration after offset turn
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
	threat := ac.Flight.ActiveManeuver.Threat

	threatBearing := geometry.CalculateBearing(
		ac.Flight.Position.Lat,
		ac.Flight.Position.Long,
		threat.Flight.Position.Lat,
		threat.Flight.Position.Long,
	)

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

	airportConflictLeft := e.turnDirectionConflictsWithAirport(ac, atc.ManeuverDirectionLeft)
	airportConflictRight := e.turnDirectionConflictsWithAirport(ac, atc.ManeuverDirectionRight)

	if airportConflictLeft && !airportConflictRight {
		return atc.ManeuverDirectionRight
	} else if airportConflictRight && !airportConflictLeft {
		return atc.ManeuverDirectionLeft
	}

	leftTurnDiff := math.Abs(geometry.BearingDiff(leftTurnBearing, threatBearing))
	rightTurnDiff := math.Abs(geometry.BearingDiff(rightTurnBearing, threatBearing))

	if leftTurnDiff > rightTurnDiff {
		return atc.ManeuverDirectionLeft
	}
	return atc.ManeuverDirectionRight
}

func (e *D9TrafficEngine) startCollisionManeuver(ac *atc.Aircraft, threat *atc.Aircraft) {
	if ac == nil {
		return
	}

	remainingDegrees := 360.0
	straightLegSec := 0.0

	// Approach and Holding phases perform a 360° orbit; all other phases turn 45° and fly a short straight leg
	if ac.Flight.Phase.Current != flightphase.Approach.Index() && ac.Flight.Phase.Current != flightphase.Holding.Index() {
		remainingDegrees = collisionManeuverHeadingOffset
		straightLegSec = collisionStraightLegSec
	}

	ac.Flight.ActiveManeuver = &atc.ManeuverState{
		Threat:                  threat,
		RemainingDegrees:        remainingDegrees,
		StraightLegSecRemaining: straightLegSec,
		TurnRateDegPerSec:       collisionTurnRateDegPerSec(ac.Flight.GroundSpeed - 20.0),
	}
	ac.Flight.ActiveManeuver.Direction = e.chooseCollisionTurnDirection(ac)
}

func (e *D9TrafficEngine) advanceCollisionManeuver(ac *atc.Aircraft, currSimZTime time.Time) {
	if ac == nil || ac.Flight.ActiveManeuver == nil {
		return
	}
	state := ac.Flight.ActiveManeuver
	deltaSec := currSimZTime.Sub(ac.Flight.Phase.LastUpdateTime).Seconds()

	if deltaSec <= 0 || deltaSec < 1.0 || deltaSec > 20.0 {
		deltaSec = 10.0
	}

	// 1. Turn Phase: Execute remaining degrees of turn
	turnSecUsed := 0.0
	if state.RemainingDegrees > 0 {
		turnSecNeeded := state.RemainingDegrees / state.TurnRateDegPerSec
		turnSecUsed = math.Min(deltaSec, turnSecNeeded)
		headingDelta := state.TurnRateDegPerSec * turnSecUsed

		sign := 1.0
		if state.Direction == atc.ManeuverDirectionLeft {
			sign = -1.0
		}
		ac.Flight.Position.Heading = geometry.NormalizeHeading(ac.Flight.Position.Heading + sign*headingDelta)
		util.LogDebugWithLabel(ac.Registration, "changed heading to %f", ac.Flight.Position.Heading)

		state.RemainingDegrees -= headingDelta
		if state.RemainingDegrees < 0.001 {
			state.RemainingDegrees = 0.0
		}
	}

	// 2. Straight Leg Phase: Deduct remaining tick time once turn is complete
	straightSecThisTick := deltaSec - turnSecUsed
	if straightSecThisTick > 0 && state.RemainingDegrees <= 0 && state.StraightLegSecRemaining > 0 {
		state.StraightLegSecRemaining -= straightSecThisTick
		if state.StraightLegSecRemaining < 0 {
			state.StraightLegSecRemaining = 0
		}
	}

	// 3. Complete Maneuver: Only clear state when both turn and straight leg phases finish
	if state.RemainingDegrees <= 0 && state.StraightLegSecRemaining <= 0 {
		ac.Flight.ActiveManeuver.Resolved = true
		e.triggerPhrase(ac)
		util.LogDebugWithLabel(ac.Registration, "avoidance action complete")
		ac.Flight.ActiveManeuver = nil
	}

	// Project position forward along current heading
	distanceMovedThisTick := (ac.Flight.GroundSpeed - 20.0) * (deltaSec / 3600.0)
	ac.Flight.Position.Lat, ac.Flight.Position.Long = geometry.Project(
		ac.Flight.Position.Lat,
		ac.Flight.Position.Long,
		ac.Flight.Position.Heading,
		distanceMovedThisTick,
	)
	ac.Flight.Phase.LastUpdateTime = currSimZTime
}
