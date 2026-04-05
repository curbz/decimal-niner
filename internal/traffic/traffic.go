package traffic

import "github.com/curbz/decimal-niner/internal/flightplan"

type Engine interface {
	GetFlightPlanPath() string
	LoadFlightPlans(string) (map[string][]flightplan.ScheduledFlight, map[string]bool)
}
