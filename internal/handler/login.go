package handler

import (
	"html/template"
	"log/slog"
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
	if err := h.loginFormTmpl.ExecuteTemplate(w, "layout", loginData{}); err != nil {
		slog.ErrorContext(r.Context(), "login form template execute error", "error", err)
	}
}

// Login validates a token from the POST form body, sets a session cookie,
// and redirects to the dashboard.
func (h *LoginHandler) Login(w http.ResponseWriter, r *http.Request) {
	if !parseBoundedPostForm(w, r) {
		return
	}
	// Body already bounded and parsed by parseBoundedPostForm above.
	token := r.PostFormValue("token") //nolint:gosec // G120: body size limit applied via parseBoundedPostForm
	if token == "" || !middleware.ValidateToken([]byte(token), h.validTokens) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		if err := h.loginFormTmpl.ExecuteTemplate(w, "layout", loginData{Error: "Invalid or missing token"}); err != nil {
			// The status code has already been written, so there's nothing
			// left to signal to the client beyond a possibly-truncated
			// response body. Capture the failure for operators.
			slog.ErrorContext(r.Context(), "login error-page template execute error", "error", err)
		}
		return
	}

	middleware.SetSessionCookie(w, h.sessionOpts)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
