# PCL Variables and Macros

This document lists the PCL variables and macros available in Decimal Niner phrase processing.
It is focused on the values and formatted outputs produced by the engine, with real phrase examples from `resources/phrases.json`.

## Raw Variables (`$`)

### `$ALTITUDE`
- Source: `ac.Flight.Position.Altitude`
- Output: raw rounded altitude value as an integer.
- Example phrase:
  - Template: `{$FACILITY} Departure, {$CALLSIGN}, passing {@ALTITUDE} for assigned altitude.`
  - Interpolated: `Heathrow Departure, speedbird123, passing 5 thousand for assigned altitude.`

### `$CALLSIGN`
- Source: `ac.Flight.Comms.Callsign`
- Output: callsign converted to lowercase.
- Example phrase:
  - Template: `{$FACILITY} Clearance, {$CALLSIGN}, at {@PARKING}, requesting IFR to {@DESTINATION}.`
  - Interpolated: `Heathrow Clearance, speedbird123, at gate bravo 12, requesting IFR to John F Kennedy.`

### `$FACILITY`
- Source: current controller name from `ac.Flight.Comms.Controller.Name`
- Output: ATC facility name, or empty string if no controller is assigned.
- Example phrase:
  - Template: `{$FACILITY} Ground, {$CALLSIGN}, at {@PARKING}, requesting engine start.`
  - Interpolated: `Heathrow Ground, speedbird123, at gate bravo 12, requesting engine start.`

### `$SQUAWK`
- Source: `ac.Flight.Squawk`
- Output: transponder code string.
- Example phrase:
  - Template: `{$CALLSIGN}, radar contact, reset transponder, squawk {$SQUAWK} and ident.`
  - Interpolated: `speedbird123, radar contact, reset transponder, squawk 1234 and ident.`

### `$HEADING`
- Source: `ac.Flight.Position.Heading`
- Output: heading normalized and rounded to 3 digits with leading zeros, e.g. `090`, `270`.
- Example phrase:
  - Template: `Heading {$HEADING}, {@ALT_CLEARANCE}, {$CALLSIGN}.`
  - Interpolated: `Heading 270, descend to 10 thousand, speedbird123.`

### `$RUNWAY`
- Source: `ac.Flight.AssignedRunway`
- Output: raw runway string.
- Example phrase:
  - Template: `{$FACILITY} Tower, {$CALLSIGN}, holding short runway {@RUNWAY}, ready for departure.`
  - Interpolated: `Heathrow Tower, speedbird123, holding short runway 27left, ready for departure.`

### `$DESTINATION`
- Source: `ac.Flight.Destination`
- Output: destination ICAO code string.
- Example phrase:
  - Template: `{$FACILITY} Delivery, {$CALLSIGN}, IFR to {@DESTINATION}, ready to copy.`
  - Interpolated: `Heathrow Delivery, speedbird123, IFR to KJFK, ready to copy.`

### `$BARO_SEALEVEL`
- Source: `s.Weather.Baro.Sealevel`
- Output: sea-level pressure rounded to integer Pascals.
- Example phrase:
  - Template: `{$CALLSIGN}, start approved, {@BARO}, report ready for taxi.`
  - Interpolated: `speedbird123, start approved, QNH 1013, report ready for taxi.`

### `$BARO_AIRCRAFT`
- Source: `s.Weather.Baro.Flight`
- Output: aircraft pressure setting rounded to integer Pascals.
- Example phrase:
  - This variable is defined for raw access though not shown in example phrases.

### `$WIND_SPEED`
- Source: `s.Weather.Wind.Speed`
- Output: wind speed in meters per second.
- Example phrase:
  - This raw variable is available but typically wrapped by `{@WIND}` in phrases.

### `$WIND_SHEAR`
- Source: `s.Weather.Wind.Shear`
- Output: wind shear in meters per second.
- Example phrase:
  - Used indirectly via `{@SHEAR}`.

### `$TURBULENCE`
- Source: `s.Weather.Turbulence`
- Output: turbulence magnitude.
- Example phrase:
  - Template: `{$FACILITY} Center, {$CALLSIGN}, level at current altitude. {@TURBULENCE}`
  - Interpolated: `Heathrow Center, speedbird123, level at current altitude. experiencing moderate turbulence.`

### `$PARKING`
- Source: `ac.Flight.AssignedParkingName`
- Output: raw parking designation string.
- Example phrase:
  - Template: `{$FACILITY} Ground, {$CALLSIGN}, at {@PARKING}, requesting taxi.`
  - Interpolated: `Heathrow Ground, speedbird123, at gate bravo 12, requesting taxi.`

### `$APPROACH_TYPE`
- Source: runway highest precision approach string.
- Output: raw approach type value, e.g. `ILS`, `VOR`, or empty if no runway available.
- Example phrase:
  - Template: `{$FACILITY} Approach, {$CALLSIGN}, requesting vectors for the approach to runway {@RUNWAY}.`
  - Interpolated: `Heathrow Approach, speedbird123, requesting vectors for the approach to runway 27left.`

