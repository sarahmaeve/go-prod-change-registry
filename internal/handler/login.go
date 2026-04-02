package handler

import (
	"html/template"
	"net/http"

	"github.com/sarah/go-prod-change-registry/internal/middleware"
	"github.com/sarah/go-prod-change-registry/web"
)

// LoginHandler handles the /login endpoint for establishing dashboard sessions.
type LoginHandler struct {
	validTokens    [][]byte
	sessionOpts    middleware.SessionOptions
	loginFormTmpl  *template.Template
}

// NewLoginHandler creates a LoginHandler.
func NewLoginHandler(tokens []string, sessionOpts middleware.SessionOptions) *LoginHandler {
	validTokens := make([][]byte, len(tokens))
	for i, t := range tokens {
		validTokens[i] = []byte(t)
	}

	loginFormTmpl := template.Must(
		template.New("").ParseFS(
			web.TemplateFS,
			"templates/layout.html",
			"templates/login.html",
		),
	)

	return &LoginHandler{
		validTokens:   validTokens,
		sessionOpts:   sessionOpts,
		loginFormTmpl: loginFormTmpl,
	}
}

// loginData is the template data for the login page.
type loginData struct {
	RefreshSec int
	Error      string
}

// ShowLoginForm renders the login form (GET /login).
func (h *LoginHandler) ShowLoginForm(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	h.loginFormTmpl.ExecuteTemplate(w, "layout", loginData{})
}

// Login validates a token from the POST form body, sets a session cookie,
// and redirects to the dashboard.
func (h *LoginHandler) Login(w http.ResponseWriter, r *http.Request) {
	token := r.FormValue("token")
	if token == "" || !middleware.ValidateToken([]byte(token), h.validTokens) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		h.loginFormTmpl.ExecuteTemplate(w, "layout", loginData{Error: "Invalid or missing token"})
		return
	}

	middleware.SetSessionCookie(w, h.sessionOpts)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
