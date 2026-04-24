package middleware

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// SessionCookieName is the name of the session cookie.
	SessionCookieName = "pcr_session"
	sessionMaxAge     = 24 * 60 * 60 // 24 hours in seconds
	nonceBytes        = 16
)

// SessionOptions configures session cookie behaviour.
type SessionOptions struct {
	Secret []byte
	Secure bool // sets the Secure flag on the cookie (requires HTTPS)
}

// SetSessionCookie creates an HMAC-signed session cookie with a per-session
// nonce and creation timestamp, and sets it on the response.
// Cookie value format: hex(nonce):unix_timestamp:hex(HMAC(secret, nonce:timestamp))
func SetSessionCookie(w http.ResponseWriter, opts SessionOptions) {
	nonce := make([]byte, nonceBytes)
	if _, err := rand.Read(nonce); err != nil {
		// Fail closed: if we can't generate a nonce, don't set a session.
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	nonceHex := hex.EncodeToString(nonce)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	sig := signSession(opts.Secret, nonceHex, timestamp)
	value := nonceHex + ":" + timestamp + ":" + sig

	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   sessionMaxAge,
		HttpOnly: true,
		Secure:   opts.Secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(24 * time.Hour),
	})
}

// ValidateSessionCookie checks if the request has a valid, non-expired session cookie.
func ValidateSessionCookie(r *http.Request, secret []byte) bool {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil {
		return false
	}

	nonce, timestamp, sig, ok := parseSessionValue(cookie.Value)
	if !ok {
		return false
	}

	// Verify the HMAC signature.
	expected := signSession(secret, nonce, timestamp)
	if !hmac.Equal([]byte(sig), []byte(expected)) {
		return false
	}

	// Verify the timestamp is within the max age window.
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}
	age := time.Since(time.Unix(ts, 0))
	return age >= 0 && age <= time.Duration(sessionMaxAge)*time.Second
}

// SessionNonce extracts the nonce from a validated session cookie.
// Returns empty string if the cookie is missing or malformed.
func SessionNonce(r *http.Request) string {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil {
		return ""
	}
	nonce, _, _, ok := parseSessionValue(cookie.Value)
	if !ok {
		return ""
	}
	return nonce
}

// GenerateCSRFToken derives a CSRF token from the session secret and nonce.
func GenerateCSRFToken(secret []byte, nonce string) string {
	return signSession(secret, "csrf", nonce)
}

// ValidateCSRFToken checks a submitted CSRF token against the session nonce.
func ValidateCSRFToken(secret []byte, nonce, token string) bool {
	if nonce == "" || token == "" {
		return false
	}
	expected := GenerateCSRFToken(secret, nonce)
	return hmac.Equal([]byte(token), []byte(expected))
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

// signSession computes HMAC-SHA256 over the concatenated parts.
func signSession(secret []byte, parts ...string) string {
	mac := hmac.New(sha256.New, secret)
	// hash.Hash.Write is documented never to return an error; the discard is safe.
	_, _ = mac.Write([]byte(strings.Join(parts, ":")))
	return hex.EncodeToString(mac.Sum(nil))
}

// parseSessionValue splits a cookie value into nonce, timestamp, and signature.
func parseSessionValue(value string) (nonce, timestamp, sig string, ok bool) {
	parts := strings.SplitN(value, ":", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}
