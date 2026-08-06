// Package appctl is the app-side client of the hostit daemon: it talks to the
// daemon's unix socket (peercred-authenticated, so no tokens), which performs
// all container and service work as the app user. It runs both on the host
// (login shell wrapper) and inside app containers (mounted socket and binary).
package appctl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"heckel.io/hostit/config"
)

const (
	// requestTimeout bounds socket calls; ensure/up can build images, so generous
	requestTimeout = 10 * time.Minute
)

// Controller is a client for the daemon's /v1/self API
type Controller struct {
	socketFile string
	client     *http.Client
}

// NewController creates a Controller talking to the daemon socket
func NewController(socketFile string) *Controller {
	return &Controller{
		socketFile: socketFile,
		client: &http.Client{
			Timeout: requestTimeout,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", socketFile)
				},
			},
		},
	}
}

// Self returns the daemon's view of the calling app (name, port, URL)
func (c *Controller) Self() (*SelfInfo, error) {
	var self SelfInfo
	if err := c.request("GET", "/v1/self", &self); err != nil {
		return nil, err
	}
	return &self, nil
}

// Ensure makes sure the app's container exists and runs (provisioning an idle
// workspace if nothing is deployed yet); used on SSH login
func (c *Controller) Ensure() (string, error) {
	return c.message("POST", "/v1/self/ensure")
}

// Deploy applies the app's hostit.yml and (re)starts it
func (c *Controller) Deploy() (string, error) {
	return c.message("POST", "/v1/self/deploy")
}

// Start starts the app's run: command inside its (running) container
func (c *Controller) Start() (string, error) {
	return c.message("POST", "/v1/self/start")
}

// Stop stops the run: command but leaves the container running
func (c *Controller) Stop() (string, error) {
	return c.message("POST", "/v1/self/stop")
}

// Restart restarts the run: command (fast; no container recreate)
func (c *Controller) Restart() (string, error) {
	return c.message("POST", "/v1/self/restart")
}

// PowerOn starts the app's container
func (c *Controller) PowerOn() (string, error) {
	return c.message("POST", "/v1/self/poweron")
}

// PowerOff stops the app's container and keeps it off across reboots
func (c *Controller) PowerOff() (string, error) {
	return c.message("POST", "/v1/self/poweroff")
}

// Reboot reboots the app's container
func (c *Controller) Reboot() (string, error) {
	return c.message("POST", "/v1/self/reboot")
}

// Status returns the app's service status output
func (c *Controller) Status() (string, error) {
	var resp outputResponse
	if err := c.request("GET", "/v1/self/status", &resp); err != nil {
		return "", err
	}
	return resp.Output, nil
}

// Logs returns the last lines of the app's output
func (c *Controller) Logs(lines int) (string, error) {
	var resp outputResponse
	if err := c.request("GET", "/v1/self/logs?lines="+strconv.Itoa(lines), &resp); err != nil {
		return "", err
	}
	return resp.Output, nil
}

// DefaultSocketFile returns the daemon socket path from the default config
func DefaultSocketFile() string {
	return config.NewConfig().SocketFile
}

func (c *Controller) message(method, path string) (string, error) {
	var resp messageResponse
	if err := c.request(method, path, &resp); err != nil {
		return "", err
	}
	return resp.Message, nil
}

func (c *Controller) request(method, path string, response any) error {
	req, err := http.NewRequest(method, "http://hostit"+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach hostit daemon at %s: %w", c.socketFile, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var errResp errorResponse
		if err := json.Unmarshal(body, &errResp); err == nil && errResp.Error != "" {
			return fmt.Errorf("%s", errResp.Error)
		}
		return fmt.Errorf("daemon request failed with HTTP %d", resp.StatusCode)
	}
	if response != nil {
		return json.Unmarshal(body, response)
	}
	return nil
}
