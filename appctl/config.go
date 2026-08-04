// The hostit.yml contract: what an app may say about how it runs, and the
// parsing and validation that keep those strings safe to act on. Everything
// here is written by the app's own owner (or their agent), so nothing in it may
// be trusted with a path outside the app.
package appctl

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"
)

var (
	errNoModeConfigured = errors.New("hostit.yml does not define an app: set \"static:\" (serve a directory of files) or \"run:\" (run your command on $PORT)")
	errAmbiguousMode    = errors.New("set either \"static:\" or \"run:\", not both")
)

// AppConfig is the per-app hostit.yml, written by the app owner (or Claude)
type AppConfig struct {
	Description string            `yaml:"description"` // One or two lines on what this app is, kept current by whoever builds it
	Prepare     string            `yaml:"prepare"`     // Optional: build step run once before the app starts (compile, npm run build)
	Static      string            `yaml:"static"`      // Static mode: any value selects it; the directory served is always PublicDir
	Run         string            `yaml:"run"`         // Process mode: shell command, must listen on $PORT
	Env         map[string]string `yaml:"env"`         // Extra environment variables
}

// LoadAppConfig reads and validates an app's hostit.yml
func LoadAppConfig(filename string) (*AppConfig, error) {
	b, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	c, err := ParseAppConfigStrict(b)
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

// ParseAppConfigStrict also refuses keys hostit does not know. Deploying uses
// this: a typo, or a config written against a setting that no longer exists
// (the old "image:" mode), should say so rather than quietly doing something
// else.
func ParseAppConfigStrict(b []byte) (*AppConfig, error) {
	c := &AppConfig{}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return c, nil
}

// Validate checks that exactly one mode is configured properly
func (c *AppConfig) Validate() error {
	if c.Static == "" && c.Run == "" {
		return errNoModeConfigured
	}
	if c.Static != "" && c.Run != "" {
		return errAmbiguousMode
	}
	return nil
}

// Mode returns the app's run mode; only meaningful after Validate
func (c *AppConfig) Mode() Mode {
	if c.Static != "" {
		return ModeStatic
	}
	return ModeProcess
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
