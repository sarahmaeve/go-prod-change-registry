package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sarah/go-prod-change-registry/internal/model"
	"github.com/sarah/go-prod-change-registry/internal/service"
)

// APIHandler exposes HTTP endpoints for managing production change events.
type APIHandler struct {
	svc *service.ChangeService
}

// NewAPIHandler returns a new APIHandler backed by the given service.
func NewAPIHandler(svc *service.ChangeService) *APIHandler {
	return &APIHandler{svc: svc}
}

// HealthCheck returns a simple status response.
func (h *APIHandler) HealthCheck(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ListEvents handles GET requests with optional query-param filters and pagination.
func (h *APIHandler) ListEvents(w http.ResponseWriter, r *http.Request) {
	var params model.ListParams

	if v := r.URL.Query().Get("start_after"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_parameter", "start_after must be RFC3339")
			return
		}
		params.StartAfter = &t
	}

	if v := r.URL.Query().Get("start_before"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_parameter", "start_before must be RFC3339")
			return
		}
		params.StartBefore = &t
	}

	if v := r.URL.Query().Get("user"); v != "" {
		params.UserName = v
	}

	if v := r.URL.Query().Get("type"); v != "" {
		params.EventType = v
	}

	if tags := r.URL.Query()["tag"]; len(tags) > 0 {
		params.Tags = make(map[string]string, len(tags))
		for _, tag := range tags {
			key, value, ok := strings.Cut(tag, ":")
			if !ok {
				writeError(w, http.StatusBadRequest, "invalid_parameter",
					fmt.Sprintf("tag %q must be in key:value format", tag))
				return
			}
			params.Tags[key] = value
		}
	}

	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_parameter", "limit must be an integer")
			return
		}
		params.Limit = n
	}

	if v := r.URL.Query().Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_parameter", "offset must be an integer")
			return
		}
		params.Offset = n
	}

	result, err := h.svc.List(r.Context(), params)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// CreateEvent handles POST requests to create a new change event.
func (h *APIHandler) CreateEvent(w http.ResponseWriter, r *http.Request) {
	var req model.CreateChangeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "could not parse JSON request body")
		return
	}

	event, err := h.svc.Create(r.Context(), &req)
	if err != nil {
		mapServiceError(w, err)
		return
	}

	w.Header().Set("Location", "/api/v1/events/"+event.ID)
	writeJSON(w, http.StatusCreated, event)
}

// GetEvent handles GET requests for a single change event by ID.
func (h *APIHandler) GetEvent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	event, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		mapServiceError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, event)
}

// UpdateEvent handles PATCH/PUT requests to partially update a change event.
func (h *APIHandler) UpdateEvent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var req model.UpdateChangeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "could not parse JSON request body")
		return
	}

	event, err := h.svc.Update(r.Context(), id, &req)
	if err != nil {
		mapServiceError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, event)
}

// DeleteEvent handles DELETE requests to remove a change event.
func (h *APIHandler) DeleteEvent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	if err := h.svc.Delete(r.Context(), id); err != nil {
		mapServiceError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ToggleStar toggles the Starred flag on a change event.
func (h *APIHandler) ToggleStar(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	event, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		mapServiceError(w, err)
		return
	}

	newStarred := !event.Starred
	req := &model.UpdateChangeRequest{Starred: &newStarred}
	updated, err := h.svc.Update(r.Context(), id, req)
	if err != nil {
		mapServiceError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, updated)
}

// mapServiceError translates known service-layer errors into appropriate HTTP responses.
func mapServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrUserNameRequired):
		writeError(w, http.StatusBadRequest, "validation_error", err.Error())
	case errors.Is(err, service.ErrEventNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
	}
}

// writeJSON serialises data as JSON and writes it with the given status code.
func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

// writeError writes a structured error response.
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
