# Testing Guide

The repository has unit and fuzz tests for the server and CLI,
real-PostgreSQL integration tests, and an HTTP smoke-test client. The commands
below reflect the current append-only API and separated browser/API auth model.

## Automated tests

Run the default test suite with the race detector and package coverage:

```bash
make test
```

Set a PostgreSQL test URL. Integration tests create and remove isolated schemas in this database:

```bash
export PCR_TEST_POSTGRES_URL='postgres://pcr@127.0.0.1/pcr_test?sslmode=disable'
go test -race -tags=integration ./...
```

Run the end-to-end smoke suite against a local server using an isolated PostgreSQL schema:

```bash
export PCR_DATABASE_URL='postgres://pcr@127.0.0.1/pcr_test?sslmode=disable'
make smoke
```

Run the seeded real-PostgreSQL dashboard functional test:

```bash
PCR_TEST_POSTGRES_URL="$PCR_TEST_POSTGRES_URL" go test -tags=integration ./internal/handler -run TestSeededDashboardViews
```

It loads `testdata/functional/phosphor-demo.json` and exercises Current, Site-wide, History, Alerts, lifecycle reduction, link annotations, alert toggling, operation closure, safely escaped link labels, the activity trail, the severity banner, and locally embedded font delivery through the real router.

Run the form-to-PostgreSQL injection regression independently:

```bash
PCR_TEST_POSTGRES_URL="$PCR_TEST_POSTGRES_URL" go test -race -tags=integration ./internal/handler -run TestRecordChangeFormTreatsSQLAsData
```

This submits SQL-looking values as an authenticated, CSRF-protected browser form through the real router and PostgreSQL store, reads the unchanged values back through the API, and performs a second form write to prove the database remains operational. Client-side validation is intentionally bypassed so the test exercises the authoritative server boundary.

Check the MCP `list_deployments` tool against real PostgreSQL:

```bash
PCR_TEST_POSTGRES_URL="$PCR_TEST_POSTGRES_URL" go test -race -tags=integration ./internal/handler -run TestMCPListDeploymentsMatchesRESTClientMapping
```

It seeds paginated, filtered, truncated, and out-of-window events and requires the tool's text to match, byte for byte, what a REST client of `GET /api/v1/events` builds for the same window. The default suite covers the tool contract, argument validation, and `/mcp` authentication; `make smoke` exercises the endpoint over HTTP.

Fuzz the form, configuration, URL, path, and CLI parsing boundaries. Go runs
one named fuzz target per invocation:

```bash
go test -run='^$' -fuzz=FuzzParseLinkForm -fuzztime=10s ./internal/handler
go test -run='^$' -fuzz=FuzzValidateLinks -fuzztime=10s ./internal/service
go test -run='^$' -fuzz=FuzzBootstrapPath -fuzztime=10s ./internal/pcrconfig
go test -run='^$' -fuzz=FuzzParseOrigin -fuzztime=10s ./internal/pcrconfig
go test -run='^$' -fuzz=FuzzValidateCredential -fuzztime=10s ./internal/pcrconfig
go test -run='^$' -fuzz=FuzzLoad -fuzztime=10s ./internal/pcrconfig
go test -run='^$' -fuzz=FuzzEndpointPathSegment -fuzztime=10s ./internal/pcrclient
go test -run='^$' -fuzz=FuzzSanitizeDiagnostic -fuzztime=10s ./internal/pcrcli
go test -run='^$' -fuzz=FuzzCLIParser -fuzztime=10s ./internal/pcrcli
```

To test an already-running server, including the Docker Compose service:

```bash
make smoke-docker
```

Override `SMOKE_DOCKER_URL` and `SMOKE_DOCKER_TOKEN` when the target does not use the Makefile defaults.

## Manual setup

For local HTTP testing, disable secure-only cookies:

```bash
make build
export PCR_API_TOKENS=test-token
export PCR_DATABASE_URL='postgres://pcr@127.0.0.1/pcr?sslmode=disable'
export PCR_COOKIE_SECURE=false
export PCR_HUMAN_AUTH_PROVIDER=github
export PCR_PUBLIC_URL=http://localhost:8080
export PCR_OAUTH_CLIENT_ID=your-local-github-client-id
export PCR_OAUTH_CLIENT_SECRET=your-local-github-client-secret
export PCR_HUMAN_AUTH_ALLOW_ANY=true
./bin/pcr-server
```

