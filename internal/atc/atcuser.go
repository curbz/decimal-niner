package atc

import (
	"fmt"
	"math"

	"github.com/curbz/decimal-niner/pkg/geometry"
	"github.com/curbz/decimal-niner/pkg/util"
)

type UserState struct {
	NearestAirport     *Airport
	Position           Position
	ActiveFacilities   map[int]*Controller // Key: 1 for COM1, 2 for COM2
	TunedFreqs         map[int]int         // Key: 1 for COM1, 2 for COM2
	TunedFacilityRoles map[int]int         // Key: 1 for COM1, 2 for COM2
	AssignedParking    ParkingSpot
	IsOnGround 	   	   bool
}

func (s *Service) GetUserState() UserState {
	return s.UserState
}

func (s *Service) NotifyUserStateChange(pos Position, tunedFreqs, tunedFacilityRoles map[int]int, isOnGround bool) {

	s.UserState.Position = pos
	if s.UserState.ActiveFacilities == nil {
		s.UserState.ActiveFacilities = make(map[int]*Controller)
	}
	s.UserState.IsOnGround = isOnGround
	s.UserState.TunedFreqs = tunedFreqs
	s.UserState.TunedFacilityRoles = tunedFacilityRoles

	for idx, freq := range tunedFreqs {
		uFreq := normaliseFreq(int(freq))
		role := tunedFacilityRoles[idx]
		if role == 0 {
			// change role to -1 otherwise locatetController will specifically match on Unicom role
			role = -1
		}
		controller := s.locateController(
			fmt.Sprintf("User_COM%d", idx),
			uFreq, // Search by freq
			role,
			pos.Lat, pos.Long, pos.Altitude,
			"",
		)

		if controller != nil {
			s.UserState.ActiveFacilities[idx] = controller
			util.LogWithLabel(fmt.Sprintf("User_COM%d", idx), "controller found for user on COM%d %d: %s %s Role: %s (%d)", idx, uFreq,
				controller.Name, controller.ICAO, roleNameMap[controller.RoleID], controller.RoleID)
		} else {
			util.LogWithLabel(fmt.Sprintf("User_COM%d", idx), "No nearby controller found for user on COM%d %d", idx, uFreq)
		}
	}

	nearestICAO := s.AirportService.GetClosestAirport(pos.Lat, pos.Long, 1000)
	if apt, found := s.Airports[nearestICAO]; found {
		s.UserState.NearestAirport = apt
	} else {
		s.UserState.NearestAirport = nil
	}
}

// UserHasRunwayClearance checks if the user occupies the runway or is less than 3nm out on approach.
func (s *Service) UserHasRunwayClearance(rwy *Runway) bool {

	if rwy == nil {
		return false
	}

	u := s.GetUserState()
	if !u.IsOnGround { return false}

	// simple AABB (Axis-Aligned Bounding Box) check to avoid expensive maths
	if math.Abs(u.Position.Lat - rwy.Lat) > 0.1 {
		return false
	}


    xtd := geometry.DistanceFromLine(u.Position.Lat, u.Position.Long, rwy.Lat, rwy.Lon, rwy.Heading)
    atd := geometry.AlongTrackDistance(u.Position.Lat, u.Position.Long, rwy.Lat, rwy.Lon, rwy.Heading)

    // 1. PHYSICAL CHECK (On Ground)
    if u.IsOnGround {
        result := xtd < 50.0 && atd > -50.0 && atd < (rwy.Length + 100.0)
		if result == true {
			util.LogWithLabel("USER", "user is occupying runway %s at %s", rwy.Name, u.NearestAirport.ICAO)
		}
		return result
	}

    // 2. APPROACH CHECK (In Air)
    // 3nm = 5556 meters. We use 1800ft AGL to cover the 3-degree glideslope height at 3nm plus a buffer of 1000
    if !u.IsOnGround && u.Position.Altitude < (rwy.ThresholdElevation + 2800) {
        // We widen the XTD slightly (80m) for the air check. 
        // Players aren't always perfectly on the center line when flying manually.
        isAligned := xtd < 80.0              
        isInside3NM := atd < 0 && atd > -5556 
        
        if isAligned && isInside3NM {
            util.LogWithLabel("USER", "User is occupying 3nm approach tunnel for runway %s at %s", rwy.Name, u.NearestAirport.ICAO)
            return true
        }
    }

    return false
}