### `$HOLD_FIX_NAME`
- Source: `ac.Flight.AssignedHold.FullName`
- Output: hold fix full name or empty string if unset.
- Example phrase:
  - Used for internal logic and available for direct phrase insertion.

### `$HOLD_FIX_IDENT`
- Source: `ac.Flight.AssignedHold.Ident`
- Output: hold fix identifier or empty string if unset.
- Example phrase:
  - Used for internal logic and available for direct phrase insertion.

### `$MA_HEADING`
- Source: `rwy.MAHeading`
- Output: missed approach heading integer or `0` if no runway.
- Example phrase:
  - Template: `{$CALLSIGN}, Roger, fly {@MA_HEADING}, climb and maintain {@MA_ALTITUDE}.`
  - Interpolated: `speedbird123, Roger, fly heading 180, climb and maintain 3 thousand.`

### `$MA_ALTITUDE`
- Source: `rwy.MAalt`
- Output: missed approach altitude integer or `0` if no runway.
- Example phrase:
  - Template: `{$CALLSIGN}, climb and maintain {@MA_ALTITUDE}, proceed to {@MA_FIX}.`
  - Interpolated: `speedbird123, climb and maintain 3 thousand, proceed to VOR HOLD.`

### `$MA_FIX`
- Source: `rwy.MAFix`
- Output: missed approach fix string or empty string if no runway.
- Example phrase:
  - Template: `{$CALLSIGN}, climb {@MA_ALTITUDE} fly {@MA_HEADING} to {@MA_FIX}`
  - Interpolated: `speedbird123, climb 3 thousand fly heading 180 to VOR HOLD`

### `$FA_ALTITUDE`
- Source: `rwy.FAFalt`
- Output: final approach fix altitude integer or `0` if no runway.
- Example phrase:
  - Used indirectly in `@ALT_CLEARANCE` for arriving flights.

## Formatted Macros (`@`)

### `@RUNWAY`
- Function: `translateRunway`
- Output: runway with `L`/`R` converted to `left`/`right`.
- Example: runway `27L` becomes `27left` in the current implementation.
- Example phrase:
  - Template: `{$CALLSIGN}, runway {@RUNWAY}, cleared for takeoff, fly runway heading[, wind {@WIND}].`
  - Interpolated: `speedbird123, runway 27left, cleared for takeoff, fly runway heading,`.

### `@PARKING`
- Function: `formatParking`
- Output: natural speech parking phrase.
  - If the value contains `RAMP` or `APRON`, it preserves words and phoneticizes single letters.
  - If it starts with digits, output becomes `gate N` or `stand N` depending on region.
  - If it starts with letter+digits, output becomes `gate <phonetic> <number>`.
- Example phrase:
  - Template: `{$CALLSIGN}, taxi to {@PARKING}.`
  - Interpolated: `speedbird123, taxi to gate bravo 12.`

### `@RUNWAY_HOLD`
- Function: `formatRunwayHold`
- Output: formats the runway access point and prefixes with `hold at`. If no access point has been assigned then the string `hold short` is output.
- Example phrase:
  - Template: `{$CALLSIGN}, taxi via {@TAXIPATH} and {@RUNWAY_HOLD}.`
  - Interpolated: `speedbird123, taxi via Charlie and hold at Alpha 13.`

### `@TAXIPATH`
- Function: `collateTaxipath`
- Output: composes arrival or departure taxi routing using available taxiway access and parking segments.
  - Taxiway names are phoneticized when they start with a letter, e.g. `A` → `Alpha` and `B12` → `Bravo 12`.
  - If no taxi path data exists, returns `taxiway`.
- Example phrase:
  - Template: `{$CALLSIGN}, taxi {@TAXIPATH}.`
  - Interpolated: `speedbird123, taxi via Alpha,Bravo 12.`

### `@DESTINATION`
- Function: `formatAirportName`
- Output: airport name with common suffixes removed (`Intl`, `Arpt`, `Airport`, `Regional`, `Municipal`).
- Example phrase:
  - Template: `{$FACILITY} Delivery, {$CALLSIGN}, at {@PARKING}, requesting IFR to {@DESTINATION}.`
  - Interpolated: `Heathrow Delivery, speedbird123, at gate bravo 12, requesting IFR to John F Kennedy.`

### `@APPROACH_TYPE`
- Function: formatted from runway highest precision approach.
- Output: appended with the word `approach`, e.g. `ILS approach`.
- Example phrase:
  - Template: `{$CALLSIGN}, {$FACILITY} Approach, fly heading {$HEADING} for {@APPROACH_TYPE}, {@ALT_CLEARANCE}.`
  - Interpolated: `speedbird123, Heathrow Approach, fly heading 270 for ILS approach, climb to 12 thousand.`

### `@MA_HEADING`
- Function: missed approach heading phrase.
- Output: `heading <N>` if runway data exists, otherwise `runway heading`.
- Example phrase:
  - Template: `{$CALLSIGN}, Roger, fly {@MA_HEADING}, climb and maintain {@MA_ALTITUDE}.`
  - Interpolated: `speedbird123, Roger, fly heading 180, climb and maintain 3 thousand.`

