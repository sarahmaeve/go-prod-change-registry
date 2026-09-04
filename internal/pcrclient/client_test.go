package pcrclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestCreateDoesNotSendCallerIdentityOrLeakCredential(t *testing.T) {
	t.Parallel()
	const credential = "person@example.com:secret-password"
	var payload map[string]any
	client := testClient(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if got := request.Header.Get("Authorization"); got != "Bearer "+credential {
			t.Errorf("Authorization = %q, want configured bearer credential", got)
		}
		if got := request.Header.Get("User-Agent"); got != "pcr/test" {
			t.Errorf("User-Agent = %q, want pcr/test", got)
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		return response(request, http.StatusCreated, `{"id":"event-1","external_id":"build-1","user_name":"person@example.com","event_type":"deployment","description":"deployed"}`), nil
	}), credential)

	event, err := client.Create(t.Context(), CreateRequest{
		ExternalID:  "build-1",
		EventType:   "deployment",
		Description: "deployed",
		Tags:        map[string]string{"env": "prod"},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if event.ID != "event-1" {
		t.Errorf("Create() event ID = %q, want event-1", event.ID)
	}
	if _, ok := payload["user_name"]; ok {
		t.Errorf("Create() payload contains user_name: %#v", payload)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal(payload): %v", err)
	}
	if strings.Contains(string(encoded), credential) {
		t.Fatal("Create() payload disclosed credential")
	}
}

func TestListEncodesEveryFilter(t *testing.T) {
	t.Parallel()
	after := time.Date(2026, 8, 1, 2, 3, 4, 0, time.UTC)
	before := after.Add(time.Hour)
	around := after.Add(30 * time.Minute)
	var got url.Values
	client := testClient(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		got = request.URL.Query()
		return response(request, http.StatusOK, `{"events":[],"total_count":0,"limit":10,"offset":20}`), nil
	}), "person@example.com:password")

	_, err := client.List(t.Context(), ListOptions{
		StartAfter:  &after,
		StartBefore: &before,
		Around:      &around,
		Window:      45 * time.Minute,
		User:        "alice",
		EventType:   "deployment",
		Tags:        map[string]string{"env": "prod", "region": "us-east-1"},
		TopLevel:    true,
		AlertedOnly: true,
		Limit:       10,
		Offset:      20,
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	wants := map[string][]string{
		"start_after":  {after.Format(time.RFC3339)},
		"start_before": {before.Format(time.RFC3339)},
		"around":       {around.Format(time.RFC3339)},
		"window":       {"45m0s"},
		"user":         {"alice"},
		"type":         {"deployment"},
		"top_level":    {"true"},
		"alerted":      {"true"},
		"limit":        {"10"},
		"offset":       {"20"},
	}
	for key, want := range wants {
		if strings.Join(got[key], "|") != strings.Join(want, "|") {
			t.Errorf("query[%q] = %q, want %q", key, got[key], want)
		}
	}
	tags := strings.Join(got["tag"], "|")
	if !strings.Contains(tags, "env:prod") || !strings.Contains(tags, "region:us-east-1") {
		t.Errorf("query[tag] = %q, want both tags", got["tag"])
	}
}

func TestCurrentEncodesRepeatableFilters(t *testing.T) {
	t.Parallel()
	var got url.Values
	client := testClient(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		got = request.URL.Query()
		return response(request, http.StatusOK, `{"events":[],"total_count":0,"limit":50,"offset":0}`), nil
	}), "person@example.com:password")
	_, err := client.Current(t.Context(), CurrentOptions{
		Team:       "platform",
		Scopes:     []string{"site", "system"},
		Severities: []string{"sev0", "sev1"},
		EventType:  "maintenance",
	})
	if err != nil {
		t.Fatalf("Current() error = %v", err)
	}
	if got.Get("for_team") != "platform" || got.Get("type") != "maintenance" {
		t.Errorf("Current() scalar query = %v", got)
	}
	if strings.Join(got["scope"], ",") != "site,system" || strings.Join(got["severity"], ",") != "sev0,sev1" {
		t.Errorf("Current() repeatable query = %v", got)
	}
}

func TestEventIDIsOneEscapedPathSegment(t *testing.T) {
	t.Parallel()
	const eventID = "../other/event?token=value#fragment"
	var escapedPath string
	client := testClient(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		escapedPath = request.URL.EscapedPath()
		return response(request, http.StatusOK, `{"id":"event-1","user_name":"alice","event_type":"deployment","description":"test"}`), nil
	}), "person@example.com:password")
	if _, err := client.Get(t.Context(), eventID); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	want := "/api/v1/events/" + url.PathEscape(eventID)
	if escapedPath != want {
		t.Errorf("Get() escaped path = %q, want %q", escapedPath, want)
	}
}

func TestResponseFailuresAreCategorizedAndBodyIsNeverExposed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status int
		kind   ErrorKind
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, kind: ErrorPermission},
		{name: "forbidden", status: http.StatusForbidden, kind: ErrorPermission},
		{name: "not found", status: http.StatusNotFound, kind: ErrorNotFound},
		{name: "bad request", status: http.StatusBadRequest, kind: ErrorRequest},
		{name: "server", status: http.StatusInternalServerError, kind: ErrorUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			const sensitiveBody = "internal-secret-response"
			client := testClient(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return response(request, test.status, sensitiveBody), nil
			}), "person@example.com:password")
			_, err := client.Get(t.Context(), "event-1")
			var clientError *Error
			if !errors.As(err, &clientError) || clientError.Kind != test.kind {
				t.Fatalf("Get() error = %v, want kind %v", err, test.kind)
			}
			if strings.Contains(err.Error(), sensitiveBody) {
				t.Fatal("Get() error disclosed response body")
			}
		})
	}
}

