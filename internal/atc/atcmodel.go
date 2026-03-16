package atc

import (
	"time"
)


type AirlineInfo struct {
	AirlineName string `json:"airline_name"`
	Callsign    string `json:"callsign"`
	CountryCode string `json:"icao_country_code"`
}

// +----------------------------------------------------------------------------------------+
// | Aircraft and nested types. Do not use unexported fields as deep copy will exclude them |
// +----------------------------------------------------------------------------------------+
type Aircraft struct {
	Flight       Flight
	Type         string
	SizeClass    string
	Code         string
	Airline      string
	Registration string
}

type Flight struct {
	Position            Position
	LastCheckedPosition Position
	Number              int
	TaxiRoute           string
	Origin              string
	Destination         string
	Phase               Phase
	Comms               Comms
	CruiseAlt           int
	AssignedParking     string
	AssignedRunway      string
	Squawk              string
	PlanAssigned		bool
}

type Position struct {
	Lat      float64
	Long     float64
	Altitude float64
	Heading  float64
}

type Phase struct {
	Class      PhaseClass
	Current    int 
	Previous   int // used for detecting changes, previous refers to last update and not necessarily the actual previous phase
	Transition time.Time
}

type PhaseClass int

const (
	Unknown          PhaseClass = iota - 1 // -1
	PreflightParked                        // 0
	Departing                              // 1 = all flight phases from startup to climb out
	Cruising                               // 2
	Arriving                               // 3 = all flight phases from approach to shutdown
	PostflightParked                       // 4
)

func (fc PhaseClass) String() string {
	return [...]string{
		"Unknown",
		"PreflightParked",
		"Departing",
		"Cruising",
		"Arriving",
		"PostflightParked",
	}[fc+1]
}

