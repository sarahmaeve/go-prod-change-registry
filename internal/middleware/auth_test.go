package middleware_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sarah/go-prod-change-registry/internal/middleware"
)

func TestAuth(t *testing.T) {
	t.Parallel()

	const validToken = "test-token-abc"
	okHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	tests := []struct {
		name            string
		method          string
		path            string
		authHeader      string
		queryToken      string
		requireForReads bool
		expectedStatus  int
	}{
		{
			name:            "valid Bearer token allows request through",
			method:          http.MethodPost,
			path:            "/api/v1/changes",
			authHeader:      "Bearer " + validToken,
			requireForReads: true,
			expectedStatus:  http.StatusOK,
		},
		{
			name:            "missing Authorization header returns 401",
			method:          http.MethodPost,
			path:            "/api/v1/changes",
			authHeader:      "",
			requireForReads: true,
			expectedStatus:  http.StatusUnauthorized,
		},
		{
			name:            "invalid token returns 401",
			method:          http.MethodPost,
			path:            "/api/v1/changes",
			authHeader:      "Bearer wrong-token",
			requireForReads: true,
			expectedStatus:  http.StatusUnauthorized,
		},
		{
			name:            "malformed Authorization header without Bearer prefix returns 401",
			method:          http.MethodPost,
			path:            "/api/v1/changes",
			authHeader:      "Basic " + validToken,
			requireForReads: true,
			expectedStatus:  http.StatusUnauthorized,
		},
		{
			name:            "GET request with requireForReads=false skips auth",
			method:          http.MethodGet,
			path:            "/api/v1/changes",
			authHeader:      "",
			requireForReads: false,
			expectedStatus:  http.StatusOK,
		},
		{
			name:            "GET request with requireForReads=true requires auth",
			method:          http.MethodGet,
			path:            "/api/v1/changes",
			authHeader:      "",
			requireForReads: true,
			expectedStatus:  http.StatusUnauthorized,
		},
		{
			name:            "HEAD request with requireForReads=false skips auth",
			method:          http.MethodHead,
			path:            "/api/v1/changes",
			authHeader:      "",
			requireForReads: false,
			expectedStatus:  http.StatusOK,
		},
		{
			name:            "POST request always requires auth regardless of requireForReads",
			method:          http.MethodPost,
			path:            "/api/v1/changes",
			authHeader:      "",
			requireForReads: false,
			expectedStatus:  http.StatusUnauthorized,
		},
		{
			name:            "valid query param token allows request through",
			method:          http.MethodGet,
			path:            "/api/v1/events",
			queryToken:      validToken,
			requireForReads: true,
			expectedStatus:  http.StatusOK,
		},
		{
			name:            "invalid query param token returns 401",
			method:          http.MethodGet,
			path:            "/api/v1/events",
			queryToken:      "wrong-token",
			requireForReads: true,
			expectedStatus:  http.StatusUnauthorized,
		},
		{
			name:            "query param token works for POST",
			method:          http.MethodPost,
			path:            "/api/v1/events",
			queryToken:      validToken,
			requireForReads: true,
			expectedStatus:  http.StatusOK,
		},
		{
			name:            "Bearer header takes precedence over query param",
			method:          http.MethodPost,
			path:            "/api/v1/events",
			authHeader:      "Bearer " + validToken,
			queryToken:      "wrong-token",
			requireForReads: true,
			expectedStatus:  http.StatusOK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mw := middleware.Auth([]string{validToken}, tc.requireForReads)
			srv := mw(okHandler)

			path := tc.path
			if tc.queryToken != "" {
				path += "?token=" + tc.queryToken
			}
			req := httptest.NewRequest(tc.method, path, nil)
			if tc.authHeader != "" {
				req.Header.Set("Authorization", tc.authHeader)
			}
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			if rec.Code != tc.expectedStatus {
				t.Fatalf("expected %d, got %d", tc.expectedStatus, rec.Code)
			}
		})
	}

	t.Run("401 response body is JSON with error code and message", func(t *testing.T) {
		t.Parallel()

		mw := middleware.Auth([]string{validToken}, true)
		srv := mw(okHandler)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/changes", nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", rec.Code)
		}

		ct := rec.Header().Get("Content-Type")
		if ct != "application/json" {
			t.Fatalf("expected Content-Type application/json, got %q", ct)
		}

		var body struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode JSON body: %v", err)
		}

		if body.Error.Code == "" {
			t.Fatal("expected error.code to be non-empty")
		}
		if body.Error.Message == "" {
			t.Fatal("expected error.message to be non-empty")
		}
	})
}
