# Database Resiliency

This document describes the database startup scenarios the server handles, the migration architecture, and how to verify resilience in each case.

## Migration Architecture

Schema is managed exclusively by `golang-migrate/migrate/v4`. The SQLite store (`internal/store/sqlite`) does **not** create or modify tables -- it only opens the connection and sets pragmas (WAL mode, foreign keys, busy timeout). Migrations are embedded in the binary via `//go:embed` from the `migrations/` directory.

### Migration files

| Migration | Purpose |
|---|---|
| `001_create_change_events.up.sql` | Creates `change_events` and `change_event_tags` tables with indexes |
| `002_add_starred_alerted.up.sql` | Adds `starred` and `alerted` INTEGER columns to `change_events` |

### Why migrations are the sole schema owner

An earlier design had a `createTableSQL` constant in the store that ran on every startup. This caused problems when migration 002 tried to `ALTER TABLE ADD COLUMN` on columns that already existed. The fix was to remove all schema management from the store and let migrations be the single source of truth.

## Startup Scenarios

The migration runner in `cmd/server/main.go` handles four distinct database states:

### 1. Fresh database (no file exists)

SQLite creates the file on first connection. The migration table (`schema_migrations`) is created by the golang-migrate driver. `m.Version()` returns `ErrNilVersion` (no migrations applied yet). Both migrations apply in sequence.

**How to test:**
```bash
rm -f /tmp/fresh-test.db
PCR_API_TOKENS=test-token PCR_DATABASE_PATH=/tmp/fresh-test.db make run
```

Expected log output:
```
INFO sqlite store opened path=/tmp/fresh-test.db ...
INFO database migrations applied successfully
INFO starting server addr=:8080
```

### 2. Existing database (all migrations applied)

`m.Up()` returns `migrate.ErrNoChange`, which is handled gracefully. The server starts normally with all existing data preserved.

**How to test:**
```bash
# Start once (applies migrations), then stop and start again
PCR_API_TOKENS=test-token make run
# Ctrl+C
PCR_API_TOKENS=test-token make run
```

Expected log output:
```
INFO sqlite store opened ...
INFO database migrations applied successfully
```

### 3. Partially migrated database

If only migration 001 has been applied (e.g., from an older version of the binary), `m.Up()` applies migration 002 to add the `starred` and `alerted` columns. Existing data is preserved.

### 4. Dirty database (previous migration failed)

If a prior migration attempt left the database in a dirty state (e.g., `ALTER TABLE` failed mid-way), the migration runner detects this via `m.Version()` returning `dirty=true`. It logs a warning, forces the version to clear the dirty flag, then retries `m.Up()`.

Additionally, if `m.Up()` encounters a "duplicate column name" error (from a column that was added outside of migrations), it forces the migration version as applied rather than aborting.

**How to simulate:**
```bash
# Manually set dirty flag in the migration table
sqlite3 registry.db "UPDATE schema_migrations SET dirty = 1;"
PCR_API_TOKENS=test-token make run
```

Expected log output:
```
WARN database is in dirty state, forcing version to resolve version=2
INFO database migrations applied successfully
```

## SQLite Configuration

These pragmas are set at connection time via DSN parameters:

| Pragma | Value | Purpose |
|---|---|---|
| `journal_mode` | `wal` | Write-Ahead Logging -- concurrent readers with single writer |
| `foreign_keys` | `on` | Enforces CASCADE deletes on `change_event_tags` |
| `busy_timeout` | 5000ms (configurable) | Retries internally when write lock is held, instead of returning SQLITE_BUSY |

### Busy timeout

The `busy_timeout` pragma (default 5 seconds, configurable via `PCR_DB_BUSY_TIMEOUT`) controls how long SQLite waits for a write lock before returning an error. This is critical for handling concurrent write bursts from multiple API clients.

**When to increase:** If you see `SQLITE_BUSY` errors in logs under load, increase the busy timeout. If writes consistently take longer than the timeout, consider switching to the PostgreSQL backend (the `store.ChangeStore` interface supports this).

### Slow query logging

All store operations (Create, GetByID, Update, Delete, List) are instrumented with latency logging:

- **Debug level:** Operations completing under the threshold (default 100ms)
- **Warn level:** Operations exceeding the threshold

Configurable via `PCR_DB_SLOW_QUERY_THRESHOLD`. Example warning:
```
WARN slow store operation op=Create duration=247ms
```

**When to investigate:** Consistent slow query warnings indicate write contention (SQLite single-writer bottleneck) or a growing dataset that needs index optimization.

## Column Order Verification

The SELECT statements in the store use explicit column lists (not `SELECT *`). The column order in all queries must match the `scanEventFields` function's `Scan` call. The verified order is:

```
id, user_name, timestamp_start, timestamp_end, event_type, description,
long_description, starred, alerted, created_at, updated_at
```

This is consistent across:
- `GetByID` SELECT
- `List` SELECT
- `scanEventFields` Scan destinations
- `Create` INSERT column list
- `Update` SET clause

Note: SQLite `ALTER TABLE ADD COLUMN` appends columns at the end of the physical table. Since we use named columns in SELECT (not `SELECT *`), physical order does not matter.

## Test Schema

Integration tests (`internal/store/sqlite/sqlite_test.go`) use a `testSchemaSQL` constant that mirrors the combined output of migrations 001 + 002. This must be kept in sync with migrations manually. If a new migration is added, update `testSchemaSQL` to match.

The test schema includes `starred` and `alerted` in the initial CREATE TABLE (rather than ALTER TABLE) since tests create fresh databases with the full schema in one step.

## Checklist for Adding New Migrations

1. Create `migrations/NNN_description.up.sql` and `migrations/NNN_description.down.sql`
2. Use `IF NOT EXISTS` / `IF EXISTS` where SQLite supports it (CREATE TABLE, CREATE INDEX, DROP TABLE, DROP INDEX)
3. For `ALTER TABLE ADD COLUMN`: SQLite does not support `IF NOT EXISTS` -- the migration runner handles duplicate column errors gracefully, but avoid relying on this
4. Update `testSchemaSQL` in `internal/store/sqlite/sqlite_test.go` to reflect the new schema
5. Update `scanEventFields` and all SELECT/INSERT/UPDATE queries if columns are added
6. Run the full test suite: `go test -tags=integration ./... -race`
7. Test against a fresh database and an existing database with prior migrations applied
