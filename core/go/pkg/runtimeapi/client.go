package runtimeapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultTimeout = 10 * time.Second
	// sessionConnectTimeout allows a remote node to create a room and return a
	// session answer without making ordinary local status requests sluggish.
	sessionConnectTimeout = 5 * time.Minute
)

// Client calls the local whitetransportd runtime API.
type Client struct {
	baseURL    *url.URL
	httpClient *http.Client
}

// ClientOption customizes a runtime API client.
type ClientOption func(*Client) error

// APIError describes a non-2xx response from the runtime API.
type APIError struct {
	Method     string
	Path       string
	StatusCode int
	Message    string
	Body       string
}

// Error returns a concise runtime API error string.
func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("%s %s returned HTTP %d: %s", e.Method, e.Path, e.StatusCode, e.Message)
	}
	return fmt.Sprintf("%s %s returned HTTP %d", e.Method, e.Path, e.StatusCode)
}

// WithHTTPClient sets the underlying HTTP client.
func WithHTTPClient(httpClient *http.Client) ClientOption {
	return func(client *Client) error {
		if httpClient == nil {
			return fmt.Errorf("runtimeapi: nil http client")
		}
		client.httpClient = httpClient
		return nil
	}
}

// WithTimeout sets the timeout on the default HTTP client.
func WithTimeout(timeout time.Duration) ClientOption {
	return func(client *Client) error {
		if timeout <= 0 {
			return fmt.Errorf("runtimeapi: timeout must be positive")
		}
		client.httpClient.Timeout = timeout
		return nil
	}
}

// NewClient creates a runtime API client for a base URL such as
// http://127.0.0.1:17680.
func NewClient(baseURL string, options ...ClientOption) (*Client, error) {
	parsed, err := parseBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	client := &Client{
		baseURL:    parsed,
		httpClient: &http.Client{Timeout: defaultTimeout},
	}
	for _, option := range options {
		if err := option(client); err != nil {
			return nil, err
		}
	}
	return client, nil
}

// Status returns the current daemon status.
func (c *Client) Status(ctx context.Context) (Status, error) {
	var out Status
	if err := c.doJSON(ctx, http.MethodGet, "/v1/status", nil, &out); err != nil {
		return Status{}, err
	}
	return out, nil
}

