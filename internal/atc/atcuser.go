package atc

import (
	"fmt"

	"github.com/curbz/decimal-niner/pkg/util"
)

type UserState struct {
	NearestICAO        string
	Position           Position
	ActiveFacilities   map[int]*Controller // Key: 1 for COM1, 2 for COM2
	TunedFreqs         map[int]int         // Key: 1 for COM1, 2 for COM2
	TunedFacilityRoles map[int]int         // Key: 1 for COM1, 2 for COM2
}

func (s *Service) GetUserState() UserState {
	return s.UserState
}

func (s *Service) NotifyUserStateChange(pos Position, tunedFreqs, tunedFacilityRoles map[int]int) {

	s.UserState.Position = pos
	if s.UserState.ActiveFacilities == nil {
		s.UserState.ActiveFacilities = make(map[int]*Controller)
	}

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
			s.UserState.NearestICAO = controller.ICAO
			util.LogWithLabel(fmt.Sprintf("User_COM%d", idx), "Controller found for user on COM%d %d: %s %s Role: %s (%d)", idx, uFreq,
				controller.Name, controller.ICAO, roleNameMap[controller.RoleID], controller.RoleID)
		} else {
			util.LogWithLabel(fmt.Sprintf("User_COM%d", idx), "No nearby controller found for user on COM%d %d", idx, uFreq)
		}
	}
}
