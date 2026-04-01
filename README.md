# go-prod-change-registry

A lightweight, append-only change registry for production environments. It records deployments, feature-flag flips, infrastructure mutations, and other production changes as immutable events in a SQLite-backed store, then exposes them through a RESTful API and an HTML dashboard. Teams use it to correlate production changes with incidents and understand what changed, when, and by whom.

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

The API is append-only. There are no PUT, PATCH, or DELETE endpoints. Events are immutable once created.

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/v1/health` | Health check (no auth required, verifies DB connectivity) |
| `POST` | `/api/v1/events` | Create a change event or meta-event |
| `GET` | `/api/v1/events` | List events (with filters) |
| `GET` | `/api/v1/events/{id}` | Get a single event |
| `GET` | `/api/v1/events/{id}/annotations` | Get derived annotation state (starred, alerted) |
| `POST` | `/api/v1/events/{id}/star` | Toggle star (creates a star or unstar meta-event) |

### Query parameters for `GET /api/v1/events`

| Parameter | Type | Description |
|---|---|---|
| `start_after` | RFC 3339 timestamp | Events with timestamp after this time |
| `start_before` | RFC 3339 timestamp | Events with timestamp before this time |
| `around` | RFC 3339 timestamp | Center of a time window (use with `window`) |
| `window` | Go duration (e.g. `30m`) | Half-width of the time window around `around` |
| `user` | string | Filter by user name |
| `type` | string | Filter by event type (`deployment`, `feature-flag`, `k8s-change`, ...) |
| `tag` | string | Filter by tag (`key=value`) |
| `top_level` | bool | If `true`, exclude meta-events (only events without a `parent_id`) |
| `limit` | int | Max results, 1-200 (default 50) |
| `offset` | int | Pagination offset |

### Examples

**Health check:**

The health endpoint does not require authentication and verifies database connectivity.
It is suitable for use as a Kubernetes liveness/readiness probe or load balancer health check.

```bash
# No auth needed
curl -s http://localhost:8080/api/v1/health
# Returns 200: {"status":"ok"}

# When database is unreachable:
# Returns 503: {"status":"unhealthy","reason":"database unreachable"}
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

**Window query (incident correlation):**

```bash
pcr "http://localhost:8080/api/v1/events?around=2026-03-31T14:32:00Z&window=30m"
```

This returns all events within 30 minutes of the given timestamp -- useful for answering "what changed around the time of an incident?"

**List top-level events only (exclude meta-events):**

```bash
pcr "http://localhost:8080/api/v1/events?top_level=true"
```

**Get a single event:**

```bash
pcr http://localhost:8080/api/v1/events/abc123
```

**Get annotations for an event (derived star/alert state):**

```bash
pcr http://localhost:8080/api/v1/events/abc123/annotations
```

Returns:

```json
{"starred": true, "alerted": false}
```

**Toggle star:**

```bash
pcr -X POST http://localhost:8080/api/v1/events/abc123/star
```

This creates a `star` or `unstar` meta-event depending on the current state.

## Meta-Events

Status changes are not stored as mutable fields on an event. Instead, they are modeled as new, immutable meta-events that reference the original event via `parent_id`.

### How it works

To star an event, a new event is created:

```json
{
  "parent_id": "original-event-id",
  "event_type": "star",
  "user_name": "sarah",
  "description": "starred"
}
```

To unstar, another meta-event is created:

```json
{
  "parent_id": "original-event-id",
  "event_type": "unstar",
  "user_name": "sarah",
  "description": "unstarred"
}
```

The current state is derived by looking at the most recent meta-event. The `GET /api/v1/events/{id}/annotations` endpoint returns the computed state.

### Meta-event types

| Type | Effect |
|---|---|
| `star` | Marks the parent event as starred |
| `unstar` | Removes the star from the parent event |
| `alert` | Marks the parent event as alerted |
| `clear-alert` | Removes the alert from the parent event |

### Lifecycle via linked events

A deployment lifecycle (or any multi-phase operation) is modeled as separate events sharing a tag rather than as a single event with start/end timestamps:

```bash
# Deploy started
pcr -X POST http://localhost:8080/api/v1/events -d '{
  "event_type": "deployment",
  "user_name": "alice",
  "description": "deploy v1.2 started",
  "tags": {"deploy_id": "abc123", "phase": "start", "env": "prod"}
}'

# Deploy completed (separate event, same deploy_id tag)
pcr -X POST http://localhost:8080/api/v1/events -d '{
  "event_type": "deployment",
  "user_name": "alice",
  "description": "deploy v1.2 completed",
  "tags": {"deploy_id": "abc123", "phase": "end", "env": "prod"}
}'
```

