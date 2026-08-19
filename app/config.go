// Package app is the tenant side of the contract: the hostit.yml schema (what
// an app may say about how it runs, parsed and validated -- it is written by
// the app's owner or their agent, so nothing in it may be trusted with a path
// outside the app) and the layout of an app's home, which both halves of the
// system agree on. Everything a node or control needs to know about an app's
// own files is named here, and nothing here knows about hosts or clusters.
package app

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var (
	errNoModeConfigured = errors.New("hostit.yml must say what this app is: \"mode: static\" (hostit serves public/) or \"mode: app\" with a \"run:\" command listening on $PORT")
	errUnknownMode      = errors.New("unknown mode; use \"mode: static\" or \"mode: app\"")
	errNoRunCommand     = errors.New("\"mode: app\" needs a \"run:\" command, which must listen on 0.0.0.0:$PORT")
	errRunWithoutApp    = errors.New("\"run:\" only applies to \"mode: app\"; a static app serves public/ and runs nothing")

	errBadSnapshotInterval = errors.New("snapshot.interval must be a duration (\"3h\", \"45m\") or 0 to turn automatic snapshots off")
)

// Config is the per-app hostit.yml, written by the app owner (or Claude)
type Config struct {
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
	Pre      string `yaml:"pre"`
	Post     string `yaml:"post"`
	Interval string `yaml:"interval"`
}

// DefaultSnapshotInterval is how often an app is snapshotted when its
// hostit.yml does not say. Every hour was too often: it spikes the pool and the
// cleaner for apps that change a few times a day, and the pre-deploy snapshot
// already covers the moment most rollbacks want.
const DefaultSnapshotInterval = 3 * time.Hour

// SnapshotInterval is how often this app wants automatic snapshots: the default
// when unset, and zero when the owner wrote 0 to opt out. An unparseable value
// is an error rather than a silent fallback -- a typo here would otherwise
// leave an app on a cadence its owner believes they changed.
func (c *Config) SnapshotInterval() (time.Duration, error) {
	raw := strings.TrimSpace(c.Snapshot.Interval)
	if raw == "" {
		return DefaultSnapshotInterval, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		// Bare "0" is not a duration to ParseDuration's eyes, but it is the
		// documented way to opt out, so it is read before the error surfaces.
		if raw == "0" {
			return 0, nil
		}
		return 0, fmt.Errorf("%w: %q is not a duration like \"3h\" or \"45m\"", errBadSnapshotInterval, raw)
	}
	if d < 0 {
		return 0, fmt.Errorf("%w: %q is negative", errBadSnapshotInterval, raw)
	}
	return d, nil
}

// LoadConfig parses and validates an app's hostit.yml. It takes the raw bytes
// rather than a path on purpose: hostit.yml is written by the tenant, so the
// daemon must read it through the app's os.Root (which refuses a symlink out of
// the home), never with os.ReadFile on a joined path, which would follow such a
// link as root -- straight to /dev/zero or a root-only file.
func LoadConfig(b []byte) (*Config, error) {
	c, err := ParseConfigStrict(b)
	if err != nil {
		return nil, fmt.Errorf("cannot parse %s: %w", ConfigFile, err)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// ParseConfig parses hostit.yml content without validating it; used by the
// agent, which tolerates half-written configs and simply idles on them
func ParseConfig(b []byte) (*Config, error) {
	c := &Config{}
	if err := yaml.Unmarshal(b, c); err != nil {
		return nil, err
	}
	return c, nil
}

// ParseConfigStrict also refuses keys hostit does not know. Deploying uses
// this: a typo, or a config written against a setting that no longer exists
// (the old "image:" mode), should say so rather than quietly doing something
// else.
func ParseConfigStrict(b []byte) (*Config, error) {
	c := &Config{}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return c, nil
}

// Validate checks that exactly one mode is configured properly
func (c *Config) Validate() error {
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
	if _, err := c.SnapshotInterval(); err != nil {
		return err
	}
	return nil
}

// Command returns what the agent runs inside the workspace container. A static
// app gets hostit's own file server, pointed at PublicDir: one place for the
// files an app puts on the web, so there is nothing to configure and nothing to
// get wrong.
func (c *Config) Command(hostitBin string) string {
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
