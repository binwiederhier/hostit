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
	"strings"

	"gopkg.in/yaml.v3"
)

var (
	errNoModeConfigured = errors.New("hostit.yml must say what this app is: \"mode: static\" (hostit serves public/) or \"mode: app\" with a \"run:\" command listening on $PORT")
	errUnknownMode      = errors.New("unknown mode; use \"mode: static\" or \"mode: app\"")
	errNoRunCommand     = errors.New("\"mode: app\" needs a \"run:\" command, which must listen on 0.0.0.0:$PORT")
	errRunWithoutApp    = errors.New("\"run:\" only applies to \"mode: app\"; a static app serves public/ and runs nothing")
)

// AppConfig is the per-app hostit.yml, written by the app owner (or Claude)
type AppConfig struct {
	Description string            `yaml:"description"` // One or two lines on what this app is, kept current by whoever builds it
	Prepare     string            `yaml:"prepare"`     // Optional: build step run once before the app starts (compile, npm run build)
	Mode        Mode              `yaml:"mode"`        // How the app runs: ModeStatic (hostit serves PublicDir) or ModeApp (Run)
	Run         string            `yaml:"run"`         // App mode: shell command, must listen on $PORT
	Env         map[string]string `yaml:"env"`         // Extra environment variables
	Snapshot    SnapshotHooks     `yaml:"snapshot"`    // Optional commands to quiesce/sync around a snapshot
}

// SnapshotHooks run in the app's container around a snapshot: Pre before it (to
// flush or .backup a database into a consistent file), Post after (to clean up).
// If Pre fails the snapshot is aborted, so a torn state is never captured.
type SnapshotHooks struct {
	Pre  string `yaml:"pre"`
	Post string `yaml:"post"`
}

// LoadAppConfig parses and validates an app's hostit.yml. It takes the raw bytes
// rather than a path on purpose: hostit.yml is written by the tenant, so the
// daemon must read it through the app's os.Root (which refuses a symlink out of
// the home), never with os.ReadFile on a joined path, which would follow such a
// link as root -- straight to /dev/zero or a root-only file.
func LoadAppConfig(b []byte) (*AppConfig, error) {
	c, err := ParseAppConfigStrict(b)
	if err != nil {
		return nil, fmt.Errorf("cannot parse %s: %w", ConfigFile, err)
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
	switch c.Mode {
	case "":
		return errNoModeConfigured
	case ModeStatic:
		if c.Run != "" {
			return errRunWithoutApp
		}
	case ModeApp:
		if c.Run == "" {
			return errNoRunCommand
		}
	default:
		return fmt.Errorf("%w: %q", errUnknownMode, c.Mode)
	}
	return nil
}

// Command returns what the agent runs inside the workspace container. A static
// app gets hostit's own file server, pointed at PublicDir: one place for the
// files an app puts on the web, so there is nothing to configure and nothing to
// get wrong.
func (c *AppConfig) Command(hostitBin string) string {
	if c.Mode == ModeStatic {
		return fmt.Sprintf("%s static", hostitBin)
	}
	return c.Run
}

// SetDescription replaces the description: value in a hostit.yml document, or
// prepends one if there is none. A one-liner, so no multi-line handling; the rest
// of the document (other keys, comments) is left untouched.
func SetDescription(content, desc string) string {
	line := "description: " + yamlQuote(desc)
	lines := strings.Split(content, "\n")
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "description:") {
			lines[i] = line
			return strings.Join(lines, "\n")
		}
	}
	if strings.TrimSpace(content) == "" {
		return line + "\n"
	}
	return line + "\n" + content
}

// yamlQuote returns a YAML double-quoted scalar, escaping backslashes and quotes.
func yamlQuote(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	return "\"" + s + "\""
}
