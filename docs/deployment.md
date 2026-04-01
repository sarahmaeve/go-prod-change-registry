# Deployment Guide

Three deployment methods, in order of simplicity.

## 1. Docker (single container)

### Prerequisites
- Docker (tested with 29.x)
- Colima, Docker Desktop, or another Docker daemon

### Build the image

```bash
make docker-build
# or: docker build -t pcr-server .
```

### Run the container

```bash
docker run -d --name pcr-server \
  -p 8080:8080 \
  -e PCR_API_TOKENS=my-secret-token \
  -e PCR_SESSION_SECRET=my-session-key \
  -v pcr-data:/data \
  pcr-server
```

The `-v pcr-data:/data` creates a named volume so the SQLite database persists across container restarts.

### Sanity check

```bash
# Health check (no auth required)
curl -s http://localhost:8080/api/v1/health | jq
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
open "http://localhost:8080/login?token=my-secret-token"
```

### Tear down

```bash
docker stop pcr-server && docker rm pcr-server
# To also remove the data volume:
docker volume rm pcr-data
```

## 2. Docker Compose

### Prerequisites
- Docker with Compose plugin (`docker compose version`)

### Start

```bash
PCR_API_TOKENS=my-secret-token docker compose up -d --build
```

Or set the env var in a `.env` file (not committed to git):
```
PCR_API_TOKENS=my-secret-token
PCR_SESSION_SECRET=my-session-key
```

Then just:
```bash
docker compose up -d --build
```

### Sanity check

Same as Docker above -- the service is available at `http://localhost:8080`.

```bash
# Quick end-to-end check
curl -s http://localhost:8080/api/v1/health | jq
curl -s -X POST -H "Authorization: Bearer my-secret-token" \
  -H "Content-Type: application/json" \
  http://localhost:8080/api/v1/events -d '{
  "user_name": "bob",
  "event_type": "feature-flag",
  "description": "compose sanity check"
}' | jq '{id, description}'
open "http://localhost:8080/login?token=my-secret-token"
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

### Edit secrets

Before applying, edit `k8s/secret.yaml` with your actual values:

```yaml
stringData:
  api-tokens: "your-actual-token"
  session-secret: "your-actual-session-secret"
```

### Apply manifests

```bash
kubectl apply -f k8s/namespace.yaml
kubectl apply -f k8s/secret.yaml
kubectl apply -f k8s/configmap.yaml
kubectl apply -f k8s/pvc.yaml
kubectl apply -f k8s/deployment.yaml
kubectl apply -f k8s/service.yaml
```

### Verify the pod is running

```bash
kubectl -n pcr get pods
# Wait for STATUS: Running and READY: 1/1

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
curl -s http://localhost:8080/api/v1/health | jq

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

# Dashboard
open "http://localhost:8080/login?token=your-actual-token"
```

### Tear down

```bash
kind delete cluster --name pcr
```

## Environment Variables Reference

All methods use the same environment variables. Key settings:

| Variable | Required | Default | Description |
|---|---|---|---|
| `PCR_API_TOKENS` | Yes | -- | Comma-separated API tokens |
| `PCR_SESSION_SECRET` | No | (random) | HMAC key for session cookies. Set for persistent sessions across restarts. |
| `PCR_DATABASE_PATH` | No | `registry.db` (binary) / `/data/registry.db` (Docker) | Path to SQLite file |
| `PCR_AUTO_MIGRATE` | No | `true` | Run schema migrations on startup |
| `PCR_ADDR` | No | `:8080` | Listen address |
| `PCR_REQUIRE_AUTH_READS` | No | `true` | Require auth for read endpoints |
| `PCR_DASHBOARD_REFRESH_SEC` | No | `60` | Dashboard auto-refresh interval in seconds |
| `PCR_READ_TIMEOUT` | No | `5s` | HTTP server read timeout (Go duration) |
| `PCR_WRITE_TIMEOUT` | No | `10s` | HTTP server write timeout (Go duration) |
| `PCR_SHUTDOWN_TIMEOUT` | No | `15s` | Graceful shutdown timeout (Go duration) |
| `PCR_DB_BUSY_TIMEOUT` | No | `5s` | How long SQLite waits for a write lock |
| `PCR_DB_SLOW_QUERY_THRESHOLD` | No | `100ms` | Log a warning when a store operation exceeds this |

See the README for the full configuration reference.

## Notes

- **SQLite limitation:** Only one instance can write to the database at a time. Docker and kind deployments are single-replica. For multi-replica, consider a PostgreSQL backend.
- **Data persistence:** Docker uses a named volume (`pcr-data`). kind uses a PersistentVolumeClaim (`pcr-data`, 1Gi). Both survive container/pod restarts.
- **Image size:** The production image is based on Alpine 3.21 with a statically-linked Go binary.
- **Restart policy:** Docker Compose sets `restart: unless-stopped`. The Kubernetes deployment uses `strategy: Recreate` to avoid two pods writing to the same volume.
