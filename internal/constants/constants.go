package constants

// Collection of numeric constants extracted from the codebase.
// Values are compile-time constants per current design (not configurable).

const (

	// Audio
	AudioSampleRate = 22050

	// Positioning and geometry
	PositionToleranceDeg = 0.0001
	FeetToNM             = 0.000164579

	// D9 traffic engine distances and thresholds
	SpawnArrivalEntryProjectOffsetNM = 40.0
	SpawnArrivalExitProjectOffsetNM  = 15.0
	FinalTargetProjectNM             = 4.0
	DefaultClimbRateNMPerFL          = 3.0
	DefaultDescentRateNMPerFL        = 3.0

	// Vertical/altitude related (feet)
	FeetPerFL                        	 = 1000
	DefaultClimbExitDepartureEntryAltFt  = 3000
	DefaultDepartureExitCruiseEntryAltFt = 10000
	DefaultCruiseExitArrivalEntryAltFt   = 10000
	DefaultArrivalExitApproachEntryAltFt = 4000
	DefaultApproachExitFinalEntryAltFt   = 1500
	TerminalEntryAltFt                   = 5000
	ApproachTerminalAltBufferFt          = 2800
	TransitionAltRegionEUFt              = 6000
	TransitionAltRegionOtherFt           = 18000

	// Intercept (formerly gate) values
	InterceptLOCUnitFt     = 318
	InterceptLOCMultiplier = 6
	InterceptLOCProjectNM  = 10.0

	// Airport/runway heuristics
	DefaultRolloutDistNM      = 0.8
	LastExitBufferNM          = 0.1
	HighSpeedExitThresholdDeg = 47.0

	// Runway length filter (meters)
	RunwayLengthMinFilterM = 5000.0

	// Runway / approach thresholds (meters) Cross-Track Distance and Along-Track Distance
	RunwayXtdThresholdM  = 50.0
	RunwayAtdNegPaddingM = -50.0
	RunwayAtdPosPaddingM = 100.0
	ApproachXtdWidenM    = 80.0

	// Runway defaults and thresholds
	RunwayWidthStandardM     = 45.0 // Standard commercial jet runway (150 ft)
	RunwayWidthNarrowM       = 30.0 // Regional / GA runway (100 ft)
	RunwayLengthLargeThreshM = 6000.0

	// Small offset used when computing runway threshold/start altitudes
	RunwayElevationOffsetFt = 100

	// Squawk generation
	SquawkMin   = 1200
	SquawkRange = 5800

	// Sentinels
	AirspaceFloorSentinel   = -99999
	AirspaceCeilingSentinel = 99999

	// Controller search heuristics
	ControllerSearchMaxRangeNM       = 100.0
	ControllerSearchLimitProximityNM = 15.0
	ControllerTargetICAOCloseNM      = 50.0
	ControllerTieBreakDeltaNM        = 2.0
	ControllerLowThresholdAltFt      = 5000
	ControllerHighThresholdAltFt     = 10000

	// Procedure assignment
	STARProbabilityFactor     = 0.5

	// Wind/check thresholds
	WindDirShiftDeg   = 15.0
	WindSpeedDeltaKts = 5.0
)
