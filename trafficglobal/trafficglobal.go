package trafficglobal

type FlightPhase int

const (
	Unknown FlightPhase = iota -1
	Cruise			// Normal cruise phase.
	Approach			// Positioning from cruise to the runway.
	Final				// Gear down on final approach.
	TaxiIn				// Any ground movement after touchdown.
	Shutdown			// Short period of spooling down engines/electrics.
	Parked				// Long period parked.
	Startup				// Short period of spooling up engines/electrics.
	TaxiOut				// Any ground movement from the gate to the runway.
	Depart				// Initial ground roll and first part of climb.
	GoAround			// Unplanned transition from approach to cruise.
	Climbout			// Remainder of climb, gear up.
	Braking				// Short period from touchdown to when fast-taxi speed is reached.
	Holding				// Holding, waiting for a flow to complete changing.
 )

func (fp FlightPhase) String() string {
	return [...]string{
		"Unknown",
		"Cruise",
		"Approach",
		"Final",
		"Taxi In",
		"Shutdown",
		"Parked",
		"Startup",
		"Taxi Out",
		"Depart",
		"Go Around",
		"Climbout",
		"Braking",
		"Waiting for flow change",
	}[fp+1]
}

func (fp FlightPhase) Index() int {
	return int(fp)
}