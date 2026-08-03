package appctl

import (
	"errors"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Mode is how an app is run: as a plain process or as a container
type Mode string

const (
	// ModeProcess runs the app's "run" command directly (must listen on $PORT)
	ModeProcess = Mode("process")
	// ModeContainer runs the app as a rootless podman container
	ModeContainer = Mode("container")
)

var (
	errNoModeConfigured = errors.New("hostit.yml does not define an app: set either \"run:\" (process mode) or \"image:\"/\"build:\" (container mode)")
	errAmbiguousMode    = errors.New("set either \"run:\" or one of \"image:\"/\"build:\", not both")
	errNoContainerPort  = errors.New("container mode requires \"container-port:\" (the port the app listens on inside the container)")
)

// AppConfig is the per-app hostit.yml, written by the app owner (or Claude)
type AppConfig struct {
	Run           string            `yaml:"run"`            // Process mode: shell command, must listen on $PORT
	Image         string            `yaml:"image"`          // Container mode: image to run
	Build         string            `yaml:"build"`          // Container mode: build context dir with a Dockerfile
	ContainerPort int               `yaml:"container-port"` // Container mode: port the app listens on inside the container
	Env           map[string]string `yaml:"env"`            // Extra environment variables
	Volumes       []string          `yaml:"volumes"`        // Container mode: volume mounts (src:dst), src relative to app dir
}

// SelfInfo is what the daemon's unix socket /v1/self endpoint returns about the
// calling app; field names match the server's app response
type SelfInfo struct {
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	Port      int       `json:"port"`
	CreatedAt time.Time `json:"created_at"`
}

// LoadAppConfig reads and validates an app's hostit.yml
func LoadAppConfig(filename string) (*AppConfig, error) {
	b, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	c := &AppConfig{}
	if err := yaml.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("cannot parse %s: %w", filename, err)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// Validate checks that exactly one mode is configured properly
func (c *AppConfig) Validate() error {
	if c.Run == "" && c.Image == "" && c.Build == "" {
		return errNoModeConfigured
	}
	if c.Run != "" && (c.Image != "" || c.Build != "") {
		return errAmbiguousMode
	}
	if c.Image != "" && c.Build != "" {
		return errAmbiguousMode
	}
	if c.Mode() == ModeContainer && c.ContainerPort == 0 {
		return errNoContainerPort
	}
	return nil
}

// Mode returns the app's run mode; only meaningful after Validate
func (c *AppConfig) Mode() Mode {
	if c.Run != "" {
		return ModeProcess
	}
	return ModeContainer
}
