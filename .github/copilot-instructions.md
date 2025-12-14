<!-- .github/copilot-instructions.md -->
# Copilot / AI agent instructions — decimal-niner

Summary
- This repository is a tiny Go CLI: the binary entrypoint is `cmd/decimalniner/main.go` and the project currently contains no modules, external deps, or tests. Changes should be minimal and focused on `cmd/` unless adding new packages.

Quick tasks
- Build locally: `go build ./cmd/decimalniner`
- Run locally: `go run ./cmd/decimalniner`
- Run all tests (if added): `go test ./...`

What I inspected
- [README.md](README.md) — project description.
- [cmd/decimalniner/main.go](cmd/decimalniner/main.go) — program entrypoint; prints a short message.

Architecture & intent (short)
- Single-binary CLI laid out in the conventional Go `cmd/<name>/main.go` layout.
- No `go.mod` detected: the repo currently targets the Go toolchain without explicit module metadata. Before adding external packages, the agent should either:
  - propose and add a `go.mod` (run `go mod init github.com/your/repo`), or
  - explain why using GOPATH-style import is intended.

Project-specific patterns
- Entrypoints live under `cmd/` and should contain minimal wiring. New functionality should live in a new package under the repository root (e.g., `internal/` or a top-level package) and be exercised from `cmd/decimalniner/main.go`.
- Keep `main` small: prefer creating functions in other packages and calling them from `main`.

Developer workflow notes for AI edits
- Small, single-purpose PRs: change one package or feature per PR.
- If you add dependencies, update/commit `go.mod` and `go.sum` and include build instructions in `README.md`.
- If adding tests, use table-driven tests and `go test ./...` in CI.

Conventions for commits and PRs
- Use imperative, concise commit messages (e.g., "add logging to CLI startup").
- Include a short PR description stating why the change is needed and how to verify (build/run commands).

Integration points & external dependencies
- None detected. If integrating with other services (APIs, simulators), document required environment variables and authentication in `README.md` and update this file.

Guidance for code generation and refactors
- Prefer small, testable functions in packages (not in `main`).
- Run `go vet` and `go fmt` on changes before proposing a PR.
- If you propose adding a module or CI, explain the reasons and provide the exact commands to run locally.

Example changes to propose
- Add logging package and wire it into `cmd/decimalniner/main.go` (include updated `go.mod` if adding external logger).
- Introduce an `internal/app` package with a `Run()` function and call it from `main`.

If anything is unclear
- Ask for clarification about desired runtime environment (GOPATH vs modules), CI requirements, or intended integration targets before making large structural changes.

Files worth checking for future context
- `README.md` — add build/run/CI info when changing the repo.
- `cmd/decimalniner/main.go` — primary integration point for new functionality.

— End
