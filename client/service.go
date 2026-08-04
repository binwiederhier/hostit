// Package client is a small Go client for the hostit admin REST API, used by the
// "hostit admin" CLI commands and usable as a library.
package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	requestTimeout = 30 * time.Second
)

// Client talks to the hostit admin REST API
type Client struct {
	host   string // Base URL, e.g. https://hostit.apps.example.com
	token  string
	client *http.Client
}

// New creates a Client for the given base URL (scheme + host) and admin token
func New(host, token string) *Client {
	return &Client{
		host:   strings.TrimSuffix(host, "/"),
		token:  token,
		client: &http.Client{Timeout: requestTimeout},
	}
}

// CreateApp creates a new app; sshKeys (if any) go into its authorized_keys,
// and an app without keys is managed through the API alone
func (c *Client) CreateApp(name string, sshKeys []string) (*App, error) {
	var app App
	err := c.request("POST", "/api/apps", &createAppRequest{Name: name, SSHKeys: sshKeys}, &app)
	if err != nil {
		return nil, err
	}
	return &app, nil
}

// Apps lists all apps
func (c *Client) Apps() ([]*App, error) {
	apps := make([]*App, 0)
	if err := c.request("GET", "/api/apps", nil, &apps); err != nil {
		return nil, err
	}
	return apps, nil
}

// App returns a single app by name
func (c *Client) App(name string) (*App, error) {
	var app App
	if err := c.request("GET", "/api/apps/"+name, nil, &app); err != nil {
		return nil, err
	}
	return &app, nil
}

// DeleteApp deletes an app, its Unix user and its home directory
func (c *Client) DeleteApp(name string) error {
	return c.request("DELETE", "/api/apps/"+name, nil, nil)
}

// SetKeys replaces an app's authorized SSH keys
func (c *Client) SetKeys(name string, sshKeys []string) error {
	return c.request("PUT", "/api/apps/"+name+"/keys", &setKeysRequest{SSHKeys: sshKeys}, nil)
}

func (c *Client) request(method, path string, body, response any) error {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reqBody = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.host+path, reqBody)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var errResp errorResponse
		if err := json.NewDecoder(resp.Body).Decode(&errResp); err == nil && errResp.Error != "" {
			return fmt.Errorf("%s (HTTP %d)", errResp.Error, resp.StatusCode)
		}
		return fmt.Errorf("request failed with HTTP %d", resp.StatusCode)
	}
	if response != nil {
		return json.NewDecoder(resp.Body).Decode(response)
	}
	return nil
}
