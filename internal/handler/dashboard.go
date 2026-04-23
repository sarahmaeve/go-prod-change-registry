package handler

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sarah/go-prod-change-registry/internal/middleware"
	"github.com/sarah/go-prod-change-registry/internal/model"
	"github.com/sarah/go-prod-change-registry/internal/service"
	"github.com/sarah/go-prod-change-registry/web"
)

// DashboardHandler serves the server-rendered HTML dashboard.
type DashboardHandler struct {
	svc           *service.ChangeService
	sessionSecret []byte
	refreshSec    int
	dashboardTmpl *template.Template
	detailTmpl    *template.Template
}

// NewDashboardHandler parses the embedded templates and returns a ready handler.
func NewDashboardHandler(svc *service.ChangeService, refreshSec int, sessionSecret []byte) *DashboardHandler {
	funcMap := template.FuncMap{
		"formatTime": func(t time.Time) string {
			return t.UTC().Format("2006-01-02 15:04:05 UTC")
		},
		"formatTags": func(tags map[string]string) []string {
			if len(tags) == 0 {
				return []string{}
			}
			out := make([]string, 0, len(tags))
			for k, v := range tags {
				out = append(out, k+"="+v)
			}
			sort.Strings(out)
			return out
		},
		"tagFilterURL": func(key, value string) string {
			q := url.Values{}
			q.Set("tag", key+":"+value)
			return "/?" + q.Encode()
		},
	}

	// Parse each page template separately with the shared layout
	// to avoid "content" block name collisions.
	dashboardTmpl := template.Must(
		template.New("").Funcs(funcMap).ParseFS(
			web.TemplateFS,
			"templates/layout.html",
			"templates/dashboard.html",
		),
	)
	detailTmpl := template.Must(
		template.New("").Funcs(funcMap).ParseFS(
			web.TemplateFS,
			"templates/layout.html",
			"templates/detail.html",
		),
	)

	return &DashboardHandler{
		svc:           svc,
		sessionSecret: sessionSecret,
		refreshSec:    refreshSec,
		dashboardTmpl: dashboardTmpl,
		detailTmpl:    detailTmpl,
	}
}

// dashboardFilters holds the current filter values for re-populating the form.
type dashboardFilters struct {
	Range       string
	StartAfter  string
	StartBefore string
	EventType   string
	UserName    string
	Alerted     bool
	Tags        []string
}

// dashboardEvent wraps a ChangeEvent with its derived annotation state.
type dashboardEvent struct {
	model.ChangeEvent
	Starred bool
	Alerted bool
}

// dashboardData is the template data for the dashboard page.
type dashboardData struct {
	RefreshSec  int
	CSRFToken   string
	Events      []dashboardEvent
	Filters     dashboardFilters
	TotalCount  int
	Limit       int
	Offset      int
	HasPrev     bool
	HasNext     bool
	PrevURL     string
	NextURL     string
	OffsetStart int
	OffsetEnd   int
}

// detailData is the template data for the detail page.
type detailData struct {
	RefreshSec  int
	Event       *model.ChangeEvent
	Annotations *model.EventAnnotations
}

// quickRanges maps the quick-select range values to durations.
var quickRanges = map[string]time.Duration{
	"5m":  5 * time.Minute,
	"30m": 30 * time.Minute,
	"1h":  time.Hour,
	"24h": 24 * time.Hour,
}

