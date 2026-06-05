package traffic

import (
	"math"

	"github.com/curbz/decimal-niner/internal/atc"
)

const (
	LastCheckedPositionLateralThreshold = 5.0 // distance in nautical miles that the aircraft must have moved since the last check to trigger cruise sector change detection logic
)

type CommonTrafficEngine struct {
	AtcService *atc.Service
}

// CheckForCruiseSectorChange will trigger cruise sector change detection logic if the aircraft
// is in cruise and has travelled at least 5 NM since the last position check
func (cte *CommonTrafficEngine) CheckForCruiseSectorChange(ac *atc.Aircraft) {

	// if we don't have a controller assigned, assign one now, update last checked position and return
	if ac.Flight.Comms.Controller == nil {
		ac.Flight.Comms.Controller = cte.AtcService.AssignController(ac)
		ac.Flight.LastCheckedPosition = ac.Flight.Position
		// no need to continue as another attempt to assign a controller now would result in the same controller
		return
	}

	// if a handoff is already in progress or the aircraft has travelled less than ~11 meters (0.0001 degrees)
	// since last check (allows for data value fluctuations) then return
	if ac.Flight.Comms.CruiseHandoff != atc.NoHandoff ||
		(math.Abs(ac.Flight.Position.Lat-ac.Flight.LastCheckedPosition.Lat) < 0.0001 &&
			math.Abs(ac.Flight.Position.Long-ac.Flight.LastCheckedPosition.Long) < 0.0001) {
		return
	}

	dist := atc.CalculateDistance(ac.Flight.Position, ac.Flight.LastCheckedPosition)
	// Only notify if moved more than threshold
	if dist > LastCheckedPositionLateralThreshold {
		// Trigger the cruise handoff detection logic
		cte.AtcService.NotifyCruisePositionChange(ac)
		// Update the checkpoint
		ac.Flight.LastCheckedPosition = ac.Flight.Position
	}
}
