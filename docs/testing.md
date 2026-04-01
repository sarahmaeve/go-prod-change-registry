# Manual Testing Guide

This guide walks through manually testing the production change registry (PCR)
server -- both the API and the web dashboard.

The event model is **append-only**: events are immutable once created. There are
no PUT or DELETE endpoints. Status changes (star, alert) are recorded as
**meta-events** that reference a parent event via `parent_id`.

---

## 1. Setup

### Build and start the server

```bash
make build
PCR_API_TOKENS=test-token PCR_SESSION_SECRET=my-test-secret ./bin/pcr-server
```

The server listens on `:8080` by default. Override with `PCR_ADDR`.

`PCR_SESSION_SECRET` is the HMAC key for dashboard session cookies. If omitted, a
random key is generated (sessions won't survive server restarts).

### Shell alias for convenience

Set these once in your terminal session so every example below is
copy-pasteable:

```bash
export PCR_TOKEN="test-token"
alias pcr='curl -s -H "Authorization: Bearer $PCR_TOKEN" -H "Content-Type: application/json"'
```

---

## 2. Creating test events

### A deployment with lifecycle tags

Model a deployment as two events sharing a `deploy_id` tag, with a
`phase:start` and `phase:end` tag respectively:

```bash
# Deployment start
pcr -X POST http://localhost:8080/api/v1/events -d '{
  "user_name": "alice",
  "event_type": "deployment",
  "description": "Deploy api v2.4.1 - start",
  "tags": {"env": "prod", "service": "api", "deploy_id": "d-001", "phase": "start"}
}' | jq

# Deployment end (a few minutes later)
pcr -X POST http://localhost:8080/api/v1/events -d '{
  "user_name": "alice",
  "event_type": "deployment",
  "description": "Deploy api v2.4.1 - end",
  "tags": {"env": "prod", "service": "api", "deploy_id": "d-001", "phase": "end"}
}' | jq
```

### A feature flag toggle

```bash
pcr -X POST http://localhost:8080/api/v1/events -d '{
  "user_name": "bob",
  "event_type": "feature-flag",
  "description": "Enable dark-mode flag",
  "long_description": "Toggled dark-mode on for 10% of staging users.",
  "tags": {"env": "staging", "flag": "dark-mode"}
}' | jq
```

### A k8s change

```bash
pcr -X POST http://localhost:8080/api/v1/events -d '{
  "user_name": "carol",
  "event_type": "k8s-change",
  "description": "Rolling restart of payments pods",
  "timestamp": "2026-03-31T10:00:00Z",
  "tags": {"env": "prod", "cluster": "us-east-1", "service": "payments"}
}' | jq
```

### An event with long_description containing links and notes

```bash
pcr -X POST http://localhost:8080/api/v1/events -d '{
  "user_name": "dave",
  "event_type": "deployment",
  "description": "Deploy billing v3.0.0",
  "long_description": "Release notes: https://github.com/example/billing/releases/tag/v3.0.0\n\nKey changes:\n- New invoice PDF renderer\n- Fixed tax rounding bug (BILL-1234)\n- Requires DB migration (already applied)",
  "tags": {"env": "prod", "service": "billing"}
}' | jq
```

### Starring an event (meta-event)

Stars are created by POSTing to the star endpoint. This creates a meta-event
with `parent_id` referencing the original event:

```bash
# Replace EVENT_ID with the id from one of the events above
pcr -X POST http://localhost:8080/api/v1/events/EVENT_ID/star | jq
```

Run again to toggle (creates an "unstar" meta-event).

### Creating an alert meta-event

Alerts are meta-events with `event_type: "alert"` and a `parent_id`:

```bash
# Replace EVENT_ID with the id of the event to alert on
pcr -X POST http://localhost:8080/api/v1/events -d '{
  "parent_id": "EVENT_ID",
  "user_name": "eve",
  "event_type": "alert",
  "description": "Hotfix deployed outside normal change window (CVE-2026-9999)"
}' | jq
```

To clear the alert later, create a `clear-alert` meta-event:

```bash
pcr -X POST http://localhost:8080/api/v1/events -d '{
  "parent_id": "EVENT_ID",
  "user_name": "eve",
  "event_type": "clear-alert",
  "description": "CVE patched, alert no longer needed"
}' | jq
```

---

## 3. Testing the API

### List all events

```bash
pcr http://localhost:8080/api/v1/events | jq
```

The response includes `events`, `total_count`, `limit`, and `offset`.

### Filter by time range

Use `start_after` and `start_before` (RFC 3339 timestamps):

```bash
pcr "http://localhost:8080/api/v1/events?start_after=2026-03-31T00:00:00Z&start_before=2026-04-01T00:00:00Z" | jq
```

### Filter with around + window

Return events within a time window centered on a point in time:

```bash
pcr "http://localhost:8080/api/v1/events?around=2026-03-31T10:00:00Z&window=15m" | jq
```

### Filter by user

```bash
pcr "http://localhost:8080/api/v1/events?user=alice" | jq
```

### Filter by event type

```bash
pcr "http://localhost:8080/api/v1/events?type=deployment" | jq
```

### Filter to top-level events only (exclude meta-events)

```bash
pcr "http://localhost:8080/api/v1/events?top_level=true" | jq
```

### Filter by a single tag

```bash
pcr "http://localhost:8080/api/v1/events?tag=env:prod" | jq
```

### Filter by multiple tags

Tags are ANDed together -- only events matching all supplied tags are returned:

```bash
pcr "http://localhost:8080/api/v1/events?tag=env:prod&tag=service:api" | jq
```

### Pagination

```bash
# First page of 2 results
pcr "http://localhost:8080/api/v1/events?limit=2&offset=0" | jq

# Second page
pcr "http://localhost:8080/api/v1/events?limit=2&offset=2" | jq
```

### Get a single event by ID

```bash
pcr http://localhost:8080/api/v1/events/EVENT_ID | jq
```

### Get annotations for an event

Returns the derived annotation state (starred, alerted) computed from
meta-events:

```bash
pcr http://localhost:8080/api/v1/events/EVENT_ID/annotations | jq
```

Expected response:

```json
{
  "starred": true,
  "alerted": false
}
```

### Toggle star

```bash
pcr -X POST http://localhost:8080/api/v1/events/EVENT_ID/star | jq
```

Run again to un-star. Each call creates a new meta-event (star or unstar).

### Create an alert meta-event

```bash
pcr -X POST http://localhost:8080/api/v1/events -d '{
  "parent_id": "EVENT_ID",
  "user_name": "ops-bot",
  "event_type": "alert",
  "description": "Anomalous error rate spike after this change"
}' | jq
```

---

## 4. Testing the dashboard

### Logging in

The dashboard uses cookie-based sessions. Log in by navigating to:

```
http://localhost:8080/login?token=test-token
```

This validates the token, sets an HttpOnly session cookie, and redirects to
the dashboard at `/`. You should see the dashboard without any token in the URL.

To test login failure, try an invalid token:

```
http://localhost:8080/login?token=wrong
```

Expected: 401 Unauthorized.

### Dashboard features

1. **Time range buttons** -- Click "Last 5 min", "Last 30 min", "Last 1 hr",
   etc. Verify the event list updates accordingly. "Last 24 hours" is the
   default when the dashboard first loads.

2. **Tag filtering** -- Click any tag badge (e.g. `env=prod`) on an event row.
   The event list should filter to show only events with that tag.

3. **Star toggle** -- Click the star icon on an event row. The star should
   toggle on and off. Reload the page to confirm the change persisted (a new
   meta-event should exist).

4. **Alert highlighting** -- Create an alert meta-event for an event (see
   section 2 above). The row for that event should have a light red background.

5. **Event detail page** -- Click the timestamp of an event to navigate to its
   detail page. Verify the full event data (including `long_description`) is
   shown, along with annotation state.

6. **Back to dashboard** -- On the detail page, click the "Back to dashboard"
   link. Verify you are returned to the dashboard. The session cookie handles
   authentication automatically -- no token in the URL.

7. **Session persistence** -- Close and reopen the browser tab. Navigate to
   `http://localhost:8080/`. You should still be authenticated (cookie is valid
   for 24 hours).

---

## 5. Testing idempotency

The `external_id` field acts as an idempotency key. These tests verify correct
behavior for creates and retries.

### Create event with external_id (expect 201)

```bash
pcr -w "\nHTTP status: %{http_code}\n" -X POST http://localhost:8080/api/v1/events -d '{
  "external_id": "test-idempotency-001",
  "user_name": "alice",
  "event_type": "deployment",
  "description": "Idempotency test event",
  "tags": {"env": "staging"}
}' | jq
```

Expected: HTTP 201 Created. Note the returned `id`.

### Retry same request (expect 200, same event returned)

```bash
pcr -w "\nHTTP status: %{http_code}\n" -X POST http://localhost:8080/api/v1/events -d '{
  "external_id": "test-idempotency-001",
  "user_name": "alice",
  "event_type": "deployment",
  "description": "Idempotency test event",
  "tags": {"env": "staging"}
}' | jq
```

Expected: HTTP 200 OK. The returned event has the same `id` and `created_at` as
the first request. No duplicate was created.

### Create with a different external_id (expect 201, new event)

```bash
pcr -w "\nHTTP status: %{http_code}\n" -X POST http://localhost:8080/api/v1/events -d '{
  "external_id": "test-idempotency-002",
  "user_name": "alice",
  "event_type": "deployment",
  "description": "A different deployment",
  "tags": {"env": "staging"}
}' | jq
```

Expected: HTTP 201 Created. A new event with a different `id`.

### Create without external_id (expect 201, no conflict)

```bash
pcr -w "\nHTTP status: %{http_code}\n" -X POST http://localhost:8080/api/v1/events -d '{
  "user_name": "alice",
  "event_type": "deployment",
  "description": "No external_id, always creates",
  "tags": {"env": "staging"}
}' | jq
```

Expected: HTTP 201 Created. Events without `external_id` never conflict with
each other or with events that have one.

---

## 6. Testing auth enforcement

### Request without a token returns 401

```bash
curl -s http://localhost:8080/api/v1/events | jq
```

Expected: HTTP 401 Unauthorized.

### Health endpoint works without auth

The health check does not require authentication and verifies database connectivity.

```bash
# Should return 200 with {"status": "ok"} when the database is reachable
curl -s http://localhost:8080/api/v1/health | jq
```

Expected: `{"status": "ok"}` with HTTP 200.

To test the unhealthy case, stop the server, corrupt or remove the database file, and restart:

```bash
# Move the DB away temporarily
mv registry.db registry.db.bak
# Restart the server (it will create a new empty DB, so this tests migration + health)
# To truly test DB failure, you would need to make the file unreadable:
chmod 000 registry.db
curl -s http://localhost:8080/api/v1/health | jq
# Expected: {"status": "unhealthy", "reason": "database unreachable"} with HTTP 503
# Restore:
chmod 644 registry.db
mv registry.db.bak registry.db
```

### Query parameter token works

```bash
curl -s "http://localhost:8080/api/v1/events?token=test-token" | jq
```

Expected: HTTP 200 with the events list.

### Invalid token returns 401

```bash
curl -s -H "Authorization: Bearer wrong-token" http://localhost:8080/api/v1/events | jq
```

Expected: HTTP 401 Unauthorized.

### Annotations endpoint requires auth

```bash
curl -s http://localhost:8080/api/v1/events/EVENT_ID/annotations | jq
```

Expected: HTTP 401 Unauthorized.

### Star endpoint requires auth

```bash
curl -s -X POST http://localhost:8080/api/v1/events/EVENT_ID/star | jq
```

Expected: HTTP 401 Unauthorized.

---

## 7. Running automated tests

### Unit tests

```bash
make test
```

### Integration tests

```bash
go test -tags=integration ./... -race
```

### All tests with coverage

```bash
go test -tags=integration ./... -race -cover
```
