package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/sarah/go-prod-change-registry/internal/middleware"
)

// uuidRegex matches a UUID v4 string (lowercase hex with hyphens).
var uuidRegex = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func TestRequestID(t *testing.T) {
	t.Parallel()

	t.Run("request without X-Request-ID gets a generated UUID", func(t *testing.T) {
		t.Parallel()

		var capturedID string
		handler := middleware.RequestID()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedID = middleware.GetRequestID(r.Context())
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if capturedID == "" {
			t.Fatal("expected a generated request ID, got empty string")
		}
	})

	t.Run("request with existing X-Request-ID preserves it", func(t *testing.T) {
		t.Parallel()

		const existingID = "my-custom-request-id"
		var capturedID string
		handler := middleware.RequestID()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedID = middleware.GetRequestID(r.Context())
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
		req.Header.Set(middleware.RequestIDHeader, existingID)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if capturedID != existingID {
			t.Fatalf("expected %q, got %q", existingID, capturedID)
		}
	})

	t.Run("response has X-Request-ID header set", func(t *testing.T) {
		t.Parallel()

		handler := middleware.RequestID()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		respID := rec.Header().Get(middleware.RequestIDHeader)
		if respID == "" {
			t.Fatal("expected X-Request-ID in response header, got empty string")
		}
	})

	t.Run("GetRequestID returns correct ID from context", func(t *testing.T) {
		t.Parallel()

		const existingID = "ctx-test-id-123"
		var capturedID string
		handler := middleware.RequestID()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedID = middleware.GetRequestID(r.Context())
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
		req.Header.Set(middleware.RequestIDHeader, existingID)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if capturedID != existingID {
			t.Fatalf("expected GetRequestID to return %q, got %q", existingID, capturedID)
		}
	})

	t.Run("generated IDs are valid UUIDs", func(t *testing.T) {
		t.Parallel()

		var capturedID string
		handler := middleware.RequestID()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedID = middleware.GetRequestID(r.Context())
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if !uuidRegex.MatchString(capturedID) {
			t.Fatalf("expected valid UUID, got %q", capturedID)
		}
	})
}
