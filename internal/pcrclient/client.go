// Package pcrclient provides a bounded, authenticated client for PCR's HTTP API.
package pcrclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const maxResponseSize = 4 << 20
const maxErrorResponseSize = 64 << 10
const maxErrorDetailSize = 4 << 10

// ErrorKind is a stable category suitable for mapping to process exit codes.
type ErrorKind int

const (
	ErrorUnexpected ErrorKind = iota
	ErrorNotFound
	ErrorUnavailable
	ErrorPermission
	ErrorRequest
)

// Error describes an API failure without retaining response bodies or headers.
// Code and Message contain only bounded, validated fields from PCR's JSON
// error envelope.
type Error struct {
	Kind    ErrorKind
	Host    string
	Status  int
	Op      string
	Code    string
	Message string
	Err     error
}

func (e *Error) Error() string {
	switch {
	case e.Status != 0 && e.Code != "" && e.Message != "":
		return fmt.Sprintf("%s: PCR at %s returned HTTP %d (%s): %s", e.Op, e.Host, e.Status, e.Code, e.Message)
	case e.Status != 0 && e.Message != "":
		return fmt.Sprintf("%s: PCR at %s returned HTTP %d: %s", e.Op, e.Host, e.Status, e.Message)
	case e.Status != 0:
		return fmt.Sprintf("%s: PCR at %s returned HTTP %d", e.Op, e.Host, e.Status)
	case e.Err != nil:
		return fmt.Sprintf("%s: PCR at %s is unavailable", e.Op, e.Host)
	default:
		return fmt.Sprintf("%s: PCR request failed", e.Op)
	}
}

func (e *Error) Unwrap() error { return e.Err }

// Options configure a Client.
type Options struct {
	Origin     *url.URL
	Credential string
	HTTPClient *http.Client
	UserAgent  string
}

// Client is an authenticated PCR API client.
type Client struct {
	origin     *url.URL
	credential string
	httpClient *http.Client
	userAgent  string
}

