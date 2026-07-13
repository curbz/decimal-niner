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
	collisionMaxVerticalSeparation = 800.0
	collisionFunnelHalfAngleDeg    = 15.0
	collisionAirportBufferNM       = 3.0
	collisionBaseTurnRateDegPerSec = 3.0
	collisionReferenceGroundSpeed  = 200.0
	collisionMinTurnRateDegPerSec  = 1.5
	collisionMaxTurnRateDegPerSec  = 6.0
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
		if !isWithinFunnel(ac, other) {
			continue
		}
		if ac.Flight.Phase.Current == flightphase.Climbout.Index() && ac.Flight.Position.Altitude >= other.Flight.Position.Altitude {
			ac.Flight.Position.Altitude = other.Flight.Position.Altitude + collisionMaxVerticalSeparation
			util.LogDebugWithLabel(ac.Registration, "entering avoidance climb")
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

func isWithinFunnel(ac, other *atc.Aircraft) bool {
	if ac == nil || other == nil {
		return false
	}

	bearingToOther := geometry.CalculateBearing(
		ac.Flight.Position.Lat,
		ac.Flight.Position.Long,
		other.Flight.Position.Lat,
		other.Flight.Position.Long,
	)
	relativeBearing := math.Abs(geometry.BearingDiff(ac.Flight.Position.Heading, bearingToOther))
	return relativeBearing <= collisionFunnelHalfAngleDeg
}

func collisionTurnRateDegPerSec(groundSpeed float64) float64 {
	if groundSpeed <= 0 {
		groundSpeed = collisionReferenceGroundSpeed
	}
	rate := collisionBaseTurnRateDegPerSec * (groundSpeed / collisionReferenceGroundSpeed)
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

func (e *D9TrafficEngine) turnDirectionWouldIntrude(ac *atc.Aircraft, direction atc.ManeuverDirection) bool {
	if ac == nil || ac.Flight.Schedule == nil {
		return false
	}
	originAp := e.AtcService.Airports[ac.Flight.Schedule.IcaoOrigin]
	destAp := e.AtcService.Airports[ac.Flight.Schedule.IcaoDest]
	if originAp == nil && destAp == nil {
		return false
	}
	turnRate := collisionTurnRateDegPerSec(ac.Flight.GroundSpeed)
	radiusNM := collisionTurnRadiusNM(ac.Flight.GroundSpeed, turnRate)
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
	if e.turnDirectionWouldIntrude(ac, atc.ManeuverDirectionRight) && !e.turnDirectionWouldIntrude(ac, atc.ManeuverDirectionLeft) {
		return atc.ManeuverDirectionLeft
	}
	if e.turnDirectionWouldIntrude(ac, atc.ManeuverDirectionLeft) && !e.turnDirectionWouldIntrude(ac, atc.ManeuverDirectionRight) {
		return atc.ManeuverDirectionRight
	}
	return atc.ManeuverDirectionRight
}

func (e *D9TrafficEngine) startCollisionManeuver(ac *atc.Aircraft) {
	if ac == nil {
		return
	}
	ac.Flight.ActiveManeuver = &atc.ManeuverState{
		Direction:         e.chooseCollisionTurnDirection(ac),
		RemainingDegrees:  360.0,
		TurnRateDegPerSec: collisionTurnRateDegPerSec(ac.Flight.GroundSpeed),
	}
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
	state.RemainingDegrees -= headingDelta
	if state.RemainingDegrees <= 0 {
		util.LogDebugWithLabel(ac.Registration, "avoidance action complete")
		ac.Flight.ActiveManeuver = nil
	}

	distanceMovedThisTick := ac.Flight.GroundSpeed * (deltaSec / 3600.0)
	ac.Flight.Position.Lat, ac.Flight.Position.Long = geometry.Project(ac.Flight.Position.Lat, ac.Flight.Position.Long, ac.Flight.Position.Heading, distanceMovedThisTick)
	ac.Flight.Phase.LastUpdateTime = currSimZTime
}
