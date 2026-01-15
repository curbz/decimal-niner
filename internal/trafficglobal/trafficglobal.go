package trafficglobal

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"

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

//TODO: pass in current sim time and only load flights that are either in progress
// or due to depart within 12 hours
func BGLReader(filePath string) map[string]ScheduledFlight {
	
	fScheds := make(map[string]ScheduledFlight)

    file, err := os.Open(filePath)
    if err != nil {
        log.Fatalf("Fatal: %v\n", err)
    }
    defer file.Close()

    buffer := make([]byte, 1024*1024)

    cnt := 0
    for {
        n, err := file.Read(buffer)
        if n == 0 {
            break
        }

        for i := 0; i < n-20; i++ {
            if isICAO(buffer[i : i+4]) {
                icao1 := string(buffer[i : i+4])

                for j := i + 5; j < i+30 && j+4 < n; j++ {
                    if isICAO(buffer[j : j+4]) {
                        icao2 := string(buffer[j : j+4])

                        if icao1 != icao2 {
                            // Step 1: Extract Aircraft and Flight Number
                            var reg string
                            var flightNum uint16
                            if i >= 50 {
                                reg, flightNum = parseFlightHeader(buffer[i-50 : i])
                            }

                            if reg != "" {
                                // Step 2: Extract Time Metadata and Flight Parameters
                                // After the second ICAO: departure time (2 bytes), flight duration (2 bytes),
                                // traffic density (1 byte), cruise level (2 bytes)
                                if j+13 < n {
                                    depTime := binary.LittleEndian.Uint16(buffer[j+4 : j+6])
									depDay, depHr, depMin := decodeTime(depTime)

									/*
                                    flightDuration := binary.LittleEndian.Uint16(buffer[j+6 : j+8])
                                    trafficDensity := buffer[j+8]
                                    cruiseLevel := binary.LittleEndian.Uint16(buffer[j+9 : j+11])

                                    arrTime := depTime + flightDuration
                                    arrDay, arrHr, arrMin := decodeTime(arrTime)

                                    // Handle week rollover: if arrival >= 10080 minutes (7 days), wrap
                                    if arrTime >= 10080 {
                                        arrTime -= 10080
                                        arrDay, arrHr, arrMin = decodeTime(arrTime)
                                    }

                                    // Cruise level scaling: try different factors
                                    cruiseLevelValue := cruiseLevel / 100
                                    if cruiseLevelValue == 0 {
                                        cruiseLevelValue = cruiseLevel
                                    }
									*/
									
									if depDay < 7 { // Simple validation for the decoded times
                                    //if depDay < 7 && arrDay < 7 { // Simple validation for the decoded times
                                        // Traffic density should be 1-100, handle out-of-range values
										/*
                                        trafficPercent := int(trafficDensity)
                                        if trafficPercent < 1 {
                                            trafficPercent = 1
                                        } else if trafficPercent > 100 {
                                            trafficPercent = trafficPercent % 100
                                            if trafficPercent == 0 {
                                                trafficPercent = 100
                                            }
                                        }
										*/
										/*
                                        fmt.Printf("[%s] Flt# %-5d | %s -> %s | Departs: %s %02d:%02d | Arrives: %s %02d:%02d | Cruise: FL%d | Traffic: %d%%\n",
                                            reg, flightNum, icao1, icao2, days[depDay], depHr, depMin,
                                            days[arrDay], arrHr, arrMin, cruiseLevelValue, trafficPercent)
										*/
                                        fmt.Printf("[%s] Flt# %-5d | %s -> %s | Departs: %s %02d:%02d\n",
                                            reg, flightNum, icao1, icao2, days[depDay], depHr, depMin)

                                            fScheds[strconv.Itoa(int(flightNum)) + "_" + reg] = ScheduledFlight{
											AircraftRegistration: reg,
											Number: int(flightNum),
											IcaoOrigin: icao1, 
											IcaoDest: icao2,
											DayOfWeek: depDay,
											DepatureHour: depHr,
											DepartureMin: depMin,
										}
                                    }

                                    i = j + 3
                                    cnt++
                                    break
                                }
                            }
                        }
                    }
                }
            }
        }
        if err == io.EOF {
            break
        }
    }

	log.Printf("Loaded %d Traffic Global flight schedules", len(fScheds))

	return fScheds
}

func isICAO(b []byte) bool {
    for _, v := range b {
        if v < 'A' || v > 'Z' {
            return false
        }
    }
    return true
}

func parseFlightHeader(b []byte) (string, uint16) {
    idx := bytes.IndexByte(b, '-')
    if idx != -1 && idx >= 2  { //&& idx <= len(b) { //-6 {
        regBytes := b[idx-2 : idx+5]  // idx+4]

        // Validate that all bytes are printable ASCII
        /*
        if !isPrintable(regBytes) {
            return "", 0
        }
        */

        reg := string(regBytes)

        // The 2 bytes following the registration are usually the Flight Number
        flightNum := binary.LittleEndian.Uint16(b[idx+len(reg) : idx+len(reg)+2])

        return reg, flightNum
    }
    return "", 0
}

func isPrintable(b []byte) bool {
    for _, v := range b {
        if v < 32 || v > 126 {
            return false
        }
    }
    return true
}

func decodeTime(t uint16) (int, int, int) {
    // Standard FSX/TrafficGlobal Weekly Minutes
    d := int(t / 1440)
    h := int((t % 1440) / 60)
    m := int(t % 60)
    return d, h, m
}

