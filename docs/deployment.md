# Deployment Guide

Three container deployment methods, in order of simplicity. For direct binary setup during development, see the manual setup in [testing.md](testing.md).

The checked-in files are generic examples. Keep real hosts, identities, network
selectors, and secrets in a private deployment repository or secret manager;
see [private configuration](private-configuration.md).

## Human identity provider

Every installation selects exactly one dashboard provider. GitHub, Google, and direct Authentik modes require an OAuth web application whose callback is the canonical public origin plus `/auth/callback`; Beyond mode uses trusted proxy headers instead.

GitHub company restriction:

```text
PCR_HUMAN_AUTH_PROVIDER=github
PCR_ALLOWED_ORGS=example-inc,example-subsidiary
```

PCR requests `read:user` and `read:org`, fetches the authenticated profile, and accepts only active membership in one configured organization. Organizations enforcing SAML SSO or OAuth-application restrictions may require an administrator to approve the OAuth application or authorize it for the organization.

Google company restriction:

```text
PCR_HUMAN_AUTH_PROVIDER=google
PCR_ALLOWED_ORGS=example.com,subsidiary.example.com
```

Create an OAuth web client on the Google consent screen. PCR validates the ID-token signature, issuer, audience, expiration, nonce, verified email, and exact signed `hd` Workspace-domain claim. The account chooser's `hd` parameter is only a hint. Consumer accounts without a Workspace hosted domain and suffix lookalikes are denied.

Authentik group restriction:

```text
PCR_HUMAN_AUTH_PROVIDER=authentik
PCR_OIDC_ISSUER_URL=https://auth.example.com/application/o/pcr/
PCR_ALLOWED_ORGS=Platform Operators,Production Readers
```

Create an Authentik OAuth2/OpenID provider and application using a confidential client, authorization-code flow, and the callback `<PCR_PUBLIC_URL>/auth/callback`. Use Authentik's [recommended per-provider issuer mode](https://docs.goauthentik.io/add-secure-apps/providers/oauth2/); this PCR integration does not support global issuer mode because Authentik continues to publish discovery beneath the application slug. Attach scope mappings for `openid`, `email`, `profile`, and `groups`, and ensure the resulting signed ID token contains `sub`, `nonce`, `preferred_username` (or a verified email), and a string-array `groups` claim. Group matching is exact and case-sensitive. Authentik application access policies can restrict entry before PCR applies its own configured group or subject policy. An individual exception uses the issuer-scoped form `authentik:<issuer>:<sub>`.

Beyond trusted-proxy restriction:

```text
PCR_HUMAN_AUTH_PROVIDER=beyond
PCR_ALLOWED_ORGS=Platform Operators,Production Readers
```

This follows the same pattern as an application using Grafana `auth.proxy`.
Beyond authenticates the browser and injects `X-Beyond-Email`,
`X-Beyond-Name`, and `X-Beyond-Groups`; PCR rechecks the configured groups and
binds its local CSRF session to the verified email on every request. PCR OAuth
client credentials and `/auth/callback` are unused in this mode.

Add PCR to Beyond with `credential_auth`. Beyond validates the minted
`<email>:<app-password>` against Authentik on every API request, applies the
same group gate as browser SSO, strips the live credential, and injects the
verified identity PCR uses for authorization and event attribution:

```yaml
applications:
  pcr:
    upstream: http://pcr-server.pcr.svc.cluster.local:8080
    host: changes.example.com
    allowed_groups: [example-group]
    credential_auth:
      token_url: https://auth.example.com/application/o/token/
      client_id: existing-public-m2m-client-id
```

External API clients then send `Authorization: Bearer <email>:<app-password>`
or `x-api-key: <email>:<app-password>`. Beyond strips caller-supplied
`X-Beyond-*` values before injecting the verified identity, and the credential
never reaches PCR. The mint purpose is bookkeeping only and does not restrict
which allowed Beyond service can validate that app password.

Requests without a credential still use Beyond's normal browser SSO path, so
this does not replace or disable PCR's dashboard login. A Beyond deployment
may omit `PCR_API_TOKENS`; retain it temporarily only when migrating existing
clients that still call PCR directly or use the old `Token` passthrough route.

