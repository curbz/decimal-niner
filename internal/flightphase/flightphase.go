package flightphase

import (
	"time"

	"github.com/curbz/decimal-niner/internal/flightclass"
)

type FlightPhase int

const (
	Unknown  FlightPhase = iota - 1
	Parked               //0 5 - Long period parked.
	Startup              //1 6 - Short period of spooling up engines/electrics.
	TaxiOut              //2 7 - Any ground movement from the gate to the runway.
	Depart               //3 8 - Initial ground roll and first part of climb.
	Climbout             //4 10 - Remainder of climb, gear up.
	Cruise               //5 0 - Normal cruise phase.
	Holding              //6 12 - Holding (waiting for a flow to complete changing)
	Approach             //7 1 -Positioning from cruise to the runway.
	Final                //8 2 - Gear down on final approach.
	GoAround             //9 9 - Unplanned transition from approach to cruise.
	Braking              //10 11 - Short period from touchdown to when fast-taxi speed is reached.
	TaxiIn               //11 3 - Any ground movement after touchdown.
	Shutdown             //12 4 - Short period of spooling down engines/electrics.
)

func (fp FlightPhase) String() string {
	return [...]string{
		"Unknown",
		"Parked",
		"Startup",
		"Taxi_Out",
		"Depart",
		"Climbout",
		"Cruise",
		"Holding",
		"Approach",
		"Final",
		"Go_Around",
		"Braking",
		"Taxi_In",
		"Shutdown",
	}[fp+1]
}

func (fp FlightPhase) Index() int {
	return int(fp)
}

type Phase struct {
	Class      flightclass.PhaseClass
	Current    int
	Previous   int // used for detecting changes, previous refers to last update and not necessarily the actual previous phase
	Transition time.Time
}
