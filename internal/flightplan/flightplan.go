package flightplan

// ScheduledFlight is the requested output struct for each parsed leg.
type ScheduledFlight struct {
	AircraftRegistration string
	Airline 			 string
	Number               int
	IcaoOrigin           string
	IcaoDest             string
	DepartureDayOfWeek   int
	DepatureHour         int
	DepartureMin         int
	ArrivalDayOfWeek     int
	ArrivalHour          int
	ArrivalMin           int
	CruiseAlt            int
}