Header trust requires network isolation. Do not expose PCR through another
Ingress, LoadBalancer, NodePort, or generally reachable ClusterIP path. Apply a
NetworkPolicy permitting the application port from Beyond's namespace/pods and
only explicitly required probes or administrative sources. A forged-header
request that can reach PCR directly has the same authority as Beyond.

The checked-in standalone manifests provide this boundary in
`k8s/networkpolicy-beyond.yaml`. Apply it whenever
`PCR_HUMAN_AUTH_PROVIDER=beyond`; do not apply that provider-specific policy to
a direct GitHub, Google, or Authentik deployment. Its selectors match the
standalone `app: pcr-server` pods and example Beyond pods labeled
`app.kubernetes.io/name: beyond` in namespace `beyond`. Adapt both selectors in
private deployment configuration to match the actual workloads. The Deployment
uses loopback exec probes so
kubelet health checks do not require another NetworkPolicy ingress exception.

Beyond's current identity contract uses verified email rather than OIDC `sub`.
PCR therefore stores `user_provider=beyond` and the lowercased email as
`user_subject`. Display-name changes preserve identity; an email change creates
a new identity and does not rewrite historical events. Logout clears PCR's
local session but does not terminate the user's Beyond or Authentik SSO session.

Multiple values have OR semantics. `PCR_HUMAN_AUTH_ALLOWED_SUBJECTS` can add stable individual exceptions; leave it empty for strictly company-only access. `PCR_HUMAN_AUTH_ALLOW_ANY=true` is intended only when company restriction is deliberately disabled and cannot be combined with either restriction.

OAuth-provider identity and membership are checked when a PCR session is
established, so normal dashboard requests make no provider calls. The check is
refreshed after the absolute `PCR_HUMAN_SESSION_DURATION` window. Beyond mode
instead rechecks the proxy identity and groups on every protected request.
Logout validates only the signed local session and CSRF token so a user whose
group was revoked can still clear the unusable cookie; it does not make a
provider call or terminate the upstream SSO session.

## API and CLI authentication

PCR keeps browser and API authority separate:

| Client path | Credential and identity behavior |
|---|---|
| Dashboard | One configured GitHub, Google, Authentik, or Beyond identity creates a signed PCR session cookie. The cookie is valid only on dashboard routes. |
| Direct API | A value from `PCR_API_TOKENS` is accepted as `Authorization: Bearer`, proxy-friendly `Authorization: Token`, or the backwards-compatible `?token=` query parameter. These opaque tokens do not identify a person: create, link, and close bodies supply `user_name`, while bodyless star/alert toggles use the generic actor `api`. |
| Beyond-fronted API | Beyond validates an Authentik `<email>:<app-password>`, strips it, and supplies trusted identity headers. PCR rechecks policy and ignores a caller-supplied `user_name`. |
| `pcr` CLI | Uses only the Beyond composite-credential path. It has no legacy-token, credential, header, or user-name flag. |

With the default `PCR_REQUIRE_AUTH_READS=true`, API reads and writes require one
of the API paths above. Setting it to `false` makes only API `GET` and `HEAD`
requests public; writes and every dashboard route remain authenticated. Avoid
the query-parameter token for new integrations because URLs are routinely
logged.

Direct GitHub, Google, and Authentik deployments still need
`PCR_API_TOKENS`; those providers authenticate dashboard users, not API
clients. Beyond deployments can omit legacy tokens. When using the checked-in
Kubernetes Deployment, either retain an `api-tokens` key in the Secret (it may
be empty in Beyond mode) or remove the corresponding `secretKeyRef` from the
manifest.

### Install and configure the CLI

Build artifacts on an operator workstation or CI builder; the server container
contains only `pcr-server`:

```bash
make build
install -d "$HOME/.local/bin"
install -m 0755 bin/pcr "$HOME/.local/bin/pcr"
pcr version
```

Interactive users can create a versioned TOML file and enter the credential at
a hidden prompt:

```bash
pcr --url https://changes.example.com config init
pcr config set-credential
pcr config show
pcr --output=table config path
pcr doctor
```

The file contains `version`, `url`, and optionally `credential`. Credential
files are written atomically with mode `0600` on POSIX systems, and PCR refuses
to use one readable by group or other users. The target must be an HTTPS
origin; loopback HTTP requires `--allow-http`.

CI should inject `PCR_CREDENTIAL` from a masked secret and set
`PCR_URL`. Never put the composite in a command flag:

