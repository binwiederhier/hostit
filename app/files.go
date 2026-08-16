package app

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"

	"heckel.io/hostit/appctl"
	"heckel.io/hostit/homefs"
)

const (
	// readmeFile is the app's own README: the agent reads it to learn what the
	// app is and writes back what it changed, and the owner sees it in the web
	// app; hostit's own instructions are not a file here (see "hostit guide").
	readmeFile = "README.md"
	// configFile is the app's own configuration, written by whoever builds it
	configFile = appctl.ConfigFile
	// homeMode is the files dir's permissions: root-owned (idmap-mounted) but
	// world-traversable, so sshd can reach .ssh/authorized_keys as the app user
	homeMode = 0o755
	// appLogFile is where the agent records an app's output, below the app's home
	appLogFile = appctl.AppLogFile
	// appStateFile is where the agent records the run: process state; maxStateRead
	// caps that tiny file when the daemon reads it
	appStateFile = appctl.AppStateFile
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

// WriteFile writes a file below the app's home directory, creating parent
// directories, and gives it to the app user. A zero mode means the default;
// anything else is used as-is, so a binary or script can arrive executable.
func (m *Manager) WriteFile(name, relPath string, content []byte, mode os.FileMode) error {
	return m.homefs.WriteFile(m.appFiles(name), relPath, content, mode)
}

// WriteFileFrom is WriteFile for a stream, so an upload never has to exist in
// memory.
func (m *Manager) WriteFileFrom(name, relPath string, r io.Reader, mode os.FileMode) error {
	return m.homefs.WriteFileFrom(m.appFiles(name), relPath, r, mode)
}

// ReadFile reads a file from the app's home directory
func (m *Manager) ReadFile(name, relPath string) ([]byte, error) {
	return m.homefs.ReadFile(m.appFiles(name), relPath)
}

// ReadFileMax reads a file up to max bytes, refusing (not truncating) anything
// larger, so a caller reading an app-controlled file cannot blow up memory.
func (m *Manager) ReadFileMax(name, relPath string, max int64) ([]byte, error) {
	return m.homefs.ReadFileMax(m.appFiles(name), relPath, max)
}

// FileExists reports whether a file exists in the app's home directory.
func (m *Manager) FileExists(name, relPath string) bool {
	return m.homefs.FileExists(m.appFiles(name), relPath)
}

// DeleteFile removes a file (or a directory and its contents) from the app's
// home directory.
func (m *Manager) DeleteFile(name, relPath string) error {
	return m.homefs.DeleteFile(m.appFiles(name), relPath)
}

// MoveFile renames or moves a file within the app's home.
func (m *Manager) MoveFile(name, fromRel, toRel string) error {
	return m.homefs.MoveFile(m.appFiles(name), fromRel, toRel)
}

// MakeDir creates a directory (and any missing parents) below the app's home and
// gives it to the app user, so the file browser can add an empty folder.
func (m *Manager) MakeDir(name, relPath string) error {
	return m.homefs.MakeDir(m.appFiles(name), relPath)
}

// StatFile returns metadata for a single file (or directory) without reading its
// whole contents: size, modtime, type, and a best-effort MIME type.
func (m *Manager) StatFile(name, relPath string) (*FileInfo, error) {
	return m.homefs.StatFile(m.appFiles(name), relPath)
}

// ListFiles returns one directory of the app, not the whole tree.
func (m *Manager) ListFiles(name, dir string) (*Listing, error) {
	return m.homefs.ListFiles(m.appFiles(name), dir)
}

// ExtractTar unpacks an uploaded tar archive into the app's home directory and
// returns the paths it wrote. Entries that would escape the home are refused.
func (m *Manager) ExtractTar(name string, r io.Reader) ([]string, error) {
	return m.homefs.ExtractTar(m.appFiles(name), r)
}

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
	conf, err := appctl.ParseAppConfig(b)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(conf.Description)
}

// SetDescription writes the app's one-line description into its hostit.yml,
// replacing an existing description: line or inserting one at the top, so it lives
// with the app's config where the assistant also keeps it current. The line
// surgery itself is appctl.SetDescription, next to the hostit.yml parsing.
func (m *Manager) SetDescription(name, desc string) error {
	desc = strings.ReplaceAll(strings.TrimSpace(desc), "\n", " ")
	var content string
	if b, err := m.node.ReadFile(name, configFile); err == nil {
		content = string(b)
	}
	return m.node.WriteFile(name, configFile, []byte(appctl.SetDescription(content, desc)), 0)
}
