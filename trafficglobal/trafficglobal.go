package trafficglobal

type FlightPhase int

const (
	FP_Unknown FlightPhase = iota -1
	FP_Cruise			// Normal cruise phase.
	FP_Approach			// Positioning from cruise to the runway.
	FP_Final				// Gear down on final approach.
	FP_TaxiIn				// Any ground movement after touchdown.
	FP_Shutdown			// Short period of spooling down engines/electrics.
	FP_Parked				// Long period parked.
	FP_Startup				// Short period of spooling up engines/electrics.
	FP_TaxiOut				// Any ground movement from the gate to the runway.
	FP_Depart				// Initial ground roll and first part of climb.
	FP_GoAround			// Unplanned transition from approach to cruise.
	FP_Climbout			// Remainder of climb, gear up.
	FP_Braking				// Short period from touchdown to when fast-taxi speed is reached.
	Holding				// Holding, waiting for a flow to complete changing.
 )