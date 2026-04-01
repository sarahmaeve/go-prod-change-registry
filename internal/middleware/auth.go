package middleware

import (
	"crypto/subtle"
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
	// Store tokens as byte slices for constant-time comparison.
	validTokens := make([][]byte, len(tokens))
	for i, t := range tokens {
		validTokens[i] = []byte(t)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

			if !validateToken([]byte(token), validTokens) {
				writeAuthError(w, "invalid token")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// validateToken checks the provided token against the valid tokens list
// using constant-time comparison to prevent timing side-channel attacks.
func validateToken(provided []byte, validTokens [][]byte) bool {
	for _, valid := range validTokens {
		if subtle.ConstantTimeCompare(provided, valid) == 1 {
			return true
		}
	}
	return false
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