For an Authentik acceptance run, replace the provider with `authentik`, set `PCR_OIDC_ISSUER_URL` to the per-provider issuer ending in `/application/o/<slug>/`, and register `http://localhost:8080/auth/callback` as an exact redirect URI. In unrestricted mode, verify the resolved signed username first. Then set `PCR_ALLOWED_ORGS` to an exact Authentik group and disable unrestricted mode to verify the signed `groups` claim.

## Beyond acceptance before deployment

Use four stages so header handling, browser behavior, edge routing, and
Kubernetes isolation fail independently.

### 1. Automated contract checks

Run the focused tests first, then the complete suite:

```bash
go test ./internal/humanauth ./internal/middleware ./internal/handler ./internal/config ./internal/router
go test ./...
```

The focused checks cover exact group matching, missing/malformed email,
case-normalized email identity, local-session binding to the current Beyond
identity, profile refresh, `Token` API credentials, and the Beyond login
bootstrap. These commands are verification instructions; they are not run as
part of documentation generation.

### 2. Local PCR header harness

Run PCR on loopback with a generic test group and no OAuth credentials:

```bash
export PCR_HUMAN_AUTH_PROVIDER=beyond
export PCR_PUBLIC_URL=http://127.0.0.1:18082
export PCR_COOKIE_SECURE=false
export PCR_ALLOWED_ORGS=engineering
export PCR_HUMAN_AUTH_ALLOW_ANY=false
```

Use `curl` with a cookie jar to call `/auth/start` with
`X-Beyond-Email`, `X-Beyond-Name`, and `X-Beyond-Groups`, then request `/`
with the same headers and cookie. Confirm that a missing email, a wrong-case
group, or a different email with the old PCR cookie is rejected. This stage
checks PCR behavior only; loopback headers are deliberately synthetic and do
not establish the production trust boundary.

Exercise the proxy-facing API scheme directly as well:

```bash
curl -sS -H "Authorization: Token $PCR_TOKEN" http://127.0.0.1:18082/api/v1/events
```

### 3. Local Beyond browser acceptance

Use Beyond's existing development Authentik stack and test account. Create a
temporary Beyond configuration outside either repository with a PCR
application entry, an exact generic `allowed_groups` value, and
`passthrough_auth_schemes: [Token]`. Route it to the locally running seeded PCR
instance. Do not add company group names to a fixture.

Verify through the Beyond hostname:

1. An unauthenticated browser is sent through Beyond's Authentik login.
2. PCR shows `Continue with Company SSO`, establishes its local session, and
   displays the verified email/name.
3. The user can open an existing incident and add a link; the activity entry
   stores `user_provider=beyond` and the normalized verified email.
4. PCR logout clears the PCR session. The Beyond/Authentik SSO session remains,
   so continuing again may not prompt for a password.
5. A sessionless API call using `Authorization: Token` reaches PCR and works;
   the same request with a bad token is rejected by PCR.
6. A caller-supplied `X-Beyond-Email` on the Token passthrough path is stripped
   by Beyond and cannot create dashboard authority.

### 4. Disposable Kubernetes pre-production

Before the production hostname is enabled, deploy the same image and manifests
to a disposable namespace behind Beyond with non-production data. Apply the
intended NetworkPolicy and verify both sides of the boundary:

- Beyond pods can reach PCR's application port.
- A pod in an unrelated namespace cannot connect directly to that port.
- Health probes remain successful under the selected network plugin.
- Browser login, name display, link creation, logout, and structured auth/audit
  logs match the local acceptance run.
- Removing the test user from the allowed group causes Beyond or PCR to deny
  subsequent requests; restoring it restores access.
- Reusing a PCR cookie while Beyond asserts a different email is rejected.
- Two PCR replicas accept the same signed session without sticky routing.
- `Authorization: Token` works through Beyond while `Bearer` is not relied on
  for opaque PCR tokens at the edge.

Promote only after the negative network test and the mismatched-identity test
both fail closed. Record the image digest, Beyond configuration revision,
NetworkPolicy, and observed audit-log entries used for acceptance.

