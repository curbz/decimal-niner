package traffic

import (
	"github.com/curbz/decimal-niner/internal/atc"
	"github.com/curbz/decimal-niner/internal/flightplan"
)

type Engine interface {
	GetFlightPlanPath() string
	LoadFlightPlans(string) (map[string][]flightplan.ScheduledFlight, map[string]bool)
	SetATCService(*atc.Service)
	RequiresAircraftData() bool		// Indicates whether the traffic engine needs to read aircraft data from X-Plane to function
	Start()
}
