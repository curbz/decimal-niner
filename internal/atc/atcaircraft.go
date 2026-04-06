package atc

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/curbz/decimal-niner/internal/flightclass"
	"github.com/curbz/decimal-niner/internal/flightphase"
	"github.com/curbz/decimal-niner/internal/flightplan"
	"github.com/curbz/decimal-niner/pkg/geometry"
	"github.com/curbz/decimal-niner/pkg/util"
	"github.com/mohae/deepcopy"
)

// +----------------------------------------------------------------------------------------+
// | Aircraft and nested types. Do not use unexported fields as deep copy will exclude them |
// +----------------------------------------------------------------------------------------+
type Aircraft struct {
	Flight       Flight
	Type         string
	SizeClass    string
	Code         string
	Registration string
}

type Flight struct {
	Position            Position
	LastCheckedPosition Position
	Number              int
	TaxiRoute           string
	Origin              string
	Destination         string
	Phase               flightphase.Phase
	Comms               Comms
	CruiseAlt           int
	AssignedParking     string
	AssignedRunway      string
	Squawk              string
	PlanAssigned        bool
	AirlineName         string
}

type Position struct {
	Lat      float64
	Long     float64
	Altitude float64
	Heading  float64
}

type AirlineInfo struct {
	AirlineName string `json:"airline_name"`
	Callsign    string `json:"callsign"`
	CountryCode string `json:"icao_country_code"`
}

func (s *Service) NotifyFlightPhaseChange(ac *Aircraft) {

	userActive := s.UserState.ActiveFacilities

	if len(userActive) == 0 {
		util.LogWithLabel(ac.Registration, "User has no active tuned ATC facilities")
		return
	}

	// set flight phase classification
	s.setFlightPhaseClass(ac)
	util.LogWithLabel(ac.Registration, "flight %d phase %s classified as %s",
		ac.Flight.Number,
		flightphase.FlightPhase(ac.Flight.Phase.Current),
		ac.Flight.Phase.Class.String())

	// for a new aircraft in a post-flight context, there is nothing to do
	if ac.Flight.Phase.Class == flightclass.PostflightParked {
		return
	}

	if !ac.Flight.PlanAssigned {
		// attempt to assign a flight plan
		planAssigned := s.AddFlightPlan(ac, s.GetCurrentZuluTime())
		if !planAssigned {
			// still no flight plan, infer origin/dest from aircraft position and phase, no gaurantees we will set anything
			// it is also ok to call this on multiple phase changes as this helps to complete the data
			s.inferFlightPlan(ac)
		}
		// in the unlikely, but possible case that we have not set a comms country code by now, use flight data as a fallback
		if ac.Flight.Comms.CountryCode == "" {
			inferCommsCountryCode(ac, s.Config.ATC.Voices.CommsCountryCodeDefault)
		}
	}

	// make a snaphot copy of aircraft current state and pass this snapshot into the phrase generation process.
	// it is safer to do it here rather than in the go routine as there would be a small chance that
	// the aircraft could get updated concurrently during the deep copy process if this statement was
	// placed within the go routine.
	v := deepcopy.Copy(ac)
	acSnap, ok := v.(*Aircraft)
	if !ok {
		util.LogWarnWithLabel(ac.Registration, "failed to deepcopy aircraft snapshot; skipping async phrase generation")
		return
	}

	util.GoSafe(func() {
		// +-----------------------------------------------------------------+
		// | Only use acSnap to reference the aircraft within the go routine |
		// +-----------------------------------------------------------------+
		acSnap.Flight.Comms.Controller = s.AssignController(acSnap)
		if acSnap.Flight.Comms.Controller != nil {
			s.Transmit(s.UserState, acSnap)
		}
	})
}

