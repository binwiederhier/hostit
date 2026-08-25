package control

import (
	"errors"
	"io/fs"
	"strings"

	"heckel.io/hostit/app"
	"heckel.io/hostit/homefs"
)

const (
	// readmeFile is the app's own README: the agent reads it to learn what the
	// app is and writes back what it changed, and the owner sees it in the web
	// app; hostit's own instructions are not a file here (see "hostit guide").
	readmeFile = "README.md"
	// configFile is the app's own configuration, written by whoever builds it
	configFile = app.ConfigFile
	// maxConfigSize caps hostit.yml when it is read on a request path. Apps write
	// their own, and the app list reads one per app on every poll, so a caller
	// must not be able to turn that into megabytes of YAML parsing.
	maxConfigSize = 64 * 1024
)

// FileType, FileInfo and Listing describe an app's files. They are owned by the
// homefs package (which produces them) and re-exported here so the server, agent
// and existing callers keep using the app-level names unchanged.
type (
	FileType = homefs.FileType
	FileInfo = homefs.FileInfo
	Listing  = homefs.Listing
)

const (
	// FileTypeFile is a regular file; FileTypeDir is a directory to descend into
	FileTypeFile = homefs.FileTypeFile
	FileTypeDir  = homefs.FileTypeDir
)

// Readme returns the app's README, which doubles as the notes an agent keeps
// about what the app is and what it changed
func (m *Manager) Readme(name string) (string, error) {
	b, err := m.node.ReadFile(name, readmeFile)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	return string(b), nil
}

// WriteReadme replaces the app's README. Like every composition here, the
// file work routes through the node agent: the file lives on the app's
// hosting node, not necessarily on control's machine.
func (m *Manager) WriteReadme(name, content string) error {
	return m.node.WriteFile(name, readmeFile, []byte(content), 0)
}

// Description returns the app's own one-liner from hostit.yml, which whoever
// builds the app is asked to keep current. It parses without validating: a
// config that names no runnable mode yet still describes the app, and the
// owner's prompt depends on this being right while they are mid-edit.
func (m *Manager) Description(name string) string {
	b, err := m.node.ReadFileMax(name, configFile, maxConfigSize)
	if err != nil {
		return ""
	}
	conf, err := app.ParseConfig(b)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(conf.Description)
}

// SnapshotConfig is the app's snapshot section as hostit.yml has it: the two
// hooks and the requested interval, all raw strings so the UI shows exactly
// what the tenant wrote. An unreadable or unparseable file reports empty,
// which the UI renders as "the default".
func (m *Manager) SnapshotConfig(name string) app.SnapshotHooks {
	b, err := m.node.ReadFileMax(name, configFile, maxConfigSize)
	if err != nil {
		return app.SnapshotHooks{}
	}
	conf, err := app.ParseConfig(b)
	if err != nil {
		return app.SnapshotHooks{}
	}
	return conf.Snapshot
}

// SetSnapshotConfig writes the snapshot section back, leaving the rest of the
// tenant's hostit.yml untouched (app.SetSnapshotConfig does the line surgery).
// The interval is validated first: a value hostit cannot parse would otherwise
// be written to the file and only surface as an error on the next deploy.
func (m *Manager) SetSnapshotConfig(name string, hooks app.SnapshotHooks) error {
	probe := &app.Config{Snapshot: hooks}
	if _, err := probe.SnapshotInterval(); err != nil {
		return err
	}
	var content string
	if b, err := m.node.ReadFileMax(name, configFile, maxConfigSize); err == nil {
		content = string(b)
	}
	return m.node.WriteFile(name, configFile, []byte(app.SetSnapshotConfig(content, hooks)), 0)
}

// SetDescription writes the app's one-line description into its hostit.yml,
// replacing an existing description: line or inserting one at the top, so it lives
// with the app's config where the assistant also keeps it current. The line
// surgery itself is app.SetDescription, next to the hostit.yml parsing.
func (m *Manager) SetDescription(name, desc string) error {
	desc = strings.ReplaceAll(strings.TrimSpace(desc), "\n", " ")
	var content string
	if b, err := m.node.ReadFileMax(name, configFile, maxConfigSize); err == nil {
		content = string(b)
	}
	return m.node.WriteFile(name, configFile, []byte(app.SetDescription(content, desc)), 0)
}
