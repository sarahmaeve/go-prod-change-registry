package router_test

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sarahmaeve/go-prod-change-registry/internal/handler"
	"github.com/sarahmaeve/go-prod-change-registry/internal/humanauth"
	"github.com/sarahmaeve/go-prod-change-registry/internal/model"
)

const mcpToolsList = `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`

func newMCPRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", "2025-06-18")
	return req
}

// countStoreCalls makes every store method observable so a test can prove a
// refused request never reached it.
func countStoreCalls(ms *mockStore) *atomic.Int32 {
	var calls atomic.Int32
	ms.listFn = func(context.Context, model.ListParams) (*model.ListResult, error) {
		calls.Add(1)
		return &model.ListResult{Events: []model.ChangeEvent{}}, nil
	}
	ms.createFn = func(_ context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error) {
		calls.Add(1)
		return event, nil
	}
	ms.toggleStarFn = func(context.Context, string, string) (*model.ChangeEvent, error) {
		calls.Add(1)
		return &model.ChangeEvent{}, nil
	}
	ms.toggleAlertFn = ms.toggleStarFn
	return &calls
}

func TestMCPRequiresAPICredentials(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		requireAuthReads bool
		authorization    string
		status           int
	}{
		{"no credential", true, "", http.StatusUnauthorized},
		{"no credential while API reads are public", false, "", http.StatusUnauthorized},
		{"wrong bearer token", true, "Bearer wrong-token", http.StatusUnauthorized},
		{"bearer token", true, "Bearer " + testToken, http.StatusOK},
		{"proxy token scheme", true, "Token " + testToken, http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r, ms := newTestRouter(t, tc.requireAuthReads)
			calls := countStoreCalls(ms)
			body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_deployments","arguments":{"start_after":"2026-10-14T18:00:00Z","start_before":"2026-10-14T21:00:00Z","offset":0}}}`
			req := newMCPRequest(t, body)
			if tc.authorization != "" {
				req.Header.Set("Authorization", tc.authorization)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != tc.status {
				t.Fatalf("POST /mcp status = %d, want %d; body = %s", rec.Code, tc.status, rec.Body.String())
			}
			if tc.status == http.StatusOK {
				if calls.Load() != 1 {
					t.Errorf("store calls = %d, want one List", calls.Load())
				}
				return
			}
			assertUnauthorizedJSON(t, rec)
			if calls.Load() != 0 {
				t.Errorf("store calls = %d for a refused request, want 0", calls.Load())
			}
		})
	}
}

// assertUnauthorizedJSON checks the refusal has the REST API's shape.
func assertUnauthorizedJSON(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error.Code != "unauthorized" {
		t.Errorf("body = %s, want the API's unauthorized error", rec.Body.String())
	}
}

func TestMCPUsesBeyondIdentityPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		email  string
		groups string
		status int
	}{
		{"allowed group", "Alice@Example.com", "finance|engineering", http.StatusOK},
		{"group not allowed", "alice@example.com", "finance", http.StatusUnauthorized},
		{"group differs in case", "alice@example.com", "Engineering", http.StatusUnauthorized},
		{"missing email", "", "engineering", http.StatusUnauthorized},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			beyond := humanauth.NewBeyond(humanauth.ProviderOptions{AllowedOrgs: []string{"engineering"}})
			humanAuthH := handler.NewBeyondHumanAuthHandler(beyond, handler.HumanAuthOptions{
				SessionSecret: []byte("test-session-secret"), SessionDuration: time.Hour,
			})
			r, ms := newTestRouterWithHumanAuth(t, true, humanAuthH, "beyond")
			calls := countStoreCalls(ms)

			req := newMCPRequest(t, mcpToolsList)
			req.Header.Set("X-Beyond-Email", tc.email)
			req.Header.Set("X-Beyond-Groups", tc.groups)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != tc.status {
				t.Fatalf("POST /mcp status = %d, want %d; body = %s", rec.Code, tc.status, rec.Body.String())
			}
			if tc.status != http.StatusOK {
				assertUnauthorizedJSON(t, rec)
			}
			if calls.Load() != 0 {
				t.Errorf("store calls = %d, want 0 for tools/list", calls.Load())
			}
		})
	}
}

func TestMCPAcceptsOnlyPOST(t *testing.T) {
	t.Parallel()

	r, _ := newTestRouter(t, true)
	for _, method := range []string{http.MethodGet, http.MethodDelete, http.MethodPut} {
		req := httptest.NewRequestWithContext(t.Context(), method, "/mcp", nil)
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("Accept", "text/event-stream")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /mcp status = %d, want 405", method, rec.Code)
		}
	}
}

