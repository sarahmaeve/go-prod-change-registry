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
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status": "unhealthy",
			"reason": "database unreachable",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *APIHandler) CreateEvent(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	defer r.Body.Close()

	var req model.CreateChangeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if err.Error() == "http: request body too large" {
			writeError(w, http.StatusRequestEntityTooLarge, "body_too_large", "request body exceeds 1MB limit")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_body", "invalid JSON request body")
		return
	}

	event, err := h.svc.Create(r.Context(), &req)
	if errors.Is(err, store.ErrDuplicate) {
		writeJSON(w, http.StatusOK, event)
		return
	}
	if err != nil {
		mapServiceError(w, err)
		return
	}

	w.Header().Set("Location", "/api/v1/events/"+event.ID)
	writeJSON(w, http.StatusCreated, event)
}

func (h *APIHandler) GetEvent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	event, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		mapServiceError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, event)
}

func (h *APIHandler) GetEventAnnotations(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	annotations, err := h.svc.GetAnnotations(r.Context(), id)
	if err != nil {
		mapServiceError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, annotations)
}

func (h *APIHandler) ListEvents(w http.ResponseWriter, r *http.Request) {
	params, perr := parseListParams(r.URL.Query())
	if perr != nil {
		writeError(w, http.StatusBadRequest, perr.code, perr.message)
		return
	}

	result, err := h.svc.List(r.Context(), params)
	if err != nil {
		slog.ErrorContext(r.Context(), "list events error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "an internal error occurred")
		return
	}

	writeJSON(w, http.StatusOK, result)
}

func (h *APIHandler) ToggleStar(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// Use a default user name for API star toggles.
	userName := "api"

	metaEvent, err := h.svc.ToggleStar(r.Context(), id, userName)
	if err != nil {
		mapServiceError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, metaEvent)
}

// mapServiceError maps service-layer errors to HTTP responses.
func mapServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrUserNameRequired):
		writeError(w, http.StatusBadRequest, "validation_error", "user_name is required")
	case errors.Is(err, service.ErrEventTypeRequired):
		writeError(w, http.StatusBadRequest, "validation_error", "event_type is required")
	case errors.Is(err, service.ErrEventNotFound):
		writeError(w, http.StatusNotFound, "not_found", "event not found")
	case errors.Is(err, service.ErrParentNotFound):
		writeError(w, http.StatusBadRequest, "validation_error", "parent event not found")
	default:
		slog.Error("internal error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "an internal error occurred")
	}
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}
