package middleware

import (
	"log/slog"
	"net/http"
	"time"
)

// statusWriter wraps http.ResponseWriter to capture the status code.
type statusWriter struct {
	http.ResponseWriter
	status int
	written bool
}

// WriteHeader captures the status code and delegates to the underlying ResponseWriter.
// Only the first call is forwarded to avoid "superfluous WriteHeader" warnings.
func (w *statusWriter) WriteHeader(code int) {
	if w.written {
		return
	}
	w.status = code
	w.written = true
	w.ResponseWriter.WriteHeader(code)
}

// Write delegates to the underlying ResponseWriter and sets status 200 if
// WriteHeader has not been called yet.
func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.written {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

// Logger returns middleware that logs each request using slog structured logging.
// It records the HTTP method, path, response status, duration, and request ID.
func Logger() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)

			duration := time.Since(start)
			requestID := GetRequestID(r.Context())

			attrs := []slog.Attr{
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", sw.status),
				slog.Duration("duration", duration),
				slog.String("request_id", requestID),
			}

			msg := r.Method + " " + r.URL.Path

			switch {
			case sw.status >= 500:
				slog.LogAttrs(
					r.Context(),
					slog.LevelError,
					msg,
					attrs...,
				)
			case sw.status >= 400:
				slog.LogAttrs(
					r.Context(),
					slog.LevelWarn,
					msg,
					attrs...,
				)
			default:
				slog.LogAttrs(
					r.Context(),
					slog.LevelInfo,
					msg,
					attrs...,
				)
			}
		})
	}
}
