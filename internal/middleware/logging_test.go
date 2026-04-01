package middleware_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sarah/go-prod-change-registry/internal/middleware"
)

// logEntry represents a parsed JSON log line from slog.
type logEntry struct {
	Level     string `json:"level"`
	Msg       string `json:"msg"`
	Method    string `json:"method"`
	Path      string `json:"path"`
	Status    int    `json:"status"`
	RequestID string `json:"request_id"`
}

// withCapturedLog sets the default slog logger to write JSON into buf,
// runs fn, then restores the previous logger.
func withCapturedLog(buf *bytes.Buffer, fn func()) {
	original := slog.Default()
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	slog.SetDefault(logger)
	defer slog.SetDefault(original)
	fn()
}

func TestLogger(t *testing.T) {
	// Note: these subtests are NOT marked parallel because they mutate
	// the global slog default logger via withCapturedLog.

	t.Run("request is logged with correct fields", func(t *testing.T) {
		var buf bytes.Buffer

		handler := middleware.Logger()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest(http.MethodGet, "/api/v1/changes", nil)
		// Inject a request ID into context so the logger can read it.
		ctx := context.WithValue(req.Context(), middleware.RequestIDKey, "test-req-id")
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()

		withCapturedLog(&buf, func() {
			handler.ServeHTTP(rec, req)
		})

		var entry logEntry
		if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
			t.Fatalf("failed to parse log output: %v\nraw: %s", err, buf.String())
		}

		if entry.Method != http.MethodGet {
			t.Errorf("expected method %q, got %q", http.MethodGet, entry.Method)
		}
		if entry.Path != "/api/v1/changes" {
			t.Errorf("expected path %q, got %q", "/api/v1/changes", entry.Path)
		}
		if entry.Status != http.StatusOK {
			t.Errorf("expected status %d, got %d", http.StatusOK, entry.Status)
		}
		if entry.RequestID != "test-req-id" {
			t.Errorf("expected request_id %q, got %q", "test-req-id", entry.RequestID)
		}
	})

	t.Run("status code is captured correctly", func(t *testing.T) {
		var buf bytes.Buffer

		handler := middleware.Logger()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))

		req := httptest.NewRequest(http.MethodPost, "/api/v1/missing", nil)
		rec := httptest.NewRecorder()

		withCapturedLog(&buf, func() {
			handler.ServeHTTP(rec, req)
		})

		var entry logEntry
		if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
			t.Fatalf("failed to parse log output: %v\nraw: %s", err, buf.String())
		}

		if entry.Status != http.StatusNotFound {
			t.Errorf("expected status %d, got %d", http.StatusNotFound, entry.Status)
		}
		if entry.Level != "WARN" {
			t.Errorf("expected log level WARN for 404, got %q", entry.Level)
		}
	})

	t.Run("5xx status is logged at error level", func(t *testing.T) {
		var buf bytes.Buffer

		handler := middleware.Logger()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))

		req := httptest.NewRequest(http.MethodGet, "/api/v1/fail", nil)
		rec := httptest.NewRecorder()

		withCapturedLog(&buf, func() {
			handler.ServeHTTP(rec, req)
		})

		var entry logEntry
		if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
			t.Fatalf("failed to parse log output: %v\nraw: %s", err, buf.String())
		}

		if entry.Status != http.StatusInternalServerError {
			t.Errorf("expected status %d, got %d", http.StatusInternalServerError, entry.Status)
		}
		if entry.Level != "ERROR" {
			t.Errorf("expected log level ERROR for 500, got %q", entry.Level)
		}
	})
}
