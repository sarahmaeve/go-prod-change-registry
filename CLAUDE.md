# Project Description

This is a service that records changes to production in a registry which is viewable as a dashboard, and is also queryable via an API.

## Coding guidelines

This project is written in Go. Typed languages are to be chosen at all possible times.

Our preferences in implementation languages are the following, in declining order:

- Go
- HTML / CSS
- Javascript for web interfaces ONLY, and we prefer TypeScript.

Never use Python, Node, or Ruby for generated development code. Bash can be used only for builds and only if necessary.

Use code guidelines from https://github.com/samber/cc-skills-golang/ when possible.
This may be available in a cc-skills-golang skill.

**IMPORTANT:** The appropriate `cc-skills-golang` skills MUST be invoked for ALL Go coding tasks. This includes but is not limited to:
- `golang-testing` for writing or reviewing tests
- `golang-code-style` for code style, formatting, and conventions
- `golang-error-handling` for error handling patterns
- `golang-naming` for naming conventions
- `golang-safety` for defensive coding
- `golang-concurrency` for concurrent code
- Other relevant skills as applicable to the task at hand

## Avoid hallucinations

When suggesting a code option, you must VERIFY the existence of every API or tool, and determine whether that matches the other tool versions which are used or suggested.

## Architecture

### Package layout
- `cmd/server/` — Entry point, dependency wiring, graceful shutdown
- `internal/config/` — Env var config (PCR_ prefix)
- `internal/model/` — ChangeEvent, ListParams, request/response types
- `internal/store/` — ChangeStore interface
- `internal/store/sqlite/` — SQLite implementation (WAL mode, busy_timeout, slow query logging)
- `internal/service/` — Business logic, validation, defaults
- `internal/handler/` — API handlers (REST/JSON) and dashboard handlers (HTML)
- `internal/middleware/` — Auth (Bearer + query param token), request logging, request ID
- `internal/router/` — Chi router wiring
- `migrations/` — Embedded SQL migrations (golang-migrate)
- `web/` — Embedded HTML templates and static CSS

### Key design decisions
- Repository pattern: `store.ChangeStore` interface allows swapping SQLite for PostgreSQL
- SQLite with WAL mode + busy_timeout for concurrent access
- Slow query threshold logging on all store operations
- Zero trust auth: all routes require token by default (PCR_REQUIRE_AUTH_READS)
- Token passed via Bearer header or ?token= query param for browser access
- Templates parsed separately per page to avoid Go template name collisions
- Star is toggleable from dashboard (POST form), alert is API-only
- Static files served outside auth middleware

### Dependencies (4 external)
- `github.com/go-chi/chi/v5` — Router
- `modernc.org/sqlite` — Pure-Go SQLite
- `github.com/google/uuid` — UUID v7 generation
- `github.com/golang-migrate/migrate/v4` — Schema migrations

### Testing
- Unit tests: `make test`
- Integration tests (SQLite): `go test -tags=integration ./... -race`
- Store tests use `//go:build integration` tag
- Tests use `t.Parallel()`, table-driven patterns, black-box packages where possible
