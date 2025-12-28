package model

import "time"

// Position
type Position struct {
	Lat      float64
	Long     float64
	Altitude float64
	Heading  float64
}

type Comms struct {
	Callsign         string
	Frequency        float64
	LastTransmission string
	LastInstruction  string
}

// Flight
type Flight struct {
	Position    Position
	FlightNum   int64
	TaxiRoute   string
	Origin      string
	Destination string
	Phase       Phase
	Comms       Comms
}

type Phase struct {
	Current   int
	Previous  int	// used for detecting changes, previous refers to last update and not necessarily the actual previous phase
	Transition time.Time
}

// Aircraft
type Aircraft struct {
	Flight       Flight
	Type         string
	Class        string
	Code         string
	Airline      string
	Registration string
}
