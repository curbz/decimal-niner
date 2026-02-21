# decimal-niner
The goal of this project is to provide realistic simulated air traffic control communications between controllers and aircraft flight crew for X-Plane 12 traffic injection plug-ins.

## Currently Supported Plug-ins

- Traffic Global for X-Plane 12 (JustFlight) version TBD Windows/Mac (no Linux version available)

## Requirements

- X-Plane 12 version 12.4.0+

## decimal-niner Supported Operating Systems

- Microsoft Windows
- Apple Mac
- Linux

## decimal-niner Core Principles

- All dependecies* must be Open Source
- No dependencies* on any subscription based software or cloud services
- No 'online' requirement for application execution
- Compatibility with all supported X-Plane desktop operating systems** (Microsoft Windows, Apple Mac and Linux)

*the use of the term 'dependencies' here excludes the supported plug-ins and requirements of those plug-ins
**not all supported plug-ins may support all X-Plane desktop operating systems, but decimal-niner does

## Dependencies

These dependencies are required for deimal-niner to function. They are not included in this software and should be installed independently before attempting to run decimal-niner.

- Sox version xxx
- Piper TTS version xxx and at least two voices

## Troubleshooting

- Your aircraft must have a radio tuned to a frequency in order to hear anything.
Search decimal-niner output for "error"

### Common Issues
- For sox error "no default audio device" on Microsoft Windows, set environment variable AUDIODRIVER to waveaudio i.e. set AUDIODRIVER=waveaudio

----

This application has no affinity or relationship with Laminar Research or supported third party traffic plug-ins.


