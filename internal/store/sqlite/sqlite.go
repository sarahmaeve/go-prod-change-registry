package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/sarah/go-prod-change-registry/internal/model"
	"github.com/sarah/go-prod-change-registry/internal/store"

	_ "modernc.org/sqlite"
)

// Compile-time interface check.
var _ store.ChangeStore = (*Store)(nil)

// Store is a SQLite-backed implementation of store.ChangeStore.
type Store struct {
	db                 *sql.DB
	slowQueryThreshold time.Duration
}

// New opens a SQLite database at dbPath and configures connection pragmas.
// Schema creation is handled by migrations — the store does not manage schema directly.
// busyTimeout controls how long SQLite waits for a write lock before returning SQLITE_BUSY.
// slowQueryThreshold sets the duration above which store operations are logged at Warn level.
func New(dbPath string, busyTimeout, slowQueryThreshold time.Duration) (*Store, error) {
	busyTimeoutMs := busyTimeout.Milliseconds()
	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(wal)&_pragma=foreign_keys(on)&_pragma=busy_timeout(%d)",
		dbPath,
		busyTimeoutMs,
	)

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite open: %w", err)
	}

	// Verify the connection is usable.
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite ping: %w", err)
	}

	slog.Info(
		"sqlite store opened",
		"path", dbPath,
		"busy_timeout_ms", busyTimeoutMs,
		"slow_query_threshold", slowQueryThreshold,
	)

	return &Store{
		db:                 db,
		slowQueryThreshold: slowQueryThreshold,
	}, nil
}

// GetDB returns the underlying *sql.DB, useful for database migrations.
func (s *Store) GetDB() *sql.DB {
	return s.db
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// logOperation logs the duration of a store operation. If the duration exceeds
// the slow query threshold, it logs at Warn level; otherwise at Debug level.
func (s *Store) logOperation(ctx context.Context, op string, start time.Time, err error) {
	duration := time.Since(start)
	attrs := []slog.Attr{
		slog.String("op", op),
		slog.Duration("duration", duration),
	}
	if err != nil {
		attrs = append(attrs, slog.String("error", err.Error()))
	}

	if duration >= s.slowQueryThreshold {
		slog.LogAttrs(
			ctx,
			slog.LevelWarn,
			"slow store operation",
			attrs...,
		)
		return
	}

	slog.LogAttrs(
		ctx,
		slog.LevelDebug,
		"store operation",
		attrs...,
	)
}

// Create inserts a new change event and its tags within a transaction.
func (s *Store) Create(ctx context.Context, event *model.ChangeEvent) (result *model.ChangeEvent, err error) {
	start := time.Now()
	defer func() { s.logOperation(ctx, "Create", start, err) }()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var tsEnd *string
	if event.TimestampEnd != nil {
		v := event.TimestampEnd.Format(time.RFC3339)
		tsEnd = &v
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO change_events (id, user_name, timestamp_start, timestamp_end, event_type, description, long_description, starred, alerted, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.ID,
		event.UserName,
		event.TimestampStart.Format(time.RFC3339),
		tsEnd,
		event.EventType,
		event.Description,
		event.LongDescription,
		boolToInt(event.Starred),
		boolToInt(event.Alerted),
		event.CreatedAt.Format(time.RFC3339),
		event.UpdatedAt.Format(time.RFC3339),
	)
	if err != nil {
		return nil, fmt.Errorf("insert event: %w", err)
	}

	if err := insertTags(ctx, tx, event.ID, event.Tags); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit tx: %w", err)
	}

	return s.GetByID(ctx, event.ID)
}

// GetByID retrieves a single change event by ID, including its tags.
// Returns (nil, nil) when the event is not found.
func (s *Store) GetByID(ctx context.Context, id string) (result *model.ChangeEvent, err error) {
	start := time.Now()
	defer func() { s.logOperation(ctx, "GetByID", start, err) }()

	row := s.db.QueryRowContext(ctx,
		`SELECT id, user_name, timestamp_start, timestamp_end, event_type, description, long_description, starred, alerted, created_at, updated_at
		 FROM change_events WHERE id = ?`, id)

	ev, err := scanEvent(row)
	if err != nil {
		return nil, err
	}
	if ev == nil {
		return nil, nil
	}

	tags, err := s.loadTagsForEvents(ctx, []string{ev.ID})
	if err != nil {
		return nil, err
	}
	ev.Tags = tags[ev.ID]

	return ev, nil
}

// Update modifies an existing change event and replaces its tags within a transaction.
func (s *Store) Update(ctx context.Context, event *model.ChangeEvent) (result *model.ChangeEvent, err error) {
	start := time.Now()
	defer func() { s.logOperation(ctx, "Update", start, err) }()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var tsEnd *string
	if event.TimestampEnd != nil {
		v := event.TimestampEnd.Format(time.RFC3339)
		tsEnd = &v
	}

	_, err = tx.ExecContext(ctx,
		`UPDATE change_events
		 SET user_name = ?, timestamp_start = ?, timestamp_end = ?, event_type = ?, description = ?, long_description = ?, starred = ?, alerted = ?, updated_at = ?
		 WHERE id = ?`,
		event.UserName,
		event.TimestampStart.Format(time.RFC3339),
		tsEnd,
		event.EventType,
		event.Description,
		event.LongDescription,
		boolToInt(event.Starred),
		boolToInt(event.Alerted),
		event.UpdatedAt.Format(time.RFC3339),
		event.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("update event: %w", err)
	}

	// Replace tags: delete old, insert new.
	if _, err := tx.ExecContext(ctx, `DELETE FROM change_event_tags WHERE event_id = ?`, event.ID); err != nil {
		return nil, fmt.Errorf("delete old tags: %w", err)
	}

	if err := insertTags(ctx, tx, event.ID, event.Tags); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit tx: %w", err)
	}

	return s.GetByID(ctx, event.ID)
}