## CLI acceptance through Beyond

The `pcr` CLI authenticates only through Beyond's Authentik
`<email>:<app-password>` credential path. It intentionally does not accept a
legacy PCR token or a credential flag. Build it, inject a temporary credential
without placing it in shell history, and exercise every read path:

```bash
make build
export PCR_URL=https://changes.example.com
printf 'Beyond credential (email:app-password): ' >&2
IFS= read -r -s PCR_CREDENTIAL
printf '\n' >&2
export PCR_CREDENTIAL

./bin/pcr version
./bin/pcr config show
./bin/pcr doctor
./bin/pcr events list --limit 1
./bin/pcr current --limit 1
```

Create one event with a unique stable external ID, retry the identical command,
and verify both responses contain the same event ID:

```bash
EXTERNAL_ID="cli-acceptance-$(date -u +%Y%m%dT%H%M%SZ)"
FIRST_ID=$(./bin/pcr events create \
  --external-id "$EXTERNAL_ID" \
  --type test \
  --description "PCR CLI acceptance test" \
  --tag purpose=cli-acceptance | jq -r .id)
SECOND_ID=$(./bin/pcr events create \
  --external-id "$EXTERNAL_ID" \
  --type test \
  --description "PCR CLI acceptance test" \
  --tag purpose=cli-acceptance | jq -r .id)
test "$FIRST_ID" = "$SECOND_ID"

./bin/pcr events get "$FIRST_ID"
./bin/pcr events annotations "$FIRST_ID"
./bin/pcr events activity "$FIRST_ID"
unset PCR_CREDENTIAL
```

The event is append-only and remains in PCR. Use a clearly test-specific type,
description, and tags; revoke the temporary app password after acceptance.
Inspect stdout, stderr, process listings, and CI logs to confirm the credential
does not appear.

## Direct API checks

These remaining examples exercise direct legacy-token authentication and API
mutations that the retry-safe CLI intentionally does not expose. In another
shell:

```bash
export PCR_TOKEN=test-token
alias pcr-api='curl -sS -H "Authorization: Bearer $PCR_TOKEN" -H "Content-Type: application/json"'
```

The unauthenticated liveness and readiness checks should return `{"status":"ok"}`:

```bash
curl -sS http://localhost:8080/livez | jq
curl -sS http://localhost:8080/readyz | jq
```

## Verify current alert state

Create a top-level incident event and retain its ID:

```bash
EVENT_ID=$(pcr-api -X POST http://localhost:8080/api/v1/events -d '{
  "user_name": "on-call",
  "event_type": "incident",
  "description": "Investigating elevated error rate",
  "tags": {"severity": "SEV0", "service": "api"}
}' | jq -r .id)
```

Open its alert state by appending an `alert` meta-event:

```bash
pcr-api -X POST http://localhost:8080/api/v1/events -d "{
  \"parent_id\": \"$EVENT_ID\",
  \"user_name\": \"on-call\",
  \"event_type\": \"alert\",
  \"description\": \"Incident response underway\"
}" | jq
```

The derived state should now be alerted:

```bash
pcr-api "http://localhost:8080/api/v1/events/$EVENT_ID/annotations" | jq
# {"starred":false,"alerted":true}

pcr-api "http://localhost:8080/api/v1/events?alerted=true&top_level=true" \
  | jq --arg id "$EVENT_ID" '.events[] | select(.id == $id)'
```

Close the state by appending a `clear-alert` meta-event:

```bash
pcr-api -X POST http://localhost:8080/api/v1/events -d "{
  \"parent_id\": \"$EVENT_ID\",
  \"user_name\": \"on-call\",
  \"event_type\": \"clear-alert\",
  \"description\": \"Incident resolved\"
}" | jq

pcr-api "http://localhost:8080/api/v1/events/$EVENT_ID/annotations" | jq
# {"starred":false,"alerted":false}
```

Current annotation state follows meta-event creation order, not a caller-supplied event timestamp. The newest `alert` or `clear-alert` transition wins; star state is reduced independently from `star` and `unstar` transitions.

## Verify current production changes

