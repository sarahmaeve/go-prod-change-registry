package handler

import (
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sarah/go-prod-change-registry/internal/model"
	"github.com/sarah/go-prod-change-registry/internal/service"
	"github.com/sarah/go-prod-change-registry/web"
)

// DashboardHandler serves the server-rendered HTML dashboard.
type DashboardHandler struct {
	svc           *service.ChangeService
	refreshSec    int
	dashboardTmpl *template.Template
	detailTmpl    *template.Template
}

// NewDashboardHandler parses the embedded templates and returns a ready handler.
func NewDashboardHandler(svc *service.ChangeService, refreshSec int) *DashboardHandler {
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
		"tagFilterURL": func(key, value, token string) string {
			q := url.Values{}
			q.Set("tag", key+":"+value)
			if token != "" {
				q.Set("token", token)
			}
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
	Token       string
}

// detailData is the template data for the detail page.
type detailData struct {
	RefreshSec  int
	Event       *model.ChangeEvent
	Annotations *model.EventAnnotations
	Token       string
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
	var params model.ListParams
	filters := dashboardFilters{}

	// Dashboard shows only top-level events (not meta-events).
	params.TopLevel = true

	// Handle time range: quick-select presets or custom datetime range.
	// Default to last 24 hours when no range is specified.
	rangeVal := r.URL.Query().Get("range")
	if rangeVal == "" {
		rangeVal = "24h"
	}
	filters.Range = rangeVal

	if d, ok := quickRanges[rangeVal]; ok {
		startAfter := time.Now().UTC().Add(-d)
		params.StartAfter = &startAfter
	} else if rangeVal == "custom" {
		if v := r.URL.Query().Get("start_after"); v != "" {
			filters.StartAfter = v
			t, err := time.Parse("2006-01-02T15:04", v)
			if err == nil {
				params.StartAfter = &t
			}
		}
		if v := r.URL.Query().Get("start_before"); v != "" {
			filters.StartBefore = v
			t, err := time.Parse("2006-01-02T15:04", v)
			if err == nil {
				params.StartBefore = &t
			}
		}
	}

	if v := r.URL.Query().Get("alerted"); v == "true" {
		filters.Alerted = true
		params.AlertedOnly = true
	}

	if v := r.URL.Query().Get("type"); v != "" {
		filters.EventType = v
		params.EventType = v
	}

	if v := r.URL.Query().Get("user"); v != "" {
		filters.UserName = v
		params.UserName = v
	}

	// Parse tag filters (repeated "tag=key:value" query params).
	if tagValues := r.URL.Query()["tag"]; len(tagValues) > 0 {
		tags := make(map[string]string, len(tagValues))
		for _, tv := range tagValues {
			if k, v, ok := strings.Cut(tv, ":"); ok && k != "" {
				tags[k] = v
				filters.Tags = append(filters.Tags, tv)
			}
		}
		if len(tags) > 0 {
			params.Tags = tags
		}
	}

	limit := model.DashboardLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	params.Limit = limit

	offset := 0
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	params.Offset = offset

	result, err := h.svc.List(r.Context(), params)
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Collect event IDs and fetch annotations in batch.
	eventIDs := make([]string, len(result.Events))
	for i, ev := range result.Events {
		eventIDs[i] = ev.ID
	}

	annotationsMap, err := h.svc.GetAnnotationsBatch(r.Context(), eventIDs)
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Build dashboard events combining events with their annotations.
	dashEvents := make([]dashboardEvent, len(result.Events))
	for i, ev := range result.Events {
		de := dashboardEvent{ChangeEvent: ev}
		if ann, ok := annotationsMap[ev.ID]; ok && ann != nil {
			de.Starred = ann.Starred
			de.Alerted = ann.Alerted
		}
		dashEvents[i] = de
	}

	offsetStart := offset + 1
	if result.TotalCount == 0 {
		offsetStart = 0
	}
	offsetEnd := offset + len(result.Events)

	token := r.URL.Query().Get("token")

	data := dashboardData{
		RefreshSec:  h.refreshSec,
		Events:      dashEvents,
		Filters:     filters,
		TotalCount:  result.TotalCount,
		Limit:       result.Limit,
		Offset:      result.Offset,
		HasPrev:     offset > 0,
		HasNext:     offset+limit < result.TotalCount,
		PrevURL:     h.paginationURL(r, offset-limit, limit),
		NextURL:     h.paginationURL(r, offset+limit, limit),
		OffsetStart: offsetStart,
		OffsetEnd:   offsetEnd,
		Token:       token,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.dashboardTmpl.ExecuteTemplate(w, "layout", data); err != nil {
		http.Error(w, "Template error", http.StatusInternalServerError)
	}
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
		Token:       r.URL.Query().Get("token"),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.detailTmpl.ExecuteTemplate(w, "layout", data); err != nil {
		http.Error(w, "Template error", http.StatusInternalServerError)
	}
}

// ToggleStar handles POST /events/{id}/star -- posts a meta-event and redirects back.
func (h *DashboardHandler) ToggleStar(w http.ResponseWriter, r *http.Request) {
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

	referer := r.Header.Get("Referer")
	if referer == "" {
		referer = "/"
		if token := r.URL.Query().Get("token"); token != "" {
			referer = "/?token=" + token
		}
	}
	http.Redirect(w, r, referer, http.StatusSeeOther)
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
