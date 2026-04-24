package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sarah/go-prod-change-registry/internal/middleware"
)

var testOpts = middleware.SessionOptions{Secret: []byte("test-secret"), Secure: false}

func TestSetSessionCookie(t *testing.T) {
	t.Parallel()

	t.Run("sets correct cookie attributes", func(t *testing.T) {
		t.Parallel()

		rec := httptest.NewRecorder()
		middleware.SetSessionCookie(rec, testOpts)

		found := findCookie(t, rec, middleware.SessionCookieName)
		if !found.HttpOnly {
			t.Error("expected HttpOnly to be true")
		}
		if found.SameSite != http.SameSiteLaxMode {
			t.Errorf("expected SameSite Lax, got %v", found.SameSite)
		}
		if found.MaxAge != 86400 {
			t.Errorf("MaxAge = %d, want 86400", found.MaxAge)
		}
		if found.Path != "/" {
			t.Errorf("Path = %q, want %q", found.Path, "/")
		}
		if found.Value == "" {
			t.Error("expected non-empty cookie value")
		}
		// Value format: nonce:timestamp:signature
		parts := strings.SplitN(found.Value, ":", 3)
		if len(parts) != 3 {
			t.Fatalf("expected cookie value with 3 colon-separated parts, got %d: %q", len(parts), found.Value)
		}
		for i, p := range parts {
			if p == "" {
				t.Errorf("cookie value part %d is empty", i)
			}
		}
	})

	t.Run("Secure flag controlled by options", func(t *testing.T) {
		t.Parallel()

		secureOpts := middleware.SessionOptions{Secret: []byte("s"), Secure: true}
		rec := httptest.NewRecorder()
		middleware.SetSessionCookie(rec, secureOpts)
		found := findCookie(t, rec, middleware.SessionCookieName)
		if !found.Secure {
			t.Error("expected Secure to be true when opts.Secure is true")
		}

		insecureOpts := middleware.SessionOptions{Secret: []byte("s"), Secure: false}
		rec2 := httptest.NewRecorder()
		middleware.SetSessionCookie(rec2, insecureOpts)
		found2 := findCookie(t, rec2, middleware.SessionCookieName)
		if found2.Secure {
			t.Error("expected Secure to be false when opts.Secure is false")
		}
	})

	t.Run("each call generates a unique nonce", func(t *testing.T) {
		t.Parallel()

		rec1 := httptest.NewRecorder()
		middleware.SetSessionCookie(rec1, testOpts)
		rec2 := httptest.NewRecorder()
		middleware.SetSessionCookie(rec2, testOpts)

		val1 := findCookie(t, rec1, middleware.SessionCookieName).Value
		val2 := findCookie(t, rec2, middleware.SessionCookieName).Value

		if val1 == val2 {
			t.Error("two calls produced identical cookie values — nonce should be random")
		}
	})

	t.Run("value differs for different secrets", func(t *testing.T) {
		t.Parallel()

		optsA := middleware.SessionOptions{Secret: []byte("secret-a")}
		optsB := middleware.SessionOptions{Secret: []byte("secret-b")}

		rec1 := httptest.NewRecorder()
		middleware.SetSessionCookie(rec1, optsA)
		rec2 := httptest.NewRecorder()
		middleware.SetSessionCookie(rec2, optsB)

		// Extract just the signature part (3rd field) — nonces will differ anyway
		sig1 := strings.SplitN(findCookie(t, rec1, middleware.SessionCookieName).Value, ":", 3)[2]
		sig2 := strings.SplitN(findCookie(t, rec2, middleware.SessionCookieName).Value, ":", 3)[2]

		if sig1 == sig2 {
			t.Error("different secrets produced identical signatures")
		}
	})
}

