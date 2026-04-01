# Roadmap

This document captures architectural direction and planned features for the production change registry.

## Architecture Decisions (Implemented)

The append-only event model, meta-events for status changes (star/alert), single-timestamp events, parent_id references, idempotency keys, and window/top-level queries are all implemented. See README.md and CLAUDE.md for current architecture documentation.

## Future Features

### CI/CD Ingestion

**Priority:** Medium — planned for after the append-only migration.

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

- Token-to-identity mapping (associate each API token with a user/team name)
- OIDC/OAuth2 integration for enterprise SSO