// New returns a PCR client with redirect refusal and bounded request time.
func New(opts Options) (*Client, error) {
	if opts.Origin == nil || opts.Origin.Scheme == "" || opts.Origin.Host == "" {
		return nil, errors.New("PCR client origin is required")
	}
	if opts.Credential == "" {
		return nil, errors.New("PCR credential is missing; set PCR_CREDENTIAL or run pcr config set-credential")
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout: 15 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	origin := *opts.Origin
	return &Client{
		origin:     &origin,
		credential: opts.Credential,
		httpClient: httpClient,
		userAgent:  opts.UserAgent,
	}, nil
}

// Event is one immutable PCR change or annotation event.
type Event struct {
	ID              string            `json:"id"`
	ExternalID      string            `json:"external_id,omitempty"`
	ParentID        string            `json:"parent_id,omitempty"`
	UserName        string            `json:"user_name"`
	UserProvider    string            `json:"user_provider,omitempty"`
	UserSubject     string            `json:"user_subject,omitempty"`
	Timestamp       time.Time         `json:"timestamp"`
	EventType       string            `json:"event_type"`
	Description     string            `json:"description"`
	LongDescription string            `json:"long_description"`
	Links           []EventLink       `json:"links,omitempty"`
	Tags            map[string]string `json:"tags,omitempty"`
	CreatedAt       time.Time         `json:"created_at"`
}

// EventLink is an ordered external reference attached to an event.
type EventLink struct {
	Label string `json:"label,omitempty"`
	URL   string `json:"url"`
}

// ListResult is a page of events.
type ListResult struct {
	Events     []Event `json:"events"`
	TotalCount int     `json:"total_count"`
	Limit      int     `json:"limit"`
	Offset     int     `json:"offset"`
}

// Annotations is the derived annotation state for an event.
type Annotations struct {
	Starred bool `json:"starred"`
	Alerted bool `json:"alerted"`
}

// ListOptions are filters accepted by GET /api/v1/events.
type ListOptions struct {
	StartAfter  *time.Time
	StartBefore *time.Time
	Around      *time.Time
	Window      time.Duration
	User        string
	EventType   string
	Tags        map[string]string
	TopLevel    bool
	AlertedOnly bool
	Limit       int
	Offset      int
}

// CurrentOptions are filters accepted by GET /api/v1/current.
type CurrentOptions struct {
	Team       string
	Scopes     []string
	Severities []string
	EventType  string
	Limit      int
	Offset     int
}

// CreateRequest is the caller-controlled portion of a new event. PCR derives
// actor identity from authenticated edge headers.
type CreateRequest struct {
	ExternalID      string            `json:"external_id"`
	EventType       string            `json:"event_type"`
	Description     string            `json:"description"`
	LongDescription string            `json:"long_description,omitempty"`
	Tags            map[string]string `json:"tags,omitempty"`
}

// List returns a filtered page of events.
func (c *Client) List(ctx context.Context, opts ListOptions) (*ListResult, error) {
	query := make(url.Values)
	setTime(query, "start_after", opts.StartAfter)
	setTime(query, "start_before", opts.StartBefore)
	setTime(query, "around", opts.Around)
	if opts.Around != nil && opts.Window != 0 {
		query.Set("window", opts.Window.String())
	}
	setString(query, "user", opts.User)
	setString(query, "type", opts.EventType)
	keys := make([]string, 0, len(opts.Tags))
	for key := range opts.Tags {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := opts.Tags[key]
		query.Add("tag", key+":"+value)
	}
	if opts.TopLevel {
		query.Set("top_level", "true")
	}
	if opts.AlertedOnly {
		query.Set("alerted", "true")
	}
	setInt(query, "limit", opts.Limit)
	setInt(query, "offset", opts.Offset)

	var result ListResult
	if err := c.get(ctx, "list events", []string{"api", "v1", "events"}, query, &result); err != nil {
		return nil, err
	}
	if result.Events == nil {
		result.Events = []Event{}
	}
	return &result, nil
}

// Get returns one event by ID.
func (c *Client) Get(ctx context.Context, eventID string) (*Event, error) {
	var event Event
	if err := c.get(ctx, "get event", []string{"api", "v1", "events", eventID}, nil, &event); err != nil {
		return nil, err
	}
	return &event, nil
}

// Annotations returns derived annotation state for one event.
func (c *Client) Annotations(ctx context.Context, eventID string) (*Annotations, error) {
	var annotations Annotations
	if err := c.get(ctx, "get event annotations", []string{"api", "v1", "events", eventID, "annotations"}, nil, &annotations); err != nil {
		return nil, err
	}
	return &annotations, nil
}

// Activity returns an event's child activity in server order.
func (c *Client) Activity(ctx context.Context, eventID string) ([]Event, error) {
	var events []Event
	if err := c.get(ctx, "get event activity", []string{"api", "v1", "events", eventID, "activity"}, nil, &events); err != nil {
		return nil, err
	}
	if events == nil {
		events = []Event{}
	}
	return events, nil
}

// Current returns a filtered page of active logical operations.
func (c *Client) Current(ctx context.Context, opts CurrentOptions) (*ListResult, error) {
	query := make(url.Values)
	setString(query, "for_team", opts.Team)
	for _, scope := range opts.Scopes {
		query.Add("scope", scope)
	}
	for _, severity := range opts.Severities {
		query.Add("severity", severity)
	}
	setString(query, "type", opts.EventType)
	setInt(query, "limit", opts.Limit)
	setInt(query, "offset", opts.Offset)

	var result ListResult
	if err := c.get(ctx, "list current operations", []string{"api", "v1", "current"}, query, &result); err != nil {
		return nil, err
	}
	if result.Events == nil {
		result.Events = []Event{}
	}
	return &result, nil
}

// Create records an event. Both 201 and the idempotent-retry 200 are success.
func (c *Client) Create(ctx context.Context, create CreateRequest) (*Event, error) {
	body, err := json.Marshal(create)
	if err != nil {
		return nil, fmt.Errorf("encode event request: %w", err)
	}
	var event Event
	if err := c.do(ctx, "create event", http.MethodPost, []string{"api", "v1", "events"}, nil, bytes.NewReader(body), []int{http.StatusOK, http.StatusCreated}, &event); err != nil {
		return nil, err
	}
	return &event, nil
}

func (c *Client) get(ctx context.Context, operation string, segments []string, query url.Values, destination any) error {
	return c.do(ctx, operation, http.MethodGet, segments, query, nil, []int{http.StatusOK}, destination)
}

func (c *Client) do(ctx context.Context, operation, method string, segments []string, query url.Values, body io.Reader, success []int, destination any) error {
	endpoint := c.endpoint(segments, query)
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return &Error{Kind: ErrorUnexpected, Host: c.origin.Host, Op: operation}
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.credential)
	if c.userAgent != "" {
		request.Header.Set("User-Agent", c.userAgent)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		return &Error{Kind: ErrorUnavailable, Host: c.origin.Host, Op: operation, Err: err}
	}
	defer func() { _ = response.Body.Close() }()
	if !containsStatus(success, response.StatusCode) {
		var code, message string
		if response.StatusCode >= http.StatusBadRequest && response.StatusCode < http.StatusInternalServerError {
			code, message = decodeErrorResponse(response.Body)
		}
		return statusError(operation, c.origin.Host, response.StatusCode, code, message)
	}

	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseSize+1))
	if err != nil {
		return &Error{Kind: ErrorUnavailable, Host: c.origin.Host, Op: operation, Err: err}
	}
	if len(data) > maxResponseSize {
		return &Error{Kind: ErrorUnavailable, Host: c.origin.Host, Op: operation}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(destination); err != nil {
		return &Error{Kind: ErrorUnavailable, Host: c.origin.Host, Op: operation}
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return &Error{Kind: ErrorUnavailable, Host: c.origin.Host, Op: operation}
	}
	return nil
}

