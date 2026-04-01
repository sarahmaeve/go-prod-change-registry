# Roadmap

This document captures architectural direction and planned features for the production change registry.

## Architectural Direction: Append-Only Event Model

The registry is moving from a mutable-record model to an **append-only, event-sourced model**. This is the most significant architectural change planned.

### Core Principles

1. **Events are immutable.** Once created, a production change event cannot be modified or deleted. A production change is a fact — it should not be rewritten.

2. **Meta-events for status changes.** Starring, alerting, or any status annotation is itself a new event with a `parent_id` referencing the original. To determine if event X is starred, query the most recent meta-event for X with the appropriate type.

3. **Single timestamp per event.** Events have one `timestamp` field (when it happened), not start/end. Lifecycle tracking (deploy started, deploy completed) uses two separate events linked by a shared tag (e.g., `deploy_id=abc123`).

4. **No updates, no deletes.** The API supports POST (create) and GET (read/query) only. No PUT, PATCH, or DELETE endpoints.

### Schema Changes (from current model)

- Remove `timestamp_start` and `timestamp_end` — replace with single `timestamp`
- Remove `starred` and `alerted` boolean columns — these become meta-events
- Add `parent_id` (nullable) — references another event's ID, making this a meta-event
- Keep: `id`, `user_name`, `timestamp`, `event_type`, `description`, `long_description`, `tags`, `created_at`
- Remove `updated_at` — events are immutable, so there is no update time

### Meta-Event Examples

Star an event:
```json
{
  "parent_id": "original-event-id",
  "event_type": "star",
  "user_name": "sarah",
  "description": "starred"
}
```

Alert an event:
```json
{
  "parent_id": "original-event-id",
  "event_type": "alert",
  "user_name": "oncall-bot",
  "description": "high-risk change flagged"
}
```

Unstar (the most recent meta-event wins):
```json
{
  "parent_id": "original-event-id",
  "event_type": "unstar",
  "user_name": "sarah",
  "description": "unstarred"
}
```

### Lifecycle via Linked Events

A deployment lifecycle is modeled as separate events sharing a tag:

```bash
# Deploy started
pcr -X POST .../events -d '{
  "event_type": "deployment",
  "description": "deploy v1.2 started",
  "tags": {"deploy_id": "abc123", "phase": "start", "env": "prod"}
}'

# Deploy completed (separate event, same deploy_id tag)
pcr -X POST .../events -d '{
  "event_type": "deployment",
  "description": "deploy v1.2 completed",
  "tags": {"deploy_id": "abc123", "phase": "end", "env": "prod"}
}'
```

Query by tag `deploy_id:abc123` to see the full lifecycle.

### Dashboard Changes

- Star toggle becomes: POST a meta-event with `parent_id` and `event_type=star` or `event_type=unstar`
- Alert indicator: derived by querying meta-events where `parent_id=X` and `event_type` is `alert`/`clear-alert`, taking the most recent
- Event detail page: shows the event plus its meta-event history (annotations timeline)

### Query Enhancements

- **Window query**: `GET /api/v1/events?around=2026-03-31T14:32:00Z&window=30m` — returns all events within 30 minutes of the given timestamp. Essential for incident correlation.
- **Exclude meta-events**: `GET /api/v1/events?top_level=true` — returns only events without a `parent_id` (excludes stars, alerts, etc.)
- **Include annotations**: `GET /api/v1/events/{id}?include=annotations` — returns the event plus its meta-event children

## Future Features

### CI/CD Ingestion

**Priority:** Medium — planned for after the append-only migration.

- **Idempotency keys**: Add an optional `external_id` field with a unique constraint. CI/CD pipelines can retry POSTs safely — if an event with the same `external_id` exists, return the existing event instead of creating a duplicate.
- **Batch import**: `POST /api/v1/events/batch` accepting an array of events. Useful for backfilling historical data or importing from other systems.
- **Webhook receivers**: Pre-built handlers for common CI/CD systems:
  - GitHub Actions (deployment events)
  - ArgoCD (sync events)
  - GitLab CI (pipeline events)
  - Generic webhook with configurable field mapping

### Data Retention

- `PCR_RETENTION_DAYS` config for automatic cleanup of old events
- Soft-delete via archival flag before hard delete
- Export endpoint (`GET /api/v1/events/export?format=csv`) for archival before cleanup

### Integration / Notification

- SSE (Server-Sent Events) stream at `/api/v1/events/stream` for real-time dashboards
- Webhook-out: notify Slack/PagerDuty when an alerted event is created
- Export API for CSV/JSON bulk download

### PostgreSQL Backend

The `store.ChangeStore` interface is designed for this swap. Blockers to address:
- Migration runner in `main.go` imports the SQLite-specific driver — needs abstraction
- Connection string configuration (currently SQLite path, needs PostgreSQL DSN)
- Tag filtering query may need GIN indexes on JSONB for performance at scale

### Authentication Improvements

- Cookie-based sessions for the dashboard (eliminate token-in-URL)
- Token-to-identity mapping (associate each API token with a user/team name)
- OIDC/OAuth2 integration for enterprise SSO
