package flightphase


type FlightPhase int

const (
	Unknown  FlightPhase = iota - 1
	Cruise               // 0 - Normal cruise phase.
	Approach             // 1 -Positioning from cruise to the runway.
	Final                // 2 - Gear down on final approach.
	TaxiIn               // 3 - Any ground movement after touchdown.
	Shutdown             // 4 - Short period of spooling down engines/electrics.
	Parked               // 5 - Long period parked.
	Startup              // 6 - Short period of spooling up engines/electrics.
	TaxiOut              // 7 - Any ground movement from the gate to the runway.
	Depart               // 8 - Initial ground roll and first part of climb.
	GoAround             // 9 - Unplanned transition from approach to cruise.
	Climbout             // 10 - Remainder of climb, gear up.
	Braking              // 11 - Short period from touchdown to when fast-taxi speed is reached.
	Holding              // 12 - Holding (waiting for a flow to complete changing)
)

func (fp FlightPhase) String() string {
	return [...]string{
		"Unknown",
		"Cruise",
		"Approach",
		"Final",
		"Taxi_In",
		"Shutdown",
		"Parked",
		"Startup",
		"Taxi_Out",
		"Depart",
		"Go_Around",
		"Climbout",
		"Braking",
		"Holding",
	}[fp+1]
}

func (fp FlightPhase) Index() int {
	return int(fp)
}