func (c *Client) endpoint(segments []string, query url.Values) *url.URL {
	endpoint := *c.origin
	rawSegments := make([]string, len(segments))
	escapedSegments := make([]string, len(segments))
	for i, segment := range segments {
		rawSegments[i] = segment
		escapedSegments[i] = url.PathEscape(segment)
	}
	endpoint.Path = "/" + strings.Join(rawSegments, "/")
	endpoint.RawPath = "/" + strings.Join(escapedSegments, "/")
	endpoint.RawQuery = query.Encode()
	return &endpoint
}

func decodeErrorResponse(body io.Reader) (string, string) {
	data, err := io.ReadAll(io.LimitReader(body, maxErrorResponseSize+1))
	if err != nil || len(data) > maxErrorResponseSize {
		return "", ""
	}
	var response struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return "", ""
	}
	code := strings.TrimSpace(response.Error.Code)
	message := strings.TrimSpace(response.Error.Message)
	if !safeErrorCode(code) || !safeErrorMessage(message) {
		return "", ""
	}
	return code, message
}

func safeErrorCode(code string) bool {
	if code == "" || len(code) > 128 {
		return false
	}
	for _, r := range code {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}

func safeErrorMessage(message string) bool {
	return message != "" && len(message) <= maxErrorDetailSize && utf8.ValidString(message) &&
		strings.IndexFunc(message, func(r rune) bool {
			return unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp)
		}) < 0
}

func statusError(operation, host string, status int, code, message string) error {
	kind := ErrorRequest
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		kind = ErrorPermission
	case status == http.StatusNotFound:
		kind = ErrorNotFound
	case status >= 500:
		kind = ErrorUnavailable
	}
	return &Error{Kind: kind, Host: host, Status: status, Op: operation, Code: code, Message: message}
}

func containsStatus(statuses []int, status int) bool {
	for _, candidate := range statuses {
		if candidate == status {
			return true
		}
	}
	return false
}

func setTime(query url.Values, name string, value *time.Time) {
	if value != nil {
		query.Set(name, value.Format(time.RFC3339))
	}
}

func setString(query url.Values, name, value string) {
	if value != "" {
		query.Set(name, value)
	}
}

func setInt(query url.Values, name string, value int) {
	if value != 0 {
		query.Set(name, strconv.Itoa(value))
	}
}
