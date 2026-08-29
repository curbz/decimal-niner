package d9traffic

import (
	"github.com/curbz/decimal-niner/internal/atc"
	"github.com/curbz/decimal-niner/internal/flightphase"
	"github.com/curbz/decimal-niner/internal/server"
)

func (e *D9TrafficEngine) ServeRadarFrame(radarSrv *server.RadarServer) {
	var blips []server.RadarBlip

	var runways []atc.Runway
	var holds []atc.Hold

	runwaysMap := make(map[string]atc.Runway)
	holdsMap := make(map[string]atc.Hold)

	// Lock or safely iterate through active aircraft
	for _, ac := range e.ActiveAircraft {
		if ac == nil {
			continue
		}

		typeOrClass := ac.Type
		if typeOrClass == "" {
			typeOrClass = ac.SizeClass
		}

		blips = append(blips, server.RadarBlip{
			Callsign:                 ac.Flight.Comms.Callsign,
			Registration:             ac.Registration, // e.g., "BAW308"
			AircraftType:             typeOrClass,     // e.g., "A20N" or "C"
			Lat:                      ac.Flight.Position.Lat,
			Lng:                      ac.Flight.Position.Long,
			Altitude:                 ac.Flight.Position.Altitude,
			Heading:                  int(ac.Flight.Position.Heading),
			Phase:                    flightphase.FlightPhase(ac.Flight.Phase.Current).String(),
			Origin:                   ac.Flight.Origin,
			Destination:              ac.Flight.Destination,
			GroundSpeed:              ac.Flight.GroundSpeed,
			ActiveCollisionAvoidance: ac.Flight.ActiveManeuver != nil,
		})

		if ac.Flight.AssignedRunway != nil {
			runwaysMap[ac.Flight.AssignedRunway.Name] = *ac.Flight.AssignedRunway
		}
		if ac.Flight.Holding != nil && ac.Flight.Holding.AssignedHold != nil {
			holdsMap[ac.Flight.Holding.AssignedHold.Ident] = *ac.Flight.Holding.AssignedHold
		}
	}

	// Convert maps to slices for serialization
	for _, rwy := range runwaysMap {
		runways = append(runways, rwy)
	}
	for _, hold := range holdsMap {
		holds = append(holds, hold)
	}

	userPos := e.AtcService.GetUserState().Position

	snapshot := server.RadarSnapshot{
		CenterLat: userPos.Lat,
		CenterLng: userPos.Long,
		Timestamp: e.AtcService.GetCurrentZuluTime(),
		Aircraft:  blips,
		Runways:   runways,
		Holds:     holds,
	}

	// Ship it to the streaming server
	radarSrv.BroadcastSnapshot(snapshot)
}
