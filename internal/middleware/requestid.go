package middleware

import (
	"context"
	"net/http"

	"github.com/google/uuid"
)

// contextKey is an unexported type for context keys in this package.
type contextKey string

// RequestIDKey is the context key for the request ID.
const RequestIDKey contextKey = "request_id"

// RequestIDHeader is the HTTP header used for request IDs.
const RequestIDHeader = "X-Request-ID"

// RequestID returns middleware that assigns a unique request ID to each request.
// If the incoming request already has an X-Request-ID header, that value is used;
// otherwise a new UUID v4 is generated.
func RequestID() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(RequestIDHeader)
			if id == "" {
				id = uuid.New().String()
			}

			ctx := context.WithValue(r.Context(), RequestIDKey, id)
			w.Header().Set(RequestIDHeader, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// GetRequestID extracts the request ID from the context.
// It returns an empty string if no request ID is present.
func GetRequestID(ctx context.Context) string {
	id, _ := ctx.Value(RequestIDKey).(string)
	return id
}
