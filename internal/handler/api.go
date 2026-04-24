package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/sarah/go-prod-change-registry/internal/model"
	"github.com/sarah/go-prod-change-registry/internal/service"
	"github.com/sarah/go-prod-change-registry/internal/store"
)

// Pinger tests database connectivity.
type Pinger interface {
	PingContext(ctx context.Context) error
}

// APIHandler serves the REST/JSON API.
type APIHandler struct {
	svc *service.ChangeService
	db  Pinger
}

// NewAPIHandler creates an APIHandler. The db parameter is used for health checks.
func NewAPIHandler(svc *service.ChangeService, db Pinger) *APIHandler {
	return &APIHandler{svc: svc, db: db}
}

// HealthCheck verifies that the service is running and the database is reachable.
func (h *APIHandler) HealthCheck(w http.ResponseWriter, r *http.Request) {
	if err := h.db.PingContext(r.Context()); err != nil {
		slog.ErrorContext(r.Context(), "health check failed: database unreachable", "error", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		if encErr := json.NewEncoder(w).Encode(map[string]string{
			"status": "unhealthy",
			"reason": "database unreachable",
		}); encErr != nil {
			slog.ErrorContext(r.Context(), "health check response encode error", "error", encErr)
		}
		return
	}

	writeJSON(r.Context(), w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *APIHandler) CreateEvent(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	// Close errors here are unactionable -- the request is over either way.
	defer func() { _ = r.Body.Close() }()

	ctx := r.Context()

	var req model.CreateChangeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if err.Error() == "http: request body too large" {
			writeError(ctx, w, http.StatusRequestEntityTooLarge, "body_too_large", "request body exceeds 1MB limit")
			return
		}
		writeError(ctx, w, http.StatusBadRequest, "invalid_body", "invalid JSON request body")
		return
	}

	event, err := h.svc.Create(ctx, &req)
	if errors.Is(err, store.ErrDuplicate) {
		writeJSON(ctx, w, http.StatusOK, event)
		return
	}
	if err != nil {
		mapServiceError(ctx, w, err)
		return
	}

	w.Header().Set("Location", "/api/v1/events/"+event.ID)
	writeJSON(ctx, w, http.StatusCreated, event)
}

func (h *APIHandler) GetEvent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx := r.Context()

	event, err := h.svc.GetByID(ctx, id)
	if err != nil {
		mapServiceError(ctx, w, err)
		return
	}

	writeJSON(ctx, w, http.StatusOK, event)
}

func (h *APIHandler) GetEventAnnotations(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx := r.Context()

	annotations, err := h.svc.GetAnnotations(ctx, id)
	if err != nil {
		mapServiceError(ctx, w, err)
		return
	}

	writeJSON(ctx, w, http.StatusOK, annotations)
}

func (h *APIHandler) ListEvents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	params, perr := parseListParams(r.URL.Query())
	if perr != nil {
		writeError(ctx, w, http.StatusBadRequest, perr.code, perr.message)
		return
	}

	result, err := h.svc.List(ctx, params)
	if err != nil {
		slog.ErrorContext(ctx, "list events error", "error", err)
		writeError(ctx, w, http.StatusInternalServerError, "internal_error", "an internal error occurred")
		return
	}

	writeJSON(ctx, w, http.StatusOK, result)
}

func (h *APIHandler) ToggleStar(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx := r.Context()

	// Use a default user name for API star toggles.
	userName := "api"

	metaEvent, err := h.svc.ToggleStar(ctx, id, userName)
	if err != nil {
		mapServiceError(ctx, w, err)
		return
	}

	writeJSON(ctx, w, http.StatusCreated, metaEvent)
}

// mapServiceError maps service-layer errors to HTTP responses.
func mapServiceError(ctx context.Context, w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrUserNameRequired):
		writeError(ctx, w, http.StatusBadRequest, "validation_error", "user_name is required")
	case errors.Is(err, service.ErrEventTypeRequired):
		writeError(ctx, w, http.StatusBadRequest, "validation_error", "event_type is required")
	case errors.Is(err, service.ErrEventNotFound):
		writeError(ctx, w, http.StatusNotFound, "not_found", "event not found")
	case errors.Is(err, service.ErrParentNotFound):
		writeError(ctx, w, http.StatusBadRequest, "validation_error", "parent event not found")
	default:
		slog.ErrorContext(ctx, "internal error", "error", err)
		writeError(ctx, w, http.StatusInternalServerError, "internal_error", "an internal error occurred")
	}
}

// writeJSON encodes data as a JSON response. If encoding fails after the
// status header has been committed there is nothing useful to send to the
// client, so the failure is logged for operators.
func writeJSON(ctx context.Context, w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		slog.ErrorContext(ctx, "json response encode error", "error", err, "status", status)
	}
}

func writeError(ctx context.Context, w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	}); err != nil {
		slog.ErrorContext(ctx, "json error response encode error", "error", err, "status", status, "code", code)
	}
}
