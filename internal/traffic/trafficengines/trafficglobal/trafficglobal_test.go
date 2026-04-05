package trafficglobal

import (
	"testing"
)

func TestCleanAirlineName(t *testing.T) {
	tests := []struct {
		name     string
		fileName string
		expected string
	}{
		{
			name:     "Space and Season",
			fileName: "21Air W24.bgl",
			expected: "21Air",
		},
		{
			name:     "Multiple Spaces and Year",
			fileName: "Abu Dhabi Aviation S23.bgl",
			expected: "Abu Dhabi Aviation",
		},
		{
			name:     "Underscore with Branch",
			fileName: "Aerolineas Argentinas_Cargo S23.bgl",
			expected: "Aerolineas Argentinas Cargo",
		},
		{
			name:     "Dash and Underscore mix",
			fileName: "AFG_KLM-MartinairCargo W25.bgl",
			expected: "AFG KLM-MartinairCargo",
		},
		{
			name:     "Simple Year Suffix",
			fileName: "Afriqiyah Airways 2022.bgl",
			expected: "Afriqiyah Airways",
		},
		{
			name:     "Underscore Season",
			fileName: "Azul_S23.bgl",
			expected: "Azul",
		},
		{
			name:     "Summer Code variation",
			fileName: "Air Vanuatu Su24.bgl",
			expected: "Air Vanuatu",
		},
		{
			name:     "No Suffix but Extension",
			fileName: "ASL_UK_W24.bgl",
			expected: "ASL UK",
		},
		{
			name:     "Name with Number and No Season",
			fileName: "9Air S24.bgl",
			expected: "9Air",
		},
		{
			name:     "Complex Branch name",
			fileName: "Aeromexico_Connect S24.bgl",
			expected: "Aeromexico Connect",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := cleanAirlineName(tt.fileName)
			if actual != tt.expected {
				t.Errorf("CleanAirlineName(%q) = %q; want %q", tt.fileName, actual, tt.expected)
			}
		})
	}
}