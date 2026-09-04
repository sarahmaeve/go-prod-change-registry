package handler

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/sarahmaeve/go-prod-change-registry/internal/middleware"
	"github.com/sarahmaeve/go-prod-change-registry/internal/model"
	"github.com/sarahmaeve/go-prod-change-registry/internal/service"
	"github.com/sarahmaeve/go-prod-change-registry/internal/store"
)

type recordChangeForm struct {
	EventType       string
	Description     string
	LongDescription string
	ExternalID      string
	Tags            string
	LinkLabel       string
	LinkURL         string
}

type recordChangeData struct {
	RefreshSec int
	UserName   string
	LogoutCSRF string
	CSRFToken  string
	Form       recordChangeForm
	Error      string
}

// ShowCreateEvent renders the authenticated record-change form.
func (h *DashboardHandler) ShowCreateEvent(w http.ResponseWriter, r *http.Request) {
	h.renderCreateEvent(w, r, http.StatusOK, recordChangeForm{}, "")
}

// CreateEvent records a top-level event attributed to the authenticated human.
func (h *DashboardHandler) CreateEvent(w http.ResponseWriter, r *http.Request) {
	if !h.validateCreateEventForm(w, r) {
		return
	}
	user, ok := humanIdentity(r)
	if !ok {
		http.Error(w, "Authentication required", http.StatusUnauthorized)
		return
	}

	form := recordChangeFormFromRequest(r)
	request, message := createChangeRequest(form, user)
	if message != "" {
		h.renderCreateEvent(w, r, http.StatusBadRequest, form, message)
		return
	}

	event, err := h.svc.Create(r.Context(), request)
	if err != nil && !errors.Is(err, store.ErrDuplicate) {
		switch {
		case errors.Is(err, service.ErrInvalidLink):
			h.renderCreateEvent(w, r, http.StatusBadRequest, form, invalidLinkMessage)
			return
		case errors.Is(err, service.ErrInvalidTags):
			h.renderCreateEvent(w, r, http.StatusBadRequest, form, err.Error())
			return
		}
		slog.ErrorContext(r.Context(), "dashboard create event error", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if event == nil {
		slog.ErrorContext(r.Context(), "dashboard create event returned no event", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	logHumanAction(r, "create_event", user, event.ID)
	http.Redirect(w, r, "/events/"+url.PathEscape(event.ID), http.StatusSeeOther)
}

func (h *DashboardHandler) validateCreateEventForm(w http.ResponseWriter, r *http.Request) bool {
	if !parseBoundedPostFormLimit(w, r, maxRecordFormBytes) {
		return false
	}
	if !middleware.ValidateCSRFToken(h.sessionSecret, humanSessionNonce(r), r.PostFormValue("csrf_token")) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return false
	}
	return true
}

func recordChangeFormFromRequest(r *http.Request) recordChangeForm {
	return recordChangeForm{
		EventType:       strings.TrimSpace(r.PostFormValue("event_type")),
		Description:     strings.TrimSpace(r.PostFormValue("description")),
		LongDescription: r.PostFormValue("long_description"),
		ExternalID:      strings.TrimSpace(r.PostFormValue("external_id")),
		Tags:            r.PostFormValue("tags"),
		LinkLabel:       strings.TrimSpace(r.PostFormValue("link_label")),
		LinkURL:         strings.TrimSpace(r.PostFormValue("link_url")),
	}
}

func createChangeRequest(form recordChangeForm, user model.UserIdentity) (*model.CreateChangeRequest, string) {
	if form.EventType == "" {
		return nil, "Event type is required."
	}
	if form.Description == "" {
		return nil, "Summary is required."
	}

	tags, message := parseRecordChangeTags(form.Tags)
	if message != "" {
		return nil, message
	}

	var links []model.EventLink
	if form.LinkLabel != "" || form.LinkURL != "" {
		links = []model.EventLink{{Label: form.LinkLabel, URL: form.LinkURL}}
	}
	return &model.CreateChangeRequest{
		ExternalID:      form.ExternalID,
		UserName:        user.Name,
		UserProvider:    user.Provider,
		UserSubject:     user.Subject,
		EventType:       form.EventType,
		Description:     form.Description,
		LongDescription: form.LongDescription,
		Links:           links,
		Tags:            tags,
	}, ""
}

func parseRecordChangeTags(value string) (map[string]string, string) {
	if strings.TrimSpace(value) == "" {
		return nil, ""
	}

	tags := make(map[string]string)
	for lineNumber, line := range strings.Split(value, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, tagValue, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		tagValue = strings.TrimSpace(tagValue)
		if !ok || key == "" {
			return nil, "Tags must use one key=value pair per line."
		}
		if _, exists := tags[key]; exists {
			return nil, fmt.Sprintf("Tag key %q is repeated on line %d.", key, lineNumber+1)
		}
		tags[key] = tagValue
	}
	return tags, ""
}

func (h *DashboardHandler) renderCreateEvent(w http.ResponseWriter, r *http.Request, status int, form recordChangeForm, message string) {
	data := recordChangeData{
		UserName:   humanUserName(r),
		LogoutCSRF: middleware.GenerateCSRFToken(h.sessionSecret, humanSessionNonce(r)),
		CSRFToken:  middleware.GenerateCSRFToken(h.sessionSecret, humanSessionNonce(r)),
		Form:       form,
		Error:      message,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := h.recordTmpl.ExecuteTemplate(w, "layout", data); err != nil {
		slog.ErrorContext(r.Context(), "record change template execute error", "error", err)
	}
}
