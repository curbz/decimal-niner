package flightclass

type PhaseClass int

const (
	Unknown          PhaseClass = iota - 1 // -1
	PreflightParked                        // 0
	Departing                              // 1 = all flight phases from startup to climb out
	Cruising                               // 2
	Arriving                               // 3 = all flight phases from approach to shutdown (includes holding)
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