```bash
test -n "$PCR_CREDENTIAL"
test -n "$PCR_URL"
pcr doctor
pcr events list --limit 1
pcr events create \
  --external-id "${BUILD_SYSTEM}-${BUILD_ID}" \
  --type deployment \
  --description "deployed ${SERVICE_NAME}" \
  --tag env=prod
```

`PCR_CREDENTIAL` overrides the file credential. URL precedence is `--url`,
`PCR_URL`, then file, with no built-in destination; config-path precedence is
`--config`, `PCR_CONFIG`, then the platform default. The CLI defaults to JSON,
also supports JSON Lines and tables, refuses redirects, bounds response bodies,
and does not print credential material. Revoke the user-bound app password when
the automation is retired; its mint purpose label is bookkeeping rather than a
PCR-only scope.

## 1. Docker (single container)

### Prerequisites
- Docker
- Colima, Docker Desktop, or another Docker daemon
- A reachable PostgreSQL database and connection URL
- A GitHub OAuth application, Google OAuth web client, or Authentik OAuth2/OpenID application with callback `<public-url>/auth/callback`; alternatively, an existing Beyond deployment and an isolated upstream network path

### Build the image

```bash
make docker-build
# or: docker build -t pcr-server .
```

### Run the container

```bash
export PCR_SESSION_SECRET="$(openssl rand -base64 48)"
export PCR_DATABASE_URL="postgres://pcr:password@postgres.example.internal:5432/pcr?sslmode=require"
export PCR_OAUTH_CLIENT_ID="your-github-client-id"
export PCR_OAUTH_CLIENT_SECRET="your-github-client-secret"
docker run -d --name pcr-server \
  -p 8080:8080 \
  -e PCR_API_TOKENS=my-secret-token \
  -e PCR_SESSION_SECRET \
  -e PCR_DATABASE_URL \
  -e PCR_HUMAN_AUTH_PROVIDER=github \
  -e PCR_PUBLIC_URL=http://localhost:8080 \
  -e PCR_OAUTH_CLIENT_ID \
  -e PCR_OAUTH_CLIENT_SECRET \
  -e PCR_HUMAN_AUTH_ALLOW_ANY=true \
  -e PCR_COOKIE_SECURE=false \
  pcr-server
```

The server applies PostgreSQL migrations on startup by default. `PCR_COOKIE_SECURE=false` is appropriate only for this local HTTP example; deployments served over HTTPS should keep the default `true`.

### Sanity check

```bash
# Health checks (no auth required)
curl -s http://localhost:8080/livez | jq
curl -s http://localhost:8080/readyz | jq
# Expected: {"status":"ok"}

# Create an event
curl -s -X POST -H "Authorization: Bearer my-secret-token" \
  -H "Content-Type: application/json" \
  http://localhost:8080/api/v1/events -d '{
  "user_name": "alice",
  "event_type": "deployment",
  "description": "docker sanity check",
  "tags": {"env": "prod"}
}' | jq

# List events
curl -s -H "Authorization: Bearer my-secret-token" \
  "http://localhost:8080/api/v1/events?top_level=true" | jq '.total_count'

# Open dashboard in browser
# Open the login form and use the configured provider
open "http://localhost:8080/login"
```

### Tear down

```bash
docker stop pcr-server && docker rm pcr-server
```

## 2. Docker Compose

### Prerequisites
- Docker with Compose plugin (`docker compose version`)

### Start

Compose starts PostgreSQL and persists it in the `postgres-data` volume.

```bash
PCR_API_TOKENS=my-secret-token \
PCR_OAUTH_CLIENT_ID=your-github-client-id \
PCR_OAUTH_CLIENT_SECRET=your-github-client-secret \
docker compose up -d --build
```

Or set the env var in a `.env` file (not committed to git):
```
PCR_API_TOKENS=my-secret-token
PCR_SESSION_SECRET=replace-with-32-byte-secret-from-openssl-rand
PCR_OAUTH_CLIENT_ID=your-github-client-id
PCR_OAUTH_CLIENT_SECRET=your-github-client-secret
```

Then just:
```bash
docker compose up -d --build
```

### Sanity check

Same as Docker above -- the service is available at `http://localhost:8080`.