func TestValidateSessionCookie(t *testing.T) {
	t.Parallel()

	secret := []byte("validate-test-secret")
	opts := middleware.SessionOptions{Secret: secret}

	t.Run("accepts cookie set by SetSessionCookie", func(t *testing.T) {
		t.Parallel()

		rec := httptest.NewRecorder()
		middleware.SetSessionCookie(rec, opts)

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
		for _, c := range rec.Result().Cookies() {
			req.AddCookie(c)
		}

		if !middleware.ValidateSessionCookie(req, secret) {
			t.Error("expected ValidateSessionCookie to return true for a correctly signed cookie")
		}
	})

	t.Run("rejects missing cookie", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
		if middleware.ValidateSessionCookie(req, secret) {
			t.Error("expected ValidateSessionCookie to return false with no cookie")
		}
	})

	t.Run("rejects tampered cookie value", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
		req.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "deadbeef"})

		if middleware.ValidateSessionCookie(req, secret) {
			t.Error("expected ValidateSessionCookie to return false for tampered cookie")
		}
	})

	t.Run("rejects malformed cookie missing parts", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
		req.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "onlyonepart"})

		if middleware.ValidateSessionCookie(req, secret) {
			t.Error("expected false for cookie with no colons")
		}
	})

	t.Run("rejects cookie signed with different secret", func(t *testing.T) {
		t.Parallel()

		optsA := middleware.SessionOptions{Secret: []byte("secret-A")}
		rec := httptest.NewRecorder()
		middleware.SetSessionCookie(rec, optsA)

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
		for _, c := range rec.Result().Cookies() {
			req.AddCookie(c)
		}

		if middleware.ValidateSessionCookie(req, []byte("secret-B")) {
			t.Error("expected false when validated with a different secret")
		}
	})
}

func TestCSRFToken(t *testing.T) {
	t.Parallel()

	secret := []byte("csrf-test-secret")
	opts := middleware.SessionOptions{Secret: secret}

	t.Run("roundtrip: generate then validate", func(t *testing.T) {
		t.Parallel()

		rec := httptest.NewRecorder()
		middleware.SetSessionCookie(rec, opts)

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
		for _, c := range rec.Result().Cookies() {
			req.AddCookie(c)
		}

		nonce := middleware.SessionNonce(req)
		if nonce == "" {
			t.Fatal("expected non-empty nonce from session cookie")
		}

		token := middleware.GenerateCSRFToken(secret, nonce)
		if token == "" {
			t.Fatal("expected non-empty CSRF token")
		}

		if !middleware.ValidateCSRFToken(secret, nonce, token) {
			t.Error("expected CSRF token to validate")
		}
	})

	t.Run("rejects empty nonce", func(t *testing.T) {
		t.Parallel()

		if middleware.ValidateCSRFToken(secret, "", "some-token") {
			t.Error("expected false for empty nonce")
		}
	})

	t.Run("rejects empty token", func(t *testing.T) {
		t.Parallel()

		if middleware.ValidateCSRFToken(secret, "some-nonce", "") {
			t.Error("expected false for empty token")
		}
	})

	t.Run("rejects wrong token", func(t *testing.T) {
		t.Parallel()

		nonce := "test-nonce-value"
		if middleware.ValidateCSRFToken(secret, nonce, "wrong-token") {
			t.Error("expected false for wrong token")
		}
	})

	t.Run("rejects token from different secret", func(t *testing.T) {
		t.Parallel()

		nonce := "test-nonce-value"
		token := middleware.GenerateCSRFToken([]byte("secret-A"), nonce)
		if middleware.ValidateCSRFToken([]byte("secret-B"), nonce, token) {
			t.Error("expected false for token generated with different secret")
		}
	})
}

func TestClearSessionCookie(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	middleware.ClearSessionCookie(rec)

	found := findCookie(t, rec, middleware.SessionCookieName)
	if found.MaxAge != -1 {
		t.Errorf("MaxAge = %d, want -1", found.MaxAge)
	}
	if found.Value != "" {
		t.Errorf("Value = %q, want empty string", found.Value)
	}
}

// findCookie extracts a cookie from a recorder by name, failing if not found.
func findCookie(t *testing.T, rec *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("cookie %q not found", name)
	return nil
}
