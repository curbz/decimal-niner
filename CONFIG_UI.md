# Configuration UI

## Overview

Run the application with `-ui` to expose the configuration editor at `http://127.0.0.1:8097`:

```sh
go run ./cmd/decimalniner -ui
# or
./decimalniner -ui
```

Use `-ui-addr` to select another loopback address, for example `-ui-addr 127.0.0.1:8100`. The UI should remain bound to loopback rather than `0.0.0.0`.

The application opens the UI in the default browser automatically. Use `-no-open-browser` to start the server without opening a browser.

The UI reads the same YAML file selected by `-config` or `D9_CONFIG_PATH`. Saved values are written atomically. Unknown YAML fields and comments are preserved when updating existing fields.

Changes require restarting decimal-niner; the application does not hot-reload configuration.

Running `-ui` starts only the configuration server. It does not load or initialize the ATC, traffic, X-Plane, or mock services, so it can be used on first run or when required configuration values are missing.

Stop the configuration UI with `Ctrl+C`, then launch decimal-niner normally once the configuration is complete.

## How rendering works

The JavaScript editor in `web/config.js` recursively renders YAML sections:

- mappings become nested sections or fields;
- booleans become checkboxes;
- numbers become number fields;
- strings become text fields;
- scalar arrays become add/remove lists;
- object arrays become repeatable cards.

Most newly-added scalar or nested fields require no HTML or JavaScript changes. Add an entry to `overrides` in `web/config.js` only when a field needs a label, enum/radio control, number constraint, or file/directory browser.

Example:

```js
"d9.new_option": { type: "radio", options: ["one", "two"] },
"atc.new_file": { type: "file" },
"atc.new_directory": { type: "directory" },
"atc.new_limit": { type: "number", step: 1, min: 1 }
```

## Adding a configuration field

1. Add the field to `config.yaml` and its Go YAML struct, with the appropriate `yaml:"field_name"` tag.
2. Add it to `internal/configui/configui.go` validation if it has domain constraints.
3. Optionally add a field override in `web/config.js`.
4. Add or update table-driven tests beside the package or UI package.
5. Run `go fmt ./...`, `go vet ./...`, and `go test ./...`.

No HTML section needs to be manually maintained for simple additions. Do not remove unknown fields from the server-side YAML merge: fields submitted by the UI replace matching nodes, while unsubmitted nodes remain intact.

## File and directory controls

The UI does not upload files. It browses paths on the local machine and writes the selected path into YAML. Relative paths are resolved from the directory containing the active config file.

For executable and file fields use `{ type: "file" }`. For directories use `{ type: "directory" }`.

## Security

The UI is intended for local administration. Keep `-ui-addr` set to `127.0.0.1` unless a deliberately controlled network deployment requires a different binding. The file browser should not be exposed beyond loopback.

## Application dashboard

Decimal-niner starts the local dashboard on every launch. The dashboard displays application status, provides Start and Stop controls, links to the configuration editor, and links to the radar page when D9 traffic is running.

The dashboard itself remains available even when application startup fails. Use Start after correcting the configuration.

The default configuration contains:

```yaml
ui:
  autostart_application: false
  open_in_browser: true
```

- `autostart_application: true` starts the application child process after the dashboard server starts.
- `open_in_browser: false` prevents automatic browser launch.
- `-no-open-browser` overrides browser opening from the command line.
- `-ui` remains accepted as a backward-compatible dashboard flag.
