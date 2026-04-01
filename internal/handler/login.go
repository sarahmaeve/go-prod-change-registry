package handler

import (
	"net/http"

	"github.com/sarah/go-prod-change-registry/internal/middleware"
)

// LoginHandler handles the /login endpoint for establishing dashboard sessions.
type LoginHandler struct {
	validTokens   [][]byte
	sessionSecret []byte
}

// NewLoginHandler creates a LoginHandler.
func NewLoginHandler(tokens []string, sessionSecret []byte) *LoginHandler {
	validTokens := make([][]byte, len(tokens))
	for i, t := range tokens {
		validTokens[i] = []byte(t)
	}
	return &LoginHandler{
		validTokens:   validTokens,
		sessionSecret: sessionSecret,
	}
}

// Login validates a token from the query parameter, sets a session cookie, and redirects to the dashboard.
func (h *LoginHandler) Login(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" || !middleware.ValidateToken([]byte(token), h.validTokens) {
		http.Error(w, "Invalid or missing token", http.StatusUnauthorized)
		return
	}

	middleware.SetSessionCookie(w, h.sessionSecret)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