Query by tag to see the full lifecycle:

```bash
pcr "http://localhost:8080/api/v1/events?tag=deploy_id%3Dabc123"
```

## Idempotency

The API supports an optional `external_id` field on events, which acts as an idempotency key. This allows CI/CD pipelines and automation to safely retry requests without creating duplicate events.

### How it works

- `external_id` is an optional string field on the create-event request.
- A partial unique index enforces that no two events share the same non-null `external_id`.
- On the first POST with a given `external_id`, the server creates the event and returns **201 Created**.
- On a subsequent POST with the same `external_id`, the server returns the existing event with **200 OK** instead of creating a duplicate.
- If `external_id` is omitted (or null), no uniqueness check is performed and the event is always created.

### Generating an external_id

Callers should construct `external_id` from a combination of the source system and a unique operation identifier. The value must be globally unique across all events in the registry.

| Source | Pattern | Example |
|---|---|---|
| GitHub Actions | `github-actions-{run_id}-{job}` | `github-actions-12345-deploy` |
| GitLab CI | `gitlab-{pipeline_id}-{job_id}` | `gitlab-8901-deploy-prod` |
| ArgoCD | `argocd-{app}-{revision}` | `argocd-api-abc123f` |
| Terraform | `terraform-{workspace}-{run_id}` | `terraform-prod-run-567` |
| LLM agent | `agent-{session}-{action}` | `agent-sess-a1b2-deploy` |

### Example: create with external_id and retry

```bash
# First request -- creates the event (201 Created)
pcr -X POST http://localhost:8080/api/v1/events -d '{
  "external_id": "github-actions-12345-deploy",
  "user_name": "ci-bot",
  "event_type": "deployment",
  "description": "Deploy api v3.1.0",
  "tags": {"service": "api", "env": "prod"}
}'

# Retry (network blip, webhook redelivery, etc.) -- returns the same event (200 OK)
pcr -X POST http://localhost:8080/api/v1/events -d '{
  "external_id": "github-actions-12345-deploy",
  "user_name": "ci-bot",
  "event_type": "deployment",
  "description": "Deploy api v3.1.0",
  "tags": {"service": "api", "env": "prod"}
}'
```

Both requests return the same event (same `id`, same `created_at`). The second request is a no-op.

## Dashboard

The built-in HTML dashboard is served at `/` and requires authentication via the `?token=` query parameter (e.g., `/?token=my-secret-token`). It provides:

- Time range buttons to filter events by predefined windows (last hour, 6h, 24h, 7d, etc.)
- Clickable tags that filter the event list to matching events
- A star toggle on each event (creates star/unstar meta-events behind the scenes)
- Visual alert highlighting for events that have active alert meta-events
- Event detail page showing the event plus its annotation history (meta-event timeline)
- Auto-refresh at a configurable interval (see `PCR_DASHBOARD_REFRESH_SEC`)

## Data Model

Events are immutable. There are no update or delete operations. The core `ChangeEvent` struct:

| Field | Type | Description |
|---|---|---|
| `id` | string | Unique identifier (generated) |
| `external_id` | string (optional) | Caller-supplied idempotency key (unique when non-null) |
| `parent_id` | string (optional) | References another event's ID, making this a meta-event |
| `user_name` | string | Who made the change |
| `timestamp` | RFC 3339 | When the change happened |
| `event_type` | string | Category: `deployment`, `feature-flag`, `k8s-change`, or custom. Meta-events use `star`, `unstar`, `alert`, `clear-alert` |
| `description` | string | Short summary |
| `long_description` | string | Detailed description |
| `tags` | map[string]string | Arbitrary key-value metadata for filtering and lifecycle linking |
| `created_at` | RFC 3339 | Record creation time |

Notably absent from the old model: `timestamp_start`, `timestamp_end`, `starred`, `alerted`, and `updated_at`. These are replaced by the single `timestamp` field, meta-events, and the principle that events do not change after creation.

## Architecture

```
cmd/server/        Entry point (main)
internal/
  config/          Environment-based configuration
  model/           Domain types (ChangeEvent, ListParams, request/response structs)
  store/           SQLite data access layer (ChangeStore interface)
  service/         Business logic
  handler/         HTTP handlers (API + dashboard)
  middleware/      Auth, request ID, logging
  router/          Route definitions (chi)
migrations/        SQL migration files
web/               Embedded static assets and HTML templates
docs/              Design documents and roadmap
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
| `make clean` | Remove build artifacts |

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
