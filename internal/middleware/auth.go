package middleware

import (
	"encoding/json"
	"net/http"
	"strings"
)

// authErrorResponse is the JSON structure returned on authentication failure.
type authErrorResponse struct {
	Error authErrorDetail `json:"error"`
}

// authErrorDetail holds the error code and message.
type authErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Auth returns middleware that validates Bearer tokens from the Authorization header.
// The tokens slice contains the list of valid tokens. If requireForReads is false,
// GET and HEAD requests are allowed without authentication. The /api/v1/health
// endpoint is always exempt from authentication.
func Auth(tokens []string, requireForReads bool) func(http.Handler) http.Handler {
	tokenSet := make(map[string]struct{}, len(tokens))
	for _, t := range tokens {
		tokenSet[t] = struct{}{}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Health endpoint is always exempt.
			if r.URL.Path == "/api/v1/health" {
				next.ServeHTTP(w, r)
				return
			}

			// Skip auth for read methods when not required.
			if !requireForReads && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
				next.ServeHTTP(w, r)
				return
			}

			token := extractToken(r)
			if token == "" {
				writeAuthError(w, "missing or malformed Authorization header")
				return
			}

			if _, ok := tokenSet[token]; !ok {
				writeAuthError(w, "invalid token")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// extractToken pulls the token from an "Authorization: Bearer <token>" header,
// falling back to a "token" query parameter for browser-based access (e.g., the dashboard).
func extractToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if auth != "" {
		const prefix = "Bearer "
		if strings.HasPrefix(auth, prefix) {
			return strings.TrimPrefix(auth, prefix)
		}
		return ""
	}

	// Fall back to query parameter for browser access.
	return r.URL.Query().Get("token")
}

// writeAuthError sends a 401 JSON error response.
func writeAuthError(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)

	resp := authErrorResponse{
		Error: authErrorDetail{
			Code:    "unauthorized",
			Message: message,
		},
	}
	_ = json.NewEncoder(w).Encode(resp)
}