// A credential that can write through the REST API gains no write path
// through MCP: the only tool is the read-only listing.
func TestMCPExposesNoWriteWithWriteCapableCredential(t *testing.T) {
	t.Parallel()

	r, ms := newTestRouter(t, true)
	var writes atomic.Int32
	ms.createFn = func(_ context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error) {
		writes.Add(1)
		return event, nil
	}
	ms.toggleStarFn = func(context.Context, string, string) (*model.ChangeEvent, error) {
		writes.Add(1)
		return &model.ChangeEvent{}, nil
	}
	ms.toggleAlertFn = ms.toggleStarFn

	restWrite := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/events",
		bytes.NewBufferString(`{"user_name":"sarah","event_type":"deployment","description":"deploy"}`))
	restWrite.Header.Set("Content-Type", "application/json")
	restWrite.Header.Set("Authorization", "Bearer "+testToken)
	restRec := httptest.NewRecorder()
	r.ServeHTTP(restRec, restWrite)
	if restRec.Code != http.StatusCreated || writes.Load() != 1 {
		t.Fatalf("REST write status = %d, writes = %d; the credential must be write-capable", restRec.Code, writes.Load())
	}

	mcpPost := func(body string) map[string]json.RawMessage {
		req := newMCPRequest(t, body)
		req.Header.Set("Authorization", "Bearer "+testToken)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		var response map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatalf("%s: decode %q: %v", body, rec.Body.String(), err)
		}
		return response
	}

	var listed struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(mcpPost(mcpToolsList)["result"], &listed); err != nil || len(listed.Tools) != 1 || listed.Tools[0].Name != "list_deployments" {
		t.Errorf("tools/list = %+v, want only list_deployments", listed)
	}
	for _, name := range []string{"create_event", "toggle_star", "toggle_alert", "add_links", "close_operation"} {
		response := mcpPost(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"` + name + `","arguments":{"id":"some-id","event_type":"deployment","user_name":"sarah"}}}`)
		if response["error"] == nil && !strings.Contains(string(response["result"]), `"isError":true`) {
			t.Errorf("tools/call %s succeeded: %v", name, response)
		}
	}
	for _, method := range []string{"resources/list", "prompts/list"} {
		result := string(mcpPost(`{"jsonrpc":"2.0","id":3,"method":"` + method + `"}`)["result"])
		if !strings.Contains(result, `[]`) {
			t.Errorf("%s = %s, want an empty list", method, result)
		}
	}
	if writes.Load() != 1 {
		t.Errorf("writes = %d after MCP requests, want only the REST write", writes.Load())
	}
}

type headerTransport struct {
	base   http.RoundTripper
	header http.Header
}

func (h *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	maps.Copy(req.Header, h.header)
	return h.base.RoundTrip(req)
}

// TestMCPSDKClient drives the endpoint as an SDK client with the newest
// protocol, the way a remote agent attaches it.
func TestMCPSDKClient(t *testing.T) {
	t.Parallel()

	r, ms := newTestRouter(t, true)
	ms.listFn = func(_ context.Context, params model.ListParams) (*model.ListResult, error) {
		return &model.ListResult{Events: []model.ChangeEvent{{
			ID: "evt-1", UserName: "argocd@example.com", EventType: "deployment", Description: "api rollout",
			Timestamp: *params.StartAfter,
		}}, TotalCount: 1}, nil
	}
	server := httptest.NewServer(r)
	defer server.Close()

	httpClient := &http.Client{Timeout: 10 * time.Second, Transport: &headerTransport{
		base: http.DefaultTransport, header: http.Header{"Authorization": {"Bearer " + testToken}},
	}}
	client := mcp.NewClient(&mcp.Implementation{Name: "pcr-test", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint: server.URL + "/mcp", HTTPClient: httpClient, DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	listed, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(listed.Tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(listed.Tools))
	}
	annotations := listed.Tools[0].Annotations
	if annotations == nil || !annotations.ReadOnlyHint || annotations.DestructiveHint == nil || *annotations.DestructiveHint {
		t.Errorf("annotations = %+v, want read-only and explicitly non-destructive", annotations)
	}

	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "list_deployments", Arguments: map[string]any{
		"start_after": "2026-10-14T18:00:00Z", "start_before": "2026-10-14T21:00:00Z", "offset": 0,
	}})
	if err != nil {
		t.Fatalf("call tool: %v", err)
	}
	if result.IsError || len(result.Content) != 1 {
		t.Fatalf("result = %+v, want one non-error content block", result)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok || !strings.Contains(text.Text, `"url":"`+testPublicURL+`/events/evt-1"`) {
		t.Errorf("content = %#v, want the event with its permalink", result.Content[0])
	}
}
