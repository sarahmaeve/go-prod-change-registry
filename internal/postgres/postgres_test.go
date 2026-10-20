//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sarahmaeve/go-prod-change-registry/internal/postgres"
)

func TestMigrateCreatesPostgreSQLSchema(t *testing.T) {
	t.Parallel()
	databaseURL := isolatedDatabaseURL(t)

	if err := postgres.Migrate(databaseURL, time.Second); err != nil {
		t.Fatalf("Migrate(): %v", err)
	}
	if err := postgres.Migrate(databaseURL, time.Second); err != nil {
		t.Fatalf("Migrate() second call: %v", err)
	}

	pool, err := postgres.Open(t.Context(), databaseURL, postgres.PoolOptions{
		MaxConnections: 3,
		ConnectTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("Open(): %v", err)
	}
	t.Cleanup(pool.Close)

	var version int
	var dirty bool
	if err := pool.QueryRow(t.Context(), "SELECT version, dirty FROM schema_migrations").Scan(&version, &dirty); err != nil {
		t.Fatalf("read migration state after repeated migration: %v", err)
	}
	if version != 1 || dirty {
		t.Fatalf("migration state = (%d, %t), want version 1, clean", version, dirty)
	}

	var dataType string
	err = pool.QueryRow(t.Context(), `
		SELECT data_type
		FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND table_name = 'change_events'
		  AND column_name = 'timestamp'
	`).Scan(&dataType)
	if err != nil {
		t.Fatalf("query timestamp column: %v", err)
	}
	if dataType != "timestamp with time zone" {
		t.Errorf("change_events.timestamp data type = %q, want %q", dataType, "timestamp with time zone")
	}

	var isIdentity string
	err = pool.QueryRow(t.Context(), `
		SELECT is_identity
		FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND table_name = 'change_events'
		  AND column_name = 'ingest_sequence'
	`).Scan(&isIdentity)
	if err != nil {
		t.Fatalf("query ingest_sequence column: %v", err)
	}
	if isIdentity != "YES" {
		t.Errorf("change_events.ingest_sequence is_identity = %q, want YES", isIdentity)
	}
}

func TestMigrateRefusesDirtyDatabase(t *testing.T) {
	t.Parallel()
	databaseURL := isolatedDatabaseURL(t)
	pool, err := pgxpool.New(t.Context(), databaseURL)
	if err != nil {
		t.Fatalf("open dirty migration pool: %v", err)
	}
	t.Cleanup(pool.Close)

	_, err = pool.Exec(t.Context(), `
		CREATE TABLE schema_migrations (
			version BIGINT NOT NULL PRIMARY KEY,
			dirty BOOLEAN NOT NULL
		);
		INSERT INTO schema_migrations (version, dirty) VALUES (1, TRUE);
	`)
	if err != nil {
		t.Fatalf("seed dirty migration: %v", err)
	}

	err = postgres.Migrate(databaseURL, time.Second)
	if err == nil {
		t.Fatal("Migrate() error = nil, want dirty-migration error")
	}
	if !strings.Contains(err.Error(), "dirty at version 1") {
		t.Errorf("Migrate() error = %q, want dirty version context", err)
	}
	var version int
	var dirty bool
	if err := pool.QueryRow(t.Context(), "SELECT version, dirty FROM schema_migrations").Scan(&version, &dirty); err != nil {
		t.Fatalf("read rejected migration state: %v", err)
	}
	if version != 1 || !dirty {
		t.Fatalf("migration state = (%d, %t), want original version 1, dirty", version, dirty)
	}
}

func TestMigrateSerializesConcurrentCallers(t *testing.T) {
	t.Parallel()
	databaseURL := isolatedDatabaseURL(t)

	const callers = 4
	start := make(chan struct{})
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			<-start
			errs <- postgres.Migrate(databaseURL, time.Second)
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("Migrate() concurrent error = %v", err)
		}
	}
}

func TestMigrateReadsVersionUnderAdvisoryLock(t *testing.T) {
	t.Parallel()
	databaseURL := isolatedDatabaseURL(t)
	if err := postgres.Migrate(databaseURL, time.Second); err != nil {
		t.Fatalf("initialize schema: %v", err)
	}
	pool, err := pgxpool.New(t.Context(), databaseURL)
	if err != nil {
		t.Fatalf("open observation pool: %v", err)
	}
	t.Cleanup(pool.Close)
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin version-table blocker: %v", err)
	}
	done := make(chan struct{})
	started := false
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			t.Errorf("release version-table blocker: %v", err)
		}
		if started {
			select {
			case <-done:
			case <-ctx.Done():
				t.Error("migration did not finish after releasing version-table blocker")
			}
		}
	})
	// Freeze the version read so pg_locks can show whether it is protected.
	// The old preflight Version call reaches this read without an advisory lock.
	if _, err := tx.Exec(t.Context(), "LOCK TABLE schema_migrations IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatalf("block version reads: %v", err)
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	applicationName := "pcr_lock_test_" + uuid.NewString()
	query := parsed.Query()
	query.Set("application_name", applicationName)
	parsed.RawQuery = query.Encode()
	result := make(chan error, 1)
	started = true
	go func() {
		defer close(done)
		result <- postgres.Migrate(parsed.String(), time.Second)
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var locked bool
		err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM pg_locks l
				WHERE l.pid = a.pid AND l.locktype = 'advisory' AND l.granted
			)
			FROM pg_stat_activity a
			WHERE a.application_name = $1 AND a.wait_event_type = 'Lock'
			  AND a.query LIKE 'SELECT version, dirty FROM %'
		`, applicationName).Scan(&locked)
		if err == nil {
			if !locked {
				t.Fatal("migration reads dirty state without holding its advisory lock")
			}
			break
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("observe version read: %v", err)
		}
		select {
		case err := <-result:
			t.Fatalf("migration returned before reaching the blocked version read: %v", err)
		case <-ctx.Done():
			t.Fatal("migration did not reach the blocked version read")
		case <-ticker.C:
		}
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("unblock version read: %v", err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("migrate current schema: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("migration did not finish after unblocking version read")
	}
}

func isolatedDatabaseURL(t *testing.T) string {
	t.Helper()

	databaseURL := os.Getenv("PCR_TEST_POSTGRES_URL")
	if databaseURL == "" {
		t.Skip("PCR_TEST_POSTGRES_URL is not set")
	}

	admin, err := pgxpool.New(t.Context(), databaseURL)
	if err != nil {
		t.Fatalf("open PostgreSQL test admin pool: %v", err)
	}
	t.Cleanup(admin.Close)

	schema := "pcr_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec(t.Context(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create test schema %q: %v", schema, err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop test schema %q: %v", schema, err)
		}
	})

	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatalf("parse PCR_TEST_POSTGRES_URL: %v", err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
