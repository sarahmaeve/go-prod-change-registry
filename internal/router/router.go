package router

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/sarah/go-prod-change-registry/internal/config"
	"github.com/sarah/go-prod-change-registry/internal/handler"
	"github.com/sarah/go-prod-change-registry/internal/middleware"
	"github.com/sarah/go-prod-change-registry/web"
)

// New creates and configures a chi.Mux with all application routes and middleware.
func New(apiHandler *handler.APIHandler, dashHandler *handler.DashboardHandler, cfg *config.Config) *chi.Mux {
	r := chi.NewRouter()

	// Global middleware (applied to all routes including static files).
	r.Use(middleware.RequestID())
	r.Use(middleware.Logger())
	r.Use(chimiddleware.Recoverer)
	r.Use(chimiddleware.RealIP)
	r.Use(chimiddleware.Timeout(30 * time.Second))

	// Static files are served without authentication.
	staticFS := http.FileServerFS(web.StaticFS)
	r.Handle("/static/*", staticFS)

	// All remaining routes require authentication.
	r.Group(func(r chi.Router) {
		r.Use(middleware.Auth(cfg.APITokens, cfg.RequireAuthReads))

		// API routes.
		r.Get("/api/v1/health", apiHandler.HealthCheck)
		r.Get("/api/v1/events", apiHandler.ListEvents)
		r.Post("/api/v1/events", apiHandler.CreateEvent)
		r.Get("/api/v1/events/{id}", apiHandler.GetEvent)
		r.Put("/api/v1/events/{id}", apiHandler.UpdateEvent)
		r.Delete("/api/v1/events/{id}", apiHandler.DeleteEvent)
		r.Post("/api/v1/events/{id}/star", apiHandler.ToggleStar)

		// Dashboard routes.
		r.Get("/", dashHandler.Dashboard)
		r.Get("/events/{id}", dashHandler.Detail)
		r.Post("/events/{id}/star", dashHandler.ToggleStar)
	})

	return r
}
