# go-prod-change-registry

A lightweight Go service that records production changes (deployments, feature-flag flips, infrastructure mutations) in a SQLite-backed registry. It exposes a RESTful API and an HTML dashboard so teams can correlate production changes with incidents and understand what changed, when, and by whom.

## Quickstart

```bash
make build
export PCR_API_TOKENS="my-secret-token"
./bin/pcr-server
```

The server starts on `:8080` by default. Open `http://localhost:8080/?token=my-secret-token` for the dashboard.

## Configuration

All configuration is via environment variables prefixed with `PCR_`.

| Variable | Required | Default | Description |
|---|---|---|---|
| `PCR_API_TOKENS` | Yes | -- | Comma-separated list of valid API tokens |
| `PCR_ADDR` | No | `:8080` | Listen address (`host:port`) |
| `PCR_DATABASE_PATH` | No | `registry.db` | Path to the SQLite database file |
| `PCR_REQUIRE_AUTH_READS` | No | `true` | Require auth for read endpoints (GET) |
| `PCR_AUTO_MIGRATE` | No | `true` | Run database migrations on startup |
| `PCR_DASHBOARD_REFRESH_SEC` | No | `60` | Dashboard auto-refresh interval in seconds |
| `PCR_READ_TIMEOUT` | No | `5s` | HTTP server read timeout (Go duration) |
| `PCR_WRITE_TIMEOUT` | No | `10s` | HTTP server write timeout (Go duration) |
| `PCR_SHUTDOWN_TIMEOUT` | No | `15s` | Graceful shutdown timeout (Go duration) |
| `PCR_DB_BUSY_TIMEOUT` | No | `5s` | SQLite busy/write-lock wait timeout |
| `PCR_DB_SLOW_QUERY_THRESHOLD` | No | `100ms` | Log a warning when a query exceeds this |

## API Reference

Set up a convenience alias:

```bash
export PCR_TOKEN="your-token"
alias pcr='curl -s -H "Authorization: Bearer $PCR_TOKEN" -H "Content-Type: application/json"'
```

### Endpoints

| Method | Path | Description |
|---|---|---|
| GET | `/api/v1/health` | Health check |
| POST | `/api/v1/events` | Create a change event |
| GET | `/api/v1/events` | List events (with filters) |
| GET | `/api/v1/events/{id}` | Get a single event |
| PUT | `/api/v1/events/{id}` | Partial update of an event |
| DELETE | `/api/v1/events/{id}` | Delete an event |
| POST | `/api/v1/events/{id}/star` | Toggle the starred flag |

### Query parameters for `GET /api/v1/events`

| Parameter | Type | Description |
|---|---|---|
| `start_after` | RFC 3339 timestamp | Events starting after this time |
| `start_before` | RFC 3339 timestamp | Events starting before this time |
| `user` | string | Filter by user name |
| `type` | string | Filter by event type (`deployment`, `feature-flag`, `k8s-change`, ...) |
| `tag` | string | Filter by tag (`key=value`) |
| `limit` | int | Max results (default 50) |
| `offset` | int | Pagination offset |

### Examples

**Health check:**

```bash
pcr http://localhost:8080/api/v1/health
```

**Create an event:**

```bash
pcr -X POST http://localhost:8080/api/v1/events -d '{
  "user_name": "alice",
  "event_type": "deployment",
  "description": "Deploy payments-service v2.4.1",
  "long_description": "Rolling update across 3 regions",
  "tags": {"service": "payments", "region": "us-east-1"}
}'
```

**List events with filters:**

```bash
pcr "http://localhost:8080/api/v1/events?type=deployment&start_after=2026-03-30T00:00:00Z&limit=10"
```

**Get a single event:**

```bash
pcr http://localhost:8080/api/v1/events/abc123
```

**Update an event (partial):**

```bash
pcr -X PUT http://localhost:8080/api/v1/events/abc123 -d '{
  "description": "Deploy payments-service v2.4.2 (rollback)",
  "alerted": true
}'
```

**Delete an event:**

```bash
pcr -X DELETE http://localhost:8080/api/v1/events/abc123
```

**Toggle star:**

```bash
pcr -X POST http://localhost:8080/api/v1/events/abc123/star
```

## Dashboard

The built-in HTML dashboard is served at `/` and requires authentication via the `?token=` query parameter (e.g., `/?token=my-secret-token`). It provides:

- Time range buttons to filter events by predefined windows (last hour, 6h, 24h, 7d, etc.)
- Clickable tags that filter the event list
- A star toggle on each event for quick bookmarking
- Visual alert highlighting for events marked with `alerted: true`
- Auto-refresh at a configurable interval (see `PCR_DASHBOARD_REFRESH_SEC`)

## Data Model

The core `ChangeEvent` struct:

| Field | Type | Description |
|---|---|---|
| `id` | string | Unique identifier (generated) |
| `user_name` | string | Who made the change |
| `timestamp_start` | RFC 3339 | When the change started |
| `timestamp_end` | RFC 3339 (optional) | When the change ended |
| `event_type` | string | Category: `deployment`, `feature-flag`, `k8s-change`, or custom |
| `description` | string | Short summary |
| `long_description` | string | Detailed description |
| `starred` | bool | Bookmarked by a user |
| `alerted` | bool | Associated with an alert/incident |
| `tags` | map[string]string | Arbitrary key-value metadata |
| `created_at` | RFC 3339 | Record creation time |
| `updated_at` | RFC 3339 | Last modification time |

## Architecture

```
cmd/server/        Entry point (main)
internal/
  config/          Environment-based configuration
  model/           Domain types (ChangeEvent, request/response structs)
  store/           SQLite data access layer
  service/         Business logic
  handler/         HTTP handlers (API + dashboard)
  middleware/      Auth, request ID, logging
  router/          Route definitions (chi)
migrations/        SQL migration files
web/               Embedded static assets and HTML templates
```

## Development

### Make targets

| Target | Description |
|---|---|
| `make build` | Compile to `bin/pcr-server` with version info |
| `make test` | Run all tests with race detector and coverage |
| `make test-short` | Run tests in short mode |
| `make lint` | Run `golangci-lint` |
| `make fmt` | Format with `gofmt` and `goimports` |
| `make run` | `go run ./cmd/server` |
| `make vet` | Run `go vet` |
| `make audit` | Run `go vet` + `govulncheck` |

### Integration tests

```bash
go test -race -tags=integration ./...
```

## Auth

The server follows a zero-trust-by-default model. Every request (reads and writes) must be authenticated unless `PCR_REQUIRE_AUTH_READS` is set to `false`, in which case only write operations require a token.

Authentication is performed via one of two methods:

1. **Bearer token header:** `Authorization: Bearer <token>`
2. **Query parameter:** `?token=<token>`

Tokens are configured through the `PCR_API_TOKENS` environment variable (comma-separated). Static files under `/static/*` are served without authentication.