func (s *Service) NotifyCruisePositionChange(ac *Aircraft) {

	util.LogWithLabel(ac.Registration, "Position update, checking for sector change")
	// 1. Determine current sector based on Lat/Long/Alt
	ac.Flight.Comms.NextController = s.locateController(ac.Registration+"_CRUISE_UPDATE", 0, 6,
		ac.Flight.Position.Lat,
		ac.Flight.Position.Long,
		ac.Flight.Position.Altitude, "")

	// 2. Check for Handoff
	if ac.Flight.Comms.Controller != nil && ac.Flight.Comms.Controller.Name != "" &&
		ac.Flight.Comms.Controller.Name != ac.Flight.Comms.NextController.Name {
		util.LogWithLabel(ac.Registration, "Handoff from %s to %s", ac.Flight.Comms.Controller.Name, ac.Flight.Comms.NextController.Name)
		// creat snapshot of aircraft state for phrase generation
		v := deepcopy.Copy(ac)
		acSnap, ok := v.(*Aircraft)
		if !ok {
			util.LogWarnWithLabel(ac.Registration, "failed to deepcopy aircraft snapshot for cruise handoff; skipping phrase generation")
		} else {
			acSnap.Flight.Comms.CruiseHandoff = HandoffExitSector
			// send to phrase generation
			s.Transmit(s.UserState, acSnap)
		}
		// update current controller
		ac.Flight.Comms.Controller = ac.Flight.Comms.NextController
	}
}

// CheckForCruiseSectorChange will trigger cruise sector change detection logic if the aircraft
// is in cruise and has travelled at least 5 NM since the last position check
func (s *Service) CheckForCruiseSectorChange(ac *Aircraft) {

	// if we are not in cruise, there is no need to check for sector changes
	if ac.Flight.Phase.Current != flightphase.Cruise.Index() {
		return
	}

	// if last check position has not yet been set, set it now and return
	if ac.Flight.LastCheckedPosition.Lat == 0 && ac.Flight.LastCheckedPosition.Long == 0 {
		ac.Flight.LastCheckedPosition = ac.Flight.Position
		return
	}

	// if we don't have a controller assigned, assign one now, update last checked position and return
	if ac.Flight.Comms.Controller == nil {
		ac.Flight.Comms.Controller = s.AssignController(ac)
		ac.Flight.LastCheckedPosition = ac.Flight.Position
		return
	}

	// if a handoff is already in progress or the aircraft has travelled less than ~11 meters (0.0001 degrees)
	// since last check (allows for data value fluctuations) then return
	if ac.Flight.Comms.CruiseHandoff != NoHandoff ||
		(math.Abs(ac.Flight.Position.Lat-ac.Flight.LastCheckedPosition.Lat) < 0.0001 &&
			math.Abs(ac.Flight.Position.Long-ac.Flight.LastCheckedPosition.Long) < 0.0001) {
		return
	}

	dist := calculateDistance(ac.Flight.Position, ac.Flight.LastCheckedPosition)
	//fmt.Println("Distance from last cruise check: ", dist, " NM")
	// Only notify if moved more than 5.0 NM
	if dist > 5.0 {
		// Trigger the cruise handoff detection logic
		s.NotifyCruisePositionChange(ac)
		// Update the checkpoint
		ac.Flight.LastCheckedPosition = ac.Flight.Position
	}
}

func (s *Service) GetAirline(code string) *AirlineInfo {
	airlineInfo, exists := s.Airlines[code]
	if !exists {
		return nil
	}
	return &airlineInfo
}

