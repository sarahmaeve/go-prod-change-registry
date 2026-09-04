package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/sarahmaeve/go-prod-change-registry/internal/middleware"
	"github.com/sarahmaeve/go-prod-change-registry/internal/model"
	"github.com/sarahmaeve/go-prod-change-registry/internal/service"
	"github.com/sarahmaeve/go-prod-change-registry/internal/store"
)

// Pinger tests database connectivity.
type Pinger interface {
	Ping(ctx context.Context) error
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

// Liveness reports whether the HTTP process can serve requests. It deliberately
// avoids dependencies so an external outage does not trigger restart loops.
func (h *APIHandler) Liveness(w http.ResponseWriter, r *http.Request) {
	writeJSON(r.Context(), w, http.StatusOK, map[string]string{"status": "ok"})
}

// Readiness reports whether the service can reach PostgreSQL and accept traffic.
func (h *APIHandler) Readiness(w http.ResponseWriter, r *http.Request) {
	if err := h.db.Ping(r.Context()); err != nil {
		slog.ErrorContext(r.Context(), "readiness check failed: database unreachable", "error", err)
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

// HealthCheck preserves the original database-aware health endpoint.
func (h *APIHandler) HealthCheck(w http.ResponseWriter, r *http.Request) {
	h.Readiness(w, r)
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
	if identity, ok := authenticatedAPIIdentity(r); ok {
		req.UserName = identity.Name
		req.UserProvider = identity.Provider
		req.UserSubject = identity.Subject
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

func (h *APIHandler) GetEventActivity(w http.ResponseWriter, r *http.Request) {
	activity, err := h.svc.GetActivity(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		mapServiceError(r.Context(), w, err)
		return
	}
	writeJSON(r.Context(), w, http.StatusOK, activity)
}

func (h *APIHandler) AddEventLinks(w http.ResponseWriter, r *http.Request) {
	var req model.AddLinksRequest
	if !decodeJSONRequest(w, r, &req) {
		return
	}
	var event *model.ChangeEvent
	var err error
	if identity, ok := authenticatedAPIIdentity(r); ok {
		event, err = h.svc.AddLinksAs(r.Context(), chi.URLParam(r, "id"), identity, req.Links)
	} else {
		event, err = h.svc.AddLinks(r.Context(), chi.URLParam(r, "id"), req.UserName, req.Links)
	}
	if err != nil {
		mapServiceError(r.Context(), w, err)
		return
	}
	writeJSON(r.Context(), w, http.StatusCreated, event)
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

// ListCurrent returns active logical operations derived from start and end phase events.
func (h *APIHandler) ListCurrent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	params, paramErr := parseCurrentParams(r.URL.Query())
	if paramErr != nil {
		writeError(ctx, w, http.StatusBadRequest, paramErr.code, paramErr.message)
		return
	}

	result, err := h.svc.ListCurrent(ctx, params)
	if err != nil {
		slog.ErrorContext(ctx, "list current events error", "error", err)
		writeError(ctx, w, http.StatusInternalServerError, "internal_error", "an internal error occurred")
		return
	}

	writeJSON(ctx, w, http.StatusOK, result)
}

func (h *APIHandler) ToggleStar(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx := r.Context()

	var metaEvent *model.ChangeEvent
	var err error
	if identity, ok := authenticatedAPIIdentity(r); ok {
		metaEvent, err = h.svc.ToggleStarAs(ctx, id, identity)
	} else {
		metaEvent, err = h.svc.ToggleStar(ctx, id, "api")
	}
	if err != nil {
		mapServiceError(ctx, w, err)
		return
	}

	writeJSON(ctx, w, http.StatusCreated, metaEvent)
}

func (h *APIHandler) ToggleAlert(w http.ResponseWriter, r *http.Request) {
	var metaEvent *model.ChangeEvent
	var err error
	if identity, ok := authenticatedAPIIdentity(r); ok {
		metaEvent, err = h.svc.ToggleAlertAs(r.Context(), chi.URLParam(r, "id"), identity)
	} else {
		metaEvent, err = h.svc.ToggleAlert(r.Context(), chi.URLParam(r, "id"), "api")
	}
	if err != nil {
		mapServiceError(r.Context(), w, err)
		return
	}
	writeJSON(r.Context(), w, http.StatusCreated, metaEvent)
}

func (h *APIHandler) CloseOperation(w http.ResponseWriter, r *http.Request) {
	var req model.CloseOperationRequest
	if !decodeJSONRequest(w, r, &req) {
		return
	}
	var event *model.ChangeEvent
	var err error
	if identity, ok := authenticatedAPIIdentity(r); ok {
		event, err = h.svc.CloseOperationAs(r.Context(), chi.URLParam(r, "id"), identity, req.Description)
	} else {
		event, err = h.svc.CloseOperation(r.Context(), chi.URLParam(r, "id"), req.UserName, req.Description)
	}
	if err != nil {
		mapServiceError(r.Context(), w, err)
		return
	}
	writeJSON(r.Context(), w, http.StatusCreated, event)
}

func authenticatedAPIIdentity(r *http.Request) (model.UserIdentity, bool) {
	principal, ok := middleware.APIPrincipalFromContext(r.Context())
	if !ok {
		return model.UserIdentity{}, false
	}
	return model.UserIdentity{
		Name:     principal.UserName,
		Provider: principal.Provider,
		Subject:  principal.Subject,
	}, true
}

// mapServiceError maps service-layer errors to HTTP responses.
func mapServiceError(ctx context.Context, w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrUserNameRequired):
		writeError(ctx, w, http.StatusBadRequest, "validation_error", "user_name is required")
	case errors.Is(err, service.ErrEventTypeRequired):
		writeError(ctx, w, http.StatusBadRequest, "validation_error", "event_type is required")
	case errors.Is(err, service.ErrInvalidTags):
		writeError(ctx, w, http.StatusBadRequest, "validation_error", err.Error())
	case errors.Is(err, service.ErrInvalidLink):
		writeError(ctx, w, http.StatusBadRequest, "validation_error", err.Error())
	case errors.Is(err, service.ErrLinksRequired):
		writeError(ctx, w, http.StatusBadRequest, "validation_error", err.Error())
	case errors.Is(err, service.ErrEventNotFound):
		writeError(ctx, w, http.StatusNotFound, "not_found", "event not found")
	case errors.Is(err, service.ErrParentNotFound):
		writeError(ctx, w, http.StatusBadRequest, "validation_error", "parent event not found")
	case errors.Is(err, service.ErrOperationNotClosable):
		writeError(ctx, w, http.StatusBadRequest, "validation_error", err.Error())
	case errors.Is(err, service.ErrOperationClosed):
		writeError(ctx, w, http.StatusConflict, "operation_closed", err.Error())
	default:
		slog.ErrorContext(ctx, "internal error", "error", err)
		writeError(ctx, w, http.StatusInternalServerError, "internal_error", "an internal error occurred")
	}
}

func decodeJSONRequest(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	defer func() { _ = r.Body.Close() }()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			writeError(r.Context(), w, http.StatusRequestEntityTooLarge, "body_too_large", "request body exceeds 1MB limit")
			return false
		}
		writeError(r.Context(), w, http.StatusBadRequest, "invalid_body", "invalid JSON request body")
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(r.Context(), w, http.StatusBadRequest, "invalid_body", "request body must contain one JSON value")
		return false
	}
	return true
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
