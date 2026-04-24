package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

// client is a thin wrapper over http.Client that knows the server's base URL
// and bearer token. It never panics on HTTP errors -- the caller decides
// what status / body shape constitutes a passing test.
type client struct {
	baseURL string
	token   string
	http    *http.Client
	verbose bool
}

func newClient(baseURL, token string, verbose bool) (*client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("cookie jar: %w", err)
	}
	return &client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http: &http.Client{
			Timeout: 10 * time.Second,
			Jar:     jar,
		},
		verbose: verbose,
	}, nil
}

// response captures the parts of an HTTP response the test cases compare against.
// Body is buffered so cases can decode it more than once.
type response struct {
	Status int
	Header http.Header
	Body   []byte
}

// do issues an HTTP request. authMode controls whether the bearer token is
// attached. Cookies are always sent via the underlying http.Client jar.
func (c *client) do(ctx context.Context, method, path string, body io.Reader, opts ...reqOpt) (*response, error) {
	cfg := reqConfig{auth: authBearer}
	for _, o := range opts {
		o(&cfg)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if cfg.contentType != "" {
		req.Header.Set("Content-Type", cfg.contentType)
	}
	switch cfg.auth {
	case authBearer:
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
	case authWrongBearer:
		req.Header.Set("Authorization", "Bearer not-the-right-token")
	case authNone:
		// no header
	}

	if c.verbose {
		fmt.Printf("    -> %s %s (auth=%v)\n", method, path, cfg.auth)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	buf, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	if c.verbose {
		preview := string(buf)
		if len(preview) > 200 {
			preview = preview[:200] + "..."
		}
		fmt.Printf("    <- %d %s\n", resp.StatusCode, preview)
	}

	return &response{
		Status: resp.StatusCode,
		Header: resp.Header,
		Body:   buf,
	}, nil
}

// getJSON is a convenience for GET + decode-into-T.
func (c *client) getJSON(ctx context.Context, path string, out any, opts ...reqOpt) (*response, error) {
	resp, err := c.do(ctx, http.MethodGet, path, nil, opts...)
	if err != nil {
		return nil, err
	}
	if out != nil && resp.Status >= 200 && resp.Status < 300 {
		if err := json.Unmarshal(resp.Body, out); err != nil {
			return resp, fmt.Errorf("decode response: %w (body: %s)", err, truncate(resp.Body, 200))
		}
	}
	return resp, nil
}

// postJSON marshals body to JSON and posts it.
func (c *client) postJSON(ctx context.Context, path string, body, out any, opts ...reqOpt) (*response, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	opts = append(opts, withContentType("application/json"))
	resp, err := c.do(ctx, http.MethodPost, path, bytes.NewReader(buf), opts...)
	if err != nil {
		return nil, err
	}
	if out != nil && resp.Status >= 200 && resp.Status < 300 {
		if err := json.Unmarshal(resp.Body, out); err != nil {
			return resp, fmt.Errorf("decode response: %w (body: %s)", err, truncate(resp.Body, 200))
		}
	}
	return resp, nil
}

// postForm submits an x-www-form-urlencoded body.
func (c *client) postForm(ctx context.Context, path string, form url.Values, opts ...reqOpt) (*response, error) {
	opts = append(opts, withContentType("application/x-www-form-urlencoded"))
	return c.do(ctx, http.MethodPost, path, strings.NewReader(form.Encode()), opts...)
}

// expectStatus returns an error if r.Status != want, formatting the body for diagnosis.
func expectStatus(r *response, want int) error {
	if r.Status != want {
		return fmt.Errorf("expected status %d, got %d (body: %s)", want, r.Status, truncate(r.Body, 200))
	}
	return nil
}

func truncate(b []byte, n int) string {
	s := string(b)
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// --- request options ---

type authMode int

const (
	authBearer authMode = iota
	authNone
	authWrongBearer
)

func (a authMode) String() string {
	switch a {
	case authNone:
		return "none"
	case authWrongBearer:
		return "wrong-bearer"
	default:
		return "bearer"
	}
}

type reqConfig struct {
	auth        authMode
	contentType string
}

type reqOpt func(*reqConfig)

func withAuth(mode authMode) reqOpt {
	return func(c *reqConfig) {
		c.auth = mode
	}
}

func withContentType(ct string) reqOpt {
	return func(c *reqConfig) {
		c.contentType = ct
	}
}
