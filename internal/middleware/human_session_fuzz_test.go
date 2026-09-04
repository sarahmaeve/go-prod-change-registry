package middleware //nolint:testpackage // fuzzing the unexported signed-session decoder is intentional

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func FuzzHumanSessionValidation(f *testing.F) {
	for _, seed := range []struct {
		payload          []byte
		expectedProvider string
	}{
		{
			payload:          []byte("{\"provider\":\"github\",\"subject\":\"123\",\"user_name\":\"alice\",\"nonce\":\"nonce\",\"issued_at\":\"2026-01-01T00:00:00Z\",\"expires_at\":\"2030-01-01T00:00:00Z\"}"),
			expectedProvider: "github",
		},
		{payload: []byte("{}"), expectedProvider: "github"},
		{payload: []byte("{\"provider\":\"github\"} trailing"), expectedProvider: "github"},
		{payload: []byte{0xff, 0xfe, 0xfd}, expectedProvider: "github"},
	} {
		f.Add(seed.payload, seed.expectedProvider)
	}

	const maxFuzzSessionPayload = 16 << 10
	secret := []byte("0123456789abcdef0123456789abcdef")
	f.Fuzz(func(t *testing.T, payload []byte, expectedProvider string) {
		if len(payload) > maxFuzzSessionPayload {
			payload = payload[:maxFuzzSessionPayload]
		}
		value := signedHumanSessionPayload(secret, payload)

		decoded, decodeErr := decodeHumanSession(secret, value)
		if decodeErr != nil && !errors.Is(decodeErr, ErrInvalidHumanSession) {
			t.Fatalf("decodeHumanSession() error = %v, want ErrInvalidHumanSession", decodeErr)
		}

		request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
		request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: value}) //nolint:gosec // Inbound fuzz input, not a response cookie.
		session, err := ReadHumanSession(request, secret, expectedProvider)
		if err != nil && !errors.Is(err, ErrInvalidHumanSession) {
			t.Fatalf("ReadHumanSession() error = %v, want ErrInvalidHumanSession", err)
		}
		if err == nil {
			if decodeErr != nil || session != decoded {
				t.Fatalf("ReadHumanSession() = %+v, decoded session = %+v, decode error = %v", session, decoded, decodeErr)
			}
			if session.Provider != expectedProvider || !session.IsValid() || session.Nonce == "" ||
				!session.ExpiresAt.After(session.IssuedAt) {
				t.Fatalf("ReadHumanSession() accepted an invalid session: %+v", session)
			}
		}

		if _, err := decodeHumanSession(secret, value+"x"); !errors.Is(err, ErrInvalidHumanSession) {
			t.Fatalf("decodeHumanSession() accepted a mutated signature: %v", err)
		}
	})
}

func signedHumanSessionPayload(secret, payload []byte) string {
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	signed := humanSessionVersion + "." + encodedPayload
	return signed + "." + humanSessionSignature(secret, signed)
}