```bash
# Quick end-to-end check
curl -s http://localhost:8080/readyz | jq
curl -s -X POST -H "Authorization: Bearer my-secret-token" \
  -H "Content-Type: application/json" \
  http://localhost:8080/api/v1/events -d '{
  "user_name": "bob",
  "event_type": "feature-flag",
  "description": "compose sanity check"
}' | jq '{id, description}'
# Open the login form and use the configured provider
open "http://localhost:8080/login"
```

### View logs

```bash
docker compose logs -f
```

### Tear down

```bash
docker compose down
# To also remove the data volume:
docker compose down -v
```

## 3. Kubernetes (kind)

### Prerequisites
- Docker (running)
- kind (`brew install kind`)
- kubectl (`brew install kubectl`)

### Create a cluster

```bash
kind create cluster --name pcr
```

### Build and load the image

kind runs its own containerd runtime, so images must be loaded explicitly:

```bash
docker build -t pcr-server:latest .
kind load docker-image pcr-server:latest --name pcr
```

### Prepare private manifests

Copy the example manifests outside the checkout before filling in real values:

```bash
install -d -m 0700 "$HOME/.config/pcr/manifests"
cp -n k8s/*.yaml "$HOME/.config/pcr/manifests/"
chmod 0600 "$HOME/.config/pcr/manifests/secret.yaml"
```

For shared environments, use a private deployment repository and your secret
manager instead. Edit the private copy of `secret.yaml`, not the tracked example:

```yaml
stringData:
  api-tokens: "your-actual-token"
  session-secret: "a-random-secret-of-at-least-32-bytes"
  database-url: "postgres://pcr:password@postgres.example.internal:5432/pcr?sslmode=require"
  oauth-client-id: "your-provider-client-id"
  oauth-client-secret: "your-provider-client-secret"
```

Edit the private copy of `configmap.yaml` as well: select `github`, `google`, `authentik`, or `beyond`; set `PCR_PUBLIC_URL` to the HTTPS origin; and set `PCR_ALLOWED_ORGS` to GitHub organizations, Google Workspace domains, or exact Authentik/Beyond groups. Authentik also requires `PCR_OIDC_ISSUER_URL`; Beyond requires the documented proxy-only network path. The checked-in placeholders are not deployment credentials, and the placeholder session secret is too short for the server to accept. Replace all values before applying the private manifests. The PostgreSQL host must be reachable from the `pcr` namespace.

### Apply manifests

```bash
kubectl apply -f "$HOME/.config/pcr/manifests/namespace.yaml"
kubectl apply -f "$HOME/.config/pcr/manifests/secret.yaml"
kubectl apply -f "$HOME/.config/pcr/manifests/configmap.yaml"
kubectl apply -f "$HOME/.config/pcr/manifests/deployment.yaml"
kubectl apply -f "$HOME/.config/pcr/manifests/service.yaml"

# Required only when PCR_HUMAN_AUTH_PROVIDER=beyond:
kubectl apply -f "$HOME/.config/pcr/manifests/networkpolicy-beyond.yaml"
```

Do not expose a Beyond-mode deployment until the NetworkPolicy is applied and
verified. A CNI that does not enforce Kubernetes NetworkPolicy cannot safely
host this trusted-header mode without an equivalent network control.

### Verify the pod is running

```bash
kubectl -n pcr get pods
# Wait for both replicas to report STATUS: Running and READY: 1/1

kubectl -n pcr logs deploy/pcr-server
# Should show: "starting server addr=:8080"
```

### Port-forward for local access

```bash
kubectl -n pcr port-forward svc/pcr-server 8080:8080
```

### Sanity check

With port-forward running (in another terminal or backgrounded):

```bash
# Health
curl -s http://localhost:8080/livez | jq
curl -s http://localhost:8080/readyz | jq

# Create event
curl -s -X POST -H "Authorization: Bearer your-actual-token" \
  -H "Content-Type: application/json" \
  http://localhost:8080/api/v1/events -d '{
  "user_name": "charlie",
  "event_type": "k8s-change",
  "description": "kind sanity check",
  "tags": {"cluster": "local", "env": "dev"}
}' | jq '{id, description}'

# List
curl -s -H "Authorization: Bearer your-actual-token" \
  "http://localhost:8080/api/v1/events?top_level=true" | jq '.total_count'

# For port-forward-only development, register http://localhost:8080/auth/callback,
# set PCR_PUBLIC_URL accordingly, and set PCR_COOKIE_SECURE=false before applying.
open "http://localhost:8080/login"
```

