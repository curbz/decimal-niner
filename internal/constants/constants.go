package constants

// Collection of numeric constants extracted from the codebase.
// Values are compile-time constants per current design (not configurable).

const (
	// Positioning and geometry
	PositionToleranceDeg = 0.0001
	FeetToNM             = 0.000164579

	// Cross-track thresholds (nautical miles)
	CrossTrackThresholdNM     = 0.05
	CrossTrackThresholdNMAlt1 = 0.10
	CrossTrackThresholdNMAlt2 = 0.15
	CrossTrackThresholdNMAlt3 = 0.45

	// Vertical/altitude related (feet)
	DefaultClimbExitDepartureEntryAltFt  = 3000
	DefaultDepartureExitCruiseEntryAltFt = 10000
	DefaultCruiseExitArrivalEntryAltFt   = 10000
	DefaultArrivalExitApproachEntryAltFt = 4000
	DefaultApproachExitFinalEntryAltFt   = 1500
	TerminalEntryAltFt                   = 5000
	ApproachTerminalAltBufferFt          = 2800
	FeetPerFL                            = 1000
	TransitionAltRegionEUFt              = 6000
	TransitionAltRegionOtherFt           = 18000
	// Small offset used when computing runway threshold/start altitudes
	RunwayElevationOffsetFt = 100

	// Intercept (formerly gate) values
	InterceptLOCUnitFt     = 318
	InterceptLOCMultiplier = 6
	InterceptLOCProjectNM  = 10.0

	// Squawk generation
	SquawkMin   = 1200
	SquawkRange = 5800

	// Sentinels
	AirspaceFloorSentinel   = -99999
	AirspaceCeilingSentinel = 99999

	// Audio
	AudioSampleRate = 22050

	// Runway / approach thresholds (meters)
	RunwayXtdThresholdM  = 50.0
	RunwayAtdNegPaddingM = -50.0
	RunwayAtdPosPaddingM = 100.0
	ApproachXtdWidenM    = 80.0

	// Distance conversions
	ThreeNMMeters = 5556

	// Airport/runway heuristics
	MinArrivalDistNM          = 0.8
	LastExitBufferNM          = 0.1
	HighSpeedExitThresholdDeg = 47.0
	STARProbabilityFactor     = 0.5

	// Controller search heuristics
	ControllerSearchLimitFreqNM      = 100.0
	ControllerSearchLimitProximityNM = 15.0
	ControllerTargetICAOCloseNM      = 50.0
	ControllerTieBreakDeltaNM        = 2.0
	ControllerLowThresholdAltFt      = 5000
	ControllerHighThresholdAltFt     = 10000

	// Runway defaults and thresholds
	RunwayWidthLargeM        = 150.0
	RunwayWidthDefaultM      = 100.0
	RunwayLengthLargeThreshM = 6000.0

	// D9 traffic engine distances and thresholds
	SpawnArrivalEntryProjectOffsetNM = 40.0
	SpawnArrivalExitProjectOffsetNM  = 15.0
	FinalTargetProjectNM             = 4.0
	DefaultClimbRateNMPerFL          = 3.0
	DefaultDescentRateNMPerFL        = 3.0

	// Wind/check thresholds
	WindDirShiftDeg   = 15.0
	WindSpeedDeltaKts = 5.0

	// Runway length filter (meters)
	RunwayLengthMinFilterM = 5000.0
)
