# decimal-niner
The goal of this project is to provide realistic simulated air traffic control communications between controllers and aircraft flight crew for X-Plane 12 traffic injection plug-ins.

decimal-niner makes use of Piper TTS (Text-To-Speech) to provide users with an infinte number of configurable ATC phrases and exceptional speech quality across a large number of pre-existing voices, including multiple country and regional accents.

Also included is an internal traffic engine for those who want to include the audible experience of realistic ATC communications without the overhead of visible rendered AI aircraft.

decimal-niner is an add-on for X-Plane and not a plug-in. This means decimal-niner executes completlely independently of X-Plane and does not impact X-Plane performance or frame rates. As an add-on, it is generally unaffected by X-Plane version changes (unless Laminar inadvertently break the web API versioning) and will also never be the root cause of an X-Plane application crash.

## Currently Supported Plug-ins

- Traffic Global for X-Plane 12 (JustFlight) version 1.1.0246 or higher for Windows/Mac (no Linux version available)

## Requirements

- X-Plane 12 version 12.4.0+
- Sox version 14.4.2+  `https://sourceforge.net/projects/sox/files/sox/`
- Piper TTS version 1.3.0+ and at least two compatible voices (.onnx and .json files) `https://github.com/OHF-Voice/piper1-gpl`

## decimal-niner Supported Operating Systems

- Microsoft Windows
- Apple Mac
- Linux

## decimal-niner Core Principles

- All dependencies* must be Open Source projects
- No dependencies* on any subscription based software or cloud services
- No 'online' requirement for decimal-niner application execution
- Compatibility with all supported X-Plane desktop operating systems** (Microsoft Windows, Apple Mac and Linux)

*the use of the term 'dependencies' here excludes the supported traffic injection plug-ins and requirements of those plug-ins
**not all supported traffic injection plug-ins may support all X-Plane desktop operating systems

## How does decimal-niner integrate with traffic injection plug-ins?

The answer to this question varies based on how each traffic injection plug-in exposes data.
### Traffic Global
decimal-niner uses the official X-Plane web API REST interface and websockets API to read and subscribe to Traffic Global's datarefs. The datarefs provide aircraft metadata and positional information including the current flight phase. As aircraft transition from one phase to another, decimal-niner will trigger an appropriate ATC phrase from a user configurable bank of phrases, interpolating pertinent data obtained from the datarefs.In this respect, decimal-niner is reactive rather than proactive for Traffic Global's inject aircraft.

## Troubleshooting

- Your aircraft must have a radio tuned to a frequency in order to hear anything.
Search decimal-niner output for "error" to locate critical application errors.

### Common Issues
- For sox error "no default audio device" on Microsoft Windows, set environment variable AUDIODRIVER to waveaudio i.e. set AUDIODRIVER=waveaudio

----

This application has no affinity or relationship with Laminar Research or supported third party traffic plug-ins.


