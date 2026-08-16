package node

import (
	"io"
	"os"

	"heckel.io/hostit/homefs"
)

// WriteFile writes a file below the app's home directory, creating parent
// directories, and gives it to the app user. A zero mode means the default;
// anything else is used as-is, so a binary or script can arrive executable.
func (m *Machine) WriteFile(name, relPath string, content []byte, mode os.FileMode) error {
	return m.homefs.WriteFile(m.AppFiles(name), relPath, content, mode)
}

// WriteFileFrom is WriteFile for a stream, so an upload never has to exist in
// memory.
func (m *Machine) WriteFileFrom(name, relPath string, r io.Reader, mode os.FileMode) error {
	return m.homefs.WriteFileFrom(m.AppFiles(name), relPath, r, mode)
}

// ReadFile reads a file from the app's home directory
func (m *Machine) ReadFile(name, relPath string) ([]byte, error) {
	return m.homefs.ReadFile(m.AppFiles(name), relPath)
}

// ReadFileMax reads a file up to max bytes, refusing (not truncating) anything
// larger, so a caller reading an app-controlled file cannot blow up memory.
func (m *Machine) ReadFileMax(name, relPath string, max int64) ([]byte, error) {
	return m.homefs.ReadFileMax(m.AppFiles(name), relPath, max)
}

// FileExists reports whether a file exists in the app's home directory.
func (m *Machine) FileExists(name, relPath string) bool {
	return m.homefs.FileExists(m.AppFiles(name), relPath)
}

// DeleteFile removes a file (or a directory and its contents) from the app's
// home directory.
func (m *Machine) DeleteFile(name, relPath string) error {
	return m.homefs.DeleteFile(m.AppFiles(name), relPath)
}

// MoveFile renames or moves a file within the app's home.
func (m *Machine) MoveFile(name, fromRel, toRel string) error {
	return m.homefs.MoveFile(m.AppFiles(name), fromRel, toRel)
}

// MakeDir creates a directory (and any missing parents) below the app's home and
// gives it to the app user, so the file browser can add an empty folder.
func (m *Machine) MakeDir(name, relPath string) error {
	return m.homefs.MakeDir(m.AppFiles(name), relPath)
}

// StatFile returns metadata for a single file (or directory) without reading its
// whole contents: size, modtime, type, and a best-effort MIME type.
func (m *Machine) StatFile(name, relPath string) (*homefs.FileInfo, error) {
	return m.homefs.StatFile(m.AppFiles(name), relPath)
}

// ListFiles returns one directory of the app, not the whole tree.
func (m *Machine) ListFiles(name, dir string) (*homefs.Listing, error) {
	return m.homefs.ListFiles(m.AppFiles(name), dir)
}

// ExtractTar unpacks an uploaded tar archive into the app's home directory and
// returns the paths it wrote. Entries that would escape the home are refused.
func (m *Machine) ExtractTar(name string, r io.Reader) ([]string, error) {
	return m.homefs.ExtractTar(m.AppFiles(name), r)
}