Create a deployment start. This example uses the legacy `deploy_id`; new integrations should prefer `change_id`:

```bash
pcr-api -X POST http://localhost:8080/api/v1/events -d '{
  "external_id": "manual-deploy-d-001-start",
  "user_name": "alice",
  "event_type": "deployment",
  "description": "Deploy api v2.4.1 started",
  "tags": {"deploy_id": "d-001", "phase": "start", "team": "payments", "scope": "service", "severity": "sev1", "env": "prod"}
}' | jq
```

It appears in Current without a time bound:

```bash
pcr-api "http://localhost:8080/api/v1/current?for_team=payments" \
  | jq '.events[] | select(.tags.deploy_id == "d-001")'
```

Append the matching end event:

```bash
pcr-api -X POST http://localhost:8080/api/v1/events -d '{
  "external_id": "manual-deploy-d-001-end",
  "user_name": "alice",
  "event_type": "deployment",
  "description": "Deploy api v2.4.1 completed",
  "tags": {"deploy_id": "d-001", "phase": "end", "env": "prod"}
}' | jq

pcr-api "http://localhost:8080/api/v1/events?tag=deploy_id:d-001" \
  | jq '.events[] | {timestamp, description, tags}'
```

History still contains both immutable rows, while Current no longer includes `d-001`:

```bash
pcr-api "http://localhost:8080/api/v1/current?for_team=payments" \
  | jq '.events[] | select(.tags.deploy_id == "d-001")'
# no output
```

Use a new logical identifier for a restarted operation. A retry of the same phase should reuse its phase-specific `external_id`. Exact lowercase phase values participate; display tag values should also be lowercase, although severity reads accept existing uppercase values such as `SEV0`.

## Dashboard checks

Register `http://localhost:8080/auth/callback`, open `http://localhost:8080/login`, sign in with the configured provider, and verify:

1. The default view contains top-level events timestamped within the last 24 hours.
2. Current shows the selected team's active work, unattributed active work, and active site-wide work regardless of age.
3. Site-wide shows only active `scope=site` work, and site rows are visually distinct.
4. Active `sev0` and `sev1` work appears in the banner by default, independently of table pagination; scope, severity, and event-type selections narrow both the active table and banner.
5. Time-range, event-type, user, and tag filters change History rows.
6. The star button changes the event's current star annotation.
7. An actively alerted event has alert styling and appears in the Alerts view if its parent event is within the selected time range.
8. The detail page displays lifecycle and annotation state, aggregated links, and an oldest-first activity trail of child annotations and correlated closure events.
9. The page refreshes at the configured `PCR_DASHBOARD_REFRESH_SEC` interval.
10. Item timestamps in tables, the severity banner, event details, and activity use the browser's local timezone, with a timezone label and 24-hour time (00–23, without AM/PM). Time comes first; the date uses the browser's locale and is omitted for events on today's local calendar date and follows the time otherwise. Dates update within a minute of local midnight, or when returning to the tab. Hovering a timestamp shows its UTC value; with JavaScript disabled, UTC remains visible. Check dates near midnight and daylight-saving transitions using the browser's timezone override.

## Idempotency

`external_id` is globally unique when present. The first request returns `201 Created`; a retry with the same value returns the original event with `200 OK`, even if other request fields differ. Events without `external_id` are always newly created.

## Authentication checks

With the default `PCR_REQUIRE_AUTH_READS=true`:

- `/livez`, `/readyz`, `/api/v1/health`, `/login`, `/auth/start`, `/auth/callback`, and `/static/*` are outside PCR's session middleware. In Beyond mode, `/auth/start` still requires identity headers from the trusted edge.
- API reads and writes require a legacy `Bearer`/`Token` credential, the backwards-compatible API `token` query parameter, or a trusted identity established by Beyond. Dashboard routes require a provider-established human session.
- Setting `PCR_REQUIRE_AUTH_READS=false` makes API `GET` and `HEAD` requests public; dashboard routes remain human-authenticated and API writes still require authentication.
- Dashboard session cookies are not API credentials. The `pcr` CLI uses the Beyond credential path and does not accept legacy opaque tokens.
- Avoid the `token` query parameter in new clients because URLs commonly appear in logs; use an Authorization header instead.