### Tear down

```bash
kind delete cluster --name pcr
```

## Environment Variables Reference

All methods use the same environment variables. Key settings:

| Variable | Required | Default | Description |
|---|---|---|---|
| `PCR_API_TOKENS` | Except Beyond | -- | Comma-separated legacy API tokens; optional when Beyond authenticates API clients |
| `PCR_HUMAN_AUTH_PROVIDER` | Yes | -- | Exactly one of `github`, `google`, `authentik`, or `beyond` |
| `PCR_PUBLIC_URL` | Yes | -- | Canonical external origin; OAuth provider callback is `<value>/auth/callback` |
| `PCR_OAUTH_CLIENT_ID` | Except Beyond | -- | Selected provider's OAuth client ID |
| `PCR_OAUTH_CLIENT_SECRET` | Except Beyond | -- | Selected provider's OAuth client secret |
| `PCR_OIDC_ISSUER_URL` | Authentik only | -- | Recommended Authentik per-provider issuer ending in `/application/o/<slug>/` |
| `PCR_ALLOWED_ORGS` | Conditional | -- | GitHub organizations, Google Workspace domains, or exact Authentik/Beyond group names |
| `PCR_HUMAN_AUTH_ALLOWED_SUBJECTS` | Conditional | -- | Stable provider subjects allowed as individual exceptions |
| `PCR_HUMAN_AUTH_ALLOW_ANY` | No | `false` | Allow any provider identity; mutually exclusive with restrictions |
| `PCR_HUMAN_SESSION_DURATION` | No | `12h` | Absolute provider-policy freshness and session window |
| `PCR_SESSION_SECRET` | No | (random 32-byte) | HMAC key for session cookies. Must be at least 32 bytes when set; generate via `openssl rand -base64 48`. Set for persistent sessions across restarts. |
| `PCR_DATABASE_URL` | Yes | -- | PostgreSQL connection URL; require TLS outside local development |
| `PCR_AUTO_MIGRATE` | No | `true` | Run schema migrations on startup |
| `PCR_ADDR` | No | `:8080` | Listen address |
| `PCR_REQUIRE_AUTH_READS` | No | `true` | Require auth for read endpoints |
| `PCR_DASHBOARD_REFRESH_SEC` | No | `60` | Dashboard auto-refresh interval in seconds |
| `PCR_READ_TIMEOUT` | No | `5s` | HTTP server read timeout (Go duration) |
| `PCR_WRITE_TIMEOUT` | No | `10s` | HTTP server write timeout (Go duration) |
| `PCR_SHUTDOWN_TIMEOUT` | No | `15s` | Graceful shutdown timeout (Go duration) |
| `PCR_DB_CONNECT_TIMEOUT` | No | `5s` | PostgreSQL startup connection timeout |
| `PCR_DB_MAX_CONNECTIONS` | No | `10` | Maximum PostgreSQL connections per replica |
| `PCR_DB_SLOW_QUERY_THRESHOLD` | No | `100ms` | Log a warning when a store operation exceeds this |
| `PCR_COOKIE_SECURE` | No | `true` | Set the `Secure` flag on session cookies. Set to `false` for local dev without TLS |

See the README for the full configuration reference.

## Notes

- **PostgreSQL:** Use a managed PostgreSQL service with TLS, automated backups, and point-in-time recovery for production.
- **Connection budget:** Ensure replica count multiplied by `PCR_DB_MAX_CONNECTIONS` remains below the database connection limit.
- **Migrations:** Startup migrations use a PostgreSQL advisory lock, so concurrent replicas serialize schema changes.
- **Image size:** The production image is based on Alpine 3.21 with a statically-linked Go binary.
- **Rollouts:** The Kubernetes Deployment uses rolling updates with two replicas.
- **Health checks:** Kubernetes uses dependency-free `/livez` for liveness and PostgreSQL-backed `/readyz` for startup and readiness.
- **Pod security:** The Deployment runs as a fixed non-root UID with a read-only root filesystem, no Linux capabilities, no privilege escalation, the runtime-default seccomp profile, and no mounted service-account token.
