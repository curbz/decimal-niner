# PCL Variables and Macros

This document lists the PCL variables and macros available in Decimal Niner phrase processing.
It is focused on the values and formatted outputs produced by the engine, with real phrase examples from `resources/phrases.json`.

## Raw Variables (`$`)

### `$ALTITUDE`
- Source: `ac.Flight.Position.Altitude`
- Output: raw rounded altitude value as an integer.
- Example phrase:
  - `{$FACILITY} Departure, {$CALLSIGN}, passing {@ALTITUDE} for assigned altitude.`

### `$CALLSIGN`
- Source: `ac.Flight.Comms.Callsign`
- Output: callsign converted to lowercase.
- Example phrase:
  - `{$FACILITY} Clearance, {$CALLSIGN}, at {@PARKING}, requesting IFR to {@DESTINATION}.`

### `$FACILITY`
- Source: current controller name from `ac.Flight.Comms.Controller.Name`
- Output: ATC facility name, or empty string if no controller is assigned.
- Example phrase:
  - `{$FACILITY} Ground, {$CALLSIGN}, at {@PARKING}, requesting engine start.`

### `$SQUAWK`
- Source: `ac.Flight.Squawk`
- Output: transponder code string.
- Example phrase:
  - `{$CALLSIGN}, radar contact, reset transponder, squawk {$SQUAWK} and ident.`

### `$HEADING`
- Source: `ac.Flight.Position.Heading`
- Output: heading normalized and rounded to 3 digits with leading zeros, e.g. `090`, `270`.
- Example phrase:
  - `Heading {$HEADING}, {@ALT_CLEARANCE}, {$CALLSIGN}.`

### `$RUNWAY`
- Source: `ac.Flight.AssignedRunway`
- Output: raw runway string.
- Example phrase:
  - `{$FACILITY} Tower, {$CALLSIGN}, holding short runway {@RUNWAY}, ready for departure.`

### `$DESTINATION`
- Source: `ac.Flight.Destination`
- Output: destination ICAO code string.
- Example phrase:
  - `{$FACILITY} Delivery, {$CALLSIGN}, IFR to {@DESTINATION}, ready to copy.`

### `$BARO_SEALEVEL`
- Source: `s.Weather.Baro.Sealevel`
- Output: sea-level pressure rounded to integer Pascals.
- Example phrase:
  - `{$CALLSIGN}, start approved, {@BARO}, report ready for taxi.`

### `$BARO_AIRCRAFT`
- Source: `s.Weather.Baro.Flight`
- Output: aircraft pressure setting rounded to integer Pascals.
- Example phrase:
  - this variable is defined for raw access though not shown in example phrases.

### `$WIND_SPEED`
- Source: `s.Weather.Wind.Speed`
- Output: wind speed in meters per second.
- Example phrase:
  - this raw variable is available but typically wrapped by `{@WIND}` in phrases.

### `$WIND_SHEAR`
- Source: `s.Weather.Wind.Shear`
- Output: wind shear in meters per second.
- Example phrase:
  - used indirectly via `{@SHEAR}`.

### `$TURBULENCE`
- Source: `s.Weather.Turbulence`
- Output: turbulence magnitude.
- Example phrase:
  - `{$FACILITY} Center, {$CALLSIGN}, level at current altitude. {@TURBULENCE}`

### `$PARKING`
- Source: `ac.Flight.AssignedParkingName`
- Output: raw parking designation string.
- Example phrase:
  - `{$FACILITY} Ground, {$CALLSIGN}, at {@PARKING}, requesting taxi.`

### `$APPROACH_TYPE`
- Source: runway highest precision approach string.
- Output: raw approach type value, e.g. `ILS`, `VOR`, or empty if no runway available.
- Example phrase:
  - `{$FACILITY} Approach, {$CALLSIGN}, requesting vectors for the approach to runway {@RUNWAY}.`

### `$HOLD_FIX_NAME`
- Source: `ac.Flight.AssignedHold.FullName`
- Output: hold fix full name or empty string if unset.
- Example phrase:
  - used for internal logic and available for direct phrase insertion.

### `$HOLD_FIX_IDENT`
- Source: `ac.Flight.AssignedHold.Ident`
- Output: hold fix identifier or empty string if unset.
- Example phrase:
  - used for internal logic and available for direct phrase insertion.

### `$MA_HEADING`
- Source: `rwy.MAHeading`
- Output: missed approach heading integer or `0` if no runway.
- Example phrase:
  - `{$CALLSIGN}, Roger, fly {@MA_HEADING}, climb and maintain {@MA_ALTITUDE}.`

### `$MA_ALTITUDE`
- Source: `rwy.MAalt`
- Output: missed approach altitude integer or `0` if no runway.
- Example phrase:
  - `{$CALLSIGN}, climb and maintain {@MA_ALTITUDE}, proceed to {@MA_FIX}.`

### `$MA_FIX`
- Source: `rwy.MAFix`
- Output: missed approach fix string or empty string if no runway.
- Example phrase:
  - `{$CALLSIGN}, climb {@MA_ALTITUDE} fly {@MA_HEADING} to {@MA_FIX}`

### `$FA_ALTITUDE`
- Source: `rwy.FAFalt`
- Output: final approach fix altitude integer or `0` if no runway.
- Example phrase:
  - used indirectly in `@ALT_CLEARANCE` for arriving flights.

## Formatted Macros (`@`)

