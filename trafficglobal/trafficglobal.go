package trafficglobal

import (
	"bytes"
	"encoding/binary"
	"io"
	"log"
	"os"

	"github.com/curbz/decimal-niner/pkg/util"
)

type FlightPhase int

const (
	Unknown FlightPhase = iota -1
	Cruise			// Normal cruise phase.
	Approach			// Positioning from cruise to the runway.
	Final				// Gear down on final approach.
	TaxiIn				// Any ground movement after touchdown.
	Shutdown			// Short period of spooling down engines/electrics.
	Parked				// Long period parked.
	Startup				// Short period of spooling up engines/electrics.
	TaxiOut				// Any ground movement from the gate to the runway.
	Depart				// Initial ground roll and first part of climb.
	GoAround			// Unplanned transition from approach to cruise.
	Climbout			// Remainder of climb, gear up.
	Braking				// Short period from touchdown to when fast-taxi speed is reached.
	Holding				// Holding, waiting for a flow to complete changing.
)

var days = []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}

func (fp FlightPhase) String() string {
	return [...]string{
		"Unknown",
		"Cruise",
		"Approach",
		"Final",
		"Taxi In",
		"Shutdown",
		"Parked",
		"Startup",
		"Taxi Out",
		"Depart",
		"Go Around",
		"Climbout",
		"Braking",
		"Waiting for flow change",
	}[fp+1]
}

func (fp FlightPhase) Index() int {
	return int(fp)
}

type ScheduledFlight struct {
	AircraftRegistration string
	Number int
	IcaoOrigin string
	IcaoDest string
	DayOfWeek int
	DepatureHour int
	DepartureMin int
	ArrivalHour int
	ArrivalMin int
	CruiseAlt int
}

type config struct {
	TG struct {
		BGLFile string          `yaml:"bgl_file"`
	} `yaml:"trafficglobal"`
}

func LoadConfig(cfgPath string) *config {
	
	log.Println("Loading Traffic Global configurations")

	cfg, err := util.LoadConfig[config](cfgPath)
	if err != nil {
		log.Fatalf("Error reading configuration file: %v\n", err)
	}
	
	return cfg
}

func BGLReader(filePath string) map[string]ScheduledFlight {
	
	fScheds := make(map[string]ScheduledFlight)

	file, err := os.Open(filePath)
	if err != nil {
		log.Fatalf("Fatal: %v\n", err)
	}
	defer file.Close()

	// Skip the first 500KB to bypass the Airport Index "junk"
	file.Seek(500*1024, 0)

	buffer := make([]byte, 1024*1024)
	log.Println("Scanning for Active Schedules...")

	for {
		n, err := file.Read(buffer)
		if n == 0 { break }

		for i := 0; i < n-30; i++ {
			if isICAO(buffer[i : i+4]) {
				icao1 := string(buffer[i : i+4])
				
				for j := i + 5; j < i+35 && j+4 < n; j++ {
					if isICAO(buffer[j : j+4]) {
						icao2 := string(buffer[j : j+4])
						
						if icao1 != icao2 {
							var reg string
							var flightNum uint16
							if i >= 60 {
								reg, flightNum = parseHeader(buffer[i-60 : i])
							}

							// Only proceed if we found a valid-looking registration
							if len(reg) > 3 {
								// Extract TimeCode (2 bytes following the second ICAO)
								tCode := binary.LittleEndian.Uint16(buffer[j+4 : j+6])
								
								// Decode into Day/Hour/Min
								day, hr, min := decodeTime(tCode)

								if day < 7 { // Simple validation for the decoded time
									fScheds[string(flightNum) + "_" + reg] = ScheduledFlight{
											AircraftRegistration: reg,
											Number: int(flightNum),
											IcaoOrigin: icao1, 
											IcaoDest: icao2,
											DayOfWeek: day,
											DepatureHour: hr,
											DepartureMin: min,
									}
								}
								
								i = j + 3 
								break 
							}
						}
					}
				}
			}
		}
		if err == io.EOF { break }
	}

	log.Printf("Loaded %d Traffic Global flight schedules", len(fScheds))

	return fScheds
}

func decodeTime(t uint16) (int, int, int) {
	// Standard FSX/TrafficGlobal Weekly Minutes
	d := int(t / 1440)
	h := int((t % 1440) / 60)
	m := int(t % 60)
	return d, h, m
}

func isICAO(b []byte) bool {
	for _, v := range b {
		if v < 'A' || v > 'Z' { return false }
	}
	return true
}

func parseHeader(b []byte) (string, uint16) {
	idx := bytes.IndexByte(b, '-')
	// Registrations are usually 5-7 chars: VT-WBW, N12345
	if idx != -1 && idx >= 2 && idx <= len(b)-6 {
		reg := b[idx-2 : idx+4]
		
		// Strict validation: registration must be alphanumeric
		for _, v := range reg {
			if v != '-' && (v < 'A' || v > 'Z') && (v < '0' || v > '9') {
				return "", 0
			}
		}

		// The 2 bytes after the registration are the Flight Number
		fNum := binary.LittleEndian.Uint16(b[idx+5 : idx+7])
		return string(reg), fNum
	}
	return "", 0
}