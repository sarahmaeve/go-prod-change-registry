# Manual Testing Guide

This guide walks through manually testing the production change registry (PCR)
server -- both the API and the web dashboard.

---

## 1. Setup

### Build and start the server

```bash
make build
PCR_API_TOKENS=test-token ./bin/pcr-server
```

The server listens on `:8080` by default. Override with `PCR_ADDR`.

### Shell alias for convenience

Set these once in your terminal session so every example below is
copy-pasteable:

```bash
export PCR_TOKEN="test-token"
alias pcr='curl -s -H "Authorization: Bearer $PCR_TOKEN" -H "Content-Type: application/json"'
```

---

## 2. Creating test events

### A deployment with tags env=prod, service=api

```bash
pcr -X POST http://localhost:8080/api/v1/events -d '{
  "user_name": "alice",
  "event_type": "deployment",
  "description": "Deploy api v2.4.1",
  "long_description": "",
  "tags": {"env": "prod", "service": "api"}
}' | jq
```

### A feature flag toggle with tag env=staging

```bash
pcr -X POST http://localhost:8080/api/v1/events -d '{
  "user_name": "bob",
  "event_type": "feature-flag",
  "description": "Enable dark-mode flag",
  "long_description": "Toggled dark-mode on for 10% of staging users.",
  "tags": {"env": "staging", "flag": "dark-mode"}
}' | jq
```

### A k8s change with end timestamp (has duration)

```bash
pcr -X POST http://localhost:8080/api/v1/events -d '{
  "user_name": "carol",
  "event_type": "k8s-change",
  "description": "Rolling restart of payments pods",
  "long_description": "",
  "timestamp_start": "2026-03-31T10:00:00Z",
  "timestamp_end":   "2026-03-31T10:05:30Z",
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

### An event with alerted: true (high risk)

Create the event first, then set the alert flag via update:

```bash
# Create the event
pcr -X POST http://localhost:8080/api/v1/events -d '{
  "user_name": "eve",
  "event_type": "deployment",
  "description": "Emergency hotfix auth-service",
  "long_description": "Hotfix for CVE-2026-9999. Deployed outside normal change window.",
  "tags": {"env": "prod", "service": "auth", "priority": "p0"}
}' | jq

# Note the returned id, then set alerted to true (replace EVENT_ID):
pcr -X PUT http://localhost:8080/api/v1/events/EVENT_ID -d '{
  "alerted": true
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

### Filter by user

```bash
pcr "http://localhost:8080/api/v1/events?user_name=alice" | jq
```

### Filter by event type

```bash
pcr "http://localhost:8080/api/v1/events?event_type=deployment" | jq
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

### Update an event (partial update with PUT)

Only the fields you provide are changed. All fields are optional:

```bash
pcr -X PUT http://localhost:8080/api/v1/events/EVENT_ID -d '{
  "description": "Deploy api v2.4.2 (updated)"
}' | jq
```

### Toggle star

```bash
pcr -X POST http://localhost:8080/api/v1/events/EVENT_ID/star | jq
```

Run again to un-star.

### Set or clear an alert

```bash
# Set alert
pcr -X PUT http://localhost:8080/api/v1/events/EVENT_ID -d '{"alerted": true}' | jq

# Clear alert
pcr -X PUT http://localhost:8080/api/v1/events/EVENT_ID -d '{"alerted": false}' | jq
```

### Delete an event

```bash
pcr -X DELETE http://localhost:8080/api/v1/events/EVENT_ID | jq
```

---

## 4. Testing the dashboard

1. Open your browser to:

   ```
   http://localhost:8080/?token=test-token
   ```

2. **Time range buttons** -- Click "Last 5 min", "Last 30 min", "Last 1 hr",
   etc. Verify the event list updates accordingly.

3. **Tag filtering** -- Click any tag badge (e.g. `env=prod`) on an event row.
   The event list should filter to show only events with that tag.

4. **Star toggle** -- Click the star icon on an event row. The star should
   toggle on and off. Reload the page to confirm the change persisted.

5. **Alert highlighting** -- Create an event and set `alerted: true` (see
   section 2 above). The row for that event should have a light red background.

6. **Event detail page** -- Click the timestamp of an event to navigate to its
   detail page. Verify the full event data (including `long_description`) is
   shown.

7. **Back to dashboard** -- On the detail page, click the "Back to dashboard"
   link. Verify you are returned to the dashboard and the token is preserved
   in the URL (you should not be prompted to re-authenticate).

---

## 5. Testing auth enforcement

### Request without a token returns 401

```bash
curl -s http://localhost:8080/api/v1/events | jq
```

Expected: HTTP 401 Unauthorized.

### Health endpoint works without auth

```bash
curl -s http://localhost:8080/api/v1/health | jq
```

Expected: HTTP 200 with a health response body.

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

---

## 6. Running automated tests

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