### `@RUNWAY`
- Function: `translateRunway`
- Output: runway with `L`/`R` converted to `left`/`right`.
- Example: runway `27L` becomes `27left` in the current implementation.
- Example phrase:
  - `{$CALLSIGN}, runway {@RUNWAY}, cleared for takeoff, fly runway heading[, wind {@WIND}].`

### `@PARKING`
- Function: `formatParking`
- Output: natural speech parking phrase.
  - If the value contains `RAMP` or `APRON`, it preserves words and phoneticizes single letters.
  - If it starts with digits, output becomes `gate N` or `stand N` depending on region.
  - If it starts with letter+digits, output becomes `gate <phonetic> <number>`.
- Example phrase:
  - `{$CALLSIGN}, taxi to {@PARKING}.`

### `@DESTINATION`
- Function: `formatAirportName`
- Output: airport name with common suffixes removed (`Intl`, `Arpt`, `Airport`, `Regional`, `Municipal`).
- Example phrase:
  - `{$FACILITY} Delivery, {$CALLSIGN}, at {@PARKING}, requesting IFR to {@DESTINATION}.`

### `@APPROACH_TYPE`
- Function: formatted from runway highest precision approach.
- Output: appended with the word `approach`, e.g. `ILS approach`.
- Example phrase:
  - `{$CALLSIGN}, {$FACILITY} Approach, fly heading {$HEADING} for {@APPROACH_TYPE}, {@ALT_CLEARANCE}.`

### `@MA_HEADING`
- Function: missed approach heading phrase.
- Output: `heading <N>` if runway data exists, otherwise `runway heading`.
- Example phrase:
  - `{$CALLSIGN}, Roger, fly {@MA_HEADING}, climb and maintain {@MA_ALTITUDE}.`

### `@MA_ALTITUDE`
- Function: `formatAltitude` with missed approach altitude.
- Output: formatted altitude text such as `2 thousand` or `flight level 330`.
- Example phrase:
  - `{$CALLSIGN}, climb and maintain {@MA_ALTITUDE}, proceed to {@MA_FIX}.`

### `@MA_FIX`
- Function: missed approach fix phrase.
- Output: runway missed approach fix name or `published hold`.
- Example phrase:
  - `{$CALLSIGN}, climb {@MA_ALTITUDE} fly {@MA_HEADING} to {@MA_FIX}`

### `@ALTITUDE`
- Function: `formatAltitude`
- Output:
  - below transition level: `X thousand` or `X thousand Y hundred`
  - at/above transition level: `flight level NN`
- Example phrase:
  - `{$FACILITY} Center, {$CALLSIGN}, with you at {@ALTITUDE}. {@TURBULENCE}.`

### `@ALT_CLEARANCE`
- Function: `generateAltClearance`
- Output: one of `descend to ...`, `maintain ...`, or `climb to ...`, based on current altitude and cleared altitude.
- Example phrase:
  - `{$CALLSIGN}, {@ALT_CLEARANCE}, fly heading {$HEADING}`

### `@BARO`
- Function: `formatBaro`
- Output:
  - North America: `altimeter ####` with inches of mercury digits
  - Elsewhere: `QNH ####` with hPa digits
- Example phrase:
  - `{$CALLSIGN}, start approved, {@BARO}, report ready for taxi.`

### `@WIND`
- Function: `formatWind`
- Output:
  - `calm` if speed is low
  - otherwise `<direction> at <knots>`, with optional gusting if turbulence is high
- Example phrase:
  - `{$CALLSIGN}, runway {@RUNWAY}, cleared for takeoff, fly runway heading[, wind {@WIND}].`

### `@SHEAR`
- Function: `formatWindShear`
- Output: if shear ≥ 15 kt, returns a caution phrase like `[caution] wind shear [alert, loss or gain of] <N> knots`.
- Example phrase:
  - `{$CALLSIGN}, cleared for take off runway {@RUNWAY}, {@SHEAR}`

### `@TURBULENCE`
- Function: `formatTurbulence`
- Output:
  - `experiencing moderate turbulence` or `experiencing severe turbulence` for pilot role
  - `<class> turbulence [reported]` for ATC role
- Example phrase:
  - `{$CALLSIGN}, {$FACILITY} Center, Roger, {@BARO}.{NOREADBACK}`

### `@HANDOFF`
- Function: `generateHandoffPhrase`
- Output: controller handoff phrase including next facility/role and frequency.
- Example phrase:
  - `{$CALLSIGN}, clear runway {@RUNWAY} [when able], {@HANDOFF}`

### `@VALEDICTION`
- Function: `generateValediction`
- Behavior: optional integer argument influences how often a valediction is returned.
  - `{@VALEDICTION}` uses default factor `5`
  - `{@VALEDICTION(4)}` uses factor `4`
- Output: `[]` with `good day`, `good evening`, or `good night` when the random check hits.
- Example phrases:
  - `{$CALLSIGN}, Roger, shutdown acknowledged. {@VALEDICTION(4)}`
  - `{$CALLSIGN}, copy that, have a {@VALEDICTION(1)}.`

### `@HOLD_FIX`
- Function: returns assigned hold fix name or `published hold`.
- Example phrase:
  - `{$CALLSIGN}, affirm, at {@HOLD_FIX}. {NOREADBACK}`

## Notes
- PCL expansion is handled by `internal/pcl/pcl.go`.
- The runtime token bindings are created in `internal/atc/atcvoice.go`.
- All phrase examples in this document come from `resources/phrases.json`.
