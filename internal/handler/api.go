package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sarah/go-prod-change-registry/internal/model"
	"github.com/sarah/go-prod-change-registry/internal/service"
)

type APIHandler struct {
	svc *service.ChangeService
}

func NewAPIHandler(svc *service.ChangeService) *APIHandler {
	return &APIHandler{svc: svc}
}

func (h *APIHandler) HealthCheck(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *APIHandler) CreateEvent(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

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
	var params model.ListParams
	q := r.URL.Query()

	if v := q.Get("start_after"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_parameter", "start_after must be RFC3339 format")
			return
		}
		params.StartAfter = &t
	}

	if v := q.Get("start_before"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_parameter", "start_before must be RFC3339 format")
			return
		}
		params.StartBefore = &t
	}

	if v := q.Get("around"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_parameter", "around must be RFC3339 format")
			return
		}
		params.Around = &t

		windowStr := q.Get("window")
		if windowStr == "" {
			windowStr = "30m"
		}
		d, err := time.ParseDuration(windowStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_parameter", "window must be a valid duration (e.g., 30m, 1h)")
			return
		}
		params.Window = &d
	}

	params.UserName = q.Get("user")
	params.EventType = q.Get("type")

	if q.Get("top_level") == "true" {
		params.TopLevel = true
	}

	// Parse tag filters.
	for _, tv := range q["tag"] {
		if k, v, ok := strings.Cut(tv, ":"); ok && k != "" {
			if params.Tags == nil {
				params.Tags = make(map[string]string)
			}
			params.Tags[k] = v
		} else {
			writeError(w, http.StatusBadRequest, "invalid_parameter", "tag must be in key:value format")
			return
		}
	}

	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_parameter", "limit must be an integer")
			return
		}
		params.Limit = n
	}

	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_parameter", "offset must be an integer")
			return
		}
		params.Offset = n
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