// AddFlightPan locates the flight plan for this aircraft situation, returns true if flight plan assigned successfully
func (s *Service) AddFlightPlan(ac *Aircraft, simTime time.Time) bool {

	simTodayDayOfWeek := util.GetISOWeekday(simTime)
	simYesterdayDayOfWeek := (simTodayDayOfWeek + 6) % 7
	simMinsSinceMidnight := simTime.Hour()*60 + simTime.Minute()

	candidateScheds := make([]flightplan.ScheduledFlight, 0)

	// find active flights using schedule times
	// when no flight found, expand search by 20 minutes up to 4 hours
	adjDep := 0
	for adjArr := 0; adjArr <= 240; adjArr = adjArr + 20 {

		adjDep = -adjArr

		// get all scheds for yesterday and filter. For yesterday's departures, active
		// flights are those where the arrival day of week is today and arrival time is greater
		// or eqaul to the current time
		key := fmt.Sprintf("%s_%d_%d", ac.Registration, ac.Flight.Number, simYesterdayDayOfWeek)
		scheds, found := s.FlightSchedules[key]
		if found {
			for _, f := range scheds {
				schedArrMinsSinceMidnight := f.ArrivalHour*60 + f.ArrivalMin + adjArr
				if f.ArrivalDayOfWeek == simTodayDayOfWeek && schedArrMinsSinceMidnight >= simMinsSinceMidnight {
					candidateScheds = append(candidateScheds, f)
				}
			}
		}

		// get all scheds for today and filter. For today's departures, active
		// flights are those where the current time is between the departure time
		// and arrival time
		key = fmt.Sprintf("%s_%d_%d", ac.Registration, ac.Flight.Number, simTodayDayOfWeek)
		scheds, found = s.FlightSchedules[key]
		if found {
			for _, f := range scheds {
				schedDepMinsSinceMidnight := f.DepatureHour*60 + f.DepartureMin + adjDep
				schedArrMinsSinceMidnight := f.ArrivalHour*60 + f.ArrivalMin + adjArr
				if simMinsSinceMidnight >= schedDepMinsSinceMidnight && simMinsSinceMidnight <= schedArrMinsSinceMidnight {
					candidateScheds = append(candidateScheds, f)
				}
			}
		}

		if len(candidateScheds) > 0 {
			// no need to expand search further, we have candidate flights so jump out here
			break
		}

	}

	if len(candidateScheds) == 0 {
		util.LogWithLabel(ac.Registration, "no active flight plan found for flight no. %d days %d and %d",
			ac.Flight.Number, simTodayDayOfWeek, simYesterdayDayOfWeek)
		if s.Config.ATC.StrictFlightPlanMatch {
			return false
		}
		// fallback to find by tail number and flight only, on any day and time
		util.LogWithLabel(ac.Registration, "find inactive flight plan for flight no. %d", ac.Flight.Number)
		for i := simTodayDayOfWeek; i <= (simTodayDayOfWeek + 6); i++ {
			day := i % 7
			key := fmt.Sprintf("%s_%d_%d", ac.Registration, ac.Flight.Number, day)
			scheds, found := s.FlightSchedules[key]
			if found {
				for _, f := range scheds {
					candidateScheds = append(candidateScheds, f)
				}
			}
		}

		if len(candidateScheds) == 0 {
			util.LogWithLabel(ac.Registration, "no inactive flight plan found for flight no. %d", ac.Flight.Number)
			return false
		}
	}

	// there should only be one flight in the candidates, but capturing instances where
	// there is multiple for diagnostics
	if len(candidateScheds) > 1 {
		util.LogWithLabel(ac.Registration, "multiple (%d) flight plans found for flight number %d days %d and %d", len(candidateScheds), ac.Flight.Number, simTodayDayOfWeek, simYesterdayDayOfWeek)
		for i, c := range candidateScheds {
			util.LogWithLabel(ac.Registration, "duplicate flight %d of %d: %v", i+1, len(candidateScheds), c)
		}
	}

	// use remaining candidate i.e. [0]
	ac.Flight.Origin = candidateScheds[0].IcaoOrigin
	ac.Flight.Destination = candidateScheds[0].IcaoDest
	ac.Flight.CruiseAlt = candidateScheds[0].CruiseAlt * 100
	ac.Flight.AirlineName = candidateScheds[0].AirlineName

	util.LogWithLabel(ac.Registration, "flight %d origin %s", ac.Flight.Number, ac.Flight.Origin)
	util.LogWithLabel(ac.Registration, "flight %d destination %s (cruise alt: %d)", ac.Flight.Number, ac.Flight.Destination, ac.Flight.CruiseAlt)

	ac.Flight.PlanAssigned = true

	return ac.Flight.PlanAssigned
}

// inferFlightPlan is last resort strategy to fill in missing origin/destination based on phase and location.
func (s *Service) inferFlightPlan(ac *Aircraft) {
	// Safety guard: If we have a full plan, don't touch it.
	if ac.Flight.Origin != "" && ac.Flight.Destination != "" {
		return
	}

	// if flight position is empty return
	if ac.Flight.Position == (Position{}) {
		return
	}

	closestAirport := s.AirportService.GetClosestAirport(ac.Flight.Position.Lat, ac.Flight.Position.Long, 4.0)

	// infer what we can from current location
	switch ac.Flight.Phase.Class {
	case flightclass.Departing:
		if ac.Flight.Origin == "" {
			util.LogWithLabel(ac.Registration, "no flight plan - inference used to assign departing flight with origin of %s", closestAirport)
			ac.Flight.Origin = closestAirport
		}
	case flightclass.Arriving:
		if ac.Flight.Destination == "" {
			util.LogWithLabel(ac.Registration, "no flight plan - inference used to assign arriving flight with destination of %s", closestAirport)
			ac.Flight.Destination = closestAirport
		}
		// Origin can safely remain empty is this scenario as it is unlikely to be referenced by ATC at this stage of flight
	}

	// we don't check Cruising phase as there is nothing we can infer - we can call again after transition to approach (Arriving phase)
	// we also do not set Flight.PlanAssigned to true
}