// Dashboard handles GET / and renders the event list.
func (h *DashboardHandler) Dashboard(w http.ResponseWriter, r *http.Request) {
	params, filters := parseDashboardRequest(r)

	result, err := h.svc.List(r.Context(), params)
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	annotations, err := h.fetchAnnotations(r.Context(), result.Events)
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	offsetStart := params.Offset + 1
	if result.TotalCount == 0 {
		offsetStart = 0
	}

	data := dashboardData{
		RefreshSec:  h.refreshSec,
		CSRFToken:   middleware.GenerateCSRFToken(h.sessionSecret, middleware.SessionNonce(r)),
		Events:      buildDashboardEvents(result.Events, annotations),
		Filters:     filters,
		TotalCount:  result.TotalCount,
		Limit:       result.Limit,
		Offset:      result.Offset,
		HasPrev:     params.Offset > 0,
		HasNext:     params.Offset+params.Limit < result.TotalCount,
		PrevURL:     h.paginationURL(r, params.Offset-params.Limit, params.Limit),
		NextURL:     h.paginationURL(r, params.Offset+params.Limit, params.Limit),
		OffsetStart: offsetStart,
		OffsetEnd:   params.Offset + len(result.Events),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.dashboardTmpl.ExecuteTemplate(w, "layout", data); err != nil {
		http.Error(w, "Template error", http.StatusInternalServerError)
	}
}

// fetchAnnotations resolves annotations for every event in the slice in a
// single batch call. Returns an empty map when there are no events, so
// callers never have to guard the lookup.
func (h *DashboardHandler) fetchAnnotations(ctx context.Context, events []model.ChangeEvent) (map[string]*model.EventAnnotations, error) {
	if len(events) == 0 {
		return map[string]*model.EventAnnotations{}, nil
	}
	ids := make([]string, len(events))
	for i, ev := range events {
		ids[i] = ev.ID
	}
	return h.svc.GetAnnotationsBatch(ctx, ids)
}

// buildDashboardEvents pairs each event with its annotation state. Events
// with no annotations (or a nil entry in the map) are returned with
// Starred/Alerted = false.
func buildDashboardEvents(events []model.ChangeEvent, annotations map[string]*model.EventAnnotations) []dashboardEvent {
	out := make([]dashboardEvent, len(events))
	for i, ev := range events {
		de := dashboardEvent{ChangeEvent: ev}
		if ann, ok := annotations[ev.ID]; ok && ann != nil {
			de.Starred = ann.Starred
			de.Alerted = ann.Alerted
		}
		out[i] = de
	}
	return out
}

// Detail handles GET /events/{id} and renders the event detail page.
func (h *DashboardHandler) Detail(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	event, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, service.ErrEventNotFound) {
			http.Error(w, "Event not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	annotations, err := h.svc.GetAnnotations(r.Context(), id)
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	data := detailData{
		RefreshSec:  0,
		Event:       event,
		Annotations: annotations,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.detailTmpl.ExecuteTemplate(w, "layout", data); err != nil {
		http.Error(w, "Template error", http.StatusInternalServerError)
	}
}

// ToggleStar handles POST /events/{id}/star -- posts a meta-event and redirects back.
func (h *DashboardHandler) ToggleStar(w http.ResponseWriter, r *http.Request) {
	// Validate CSRF token from form submission.
	nonce := middleware.SessionNonce(r)
	csrfToken := r.FormValue("csrf_token")
	if !middleware.ValidateCSRFToken(h.sessionSecret, nonce, csrfToken) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	id := chi.URLParam(r, "id")

	_, err := h.svc.ToggleStar(r.Context(), id, "dashboard-user")
	if err != nil {
		if errors.Is(err, service.ErrEventNotFound) {
			http.Error(w, "Event not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	redirect := "/"
	if referer := r.Header.Get("Referer"); referer != "" {
		// security: only use the path to prevent open redirects to external hosts.
		if u, err := url.Parse(referer); err == nil && u.Path != "" {
			redirect = u.Path
			if u.RawQuery != "" {
				redirect += "?" + u.RawQuery
			}
		}
	}
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

// paginationURL builds a URL preserving current query params but updating offset and limit.
func (h *DashboardHandler) paginationURL(r *http.Request, newOffset, limit int) string {
	if newOffset < 0 {
		newOffset = 0
	}
	q := url.Values{}
	for key, vals := range r.URL.Query() {
		if key == "offset" || key == "limit" {
			continue
		}
		for _, v := range vals {
			q.Add(key, v)
		}
	}
	q.Set("offset", strconv.Itoa(newOffset))
	q.Set("limit", strconv.Itoa(limit))
	return fmt.Sprintf("/?%s", q.Encode())
}
