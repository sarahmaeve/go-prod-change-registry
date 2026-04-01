package middleware

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"time"
)

const (
	// SessionCookieName is the name of the session cookie.
	SessionCookieName = "pcr_session"
	sessionValue      = "authenticated"
	sessionMaxAge     = 24 * 60 * 60 // 24 hours in seconds
)

// SetSessionCookie creates an HMAC-signed session cookie and sets it on the response.
func SetSessionCookie(w http.ResponseWriter, secret []byte) {
	sig := signSession(secret)
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    sig,
		Path:     "/",
		MaxAge:   sessionMaxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(24 * time.Hour),
	})
}

// ValidateSessionCookie checks if the request has a valid session cookie.
func ValidateSessionCookie(r *http.Request, secret []byte) bool {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil {
		return false
	}
	expected := signSession(secret)
	return hmac.Equal([]byte(cookie.Value), []byte(expected))
}

// ClearSessionCookie removes the session cookie.
func ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:   SessionCookieName,
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})
}

func signSession(secret []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(sessionValue))
	return hex.EncodeToString(mac.Sum(nil))
}
