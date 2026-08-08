package app

import (
	"archive/tar"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"heckel.io/hostit/appctl"
)

const (
	// readmeFile is the app's own README: the agent reads it to learn what the
	// app is and writes back what it changed, and the owner sees it in the web
	// app; hostit's own instructions are not a file here (see "hostit guide").
	readmeFile = "README.md"
	// maxUploadSize caps a single uploaded file
	maxUploadSize = 64 * 1024 * 1024
	// defaultFileMode is what an upload gets when it does not ask for a mode
	defaultFileMode = 0o644
	// configFile is the app's own configuration, written by whoever builds it
	configFile = appctl.ConfigFile
	// homeMode is the app home's permissions: the app user and hostit only
	homeMode = 0o750
	// appLogFile is where the agent records an app's output, below the app's home
	appLogFile = appctl.AppLogFile
	// appStateFile is where the agent records the run: process state; maxStateRead
	// caps that tiny file when the daemon reads it
	appStateFile = appctl.AppStateFile
	maxStateRead = 64
	// maxLogRead caps how much of that log a request reads; the agent rotates it
	// at 10 MB, and a reader only ever wants the tail
	maxLogRead = 16 * 1024 * 1024
	// tempPrefix marks the scratch file an upload streams into before it is
	// renamed into place; callers may not write names that start with it
	tempPrefix = ".hostit-upload-"
	// maxListEntries caps one directory listing; a bigger directory comes back
	// truncated rather than as megabytes of JSON
	maxListEntries = 500
	// maxConfigSize caps hostit.yml when it is read on a request path. Apps write
	// their own, and the app list reads one per app on every poll, so a caller
	// must not be able to turn that into megabytes of YAML parsing.
	maxConfigSize = 64 * 1024
)

var (
	// protectedDirs hold hostit's own state and the account's SSH keys: callers
	// may neither read them through the API nor overwrite them. Other dotfiles
	// are merely hidden from listings (useradd copies shell dotfiles from
	// /etc/skel, which are noise for an agent) but stay writable, so an app can
	// still have its own .env or .dockerignore.
	protectedDirs = []string{".hostit/", ".ssh/", ".config/", ".local/", ".cache/"}

	// skippedDirs are somebody else's code: an agent gains nothing from paging
	// through them, and they dwarf everything the app actually wrote
	skippedDirs = []string{"node_modules", "vendor", "__pycache__", ".git", ".venv", "venv", "target"}
)

// FileType distinguishes the two things a listing can contain
type FileType string

const (
	// FileTypeFile is a regular file; FileTypeDir is a directory to descend into
	FileTypeFile = FileType("file")
	FileTypeDir  = FileType("dir")
)

