package flightphase

import (
	"time"

	"github.com/curbz/decimal-niner/internal/flightclass"
)

type FlightPhase int

const (
	Unknown   FlightPhase = iota - 1
	Parked                //0 - Long period parked.
	Startup               //1 - Short period of spooling up engines/electrics.
	TaxiOut               //2 - Any ground movement from the gate to the runway.
	Takeoff               //3 - Initial ground roll and first part of climb.
	Climbout              //4 - Remainder of climb, gear up.
	Departure             //5 - SID/transition from terminal to enroute
	Cruise                //6 - Normal cruise phase.
	Arrival               //7 - STAR/transition from enroute to terminal
	Holding               //8 - Holding
	Approach              //9 - Approach
	Final                 //10 - Gear down on final approach.
	GoAround              //11 - Unplanned transition from approach to cruise.
	Braking               //12 - Short period from touchdown to when fast-taxi speed is reached.
	TaxiIn                //13 - Any ground movement after touchdown.
	Shutdown              //14 - Short period of spooling down engines/electrics.
)

func (fp FlightPhase) String() string {
	return [...]string{
		"Unknown",
		"Parked",
		"Startup",
		"Taxi_Out",
		"Takeoff",
		"Climbout",
		"Departure",
		"Cruise",
		"Arrival",
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
	Class                   flightclass.PhaseClass
	Current                 int
	Previous                int // used for detecting changes, previous refers to last update and not necessarily the actual previous phase
	Transition              time.Time
	LastUpdateTime          time.Time
	EstimatedNextTransition time.Time     // used by d9traffic engine to estimate when the next phase transition will occur
	TotalDuration           time.Duration // used by d9traffic to record total duration of current phase
	InitialAltitude         float64       // used by d9traffic for altitude calculations on phase changes
	PositionComplete        bool          // set by position-driven updaters when the aircraft has reached the target for the phase
}