func TestStructuredAPIErrorIsActionable(t *testing.T) {
	t.Parallel()

	client := testClient(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return response(request, http.StatusBadRequest, `{"error":{"code":"validation_error","message":"maintenance events require phase=start and change_id"}}`), nil
	}), "person@example.com:password")
	_, err := client.Create(t.Context(), CreateRequest{EventType: "maintenance"})
	var clientError *Error
	if !errors.As(err, &clientError) {
		t.Fatalf("Create() error = %v, want *Error", err)
	}
	if clientError.Code != "validation_error" || clientError.Message != "maintenance events require phase=start and change_id" {
		t.Fatalf("structured error = code %q, message %q", clientError.Code, clientError.Message)
	}
	want := "HTTP 400 (validation_error): maintenance events require phase=start and change_id"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("Create() error = %q, want actionable diagnostic containing %q", err, want)
	}
}

func TestStructuredServerErrorIsOpaque(t *testing.T) {
	t.Parallel()

	const sensitive = "database connection string leaked"
	client := testClient(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return response(request, http.StatusInternalServerError, `{"error":{"code":"internal_error","message":"database connection string leaked"}}`), nil
	}), "person@example.com:password")
	_, err := client.Get(t.Context(), "event-1")
	var clientError *Error
	if !errors.As(err, &clientError) {
		t.Fatalf("Get() error = %v, want *Error", err)
	}
	if clientError.Code != "" || clientError.Message != "" || strings.Contains(err.Error(), sensitive) {
		t.Fatalf("Get() error exposed structured server detail: %#v", clientError)
	}
}

func TestUnsafeStructuredAPIErrorIsNotExposed(t *testing.T) {
	t.Parallel()

	const unsafe = "secret\nforged diagnostic"
	client := testClient(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return response(request, http.StatusBadRequest, `{"error":{"code":"validation_error","message":"secret\nforged diagnostic"}}`), nil
	}), "person@example.com:password")
	_, err := client.Get(t.Context(), "event-1")
	if strings.Contains(err.Error(), unsafe) || strings.Contains(err.Error(), "forged diagnostic") {
		t.Fatalf("Get() error exposed unsafe response detail: %q", err)
	}
}

func TestMalformedAndOversizedResponses(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
	}{
		{name: "malformed", body: "not JSON"},
		{name: "multiple values", body: `{} {}`},
		{name: "oversized", body: strings.Repeat("x", maxResponseSize+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			client := testClient(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return response(request, http.StatusOK, test.body), nil
			}), "person@example.com:password")
			_, err := client.Get(t.Context(), "event-1")
			var clientError *Error
			if !errors.As(err, &clientError) || clientError.Kind != ErrorUnavailable {
				t.Fatalf("Get() error = %v, want unavailable", err)
			}
		})
	}
}

func TestDefaultClientRejectsRedirect(t *testing.T) {
	t.Parallel()
	origin, err := url.Parse("https://pcr.example.com")
	if err != nil {
		t.Fatalf("Parse(): %v", err)
	}
	client, err := New(Options{Origin: origin, Credential: "person@example.com:password"})
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	err = client.httpClient.CheckRedirect(&http.Request{}, []*http.Request{{}})
	if !errors.Is(err, http.ErrUseLastResponse) {
		t.Errorf("CheckRedirect() error = %v, want http.ErrUseLastResponse", err)
	}
}

func TestTransportErrorDoesNotExposeCause(t *testing.T) {
	t.Parallel()
	const transportSecret = "secret-in-transport-error"
	client := testClient(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New(transportSecret)
	}), "person@example.com:password")
	_, err := client.Get(context.Background(), "event-1")
	if err == nil || strings.Contains(err.Error(), transportSecret) {
		t.Fatalf("Get() error = %v, want opaque transport error", err)
	}
}

func FuzzEndpointPathSegment(f *testing.F) {
	f.Add("event-1")
	f.Add("../event?token=value#fragment")
	f.Add("\x1b[31m/../../")
	f.Fuzz(func(t *testing.T, eventID string) {
		origin, err := url.Parse("https://pcr.example.com")
		if err != nil {
			t.Fatalf("Parse(): %v", err)
		}
		client, err := New(Options{Origin: origin, Credential: "person@example.com:password", HTTPClient: &http.Client{}})
		if err != nil {
			t.Fatalf("New(): %v", err)
		}
		endpoint := client.endpoint([]string{"api", "v1", "events", eventID}, nil)
		want := "/api/v1/events/" + url.PathEscape(eventID)
		if endpoint.EscapedPath() != want {
			t.Fatalf("endpoint escaped path = %q, want %q", endpoint.EscapedPath(), want)
		}
	})
}

func testClient(t *testing.T, transport http.RoundTripper, credential string) *Client {
	t.Helper()
	origin, err := url.Parse("https://pcr.example.com")
	if err != nil {
		t.Fatalf("Parse(): %v", err)
	}
	client, err := New(Options{
		Origin:     origin,
		Credential: credential,
		HTTPClient: &http.Client{Transport: transport},
		UserAgent:  "pcr/test",
	})
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	return client
}

func response(request *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}
}
