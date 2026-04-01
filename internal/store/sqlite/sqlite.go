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
)

// Compile-time interface check.
var _ store.ChangeStore = (*Store)(nil)

// Store is a SQLite-backed implementation of store.ChangeStore.
type Store struct {
	db                 *sql.DB
	slowQueryThreshold time.Duration
}

// New wraps an existing *sql.DB connection as a Store.
// slowQueryThreshold sets the duration above which store operations are logged at Warn level.
func New(db *sql.DB, slowQueryThreshold time.Duration) *Store {
	return &Store{
		db:                 db,
		slowQueryThreshold: slowQueryThreshold,
	}
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

	var parentID *string
	if event.ParentID != "" {
		parentID = &event.ParentID
	}

	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO change_events (id, parent_id, user_name, timestamp, event_type, description, long_description, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		event.ID,
		parentID,
		event.UserName,
		event.Timestamp.Format(time.RFC3339),
		event.EventType,
		event.Description,
		event.LongDescription,
		event.CreatedAt.Format(time.RFC3339),
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

	row := s.db.QueryRowContext(
		ctx,
		`SELECT id, parent_id, user_name, timestamp, event_type, description, long_description, created_at
		 FROM change_events WHERE id = ?`,
		id,
	)

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
		`SELECT id, parent_id, user_name, timestamp, event_type, description, long_description, created_at
		 FROM change_events%s
		 ORDER BY timestamp DESC, id ASC
		 LIMIT ? OFFSET ?`,
		where,
	)

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

// GetAnnotations returns the derived annotation state (starred, alerted) for a
// single event by walking its meta-events in reverse chronological order.
func (s *Store) GetAnnotations(ctx context.Context, eventID string) (result *model.EventAnnotations, err error) {
	start := time.Now()
	defer func() { s.logOperation(ctx, "GetAnnotations", start, err) }()

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT event_type FROM change_events
		 WHERE parent_id = ? AND event_type IN ('star', 'unstar', 'alert', 'clear-alert')
		 ORDER BY created_at DESC, id DESC`,
		eventID,
	)
	if err != nil {
		return nil, fmt.Errorf("query annotations: %w", err)
	}
	defer rows.Close()

	annotations := &model.EventAnnotations{}
	starResolved := false
	alertResolved := false

	for rows.Next() {
		if starResolved && alertResolved {
			break
		}

		var eventType string
		if err := rows.Scan(&eventType); err != nil {
			return nil, fmt.Errorf("scan annotation: %w", err)
		}

		switch eventType {
		case "star":
			if !starResolved {
				annotations.Starred = true
				starResolved = true
			}
		case "unstar":
			if !starResolved {
				annotations.Starred = false
				starResolved = true
			}
		case "alert":
			if !alertResolved {
				annotations.Alerted = true
				alertResolved = true
			}
		case "clear-alert":
			if !alertResolved {
				annotations.Alerted = false
				alertResolved = true
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration: %w", err)
	}

	return annotations, nil
}

// GetAnnotationsBatch returns the derived annotation state for multiple events.
func (s *Store) GetAnnotationsBatch(ctx context.Context, eventIDs []string) (result map[string]*model.EventAnnotations, err error) {
	start := time.Now()
	defer func() { s.logOperation(ctx, "GetAnnotationsBatch", start, err) }()

	if len(eventIDs) == 0 {
		return make(map[string]*model.EventAnnotations), nil
	}

	placeholders := make([]string, len(eventIDs))
	args := make([]any, len(eventIDs))
	for i, id := range eventIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	query := fmt.Sprintf(
		`SELECT parent_id, event_type FROM change_events
		 WHERE parent_id IN (%s) AND event_type IN ('star', 'unstar', 'alert', 'clear-alert')
		 ORDER BY created_at DESC, id DESC`,
		strings.Join(placeholders, ","),
	)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query annotations batch: %w", err)
	}
	defer rows.Close()

	// Track which annotations have been resolved per parent.
	type resolvedState struct {
		starResolved  bool
		alertResolved bool
	}
	resolved := make(map[string]*resolvedState)
	annotations := make(map[string]*model.EventAnnotations)

	// Initialize entries for all requested IDs.
	for _, id := range eventIDs {
		annotations[id] = &model.EventAnnotations{}
		resolved[id] = &resolvedState{}
	}

	for rows.Next() {
		var parentID, eventType string
		if err := rows.Scan(&parentID, &eventType); err != nil {
			return nil, fmt.Errorf("scan annotation: %w", err)
		}

		state := resolved[parentID]
		if state == nil {
			continue
		}

		switch eventType {
		case "star":
			if !state.starResolved {
				annotations[parentID].Starred = true
				state.starResolved = true
			}
		case "unstar":
			if !state.starResolved {
				annotations[parentID].Starred = false
				state.starResolved = true
			}
		case "alert":
			if !state.alertResolved {
				annotations[parentID].Alerted = true
				state.alertResolved = true
			}
		case "clear-alert":
			if !state.alertResolved {
				annotations[parentID].Alerted = false
				state.alertResolved = true
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration: %w", err)
	}

	return annotations, nil
}

// --- helpers ---

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

// scanEventFields scans 8 columns from a change_events row into a ChangeEvent.
func scanEventFields(sc scanner) (*model.ChangeEvent, error) {
	var ev model.ChangeEvent
	var parentID *string
	var timestamp, createdAt string

	err := sc.Scan(
		&ev.ID,
		&parentID,
		&ev.UserName,
		&timestamp,
		&ev.EventType,
		&ev.Description,
		&ev.LongDescription,
		&createdAt,
	)
	if err != nil {
		return nil, err
	}

	// Convert nullable parent_id to string (empty when NULL).
	if parentID != nil {
		ev.ParentID = *parentID
	}

	var parseErr error
	ev.Timestamp, parseErr = time.Parse(time.RFC3339, timestamp)
	if parseErr != nil {
		return nil, fmt.Errorf("parse timestamp: %w", parseErr)
	}

	ev.CreatedAt, parseErr = time.Parse(time.RFC3339, createdAt)
	if parseErr != nil {
		return nil, fmt.Errorf("parse created_at: %w", parseErr)
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

// insertTags inserts all tags for an event within the given transaction.
func insertTags(ctx context.Context, tx *sql.Tx, eventID string, tags map[string]string) error {
	if len(tags) == 0 {
		return nil
	}

	stmt, err := tx.PrepareContext(
		ctx,
		`INSERT INTO change_event_tags (event_id, key, value) VALUES (?, ?, ?)`,
	)
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

	// Around+Window takes precedence over StartAfter/StartBefore when set.
	if params.Around != nil && params.Window != nil && *params.Window > 0 {
		windowStart := params.Around.Add(-*params.Window)
		windowEnd := params.Around.Add(*params.Window)
		clauses = append(clauses, "timestamp >= ?")
		args = append(args, windowStart.Format(time.RFC3339))
		clauses = append(clauses, "timestamp < ?")
		args = append(args, windowEnd.Format(time.RFC3339))
	} else {
		if params.StartAfter != nil {
			clauses = append(clauses, "timestamp >= ?")
			args = append(args, params.StartAfter.Format(time.RFC3339))
		}

		if params.StartBefore != nil {
			clauses = append(clauses, "timestamp < ?")
			args = append(args, params.StartBefore.Format(time.RFC3339))
		}
	}

	if params.UserName != "" {
		clauses = append(clauses, "user_name = ?")
		args = append(args, params.UserName)
	}

	if params.EventType != "" {
		clauses = append(clauses, "event_type = ?")
		args = append(args, params.EventType)
	}

	if params.TopLevel {
		clauses = append(clauses, "parent_id IS NULL")
	}

	if len(params.Tags) > 0 {
		tagClauses := make([]string, 0, len(params.Tags))
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
