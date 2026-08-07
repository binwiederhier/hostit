// Package client is a small Go client for the hostit admin REST API, used by the
// "hostit admin" CLI commands and usable as a library.
package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

// Deploy applies the app's hostit.yml and (re)starts it, and returns what the
// server said it did
func (c *Client) Deploy(name string) (string, error) {
	return c.action(name, "deploy")
}

// Start starts the app's run: command inside its (running) container
func (c *Client) Start(name string) error {
	_, err := c.action(name, "start")
	return err
}

// Stop stops the run: command but leaves the container running
func (c *Client) Stop(name string) error {
	_, err := c.action(name, "stop")
	return err
}

// Restart reloads the run: command without recreating the container
func (c *Client) Restart(name string) error {
	_, err := c.action(name, "restart")
	return err
}

// PowerOn starts the app's container
func (c *Client) PowerOn(name string) error {
	_, err := c.action(name, "poweron")
	return err
}

// PowerOff stops the app's container (and keeps it stopped across reboots)
func (c *Client) PowerOff(name string) error {
	_, err := c.action(name, "poweroff")
	return err
}

// Reboot restarts the app's container
func (c *Client) Reboot(name string) error {
	_, err := c.action(name, "reboot")
	return err
}

// SnapshotInfo is one snapshot as returned by the API
type SnapshotInfo struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	CreatedAt time.Time `json:"created_at"`
	Auto      bool      `json:"auto"`
}

// Snapshots lists an app's snapshots, newest first
func (c *Client) Snapshots(name string) ([]SnapshotInfo, error) {
	var snaps []SnapshotInfo
	if err := c.request("GET", "/api/apps/"+url.PathEscape(name)+"/snapshots", nil, &snaps); err != nil {
		return nil, err
	}
	return snaps, nil
}

// Snapshot takes a manual snapshot of the app, optionally labelled
func (c *Client) Snapshot(name, label string) (*SnapshotInfo, error) {
	var snap SnapshotInfo
	body := map[string]string{"label": label}
	if err := c.request("POST", "/api/apps/"+url.PathEscape(name)+"/snapshots", body, &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

// Rollback restores an app to the given snapshot
func (c *Client) Rollback(name, id string) error {
	path := "/api/apps/" + url.PathEscape(name) + "/snapshots/" + url.PathEscape(id) + "/restore"
	return c.request("POST", path, nil, nil)
}

// Logs returns the app's recent output
func (c *Client) Logs(name string, lines int) (string, error) {
	var resp outputResponse
	path := fmt.Sprintf("/api/apps/%s/logs?lines=%d", url.PathEscape(name), lines)
	if err := c.request("GET", path, nil, &resp); err != nil {
		return "", err
	}
	return resp.Output, nil
}

// Run executes one shell command inside the app's container. A zero timeout
// leaves the bound to the server, which caps it either way.
func (c *Client) Run(name, command string, timeoutSeconds int) (*RunResult, error) {
	var res RunResult
	req := &runRequest{Command: command, TimeoutSeconds: timeoutSeconds}
	if err := c.request("POST", "/api/apps/"+url.PathEscape(name)+"/run", req, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// action posts one lifecycle verb and returns the server's message
func (c *Client) action(name, verb string) (string, error) {
	var resp messageResponse
	if err := c.request("POST", "/api/apps/"+url.PathEscape(name)+"/"+verb, nil, &resp); err != nil {
		return "", err
	}
	return resp.Message, nil
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
