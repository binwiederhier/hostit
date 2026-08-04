// The hostit.yml contract: what an app may say about how it runs, and the
// parsing and validation that keep those strings safe to act on. Everything
// here is written by the app's own owner (or their agent), so nothing in it may
// be trusted with a path outside the app.
package appctl

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

var (
	errNoModeConfigured = errors.New("hostit.yml does not define an app: set \"static:\" (serve files), \"run:\" (run a command) or \"image:\"/\"build:\" (your own container image)")
	errAmbiguousMode    = errors.New("set either \"static:\", \"run:\" or one of \"image:\"/\"build:\", not several")
	errNoContainerPort  = errors.New("container mode requires \"container-port:\" (the port the app listens on inside the container)")
	errInvalidVolume    = errors.New("invalid mount")

	// allowedVolumeOptions is what a mount may ask for. Notably absent: "U",
	// which tells podman to chown the source to the container's uid, and the
	// propagation options, which reach outside the container.
	allowedVolumeOptions = map[string]bool{"ro": true, "rw": true, "z": true, "Z": true}
)

// AppConfig is the per-app hostit.yml, written by the app owner (or Claude)
type AppConfig struct {
	Description   string            `yaml:"description"`    // One or two lines on what this app is, kept current by whoever builds it
	Static        string            `yaml:"static"`         // Static mode: any value selects it; the directory served is always PublicDir
	Run           string            `yaml:"run"`            // Process mode: shell command, must listen on $PORT
	Image         string            `yaml:"image"`          // Container mode: image to run
	Build         string            `yaml:"build"`          // Container mode: build context dir with a Dockerfile
	ContainerPort int               `yaml:"container-port"` // Container mode: port the app listens on inside the container
	Env           map[string]string `yaml:"env"`            // Extra environment variables
	Volumes       []string          `yaml:"volumes"`        // Container mode: volume mounts (src:dst), src relative to app dir
}

// LoadAppConfig reads and validates an app's hostit.yml
func LoadAppConfig(filename string) (*AppConfig, error) {
	b, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	c, err := ParseAppConfig(b)
	if err != nil {
		return nil, fmt.Errorf("cannot parse %s: %w", filename, err)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// ParseAppConfig parses hostit.yml content without validating it; used by the
// agent, which tolerates half-written configs and simply idles on them
func ParseAppConfig(b []byte) (*AppConfig, error) {
	c := &AppConfig{}
	if err := yaml.Unmarshal(b, c); err != nil {
		return nil, err
	}
	return c, nil
}

// Validate checks that exactly one mode is configured properly
func (c *AppConfig) Validate() error {
	if c.Static == "" && c.Run == "" && c.Image == "" && c.Build == "" {
		return errNoModeConfigured
	}
	set := 0
	for _, configured := range []bool{c.Static != "", c.Run != "", c.Image != "" || c.Build != ""} {
		if configured {
			set++
		}
	}
	if set > 1 {
		return errAmbiguousMode
	}
	if c.Image != "" && c.Build != "" {
		return errAmbiguousMode
	}
	if c.Mode() == ModeContainer && c.ContainerPort == 0 {
		return errNoContainerPort
	}
	if err := validateBuild(c.Build); err != nil {
		return err
	}
	return validateVolumes(c.Volumes)
}

// validateVolumes keeps volume mounts inside the app. These strings become
// arguments to root podman, so an absolute or climbing source would mount a
// host directory into the container -- and with ":U" hand it to the app's uid.
func validateVolumes(volumes []string) error {
	for _, volume := range volumes {
		src, rest, found := strings.Cut(volume, ":")
		if !found {
			return fmt.Errorf("%w: volume %q must be src:dst", errInvalidVolume, volume)
		}
		if err := containedPath(src, "volume source"); err != nil {
			return err
		}
		_, options, hasOptions := strings.Cut(rest, ":")
		if !hasOptions {
			continue
		}
		for _, option := range strings.Split(options, ",") {
			if !allowedVolumeOptions[option] {
				return fmt.Errorf("%w: volume option %q is not allowed", errInvalidVolume, option)
			}
		}
	}
	return nil
}

// validateBuild keeps the build context inside the app; podman reads it as root
func validateBuild(build string) error {
	if build == "" {
		return nil
	}
	return containedPath(build, "build context")
}

// containedPath rejects a path that is absolute or climbs out of the app dir
func containedPath(p, what string) error {
	if p == "" || path.IsAbs(p) || filepath.IsAbs(p) {
		return fmt.Errorf("%w: %s %q must be relative to the app directory", errInvalidVolume, what, p)
	}
	for _, segment := range strings.Split(filepath.ToSlash(p), "/") {
		if segment == ".." {
			return fmt.Errorf("%w: %s %q must not leave the app directory", errInvalidVolume, what, p)
		}
	}
	return nil
}

// Mode returns the app's run mode; only meaningful after Validate
func (c *AppConfig) Mode() Mode {
	if c.Static != "" {
		return ModeStatic
	}
	if c.Run != "" {
		return ModeProcess
	}
	return ModeContainer
}

// Command returns what the agent runs inside the workspace container. Static
// apps get hostit's own file server, so they need no runtime of their own, and
// it always serves PublicDir: one place for the files an app puts on the web,
// whatever the config says.
func (c *AppConfig) Command(hostitBin string) string {
	if c.Mode() == ModeStatic {
		return fmt.Sprintf("%s static --dir %q", hostitBin, PublicDir)
	}
	return c.Run
}
