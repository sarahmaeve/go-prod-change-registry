package router

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/sarahmaeve/go-prod-change-registry/internal/config"
	"github.com/sarahmaeve/go-prod-change-registry/internal/handler"
	"github.com/sarahmaeve/go-prod-change-registry/internal/middleware"
	"github.com/sarahmaeve/go-prod-change-registry/web"
)

// New creates and configures a chi.Mux with all application routes and middleware.
func New(apiHandler *handler.APIHandler, dashHandler *handler.DashboardHandler, humanAuthHandler *handler.HumanAuthHandler, mcpHandler http.Handler, cfg *config.Config) *chi.Mux {
	r := chi.NewRouter()

	// Global middleware (applied to all routes including static files).
	r.Use(middleware.RequestID())
	r.Use(middleware.Logger())
	r.Use(middleware.SecurityHeaders())
	r.Use(chimiddleware.Recoverer)
	r.Use(chimiddleware.Timeout(30 * time.Second))

	// Static files and health checks are served without authentication.
	staticFS := http.FileServerFS(web.StaticFS)
	r.Handle("/static/*", staticFS)
	r.Get("/livez", apiHandler.Liveness)
	r.Get("/readyz", apiHandler.Readiness)
	r.Get("/api/v1/health", apiHandler.HealthCheck)
	r.Get("/login", humanAuthHandler.ShowLogin)
	r.Get("/auth/start", humanAuthHandler.Start)
	r.Get("/auth/callback", humanAuthHandler.Callback)

	// API routes accept explicit tokens only. Browser session cookies are scoped
	// to dashboard routes so PCR's own ambient cookie authority cannot
	// authenticate API writes that do not carry dashboard CSRF tokens. In
	// Beyond mode, the edge may instead authenticate an app password and inject
	// a verified request identity; the NetworkPolicy is part of that trust
	// boundary.
	r.Group(func(r chi.Router) {
		r.Use(middleware.AuthWithTrustedIdentity(
			cfg.APITokens,
			cfg.RequireAuthReads,
			nil,
			humanAuthHandler.TrustedRequestPrincipal(),
		))

		r.Get("/api/v1/current", apiHandler.ListCurrent)
		r.Get("/api/v1/events", apiHandler.ListEvents)
		r.Post("/api/v1/events", apiHandler.CreateEvent)
		r.Get("/api/v1/events/{id}", apiHandler.GetEvent)
		r.Get("/api/v1/events/{id}/annotations", apiHandler.GetEventAnnotations)
		r.Get("/api/v1/events/{id}/activity", apiHandler.GetEventActivity)
		r.Post("/api/v1/events/{id}/links", apiHandler.AddEventLinks)
		r.Post("/api/v1/events/{id}/star", apiHandler.ToggleStar)
		r.Post("/api/v1/events/{id}/alert", apiHandler.ToggleAlert)
		r.Post("/api/v1/events/{id}/close", apiHandler.CloseOperation)

		// MCP is a POST-only JSON-RPC endpoint with read-only tools. It sits
		// behind the same credentials as the REST API; because every call is a
		// POST, it requires them even when PCR_REQUIRE_AUTH_READS=false.
		r.Post("/mcp", mcpHandler.ServeHTTP)
	})

	// Dashboard routes accept only locally validated human sessions.
	r.Group(func(r chi.Router) {
		if principalForRequest := humanAuthHandler.TrustedRequestPrincipal(); principalForRequest != nil {
			r.Use(middleware.RequireBoundHumanAuth(cfg.SessionSecret, humanAuthHandler.IdentityProvider(), principalForRequest))
		} else {
			r.Use(middleware.RequireHumanAuth(cfg.SessionSecret, humanAuthHandler.IdentityProvider()))
		}

		r.Get("/", dashHandler.Dashboard)
		r.Get("/events/new", dashHandler.ShowCreateEvent)
		r.Post("/events", dashHandler.CreateEvent)
		r.Get("/events/{id}", dashHandler.Detail)
		r.Post("/events/{id}/star", dashHandler.ToggleStar)
		r.Post("/events/{id}/alert", dashHandler.ToggleAlert)
		r.Post("/events/{id}/links", dashHandler.AddLinks)
		r.Post("/events/{id}/close", dashHandler.CloseOperation)
	})

	// Logout needs only the signed local session plus the handler's CSRF check.
	// Requiring the current Beyond group would prevent a revoked user from
	// clearing the now-unusable local cookie.
	r.With(middleware.RequireHumanAuth(cfg.SessionSecret, humanAuthHandler.IdentityProvider())).Post("/logout", humanAuthHandler.Logout)

	return r
}