// Nodes returns discovered runtime nodes.
func (c *Client) Nodes(ctx context.Context) ([]Node, error) {
	var out []Node
	if err := c.doJSON(ctx, http.MethodGet, "/v1/nodes", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Connect asks the daemon to connect to a node. Empty nodeID means auto-select.
func (c *Client) Connect(ctx context.Context, nodeID string) (Status, error) {
	body := struct {
		NodeID string `json:"node_id,omitempty"`
	}{NodeID: nodeID}
	var out Status
	if err := c.doJSONWithTimeout(ctx, sessionConnectTimeout, http.MethodPost, "/v1/session/connect", body, &out); err != nil {
		return Status{}, err
	}
	return out, nil
}

// Disconnect asks the daemon to disconnect the active session.
func (c *Client) Disconnect(ctx context.Context) (Status, error) {
	var out Status
	if err := c.doJSON(ctx, http.MethodPost, "/v1/session/disconnect", nil, &out); err != nil {
		return Status{}, err
	}
	return out, nil
}

// Carriers returns the daemon carrier health snapshot.
func (c *Client) Carriers(ctx context.Context) (map[string]CarrierSnapshot, error) {
	var out map[string]CarrierSnapshot
	if err := c.doJSON(ctx, http.MethodGet, "/v1/carriers", nil, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]CarrierSnapshot{}
	}
	return out, nil
}

// Build returns daemon build metadata.
func (c *Client) Build(ctx context.Context) (BuildInfo, error) {
	var out BuildInfo
	if err := c.doJSON(ctx, http.MethodGet, "/v1/build", nil, &out); err != nil {
		return BuildInfo{}, err
	}
	return out, nil
}

// DetailedHealth returns expanded daemon diagnostics.
func (c *Client) DetailedHealth(ctx context.Context) (DetailedHealth, error) {
	var out DetailedHealth
	if err := c.doJSON(ctx, http.MethodGet, "/v1/health/detailed", nil, &out); err != nil {
		return DetailedHealth{}, err
	}
	if out.Carriers == nil {
		out.Carriers = map[string]CarrierSnapshot{}
	}
	return out, nil
}

// Plan returns a carrier plan for a traffic class and optional payload size.
func (c *Client) Plan(ctx context.Context, trafficClass string, payloadBytes int) (RoutePlan, error) {
	query := url.Values{}
	if strings.TrimSpace(trafficClass) != "" {
		query.Set("traffic", trafficClass)
	}
	if payloadBytes > 0 {
		query.Set("payload_bytes", fmt.Sprintf("%d", payloadBytes))
	}
	apiPath := "/v1/plan"
	if encoded := query.Encode(); encoded != "" {
		apiPath += "?" + encoded
	}
	var out RoutePlan
	if err := c.doJSON(ctx, http.MethodGet, apiPath, nil, &out); err != nil {
		return RoutePlan{}, err
	}
	return out, nil
}

func parseBaseURL(rawBaseURL string) (*url.URL, error) {
	trimmed := strings.TrimSpace(rawBaseURL)
	if trimmed == "" {
		return nil, fmt.Errorf("runtimeapi: base URL is required")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("runtimeapi: parse base URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("runtimeapi: base URL must use http or https")
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("runtimeapi: base URL host is required")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed, nil
}

func (c *Client) doJSON(ctx context.Context, method string, apiPath string, requestBody any, out any) error {
	return c.doJSONWithClient(ctx, c.httpClient, method, apiPath, requestBody, out)
}

func (c *Client) doJSONWithTimeout(ctx context.Context, timeout time.Duration, method string, apiPath string, requestBody any, out any) error {
	client := *c.httpClient
	client.Timeout = timeout
	return c.doJSONWithClient(ctx, &client, method, apiPath, requestBody, out)
}

func (c *Client) doJSONWithClient(ctx context.Context, httpClient *http.Client, method string, apiPath string, requestBody any, out any) error {
	var body io.Reader
	if requestBody != nil {
		payload, err := json.Marshal(requestBody)
		if err != nil {
			return fmt.Errorf("runtimeapi: encode %s %s request: %w", method, apiPath, err)
		}
		body = bytes.NewReader(payload)
	}

	request, err := http.NewRequestWithContext(ctx, method, c.endpoint(apiPath), body)
	if err != nil {
		return fmt.Errorf("runtimeapi: create %s %s request: %w", method, apiPath, err)
	}
	request.Header.Set("Accept", "application/json")
	if requestBody != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("runtimeapi: %s %s request failed: %w", method, apiPath, err)
	}
	defer response.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("runtimeapi: read %s %s response: %w", method, apiPath, err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return apiError(method, apiPath, response.StatusCode, responseBody)
	}
	if out == nil {
		return nil
	}
	if len(bytes.TrimSpace(responseBody)) == 0 {
		return fmt.Errorf("runtimeapi: decode %s %s response: empty body", method, apiPath)
	}
	if err := json.Unmarshal(responseBody, out); err != nil {
		return fmt.Errorf("runtimeapi: decode %s %s response: %w", method, apiPath, err)
	}
	return nil
}

func (c *Client) endpoint(apiPath string) string {
	pathPart := apiPath
	queryPart := ""
	if cut := strings.Index(apiPath, "?"); cut >= 0 {
		pathPart = apiPath[:cut]
		queryPart = apiPath[cut+1:]
	}
	resolved := *c.baseURL
	resolved.Path = strings.TrimRight(c.baseURL.Path, "/") + "/" + strings.TrimLeft(pathPart, "/")
	resolved.RawQuery = queryPart
	return resolved.String()
}

func apiError(method string, apiPath string, statusCode int, body []byte) error {
	message := ""
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err == nil {
		if value, ok := payload["error"].(string); ok {
			message = value
		} else if value, ok := payload["message"].(string); ok {
			message = value
		}
	}
	if message == "" {
		message = strings.TrimSpace(string(body))
	}
	return &APIError{
		Method:     method,
		Path:       apiPath,
		StatusCode: statusCode,
		Message:    message,
		Body:       string(body),
	}
}
