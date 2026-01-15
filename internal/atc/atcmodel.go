package atc

import (
	"time"
)

type UserState struct {
	NearestICAO      string
	Position         Position
	ActiveFacilities map[int]*Controller // Key: 1 for COM1, 2 for COM2
	TunedFreqs       map[int]int         // Key: 1 for COM1, 2 for COM2
	TunedFacilities  map[int]int         // Key: 1 for COM1, 2 for COM2
}

type AirlineInfo struct {
	AirlineName string `json:"airline_name"`
	Callsign    string `json:"callsign"`
	CountryCode string `json:"icao_country_code"`
}

type Aircraft struct {
	Flight       Flight
	Type         string
	Class        string
	Code         string
	Airline      string
	Registration string
}

type Flight struct {
	Position        Position
	Number          int
	TaxiRoute       string
	Origin          string
	Destination     string
	Phase           Phase
	Comms           Comms
	AssignedParking string
	AssignedRunway  string
}

type Position struct {
	Lat      float64
	Long     float64
	Altitude float64
	Heading  float64
}

type Phase struct {
	Current    int
	Previous   int // used for detecting changes, previous refers to last update and not necessarily the actual previous phase
	Transition time.Time
}

type Comms struct {
	Callsign         string
	Controller       *Controller
	CountryCode		 string
	LastTransmission string
	LastInstruction  string
}

// ATCMessage represents a single ATC communication message
type ATCMessage struct {
	ICAO        string
	Callsign    string
	Role        string
	Text        string
	FlightPhase int
	CountryCode string
}

type Airspace struct {
	Floor, Ceiling float64
	Points         [][2]float64
}

type Controller struct {
	Name, ICAO string
	RoleID     int
	Freqs      []int
	Lat, Lon   float64
	IsPoint    bool
	IsRegion   bool
	Airspaces  []Airspace
}
