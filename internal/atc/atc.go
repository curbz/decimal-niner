package atc

type Service struct {
	// go channel to trigger instructions
	Channel chan struct{}
	Positions []Position
}

type ServiceInterface interface {
	Run()
}

type Position struct {
	Name string
	Frequency float64
}

func New() *Service {

	return &Service{
		Channel: make(chan struct{}, 1),
		Positions: []Position{
			{Name: "Clearance Delivery", Frequency: 118.1},
			{Name: "Ground", Frequency: 121.9},
			{Name: "Tower", Frequency: 118.1},
			{Name: "Departure", Frequency: 122.6},
			{Name: "Center", Frequency: 128.2},
			{Name: "Approach", Frequency: 124.5},
			{Name: "TRACON", Frequency: 127.2},
			{Name: "Ocennic", Frequency: 135.0},
		},
	}
}

// main function to run the ATC service
func (s *Service) Run() {
	// main loop to read from channel and process instructions
}