func (s *Service) setFlightPhaseClass(ac *Aircraft) {

	ph := &ac.Flight.Phase

	// 1. STICKY GUARD:
	// If we've already assigned a specific class (like Preflight or Postflight),
	// and the Sim phase hasn't actually changed, don't re-run the heavy logic.
	if ph.Class != flightclass.Unknown && ph.Current == ph.Previous {
		return
	}

	switch ph.Current {
	// we include Shutdown here as there has been scenarios observed in the traffic global
	// plugin whereby the aircraft has been assigned a new flight plan whilst still in
	// the shutdown state
	case flightphase.Parked.Index(), flightphase.Shutdown.Index():
		if ph.Previous == flightphase.Unknown.Index() {
			// new aircraft flight - determine if preflight or postflight
			if ac.Flight.Origin == "" || ac.Flight.Destination == "" {
				util.LogWarnWithLabel(ac.Registration, "no origin/destination for parked aircraft flight %d - unable to determine flight phase classification", ac.Flight.Number)
				ph.Class = flightclass.Unknown
				return
			}
			currAirport := s.AirportService.GetClosestAirport(ac.Flight.Position.Lat, ac.Flight.Position.Long, 4.0)
			if ac.Flight.Destination == currAirport {
				util.LogWithLabel(ac.Registration, "flight %d is parked at destination airport %s", ac.Flight.Number, ac.Flight.Destination)
				ph.Class = flightclass.PostflightParked
				return
			} else {
				util.LogWithLabel(ac.Registration, "flight %d is parked at origin airport %s", ac.Flight.Number, ac.Flight.Origin)
				ph.Class = flightclass.PreflightParked
				return
			}
		} else {
			ph.Class = flightclass.PostflightParked
			return
		}
	case flightphase.Startup.Index(),
		flightphase.TaxiOut.Index(),
		flightphase.Depart.Index(),
		flightphase.Climbout.Index():
		ph.Class = flightclass.Departing
		return
	case flightphase.Approach.Index(),
		flightphase.Holding.Index(),
		flightphase.Final.Index(),
		flightphase.GoAround.Index(),
		flightphase.Braking.Index(),
		flightphase.TaxiIn.Index():
		ph.Class = flightclass.Arriving
		return
	case flightphase.Cruise.Index():
		ph.Class = flightclass.Cruising
		return
	default:
		ph.Class = flightclass.Unknown
	}
}

func (s *Service) getTransistionAltitude(ac *Aircraft) (transitionAlt int) {

	// 1. Try the Controller's ICAO first (works for Tower/Approach)

	if ac.Flight.Comms.Controller != nil {
		cIcao := ac.Flight.Comms.Controller.ICAO
		if ap, ok := s.Airports[cIcao]; ok && ap.TransAlt > 0 {
			return ap.TransAlt
		}
	}

	// 2. FALLBACK: Look at the nearest airport under the plane
	// This is crucial for Center controllers who don't have a TransAlt
	nearICAO := s.AirportService.GetClosestAirport(ac.Flight.Position.Lat, ac.Flight.Position.Long, 30.0)
	if nearAp, ok := s.Airports[nearICAO]; ok && nearAp.TransAlt > 0 {
		transitionAlt = nearAp.TransAlt
	} else {
		// 3. FINAL FALLBACK: Continental Standards
		// If ICAO starts with E or L (Europe), use 6000, otherwise 18000
		if strings.HasPrefix(nearICAO, "E") || strings.HasPrefix(nearICAO, "L") {
			transitionAlt = 6000
		} else {
			transitionAlt = 18000
		}
	}

	return transitionAlt
}

func calculateDistance(pos1, pos2 Position) float64 {
	return geometry.DistNM(pos1.Lat, pos1.Long, pos2.Lat, pos2.Long)
}