// FileInfo describes one entry in an app's directory
type FileInfo struct {
	Path     string    `json:"path"`
	Type     FileType  `json:"type"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	Mime     string    `json:"mime,omitempty"` // set by StatFile only, not by directory listings
}

// Listing is one directory's worth of entries
type Listing struct {
	Path      string      `json:"path"`
	Files     []*FileInfo `json:"files"`
	Truncated bool        `json:"truncated"`
}

// WriteFile writes a file below the app's home directory, creating parent
// directories, and gives it to the app user. A zero mode means the default;
// anything else is used as-is, so a binary or script can arrive executable.
func (m *Manager) WriteFile(name, relPath string, content []byte, mode os.FileMode) error {
	return m.WriteFileFrom(name, relPath, bytes.NewReader(content), mode)
}

// WriteFileFrom is WriteFile for a stream, so an upload never has to exist in
// memory. It writes to a temporary file and renames on success: a body that
// turns out to be too big leaves neither a partial file nor a damaged old one.
func (m *Manager) WriteFileFrom(name, relPath string, r io.Reader, mode os.FileMode) error {
	rel, err := m.safeRel(name, relPath)
	if err != nil {
		return err
	}
	root, err := m.appRoot(name)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.MkdirAll(path.Dir(rel), 0o755); err != nil {
		return err
	}
	tmpRel := path.Join(path.Dir(rel), tempPrefix+randomSuffix())
	tmp, err := root.OpenFile(tmpRel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, fileMode(mode))
	if err != nil {
		return err
	}
	defer root.Remove(tmpRel) // No-op once renamed
	// One byte past the cap, so a file exactly at the limit still fits and
	// anything beyond it is detected without reading the rest
	written, err := io.Copy(tmp, io.LimitReader(r, maxUploadSize+1))
	if err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if written > maxUploadSize {
		return fmt.Errorf("%w: file exceeds %d bytes", ErrInvalid, maxUploadSize)
	}
	// O_CREATE honours the mode only on creation, and umask may have trimmed it
	if err := root.Chmod(tmpRel, fileMode(mode)); err != nil {
		return err
	}
	if err := m.chownToApp(name, tmpRel); err != nil {
		return err
	}
	return root.Rename(tmpRel, rel)
}

// randomSuffix names an upload's scratch file; concurrent uploads of the same
// path must not land on each other
func randomSuffix() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err) // Only fails if the system entropy source is broken
	}
	return hex.EncodeToString(b)
}

// fileMode keeps uploaded permissions sane: the owner can always read and
// write, nobody else can write, and anything beyond that (the execute bits) is
// the caller's choice
func fileMode(mode os.FileMode) os.FileMode {
	if mode == 0 {
		return defaultFileMode
	}
	return (mode.Perm() | 0o600) &^ 0o022
}

// ReadFile reads a file from the app's home directory
func (m *Manager) ReadFile(name, relPath string) ([]byte, error) {
	rel, err := m.safeRel(name, relPath)
	if err != nil {
		return nil, err
	}
	root, err := m.appRoot(name)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.ReadFile(rel)
}

// ReadFileMax reads a file up to max bytes, refusing (not truncating) anything
// larger, so a caller reading an app-controlled file cannot blow up memory.
func (m *Manager) ReadFileMax(name, relPath string, max int64) ([]byte, error) {
	rel, err := m.safeRel(name, relPath)
	if err != nil {
		return nil, err
	}
	root, err := m.appRoot(name)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return readCapped(root, rel, max)
}

// FileExists reports whether a file exists in the app's home directory.
func (m *Manager) FileExists(name, relPath string) bool {
	rel, err := m.safeRel(name, relPath)
	if err != nil {
		return false
	}
	root, err := m.appRoot(name)
	if err != nil {
		return false
	}
	defer root.Close()
	if _, err := root.Stat(rel); err != nil {
		return false
	}
	return true
}

// DeleteFile removes a file (or a directory and its contents) from the app's
// home directory. RemoveAll so deleting a folder from the file browser works and
// so a repeated delete is idempotent.
func (m *Manager) DeleteFile(name, relPath string) error {
	rel, err := m.safeRel(name, relPath)
	if err != nil {
		return err
	}
	root, err := m.appRoot(name)
	if err != nil {
		return err
	}
	defer root.Close()
	return root.RemoveAll(rel)
}

// MoveFile renames or moves a file within the app's home -- dragging it into
// another folder, or renaming it in place. Both paths stay inside the home
// (safeRel), the destination's parent is created if needed, and an existing
// destination is refused rather than silently clobbered.
func (m *Manager) MoveFile(name, fromRel, toRel string) error {
	from, err := m.safeRel(name, fromRel)
	if err != nil {
		return err
	}
	to, err := m.safeRel(name, toRel)
	if err != nil {
		return err
	}
	root, err := m.appRoot(name)
	if err != nil {
		return err
	}
	defer root.Close()
	if _, err := root.Stat(to); err == nil {
		return fmt.Errorf("cannot move to %s: a file already exists there", toRel)
	}
	if err := root.MkdirAll(path.Dir(to), 0o755); err != nil {
		return err
	}
	return root.Rename(from, to)
}

// MakeDir creates a directory (and any missing parents) below the app's home and
// gives it to the app user, so the file browser can add an empty folder. It
// refuses a path that already exists rather than silently succeeding.
func (m *Manager) MakeDir(name, relPath string) error {
	rel, err := m.safeRel(name, relPath)
	if err != nil {
		return err
	}
	root, err := m.appRoot(name)
	if err != nil {
		return err
	}
	defer root.Close()
	if _, err := root.Stat(rel); err == nil {
		return fmt.Errorf("%w: %s already exists", ErrInvalid, relPath)
	}
	if err := root.MkdirAll(rel, 0o755); err != nil {
		return err
	}
	return m.chownToApp(name, rel)
}

// StatFile returns metadata for a single file (or directory) without reading its
// whole contents: size, modtime, type, and a best-effort MIME type. The editor
// uses it to tell a text file (worth opening in the editor) from a binary one
// (show a details card) without downloading the file just to find out.
func (m *Manager) StatFile(name, relPath string) (*FileInfo, error) {
	rel, err := m.safeRel(name, relPath)
	if err != nil {
		return nil, err
	}
	root, err := m.appRoot(name)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := root.Stat(rel)
	if err != nil {
		return nil, err
	}
	file := &FileInfo{Path: rel, Type: FileTypeFile, Size: info.Size(), Modified: info.ModTime()}
	if info.IsDir() {
		file.Type, file.Size = FileTypeDir, 0
		return file, nil
	}
	file.Mime = detectMime(root, rel)
	return file, nil
}

// detectMime returns a best-effort MIME type for a file: by extension first,
// then by sniffing the first 512 bytes when the extension is unknown, so a
// no-extension binary still resolves to a non-text type.
func detectMime(root *os.Root, rel string) string {
	if t := mime.TypeByExtension(path.Ext(rel)); t != "" {
		return t
	}
	f, err := root.Open(rel)
	if err != nil {
		return "application/octet-stream"
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := io.ReadFull(f, buf) // short files give ErrUnexpectedEOF with n set; that is fine
	return http.DetectContentType(buf[:n])
}

// ListFiles returns the app's own files, skipping hostit's internal state
// ListFiles returns one directory of the app, not the whole tree. An app with a
// node_modules would otherwise answer with tens of thousands of entries, built
// in memory, on the endpoint an agent calls first. Directories come back as
// entries to descend into, and an overlong directory is cut with Truncated set
// rather than silently shortened.
func (m *Manager) ListFiles(name, dir string) (*Listing, error) {
	rel := ""
	if strings.TrimSpace(dir) != "" && strings.Trim(dir, "./") != "" {
		var err error
		if rel, err = m.safeRel(name, dir); err != nil {
			return nil, err
		}
	}
	root, err := m.appRoot(name)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	target := rel
	if target == "" {
		target = "."
	}
	entries, err := fs.ReadDir(root.FS(), target)
	if err != nil {
		return nil, err
	}
	listing := &Listing{Path: rel, Files: make([]*FileInfo, 0, len(entries))}
	for _, entry := range entries {
		child := entry.Name()
		if rel != "" {
			child = rel + "/" + child
		}
		if isHiddenFromListing(child) || (entry.IsDir() && isHiddenFromListing(child+"/")) {
			continue
		}
		if len(listing.Files) >= maxListEntries {
			listing.Truncated = true
			break
		}
		info, err := entry.Info()
		if err != nil {
			continue // An entry that vanished mid-listing is not worth failing over
		}
		file := &FileInfo{Path: child, Type: FileTypeFile, Size: info.Size(), Modified: info.ModTime()}
		if entry.IsDir() {
			file.Type, file.Size = FileTypeDir, 0
		}
		listing.Files = append(listing.Files, file)
	}
	return listing, nil
}

// ExtractTar unpacks an uploaded tar archive into the app's home directory and
// returns the paths it wrote. Entries that would escape the home are refused.
func (m *Manager) ExtractTar(name string, r io.Reader) ([]string, error) {
	root, err := m.appRoot(name)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	written := make([]string, 0)
	tr := tar.NewReader(r)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return written, nil
		} else if err != nil {
			return nil, fmt.Errorf("%w: broken archive: %s", ErrInvalid, err.Error())
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if _, err := m.safeRel(name, header.Name); err != nil {
				return nil, err
			}
			continue
		case tar.TypeReg:
			rel, err := m.safeRel(name, header.Name)
			if err != nil {
				return nil, err
			}
			// Refuse an oversized entry rather than truncating it: a silently
			// half-written file reported as written is worse than an error
			if header.Size > maxUploadSize {
				return nil, fmt.Errorf("%w: %q exceeds %d bytes", ErrInvalid, header.Name, maxUploadSize)
			}
			if err := root.MkdirAll(path.Dir(rel), 0o755); err != nil {
				return nil, err
			}
			mode := fileMode(os.FileMode(header.Mode))
			f, err := root.OpenFile(rel, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
			if err != nil {
				return nil, err
			}
			_, err = io.Copy(f, io.LimitReader(tr, maxUploadSize))
			closeErr := f.Close()
			if err != nil {
				return nil, err
			}
			if closeErr != nil {
				return nil, closeErr
			}
			if err := root.Chmod(rel, mode); err != nil {
				return nil, err
			}
			if err := m.chownToApp(name, rel); err != nil {
				return nil, err
			}
			written = append(written, rel)
		default:
			// Symlinks and devices could point anywhere; refuse them outright
			return nil, fmt.Errorf("%w: archive entry %q has an unsupported type", ErrInvalid, header.Name)
		}
	}
}

// Readme returns the app's README, which doubles as the notes an agent keeps
// about what the app is and what it changed
func (m *Manager) Readme(name string) (string, error) {
	b, err := m.ReadFile(name, readmeFile)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	return string(b), nil
}

// WriteReadme replaces the app's README
func (m *Manager) WriteReadme(name, content string) error {
	return m.WriteFile(name, readmeFile, []byte(content), 0)
}

// Description returns the app's own one-liner from hostit.yml, which whoever
// builds the app is asked to keep current. It parses without validating: a
// config that names no runnable mode yet still describes the app, and the
// owner's prompt depends on this being right while they are mid-edit.
func (m *Manager) Description(name string) string {
	root, err := m.appRoot(name)
	if err != nil {
		return ""
	}
	defer root.Close()
	b, err := readCapped(root, configFile, maxConfigSize)
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
// with the app's config where the assistant also keeps it current.
func (m *Manager) SetDescription(name, desc string) error {
	desc = strings.ReplaceAll(strings.TrimSpace(desc), "\n", " ")
	var content string
	if b, err := m.ReadFile(name, configFile); err == nil {
		content = string(b)
	}
	return m.WriteFile(name, configFile, []byte(setYAMLDescription(content, desc)), 0)
}

// setYAMLDescription replaces the description: value in a hostit.yml document, or
// prepends one if there is none. A one-liner, so no multi-line handling; the rest
// of the document (other keys, comments) is left untouched.
func setYAMLDescription(content, desc string) string {
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

// readCapped reads a file that a caller controls, refusing it beyond max rather
// than reading a prefix: half a YAML document is worse than none
func readCapped(root *os.Root, rel string, max int64) ([]byte, error) {
	f, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if stat.Size() > max {
		return nil, fmt.Errorf("%w: %s is larger than %d bytes", ErrInvalid, rel, max)
	}
	return io.ReadAll(io.LimitReader(f, max))
}

// appRoot opens the app's home as a rooted filesystem. The app user owns that
// directory -- it is bind-mounted into their container and writable over scp --
// so every path the daemon touches as root must be resolved by the kernel with
// symlinks refused, not merely checked as a string.
func (m *Manager) appRoot(name string) (*os.Root, error) {
	home := m.appHome(name)
	// CreateUser makes this directory; create it anyway so file operations still
	// work for an app whose Unix user was set up elsewhere (and in tests)
	if err := os.MkdirAll(home, homeMode); err != nil {
		return nil, err
	}
	return os.OpenRoot(home)
}

// safeRel validates a client-supplied path and returns it cleaned and relative,
// ready to hand to an appRoot. It rejects the obvious escapes early and with a
// useful message; containment itself is the root's job.
func (m *Manager) safeRel(name, relPath string) (string, error) {
	raw := strings.TrimSpace(strings.ReplaceAll(relPath, `\`, "/"))
	// Refuse absolute paths and any ".." segment outright rather than quietly
	// normalizing them away: a caller asking for "../../etc/passwd" is confused
	// or hostile, and either way should hear about it
	if raw == "" || strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("%w: path must be relative to the app directory, got %q", ErrInvalid, relPath)
	}
	for _, segment := range strings.Split(raw, "/") {
		if segment == ".." {
			return "", fmt.Errorf("%w: path must not leave the app directory, got %q", ErrInvalid, relPath)
		}
	}
	cleaned := strings.TrimPrefix(path.Clean("/"+raw), "/")
	if cleaned == "" || cleaned == "." {
		return "", fmt.Errorf("%w: invalid path %q", ErrInvalid, relPath)
	}
	if isProtected(cleaned) {
		return "", fmt.Errorf("%w: %q is managed by hostit", ErrInvalid, relPath)
	}
	if strings.HasPrefix(path.Base(cleaned), tempPrefix) {
		return "", fmt.Errorf("%w: %q is reserved", ErrInvalid, relPath)
	}
	return cleaned, nil
}

// chownToApp gives a written file to the app user, so it is theirs inside the
// container (where their uid is root) and over SSH. The path was just created
// through the app's root, and ChownToUser does not follow symlinks, so losing a
// race here means chowning a link the app user planted, not its target.
func (m *Manager) chownToApp(name, rel string) error {
	return m.ops.ChownToUser(name, filepath.Join(m.appHome(name), filepath.FromSlash(rel)))
}

// isProtected reports paths hostit manages on the app's behalf
func isProtected(rel string) bool {
	for _, prefix := range protectedDirs {
		if strings.HasPrefix(rel, prefix) || rel == strings.TrimSuffix(prefix, "/") {
			return true
		}
	}
	return false
}

// isHiddenFromListing additionally drops dotfiles from what an agent is shown
func isHiddenFromListing(rel string) bool {
	if isProtected(rel) {
		return true
	}
	for _, segment := range strings.Split(strings.TrimSuffix(rel, "/"), "/") {
		if strings.HasPrefix(segment, ".") || slices.Contains(skippedDirs, segment) {
			return true
		}
	}
	return false
}
