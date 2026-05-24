package traffic

import (
	"math"

	"github.com/curbz/decimal-niner/internal/atc"
	"github.com/curbz/decimal-niner/internal/flightplan"
	"github.com/curbz/decimal-niner/pkg/geometry"
	"github.com/curbz/decimal-niner/pkg/util"
)

const (
	DEPARTURE_CONTEXT = 0
	ARRIVAL_CONTEXT   = 1
)

type Engine interface {
	GetFlightPlanPath() string
	LoadFlightPlans(string) (map[string][]flightplan.ScheduledFlight, map[string]bool)
	SetATCService(*atc.Service)
	RequiresAircraftData() bool // Indicates whether the traffic engine needs to read aircraft data from X-Plane to function
	Start()
}

// assignRunwayAccessPoint assigns the runway access or exit point depending on whether the arrOrDep flag
// is set to arrival (0) or departure (1)
func AssignRunwayAccessPoint(ac *atc.Aircraft, ap *atc.Airport, arrOrDep int) {

	minDistToGate := math.MaxFloat64
	var selected *atc.AccessPoint
	spot := ac.Flight.AssignedParkingSpot

	rwy := ac.Flight.AssignedRunway
	if rwy == nil {
		var exists bool
		rwy, exists = ap.Runways[ac.Flight.AssignedRunwayName]
		if !exists {
			util.LogWarnWithLabel(ac.Registration, "unable to assign runway access - runway name %s not found at %s",
				ac.Flight.AssignedRunwayName, ap.ICAO)
			return
		}
		ac.Flight.AssignedRunway = rwy
	}

	var accessMap map[string]*atc.AccessPoint
	if arrOrDep == ARRIVAL_CONTEXT {
		accessMap = rwy.ArrivalAccess
		//TODO: decide on if we want logic to consider IsHighSpeed, aircraft size, IsNearEnd etc. for arrivals
	} else {
		accessMap = rwy.DepartureAccess
	}

	for _, access := range accessMap {
		// Which of these qualified entries is closest to our PARKED position?
		dist := geometry.DistNM(spot.Lat, spot.Lon, access.Coord.Lat, access.Coord.Lon)
		if dist < minDistToGate {
			minDistToGate = dist
			selected = access
		}
	}

	if arrOrDep == ARRIVAL_CONTEXT {
		ac.Flight.ArrivalAccess = selected
	} else {
		ac.Flight.DepartureAccess = selected
	}

}
