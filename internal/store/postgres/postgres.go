package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sarahmaeve/go-prod-change-registry/internal/model"
	"github.com/sarahmaeve/go-prod-change-registry/internal/store"
)

// Compile-time interface check.
var _ store.ChangeStore = (*Store)(nil)

// Store is a PostgreSQL-backed implementation of store.ChangeStore.
type Store struct {
	pool               *pgxpool.Pool
	slowQueryThreshold time.Duration
}

// New wraps an existing PostgreSQL pool as a Store.
// slowQueryThreshold sets the duration above which store operations are logged at Warn level.
func New(pool *pgxpool.Pool, slowQueryThreshold time.Duration) *Store {
	return &Store{
		pool:               pool,
		slowQueryThreshold: slowQueryThreshold,
	}
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	s.pool.Close()
	return nil
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

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var parentID *string
	if event.ParentID != "" {
		parentID = &event.ParentID
	}

	_, err = tx.Exec(
		ctx,
		`INSERT INTO change_events (id, external_id, parent_id, user_name, user_provider, user_subject, timestamp, event_type, description, long_description, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		event.ID,
		nullableString(event.ExternalID),
		parentID,
		event.UserName,
		nullableString(event.UserProvider),
		nullableString(event.UserSubject),
		canonicalTime(event.Timestamp),
		event.EventType,
		event.Description,
		event.LongDescription,
		canonicalTime(event.CreatedAt),
	)
	if err != nil && event.ExternalID != "" && isUniqueViolation(err) {
		// Event with this external_id already exists — return it (idempotent).
		// Rollback error is unactionable here; we already have the duplicate-
		// detection result and are about to return.
		_ = tx.Rollback(ctx)
		existing, lookupErr := s.GetByExternalID(ctx, event.ExternalID)
		if lookupErr != nil {
			return nil, lookupErr
		}
		return existing, store.ErrDuplicate
	}
	if err != nil {
		return nil, fmt.Errorf("insert event: %w", err)
	}

	if err := insertTags(ctx, tx, event.ID, event.Tags); err != nil {
		return nil, err
	}
	if err := insertLinks(ctx, tx, event.ID, event.Links); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit tx: %w", err)
	}

	result = &model.ChangeEvent{
		ID:              event.ID,
		ExternalID:      event.ExternalID,
		ParentID:        event.ParentID,
		UserName:        event.UserName,
		UserProvider:    event.UserProvider,
		UserSubject:     event.UserSubject,
		Timestamp:       canonicalTime(event.Timestamp),
		EventType:       event.EventType,
		Description:     event.Description,
		LongDescription: event.LongDescription,
		Links:           slices.Clone(event.Links),
		Tags:            maps.Clone(event.Tags),
		CreatedAt:       canonicalTime(event.CreatedAt),
	}
	return result, nil
}

// ToggleStar atomically appends the opposite star transition.
func (s *Store) ToggleStar(ctx context.Context, eventID string, user model.UserIdentity) (result *model.ChangeEvent, err error) {
	start := time.Now()
	defer func() { s.logOperation(ctx, "ToggleStar", start, err) }()
	return s.toggleTransition(ctx, eventID, user, model.EventTypeStar, model.EventTypeUnstar, "starred", "unstarred")
}

// ToggleAlert atomically appends the opposite alert transition.
func (s *Store) ToggleAlert(ctx context.Context, eventID string, user model.UserIdentity) (result *model.ChangeEvent, err error) {
	start := time.Now()
	defer func() { s.logOperation(ctx, "ToggleAlert", start, err) }()
	return s.toggleTransition(ctx, eventID, user, model.EventTypeAlert, model.EventTypeClearAlert, "alert opened", "alert cleared")
}

// toggleTransition locks the parent row so concurrent replicas cannot both
// observe and append the same transition.
//
//nolint:funlen // Keeping the lock, state read, insert, and commit together makes the transaction boundary auditable.
func (s *Store) toggleTransition(
	ctx context.Context,
	eventID string,
	user model.UserIdentity,
	activeType, inactiveType, activeDescription, inactiveDescription string,
) (result *model.ChangeEvent, err error) {

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var exists int
	err = tx.QueryRow(ctx, `SELECT 1 FROM change_events WHERE id = $1 FOR UPDATE`, eventID).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("check parent event: %w", err)
	}

	eventType := activeType
	description := activeDescription
	var latestType string
	err = tx.QueryRow(
		ctx,
		`SELECT event_type FROM change_events
		 WHERE parent_id = $1 AND event_type IN ($2, $3)
		 ORDER BY ingest_sequence DESC LIMIT 1`,
		eventID,
		activeType,
		inactiveType,
	).Scan(&latestType)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("read transition state: %w", err)
	}
	if err == nil && latestType == activeType {
		eventType = inactiveType
		description = inactiveDescription
	}

	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("generate event ID: %w", err)
	}
	now := canonicalTime(time.Now())
	result = &model.ChangeEvent{
		ID:           id.String(),
		ParentID:     eventID,
		UserName:     user.Name,
		UserProvider: user.Provider,
		UserSubject:  user.Subject,
		Timestamp:    now,
		EventType:    eventType,
		Description:  description,
		Tags:         make(map[string]string),
		CreatedAt:    now,
	}

	_, err = tx.Exec(
		ctx,
		`INSERT INTO change_events
		 (id, external_id, parent_id, user_name, user_provider, user_subject, timestamp, event_type, description, long_description, created_at)
		 VALUES ($1, NULL, $2, $3, $4, $5, $6, $7, $8, '', $9)`,
		result.ID,
		result.ParentID,
		result.UserName,
		nullableString(result.UserProvider),
		nullableString(result.UserSubject),
		result.Timestamp,
		result.EventType,
		result.Description,
		result.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("insert transition: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit tx: %w", err)
	}
	return result, nil
}

// GetByID retrieves a single change event by ID, including its tags.
// Returns (nil, nil) when the event is not found.
func (s *Store) GetByID(ctx context.Context, id string) (result *model.ChangeEvent, err error) {
	start := time.Now()
	defer func() { s.logOperation(ctx, "GetByID", start, err) }()

	row := s.pool.QueryRow(
		ctx,
		`SELECT id, external_id, parent_id, user_name, user_provider, user_subject, timestamp, event_type, description, long_description, created_at
		 FROM change_events WHERE id = $1`,
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
	links, err := s.loadLinksForEvents(ctx, []string{ev.ID})
	if err != nil {
		return nil, err
	}
	ev.Links = links[ev.ID]

	return ev, nil
}

// List queries change events with optional filters and pagination.
func (s *Store) List(ctx context.Context, params model.ListParams) (result *model.ListResult, err error) {
	start := time.Now()
	defer func() { s.logOperation(ctx, "List", start, err) }()

	where, args := buildWhereClause(params)
	limit := params.EffectiveLimit()

	// Count total matching rows.
	// `where` comes from buildWhereClause and is composed only of constant
	// SQL fragments containing numbered placeholders; user input is bound via
	// the args slice passed to QueryRow, never interpolated.
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM change_events%s", where)
	var totalCount int
	if err := s.pool.QueryRow(ctx, countQuery, args...).Scan(&totalCount); err != nil {
		return nil, fmt.Errorf("count events: %w", err)
	}

	// Fetch the page. Same constraint as countQuery above: user input is
	// bound via parameters, only the WHERE clause shape is interpolated.
	selectQuery := fmt.Sprintf(
		`SELECT id, external_id, parent_id, user_name, user_provider, user_subject, timestamp, event_type, description, long_description, created_at
		 FROM change_events%s
		 ORDER BY timestamp DESC, id ASC
		 LIMIT $%d OFFSET $%d`,
		where,
		len(args)+1,
		len(args)+2,
	)

	fetchArgs := make([]any, 0, len(args)+2)
	fetchArgs = append(fetchArgs, args...)
	fetchArgs = append(fetchArgs, limit, params.Offset)
	rows, err := s.pool.Query(ctx, selectQuery, fetchArgs...)
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

	// Load related records for all returned events in bounded queries.
	if len(eventIDs) > 0 {
		tagMap, err := s.loadTagsForEvents(ctx, eventIDs)
		if err != nil {
			return nil, err
		}
		for i := range events {
			events[i].Tags = tagMap[events[i].ID]
		}
		linkMap, err := s.loadLinksForEvents(ctx, eventIDs)
		if err != nil {
			return nil, err
		}
		for i := range events {
			events[i].Links = linkMap[events[i].ID]
		}
	}

	return &model.ListResult{
		Events:     events,
		TotalCount: totalCount,
		Limit:      limit,
		Offset:     params.Offset,
	}, nil
}

const currentCTEs = `WITH phase_event AS (
	SELECT tag.event_id AS id,
	       event.event_type,
	       event.timestamp,
	       tag.value AS phase
	FROM change_event_tags AS tag
	JOIN change_events AS event ON event.id = tag.event_id
	WHERE tag.key = 'phase'
	  AND tag.value IN ('start', 'end')
	  AND event.parent_id IS NULL
),
correlated AS MATERIALIZED (
	SELECT phase_event.id,
	       phase_event.event_type,
	       phase_event.timestamp,
	       phase_event.phase,
	       CASE
	           WHEN NULLIF(change_tag.value, '') IS NOT NULL THEN 'change_id'
	           ELSE 'deploy_id'
	       END AS correlation_key,
	       COALESCE(
	           NULLIF(change_tag.value, ''),
	           NULLIF(deploy_tag.value, '')
	       ) AS correlation_value
	FROM phase_event
	LEFT JOIN change_event_tags AS change_tag
	       ON change_tag.event_id = phase_event.id AND change_tag.key = 'change_id'
	LEFT JOIN change_event_tags AS deploy_tag
	       ON deploy_tag.event_id = phase_event.id AND deploy_tag.key = 'deploy_id'
),
ended AS MATERIALIZED (
	SELECT DISTINCT event_type, correlation_key, correlation_value
	FROM correlated
	WHERE phase = 'end'
	  AND correlation_value IS NOT NULL
),
active AS (
	SELECT candidate.id,
	       candidate.timestamp,
	       candidate.correlation_key,
	       candidate.correlation_value,
	       ROW_NUMBER() OVER (
	           PARTITION BY candidate.event_type,
	                        candidate.correlation_key,
	                        candidate.correlation_value
	           ORDER BY candidate.timestamp ASC, candidate.id ASC
	       ) AS representative_rank
	FROM correlated AS candidate
	LEFT JOIN ended
	       ON ended.event_type = candidate.event_type
	      AND ended.correlation_key = candidate.correlation_key
	      AND ended.correlation_value = candidate.correlation_value
	WHERE candidate.phase = 'start'
	  AND candidate.correlation_value IS NOT NULL
	  AND ended.correlation_value IS NULL
)
`

const currentFrom = `FROM active
JOIN change_events AS event ON event.id = active.id
LEFT JOIN change_event_tags AS team
	ON team.event_id = active.id AND team.key = 'team'
LEFT JOIN change_event_tags AS scope
	ON scope.event_id = active.id AND scope.key = 'scope'
LEFT JOIN change_event_tags AS severity
	ON severity.event_id = active.id AND severity.key = 'severity'`

// ListCurrent returns one representative start event for each logical operation
// that has no matching end event. Filtering and pagination apply after reduction.
func (s *Store) ListCurrent(ctx context.Context, params model.CurrentParams) (result *model.ListResult, err error) {
	start := time.Now()
	defer func() { s.logOperation(ctx, "ListCurrent", start, err) }()

	where, args := buildCurrentWhereClause(params)
	limit := params.EffectiveLimit()

	// SQL fragments are constants and user input is passed only as bound values.
	countQuery := fmt.Sprintf("%sSELECT COUNT(*) %s%s", currentCTEs, currentFrom, where)
	var totalCount int
	if err := s.pool.QueryRow(ctx, countQuery, args...).Scan(&totalCount); err != nil {
		return nil, fmt.Errorf("count current events: %w", err)
	}

	selectQuery := fmt.Sprintf(
		`%sSELECT event.id, event.external_id, event.parent_id, event.user_name,
		       event.user_provider, event.user_subject, event.timestamp,
		       event.event_type, event.description, event.long_description, event.created_at
		%s%s
		ORDER BY event.timestamp DESC, event.id ASC
		LIMIT $%d OFFSET $%d`,
		currentCTEs,
		currentFrom,
		where,
		len(args)+1,
		len(args)+2,
	)
	fetchArgs := make([]any, 0, len(args)+2)
	fetchArgs = append(fetchArgs, args...)
	fetchArgs = append(fetchArgs, limit, params.Offset)

	rows, err := s.pool.Query(ctx, selectQuery, fetchArgs...)
	if err != nil {
		return nil, fmt.Errorf("list current events: %w", err)
	}
	defer rows.Close()

	events := make([]model.ChangeEvent, 0)
	eventIDs := make([]string, 0)
	for rows.Next() {
		event, err := scanEventFromRows(rows)
		if err != nil {
			return nil, fmt.Errorf("scan current event: %w", err)
		}
		events = append(events, *event)
		eventIDs = append(eventIDs, event.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("current rows iteration: %w", err)
	}

	if len(eventIDs) > 0 {
		tagMap, err := s.loadTagsForEvents(ctx, eventIDs)
		if err != nil {
			return nil, err
		}
		for i := range events {
			events[i].Tags = tagMap[events[i].ID]
		}
		linkMap, err := s.loadLinksForEvents(ctx, eventIDs)
		if err != nil {
			return nil, err
		}
		for i := range events {
			events[i].Links = linkMap[events[i].ID]
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
//
// Complexity comes from the chronological walk plus four meta-event types
// (star/unstar, alert/clear-alert) each gated on whether their resolution
// is still pending. Splitting the loop body out hides the state machine.
//
//nolint:gocognit // meta-event resolution loop; complexity is inherent to the state machine
func (s *Store) GetAnnotations(ctx context.Context, eventID string) (result *model.EventAnnotations, err error) {
	start := time.Now()
	defer func() { s.logOperation(ctx, "GetAnnotations", start, err) }()

	rows, err := s.pool.Query(
		ctx,
		`SELECT event_type FROM change_events
		 WHERE parent_id = $1 AND event_type IN ('star', 'unstar', 'alert', 'clear-alert')
		 ORDER BY ingest_sequence DESC`,
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
// It runs the same per-event resolution as GetAnnotations, fanned out across a
// shared rows iteration with per-parent state tracking -- avoiding N+1 queries.
//
//nolint:gocognit,funlen // batched variant of GetAnnotations; same state machine, plus per-parent tracking
func (s *Store) GetAnnotationsBatch(ctx context.Context, eventIDs []string) (result map[string]*model.EventAnnotations, err error) {
	start := time.Now()
	defer func() { s.logOperation(ctx, "GetAnnotationsBatch", start, err) }()

	if len(eventIDs) == 0 {
		return make(map[string]*model.EventAnnotations), nil
	}

	rows, err := s.pool.Query(
		ctx,
		`SELECT parent_id, event_type FROM change_events
		 WHERE parent_id = ANY($1) AND event_type IN ('star', 'unstar', 'alert', 'clear-alert')
		 ORDER BY ingest_sequence DESC`,
		eventIDs,
	)
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

// GetByExternalID retrieves a single change event by its external_id, including its tags.
// Returns (nil, nil) when no event with the given external_id exists.
func (s *Store) GetByExternalID(ctx context.Context, externalID string) (result *model.ChangeEvent, err error) {
	start := time.Now()
	defer func() { s.logOperation(ctx, "GetByExternalID", start, err) }()

	row := s.pool.QueryRow(
		ctx,
		`SELECT id, external_id, parent_id, user_name, user_provider, user_subject, timestamp, event_type, description, long_description, created_at
		 FROM change_events WHERE external_id = $1`,
		externalID,
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
	links, err := s.loadLinksForEvents(ctx, []string{ev.ID})
	if err != nil {
		return nil, err
	}
	ev.Links = links[ev.ID]

	return ev, nil
}

// --- helpers ---

// scanner is satisfied by both pgx.Row and pgx.Rows.
type scanner interface {
	Scan(dest ...any) error
}

// scanEventFields scans the selected change_events columns into a ChangeEvent.
func scanEventFields(sc scanner) (*model.ChangeEvent, error) {
	var ev model.ChangeEvent
	var externalID *string
	var parentID *string
	var userProvider *string
	var userSubject *string

	err := sc.Scan(
		&ev.ID,
		&externalID,
		&parentID,
		&ev.UserName,
		&userProvider,
		&userSubject,
		&ev.Timestamp,
		&ev.EventType,
		&ev.Description,
		&ev.LongDescription,
		&ev.CreatedAt,
	)
	if err != nil {
		return nil, err
	}

	// Convert nullable external_id to string (empty when NULL).
	if externalID != nil {
		ev.ExternalID = *externalID
	}

	// Convert nullable parent_id to string (empty when NULL).
	if parentID != nil {
		ev.ParentID = *parentID
	}
	if userProvider != nil {
		ev.UserProvider = *userProvider
	}
	if userSubject != nil {
		ev.UserSubject = *userSubject
	}

	ev.Timestamp = canonicalTime(ev.Timestamp)
	ev.CreatedAt = canonicalTime(ev.CreatedAt)

	return &ev, nil
}

// scanEvent scans from a pgx.Row, returning (nil, nil) on ErrNoRows.
func scanEvent(row pgx.Row) (*model.ChangeEvent, error) {
	ev, err := scanEventFields(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan event: %w", err)
	}
	return ev, nil
}

// scanEventFromRows scans from pgx.Rows (the cursor is already on a valid row).
func scanEventFromRows(rows pgx.Rows) (*model.ChangeEvent, error) {
	return scanEventFields(rows)
}

// insertTags inserts all tags for an event within the given transaction.
func insertTags(ctx context.Context, tx pgx.Tx, eventID string, tags map[string]string) error {
	if len(tags) == 0 {
		return nil
	}

	for k, v := range tags {
		if _, err := tx.Exec(ctx, `INSERT INTO change_event_tags (event_id, key, value) VALUES ($1, $2, $3)`, eventID, k, v); err != nil {
			return fmt.Errorf("insert tag %q: %w", k, err)
		}
	}

	return nil
}

func insertLinks(ctx context.Context, tx pgx.Tx, eventID string, links []model.EventLink) error {
	if len(links) == 0 {
		return nil
	}

	for position, link := range links {
		if _, err := tx.Exec(ctx, `INSERT INTO change_event_links (event_id, position, label, url) VALUES ($1, $2, $3, $4)`, eventID, position, link.Label, link.URL); err != nil {
			return fmt.Errorf("insert link %d: %w", position, err)
		}
	}
	return nil
}

// loadTagsForEvents fetches tags for the given event IDs in one query.
func (s *Store) loadTagsForEvents(ctx context.Context, ids []string) (map[string]map[string]string, error) {
	if len(ids) == 0 {
		return make(map[string]map[string]string), nil
	}

	rows, err := s.pool.Query(ctx, `SELECT event_id, key, value FROM change_event_tags WHERE event_id = ANY($1)`, ids)
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

func (s *Store) loadLinksForEvents(ctx context.Context, ids []string) (map[string][]model.EventLink, error) {
	if len(ids) == 0 {
		return make(map[string][]model.EventLink), nil
	}

	rows, err := s.pool.Query(ctx, `SELECT event_id, label, url FROM change_event_links WHERE event_id = ANY($1) ORDER BY event_id, position`, ids)
	if err != nil {
		return nil, fmt.Errorf("load links: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]model.EventLink)
	for rows.Next() {
		var eventID string
		var link model.EventLink
		if err := rows.Scan(&eventID, &link.Label, &link.URL); err != nil {
			return nil, fmt.Errorf("scan link: %w", err)
		}
		result[eventID] = append(result[eventID], link)
	}
	return result, rows.Err()
}

// buildWhereClause constructs the WHERE clause and parameter list for List queries.
//
//nolint:gocognit // flat clause-per-filter construction; splitting it would hide the placeholder numbering each clause shares through bind.
func buildWhereClause(params model.ListParams) (string, []any) {
	clauses := make([]string, 0)
	args := make([]any, 0)

	if params.ParentID != "" {
		clauses = append(clauses, "parent_id = "+bind(&args, params.ParentID))
	}

	// Around+Window takes precedence over StartAfter/StartBefore when set.
	if params.Around != nil && params.Window != nil && *params.Window > 0 {
		windowStart := params.Around.Add(-*params.Window)
		windowEnd := params.Around.Add(*params.Window)
		clauses = append(clauses, "timestamp >= "+bind(&args, canonicalTime(windowStart)))
		clauses = append(clauses, "timestamp < "+bind(&args, canonicalTime(windowEnd)))
	} else {
		if params.StartAfter != nil {
			clauses = append(clauses, "timestamp >= "+bind(&args, canonicalTime(*params.StartAfter)))
		}

		if params.StartBefore != nil {
			clauses = append(clauses, "timestamp < "+bind(&args, canonicalTime(*params.StartBefore)))
		}
	}

	if params.UserName != "" {
		clauses = append(clauses, "user_name = "+bind(&args, params.UserName))
	}

	if params.EventType != "" {
		clauses = append(clauses, "event_type = "+bind(&args, params.EventType))
	}
	if params.TopLevel {
		clauses = append(clauses, "parent_id IS NULL")
	}

	if params.AlertedOnly {
		// Find events whose most recent alert/clear-alert meta-event is "alert".
		// This subquery gets parent IDs where the latest annotation is an active alert.
		clauses = append(clauses, `id IN (
			SELECT parent_id FROM change_events AS meta
			WHERE meta.event_type IN ('alert', 'clear-alert')
			AND meta.parent_id IS NOT NULL
			AND NOT EXISTS (
				SELECT 1 FROM change_events AS newer
				WHERE newer.parent_id = meta.parent_id
				AND newer.event_type IN ('alert', 'clear-alert')
				AND newer.ingest_sequence > meta.ingest_sequence
			)
			AND meta.event_type = 'alert'
		)`)
	}

	if len(params.Tags) > 0 {
		tagClauses := make([]string, 0, len(params.Tags))
		for k, v := range params.Tags {
			keyParam := bind(&args, k)
			valueParam := bind(&args, v)
			tagClauses = append(tagClauses, "(key = "+keyParam+" AND value = "+valueParam+")")
		}
		subquery := fmt.Sprintf(
			"id IN (SELECT event_id FROM change_event_tags WHERE %s GROUP BY event_id HAVING COUNT(DISTINCT key) = %s)",
			strings.Join(tagClauses, " OR "),
			bind(&args, len(params.Tags)),
		)
		clauses = append(clauses, subquery)
	}

	if len(clauses) == 0 {
		return "", make([]any, 0)
	}

	return " WHERE " + strings.Join(clauses, " AND "), args
}

func buildCurrentWhereClause(params model.CurrentParams) (string, []any) {
	clauses := []string{"active.representative_rank = 1"}
	args := make([]any, 0)

	if params.ForTeam != "" {
		clauses = append(clauses, "(team.value = "+bind(&args, params.ForTeam)+" OR NULLIF(team.value, '') IS NULL OR scope.value = 'site')")
	}
	if len(params.Scopes) > 0 {
		placeholders := make([]string, len(params.Scopes))
		for i, scope := range params.Scopes {
			placeholders[i] = bind(&args, scope)
		}
		clauses = append(clauses, "scope.value IN ("+strings.Join(placeholders, ",")+")")
	}
	if len(params.Severities) > 0 {
		placeholders := make([]string, len(params.Severities))
		for i, severity := range params.Severities {
			placeholders[i] = bind(&args, strings.ToLower(severity))
		}
		clauses = append(clauses, "LOWER(severity.value) IN ("+strings.Join(placeholders, ",")+")")
	}
	if params.EventType != "" {
		clauses = append(clauses, "event.event_type = "+bind(&args, params.EventType))
	}
	if params.CorrelationKey != "" {
		clauses = append(clauses, "active.correlation_key = "+bind(&args, params.CorrelationKey))
	}
	if params.CorrelationValue != "" {
		clauses = append(clauses, "active.correlation_value = "+bind(&args, params.CorrelationValue))
	}

	return " WHERE " + strings.Join(clauses, " AND "), args
}

func bind(args *[]any, value any) string {
	*args = append(*args, value)
	return fmt.Sprintf("$%d", len(*args))
}

func canonicalTime(t time.Time) time.Time {
	return t.UTC().Truncate(time.Second)
}

// nullableString returns a *string pointer for use with SQL parameters.
// It returns nil when s is empty, so the column is stored as NULL.
func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// isUniqueViolation reports whether an external ID already exists. Other
// unique failures are programming or schema errors and must remain visible.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "idx_change_events_external_id"
}
