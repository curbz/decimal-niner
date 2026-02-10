package atc

import (
	"fmt"
	"strings"
)

func getTransitionLevel(ta int, currBaroPascals float64) int {
	// Standard pressure in inches of mercury
	const standardPressure = 101325.0 // Pascals
	
	// If pressure is standard or higher, TL is usually TA + 1000ft
	if currBaroPascals >= standardPressure {
		return (ta / 100) + 10 // e.g., 6000ft -> FL70
	}
	
	// If pressure is low, we need more space, so we add an extra level
	return (ta / 100) + 20 // e.g., 6000ft -> FL80
}

func formatBaro(icao string, pascals float64) string {

    digits := ""

    // Determine the regional "Keyword"
    prefix := "QNH" 
    if strings.HasPrefix(icao, "K") || strings.HasPrefix(icao, "C") {
        prefix = "altimeter"
        inHg := pascals * 0.0002953 // Convert Pascals to inches of mercury
        digits = strings.ReplaceAll(fmt.Sprintf("%.2f", inHg), ".", "") // "2992"
    } else {
        hpa := int(pascals / 100) // Convert pascals to hPa
        digits = fmt.Sprintf("%d", hpa) // "1013"
    }

    // Return the full verbal string to replace {BARO}
    return fmt.Sprintf("%s %s", prefix, digits)
}

