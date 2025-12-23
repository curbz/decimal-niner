package model

import "time"

// Position
type Position struct {
	Lat      float64
	Long     float64
	Altitude float64
	Heading  float64
}

type ATC struct {
	Callsign         string
	Frequency        float64
	LastTransmission string
	LastInstruction     string
}

// Flight
type Flight struct {
	Position    Position
	FlightNum   int64
	TaxiRoute   string
	Origin      string
	Destination string
	Phase       int
	PhaseTransition time.Time
	ATC         ATC
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