// Delete removes a change event by ID. Returns an error if the event does not exist.
func (s *Store) Delete(ctx context.Context, id string) (err error) {
	start := time.Now()
	defer func() { s.logOperation(ctx, "Delete", start, err) }()

	res, err := s.db.ExecContext(ctx, `DELETE FROM change_events WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete event: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("event %s not found", id)
	}

	return nil
}

// List queries change events with optional filters and pagination.
func (s *Store) List(ctx context.Context, params model.ListParams) (result *model.ListResult, err error) {
	start := time.Now()
	defer func() { s.logOperation(ctx, "List", start, err) }()

	where, args := buildWhereClause(params)
	limit := params.EffectiveLimit()

	// Count total matching rows.
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM change_events%s", where)
	var totalCount int
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&totalCount); err != nil {
		return nil, fmt.Errorf("count events: %w", err)
	}

	// Fetch the page.
	selectQuery := fmt.Sprintf(
		`SELECT id, user_name, timestamp_start, timestamp_end, event_type, description, long_description, starred, alerted, created_at, updated_at
		 FROM change_events%s
		 ORDER BY timestamp_start DESC, id ASC
		 LIMIT ? OFFSET ?`, where)

	fetchArgs := make([]any, 0, len(args)+2)
	fetchArgs = append(fetchArgs, args...)
	fetchArgs = append(fetchArgs, limit, params.Offset)
	rows, err := s.db.QueryContext(ctx, selectQuery, fetchArgs...)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()

	events := make([]model.ChangeEvent, 0)
	eventIDs := make([]string, 0)
	for rows.Next() {
		ev, err := scanEventFromRows(rows)
		if err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		events = append(events, *ev)
		eventIDs = append(eventIDs, ev.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration: %w", err)
	}

	// Load tags for all returned events in a single query.
	if len(eventIDs) > 0 {
		tagMap, err := s.loadTagsForEvents(ctx, eventIDs)
		if err != nil {
			return nil, err
		}
		for i := range events {
			events[i].Tags = tagMap[events[i].ID]
		}
	}

	return &model.ListResult{
		Events:     events,
		TotalCount: totalCount,
		Limit:      limit,
		Offset:     params.Offset,
	}, nil
}

// boolToInt converts a bool to an int for SQLite storage.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// --- helpers ---

// insertTags inserts all tags for an event within the given transaction.
func insertTags(ctx context.Context, tx *sql.Tx, eventID string, tags map[string]string) error {
	if len(tags) == 0 {
		return nil
	}

	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO change_event_tags (event_id, key, value) VALUES (?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare insert tags: %w", err)
	}
	defer stmt.Close()

	for k, v := range tags {
		if _, err := stmt.ExecContext(ctx, eventID, k, v); err != nil {
			return fmt.Errorf("insert tag %q: %w", k, err)
		}
	}

	return nil
}

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

// scanEventFields scans a change_events row into a ChangeEvent.
func scanEventFields(s scanner) (*model.ChangeEvent, error) {
	var ev model.ChangeEvent
	var tsStart, createdAt, updatedAt string
	var tsEnd *string
	var starred, alerted int

	err := s.Scan(
		&ev.ID,
		&ev.UserName,
		&tsStart,
		&tsEnd,
		&ev.EventType,
		&ev.Description,
		&ev.LongDescription,
		&starred,
		&alerted,
		&createdAt,
		&updatedAt,
	)
	if err != nil {
		return nil, err
	}

	ev.Starred = starred != 0
	ev.Alerted = alerted != 0

	ev.TimestampStart, err = time.Parse(time.RFC3339, tsStart)
	if err != nil {
		return nil, fmt.Errorf("parse timestamp_start: %w", err)
	}

	if tsEnd != nil {
		t, err := time.Parse(time.RFC3339, *tsEnd)
		if err != nil {
			return nil, fmt.Errorf("parse timestamp_end: %w", err)
		}
		ev.TimestampEnd = &t
	}

	ev.CreatedAt, err = time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}

	ev.UpdatedAt, err = time.Parse(time.RFC3339, updatedAt)
	if err != nil {
		return nil, fmt.Errorf("parse updated_at: %w", err)
	}

	return &ev, nil
}

// scanEvent scans from a *sql.Row, returning (nil, nil) on ErrNoRows.
func scanEvent(row *sql.Row) (*model.ChangeEvent, error) {
	ev, err := scanEventFields(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan event: %w", err)
	}
	return ev, nil
}

// scanEventFromRows scans from *sql.Rows (the cursor is already on a valid row).
func scanEventFromRows(rows *sql.Rows) (*model.ChangeEvent, error) {
	return scanEventFields(rows)
}

// loadTagsForEvents fetches tags for the given event IDs in one query.
func (s *Store) loadTagsForEvents(ctx context.Context, ids []string) (map[string]map[string]string, error) {
	if len(ids) == 0 {
		return make(map[string]map[string]string), nil
	}

	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}

	query := fmt.Sprintf(
		`SELECT event_id, key, value FROM change_event_tags WHERE event_id IN (%s)`,
		strings.Join(placeholders, ","),
	)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("load tags: %w", err)
	}
	defer rows.Close()

	result := make(map[string]map[string]string)
	for rows.Next() {
		var eventID, key, value string
		if err := rows.Scan(&eventID, &key, &value); err != nil {
			return nil, fmt.Errorf("scan tag: %w", err)
		}
		if result[eventID] == nil {
			result[eventID] = make(map[string]string)
		}
		result[eventID][key] = value
	}

	return result, rows.Err()
}

// buildWhereClause constructs the WHERE clause and parameter list for List queries.
func buildWhereClause(params model.ListParams) (string, []any) {
	clauses := make([]string, 0)
	args := make([]any, 0)

	if params.StartAfter != nil {
		clauses = append(clauses, "timestamp_start >= ?")
		args = append(args, params.StartAfter.Format(time.RFC3339))
	}

	if params.StartBefore != nil {
		clauses = append(clauses, "timestamp_start < ?")
		args = append(args, params.StartBefore.Format(time.RFC3339))
	}

	if params.UserName != "" {
		clauses = append(clauses, "user_name = ?")
		args = append(args, params.UserName)
	}

	if params.EventType != "" {
		clauses = append(clauses, "event_type = ?")
		args = append(args, params.EventType)
	}

	if params.Alerted != nil {
		clauses = append(clauses, "alerted = ?")
		args = append(args, boolToInt(*params.Alerted))
	}

	if len(params.Tags) > 0 {
		tagClauses := make([]string, 0)
		for k, v := range params.Tags {
			tagClauses = append(tagClauses, "(key = ? AND value = ?)")
			args = append(args, k, v)
		}
		subquery := fmt.Sprintf(
			"id IN (SELECT event_id FROM change_event_tags WHERE %s GROUP BY event_id HAVING COUNT(DISTINCT key) = ?)",
			strings.Join(tagClauses, " OR "),
		)
		clauses = append(clauses, subquery)
		args = append(args, len(params.Tags))
	}

	if len(clauses) == 0 {
		return "", make([]any, 0)
	}

	return " WHERE " + strings.Join(clauses, " AND "), args
}