### `@MA_ALTITUDE`
- Function: `formatAltitude` with missed approach altitude.
- Output: formatted altitude text such as `2 thousand` or `flight level 330`.
- Example phrase:
  - Template: `{$CALLSIGN}, climb and maintain {@MA_ALTITUDE}, proceed to {@MA_FIX}.`
  - Interpolated: `speedbird123, climb and maintain 3 thousand, proceed to VOR HOLD.`

### `@MA_FIX`
- Function: missed approach fix phrase.
- Output: runway missed approach fix name or `published hold`.
- Example phrase:
  - Template: `{$CALLSIGN}, climb {@MA_ALTITUDE} fly {@MA_HEADING} to {@MA_FIX}`
  - Interpolated: `speedbird123, climb 3 thousand fly heading 180 to VOR HOLD`

### `@ALTITUDE`
- Function: `formatAltitude`
- Output:
  - below transition level: `X thousand` or `X thousand Y hundred`
  - at/above transition level: `flight level NN`
- Example phrase:
  - Template: `{$FACILITY} Center, {$CALLSIGN}, with you at {@ALTITUDE}. {@TURBULENCE}.`
  - Interpolated: `Heathrow Center, speedbird123, with you at 5 thousand. experiencing moderate turbulence.`

### `@ALT_CLEARANCE`
- Function: `generateAltClearance`
- Output: one of `descend to ...`, `maintain ...`, or `climb to ...`, based on current altitude and cleared altitude.
- Example phrase:
  - Template: `{$CALLSIGN}, {@ALT_CLEARANCE}, fly heading {$HEADING}`
  - Interpolated: `speedbird123, climb to 12 thousand, fly heading 270`

### `@BARO`
- Function: `formatBaro`
- Output:
  - North America: `altimeter ####` with inches of mercury digits
  - Elsewhere: `QNH ####` with hPa digits
- Example phrase:
  - Template: `{$CALLSIGN}, start approved, {@BARO}, report ready for taxi.`
  - Interpolated: `speedbird123, start approved, QNH 1013, report ready for taxi.`

### `@WIND`
- Function: `formatWind`
- Output:
  - `calm` if speed is low
  - otherwise `<direction> at <knots>`, with optional gusting if turbulence is high
- Example phrase:
  - Template: `{$CALLSIGN}, runway {@RUNWAY}, cleared for takeoff, fly runway heading[, wind {@WIND}].`
  - Interpolated: `speedbird123, runway 27left, cleared for takeoff, fly runway heading, wind 270 at 9 knots.`

### `@SHEAR`
- Function: `formatWindShear`
- Output: if shear ≥ 15 kt, returns a caution phrase like `[caution] wind shear [alert, loss or gain of] <N> knots`.
- Example phrase:
  - Template: `{$CALLSIGN}, cleared for take off runway {@RUNWAY}, {@SHEAR}`
  - Interpolated: `speedbird123, cleared for take off runway 27left, [caution] wind shear [alert, loss or gain of] 20 knots`

### `@TURBULENCE`
- Function: `formatTurbulence`
- Output:
  - `experiencing moderate turbulence` or `experiencing severe turbulence` for pilot role
  - `<class> turbulence [reported]` for ATC role
- Example phrase:
  - Template: `{$CALLSIGN}, {$FACILITY} Center, Roger, {@BARO}.{NOREADBACK}`
  - Interpolated: `speedbird123, Heathrow Center, Roger, QNH 1013.`

### `@HANDOFF`
- Function: `generateHandoffPhrase`
- Output: controller handoff phrase including next facility/role and frequency.
- Example phrase:
  - Template: `{$CALLSIGN}, clear runway {@RUNWAY} [when able], {@HANDOFF}`
  - Interpolated: `speedbird123, clear runway 27left, [contact] Center Approach on 123 decimal 45 [good day]`

### `@VALEDICTION`
- Function: `generateValediction`
- Behavior: optional integer argument influences how often a valediction is returned.
  - `{@VALEDICTION}` uses default factor `5`
  - `{@VALEDICTION(4)}` uses factor `4`
- Output: `[]` with `good day`, `good evening`, or `good night` when the random check hits.
- Example phrases:
  - Template: `{$CALLSIGN}, Roger, shutdown acknowledged. {@VALEDICTION(4)}`
  - Interpolated: `speedbird123, Roger, shutdown acknowledged. [good day]`
  - Template: `{$CALLSIGN}, copy that, have a {@VALEDICTION(1)}.`
  - Interpolated: `speedbird123, copy that, have a [good evening].`

### `@HOLD_FIX`
- Function: returns assigned hold fix name or `published hold`.
- Example phrase:
  - Template: `{$CALLSIGN}, affirm, at {@HOLD_FIX}. {NOREADBACK}`
  - Interpolated: `speedbird123, affirm, at VOR HOLD.`

## Notes
- PCL expansion is handled by `internal/pcl/pcl.go`.
- The runtime token bindings are created in `internal/atc/atcvoice.go`.
- All phrase examples in this document come from `resources/phrases.json`.
