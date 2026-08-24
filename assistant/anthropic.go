package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	// anthropicURL is the Messages API endpoint; overridable in tests
	anthropicURL = "https://api.anthropic.com/v1/messages"
	// anthropicVersion is the required API version header
	anthropicVersion = "2023-06-01"
	// requestTimeout bounds a single model call. Thinking plus a big tool result
	// can take a while, so this is generous.
	requestTimeout = 5 * time.Minute
)

// completer is the one thing the loop needs from the model: send a request, get a
// reply. An interface so the loop can be tested without calling Anthropic.
type completer interface {
	complete(ctx context.Context, req request) (*response, error)
}

// Client talks to the Anthropic Messages API over HTTP
type Client struct {
	apiKey string
	url    string
	http   *http.Client
}

var _ completer = (*Client)(nil)

// NewClient returns a client for the given API key
func NewClient(apiKey string) *Client {
	return NewClientAt(apiKey, anthropicURL)
}

// NewClientAt is NewClient pointed somewhere else: a stand-in Messages API in a
// test, or a gateway an operator puts in front of the real one. Exported so the
// wiring above this package can be driven end to end without a stub interface
// widening the package's API for the sake of one test.
func NewClientAt(apiKey, url string) *Client {
	return &Client{
		apiKey: apiKey,
		url:    url,
		http:   &http.Client{Timeout: requestTimeout},
	}
}

// complete sends one Messages API request and returns the model's reply
func (c *Client) complete(ctx context.Context, req request) (*response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", anthropicVersion)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("anthropic API: %s", apiErrorMessage(resp.StatusCode, respBody))
	}
	var out response
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("cannot parse model reply: %w", err)
	}
	return &out, nil
}

// apiErrorMessage extracts the API's error message, falling back to the raw body
func apiErrorMessage(status int, body []byte) string {
	var apiErr apiError
	if err := json.Unmarshal(body, &apiErr); err == nil && apiErr.Error.Message != "" {
		return fmt.Sprintf("HTTP %d: %s", status, apiErr.Error.Message)
	}
	return fmt.Sprintf("HTTP %d: %s", status, string(body))
}
