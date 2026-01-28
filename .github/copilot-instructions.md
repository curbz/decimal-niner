<!-- .github/copilot-instructions.md -->
# Copilot / AI agent instructions — decimal-niner

Summary
- `decimal-niner` is a small Go CLI. The primary entrypoint is `cmd/decimalniner/main.go` and most implementation lives in packages under `internal/` and `pkg/`.

Quick tasks
- Build locally: `go build ./cmd/decimalniner`
- Run locally: `go run ./cmd/decimalniner`
- Run tests: `go test ./...`

Repository state
- This repository uses Go modules (`go.mod` is present at the repo root). Add or update dependencies via the `go` tool and commit `go.mod`/`go.sum`.
- There are existing unit tests (see `internal/atc/`) — prefer keeping changes small and covered by tests.

Architecture & intent
- Keep `main` minimal: wire together packages and flags, then call into package-level functions.
- New functionality should live in a package under `internal/` or `pkg/` and be exercised from `cmd/decimalniner`.

Project patterns
- Use table-driven tests for unit tests and place them next to the package under test.
- Prefer small, exported helpers where appropriate so tests can cover behavior.

Developer workflow for AI edits
- Make small, focused changes in their own commits/PRs.
- If adding dependencies: run `go get` or `go mod tidy`, commit `go.mod` and `go.sum`, and update `README.md` with any build/run changes.
- Run `go vet` and `gofmt` (or `go fmt`) before submitting changes.

Conventions for commits and PRs
- Commit message style: imperative and concise (e.g., "add logging to startup").
- PR description: why the change matters and exact steps to verify (build/run/test commands).

Guidance for code generation and refactors
- Prefer small, testable functions in packages rather than adding logic to `main`.
- When adding a new package, include unit tests and a brief example of usage from `cmd/decimalniner`.

Examples of acceptable changes
- Add a logging helper and wire it into `cmd/decimalniner/main.go` (update `go.mod` if using an external logger).
- Create `internal/app` with a `Run()` function and call it from `main` to keep `main` minimal.

If anything is unclear
- Ask before making large structural changes (e.g., converting to a multi-module layout, changing package layout, or adding CI that changes developer workflow).

Files to check when working here
- The CLI entrypoint: [cmd/decimalniner/main.go](cmd/decimalniner/main.go)
- Tests and package code: `internal/atc/` and other packages under `internal/` and `pkg/`

Verification
- After edits run:

```bash
go fmt ./...
go vet ./...
go test ./...
```

— End
