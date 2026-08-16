package control

import (
	"errors"
	"io/fs"
	"strings"

	"heckel.io/hostit/appconf"
	"heckel.io/hostit/homefs"
)

const (
	// readmeFile is the app's own README: the agent reads it to learn what the
	// app is and writes back what it changed, and the owner sees it in the web
	// app; hostit's own instructions are not a file here (see "hostit guide").
	readmeFile = "README.md"
	// configFile is the app's own configuration, written by whoever builds it
	configFile = appconf.ConfigFile
	// homeMode is the files dir's permissions: root-owned (idmap-mounted) but
	// world-traversable, so sshd can reach .ssh/authorized_keys as the app user
	homeMode = 0o755
	// appLogFile is where the agent records an app's output, below the app's home
	appLogFile = appconf.AppLogFile
	// appStateFile is where the agent records the run: process state; maxStateRead
	// caps that tiny file when the daemon reads it
	appStateFile = appconf.AppStateFile
	maxStateRead = 64
	// maxLogRead caps how much of that log a request reads; the agent rotates it
	// at 10 MB, and a reader only ever wants the tail
	maxLogRead = 16 * 1024 * 1024
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
	conf, err := appconf.ParseAppConfig(b)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(conf.Description)
}

// SetDescription writes the app's one-line description into its hostit.yml,
// replacing an existing description: line or inserting one at the top, so it lives
// with the app's config where the assistant also keeps it current. The line
// surgery itself is appconf.SetDescription, next to the hostit.yml parsing.
func (m *Manager) SetDescription(name, desc string) error {
	desc = strings.ReplaceAll(strings.TrimSpace(desc), "\n", " ")
	var content string
	if b, err := m.node.ReadFile(name, configFile); err == nil {
		content = string(b)
	}
	return m.node.WriteFile(name, configFile, []byte(appconf.SetDescription(content, desc)), 0)
}
