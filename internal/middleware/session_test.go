package middleware_test

import (
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sarah/go-prod-change-registry/internal/middleware"
)

func TestSetSessionCookie(t *testing.T) {
	t.Parallel()

	t.Run("sets correct cookie attributes", func(t *testing.T) {
		t.Parallel()

		rec := httptest.NewRecorder()
		middleware.SetSessionCookie(rec, []byte("test-secret"))

		cookies := rec.Result().Cookies()
		var found *http.Cookie
		for _, c := range cookies {
			if c.Name == middleware.SessionCookieName {
				found = c
				break
			}
		}
		if found == nil {
			t.Fatal("expected cookie with name pcr_session, got none")
		}
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
		if _, err := hex.DecodeString(found.Value); err != nil {
			t.Errorf("cookie value %q is not valid hex: %v", found.Value, err)
		}
	})

	t.Run("value is deterministic for same secret", func(t *testing.T) {
		t.Parallel()

		secret := []byte("deterministic-secret")

		rec1 := httptest.NewRecorder()
		middleware.SetSessionCookie(rec1, secret)

		rec2 := httptest.NewRecorder()
		middleware.SetSessionCookie(rec2, secret)

		val1 := cookieValue(t, rec1, middleware.SessionCookieName)
		val2 := cookieValue(t, rec2, middleware.SessionCookieName)

		if val1 != val2 {
			t.Errorf("same secret produced different values: %q vs %q", val1, val2)
		}
	})

	t.Run("value differs for different secrets", func(t *testing.T) {
		t.Parallel()

		rec1 := httptest.NewRecorder()
		middleware.SetSessionCookie(rec1, []byte("secret-a"))

		rec2 := httptest.NewRecorder()
		middleware.SetSessionCookie(rec2, []byte("secret-b"))

		val1 := cookieValue(t, rec1, middleware.SessionCookieName)
		val2 := cookieValue(t, rec2, middleware.SessionCookieName)

		if val1 == val2 {
			t.Error("different secrets produced identical cookie values")
		}
	})
}

func TestValidateSessionCookie(t *testing.T) {
	t.Parallel()

	secret := []byte("validate-test-secret")

	t.Run("accepts cookie set by SetSessionCookie", func(t *testing.T) {
		t.Parallel()

		rec := httptest.NewRecorder()
		middleware.SetSessionCookie(rec, secret)

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		for _, c := range rec.Result().Cookies() {
			req.AddCookie(c)
		}

		if !middleware.ValidateSessionCookie(req, secret) {
			t.Error("expected ValidateSessionCookie to return true for a correctly signed cookie")
		}
	})

	t.Run("rejects missing cookie", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if middleware.ValidateSessionCookie(req, secret) {
			t.Error("expected ValidateSessionCookie to return false with no cookie")
		}
	})

	t.Run("rejects tampered cookie value", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "deadbeef"})

		if middleware.ValidateSessionCookie(req, secret) {
			t.Error("expected ValidateSessionCookie to return false for tampered cookie")
		}
	})

	t.Run("rejects cookie signed with different secret", func(t *testing.T) {
		t.Parallel()

		rec := httptest.NewRecorder()
		middleware.SetSessionCookie(rec, []byte("secret-A"))

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		for _, c := range rec.Result().Cookies() {
			req.AddCookie(c)
		}

		if middleware.ValidateSessionCookie(req, []byte("secret-B")) {
			t.Error("expected ValidateSessionCookie to return false when validated with a different secret")
		}
	})
}

func TestClearSessionCookie(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	middleware.ClearSessionCookie(rec)

	cookies := rec.Result().Cookies()
	var found *http.Cookie
	for _, c := range cookies {
		if c.Name == middleware.SessionCookieName {
			found = c
			break
		}
	}
	if found == nil {
		t.Fatal("expected cookie with name pcr_session")
	}
	if found.MaxAge != -1 {
		t.Errorf("MaxAge = %d, want -1", found.MaxAge)
	}
	if found.Value != "" {
		t.Errorf("Value = %q, want empty string", found.Value)
	}
}

// cookieValue extracts a cookie's value from a recorder by name.
func cookieValue(t *testing.T, rec *httptest.ResponseRecorder, name string) string {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c.Value
		}
	}
	t.Fatalf("cookie %q not found", name)
	return ""
}
