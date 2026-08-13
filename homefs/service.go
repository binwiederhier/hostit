// Package homefs owns the file I/O inside an app's home directory. Every path is
// resolved within the home through os.OpenRoot, so a symlink the app owner planted
// (their home is bind-mounted into their container and writable over scp) cannot walk
// the root daemon out of the home: the kernel refuses the traversal, not a lexical
// string check. The Service is stateless per call -- it opens os.OpenRoot(home) each
// time -- so a caller only ever hands it an app's absolute home path. Giving a
// just-created path to the app user is a hostit concern, not a plain-filesystem one,
// so it is injected as a Chowner callback and run through the same root the write used.
package homefs

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
	"slices"
	"strings"
	"time"
)

const (
	// maxUploadSize caps a single uploaded file
	maxUploadSize = 64 * 1024 * 1024
	// defaultFileMode is what an upload gets when it does not ask for a mode
	defaultFileMode = 0o644
	// homeMode is the app home's permissions when this package must create it
	// defensively: the app user and hostit only. It matches the mode useradd gives
	// the home, so a home created here looks the same as one set up elsewhere.
	homeMode = 0o750
	// tempPrefix marks the scratch file an upload streams into before it is
	// renamed into place; callers may not write names that start with it
	tempPrefix = ".hostit-upload-"
	// maxListEntries caps one directory listing; a bigger directory comes back
	// truncated rather than as megabytes of JSON
	maxListEntries = 500
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

// Chowner gives a just-created path (relative to the app home) to the app user. It
// is called through the same os.Root the write used, so an app owner cannot swap an
// intermediate directory for a symlink and redirect the root daemon's chown onto a
// host path. Injected by the caller because ownership is a hostit concern, not a
// plain-filesystem one.
type Chowner func(root *os.Root, rel string) error

// Service performs file I/O within an app's home, resolving every path through
// os.OpenRoot so symlinks cannot escape the home.
type Service struct {
	// errInvalid is wrapped by every rejection the caller reports as a bad request
	// (a traversing path, an oversized upload). Injected so it matches the caller's
	// own sentinel across the package boundary.
	errInvalid error
}

// New creates a Service. errInvalid is the sentinel every request-validation
// rejection wraps, so a caller's errors.Is check keeps working across the boundary.
func New(errInvalid error) *Service {
	return &Service{errInvalid: errInvalid}
}

// WriteFile writes a file below the app's home directory, creating parent
// directories, and gives it to the app user. A zero mode means the default;
// anything else is used as-is, so a binary or script can arrive executable.
func (s *Service) WriteFile(home, relPath string, content []byte, mode os.FileMode, chown Chowner) error {
	return s.WriteFileFrom(home, relPath, bytes.NewReader(content), mode, chown)
}

// WriteFileFrom is WriteFile for a stream, so an upload never has to exist in
// memory. It writes to a temporary file and renames on success: a body that
// turns out to be too big leaves neither a partial file nor a damaged old one.
func (s *Service) WriteFileFrom(home, relPath string, r io.Reader, mode os.FileMode, chown Chowner) error {
	rel, err := s.safeRel(relPath)
	if err != nil {
		return err
	}
	root, err := s.OpenRoot(home)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.MkdirAll(path.Dir(rel), 0o755); err != nil {
		return err
	}
	if err := chownParents(root, rel, chown); err != nil {
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
		return fmt.Errorf("%w: file exceeds %d bytes", s.errInvalid, maxUploadSize)
	}
	// O_CREATE honours the mode only on creation, and umask may have trimmed it
	if err := root.Chmod(tmpRel, fileMode(mode)); err != nil {
		return err
	}
	if err := chown(root, tmpRel); err != nil {
		return err
	}
	return root.Rename(tmpRel, rel)
}

// chownParents gives every parent directory of rel to the app user. MkdirAll
// creates missing parents as the daemon (root); a root-owned directory falls
// outside the container's uid map, so without this an upload or deploy that first
// creates uploads/ or public/ leaves it owned by host root -- the app then sees it
// as nobody and cannot read or even chown it. Re-chowning a parent that already
// belonged to the app is harmless, so there is no need to track which MkdirAll
// actually created. It runs through the same os.Root as the write, so a planted
// symlink cannot redirect the chown out of the home.
func chownParents(root *os.Root, rel string, chown Chowner) error {
	dir := path.Dir(rel)
	if dir == "." || dir == "/" {
		return nil
	}
	parts := strings.Split(dir, "/")
	for i := range parts {
		if err := chown(root, path.Join(parts[:i+1]...)); err != nil {
			return err
		}
	}
	return nil
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
func (s *Service) ReadFile(home, relPath string) ([]byte, error) {
	rel, err := s.safeRel(relPath)
	if err != nil {
		return nil, err
	}
	root, err := s.OpenRoot(home)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.ReadFile(rel)
}

// ReadFileMax reads a file up to max bytes, refusing (not truncating) anything
// larger, so a caller reading an app-controlled file cannot blow up memory.
func (s *Service) ReadFileMax(home, relPath string, max int64) ([]byte, error) {
	rel, err := s.safeRel(relPath)
	if err != nil {
		return nil, err
	}
	root, err := s.OpenRoot(home)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return s.ReadCapped(root, rel, max)
}

// FileExists reports whether a file exists in the app's home directory.
func (s *Service) FileExists(home, relPath string) bool {
	rel, err := s.safeRel(relPath)
	if err != nil {
		return false
	}
	root, err := s.OpenRoot(home)
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
func (s *Service) DeleteFile(home, relPath string) error {
	rel, err := s.safeRel(relPath)
	if err != nil {
		return err
	}
	root, err := s.OpenRoot(home)
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
func (s *Service) MoveFile(home, fromRel, toRel string) error {
	from, err := s.safeRel(fromRel)
	if err != nil {
		return err
	}
	to, err := s.safeRel(toRel)
	if err != nil {
		return err
	}
	root, err := s.OpenRoot(home)
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
func (s *Service) MakeDir(home, relPath string, chown Chowner) error {
	rel, err := s.safeRel(relPath)
	if err != nil {
		return err
	}
	root, err := s.OpenRoot(home)
	if err != nil {
		return err
	}
	defer root.Close()
	if _, err := root.Stat(rel); err == nil {
		return fmt.Errorf("%w: %s already exists", s.errInvalid, relPath)
	}
	if err := root.MkdirAll(rel, 0o755); err != nil {
		return err
	}
	return chown(root, rel)
}

// StatFile returns metadata for a single file (or directory) without reading its
// whole contents: size, modtime, type, and a best-effort MIME type. The editor
// uses it to tell a text file (worth opening in the editor) from a binary one
// (show a details card) without downloading the file just to find out.
func (s *Service) StatFile(home, relPath string) (*FileInfo, error) {
	rel, err := s.safeRel(relPath)
	if err != nil {
		return nil, err
	}
	root, err := s.OpenRoot(home)
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

// ListFiles returns one directory of the app, not the whole tree. An app with a
// node_modules would otherwise answer with tens of thousands of entries, built
// in memory, on the endpoint an agent calls first. Directories come back as
// entries to descend into, and an overlong directory is cut with Truncated set
// rather than silently shortened.
func (s *Service) ListFiles(home, dir string) (*Listing, error) {
	rel := ""
	if strings.TrimSpace(dir) != "" && strings.Trim(dir, "./") != "" {
		var err error
		if rel, err = s.safeRel(dir); err != nil {
			return nil, err
		}
	}
	root, err := s.OpenRoot(home)
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
func (s *Service) ExtractTar(home string, r io.Reader, chown Chowner) ([]string, error) {
	root, err := s.OpenRoot(home)
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
			return nil, fmt.Errorf("%w: broken archive: %s", s.errInvalid, err.Error())
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if _, err := s.safeRel(header.Name); err != nil {
				return nil, err
			}
			continue
		case tar.TypeReg:
			rel, err := s.safeRel(header.Name)
			if err != nil {
				return nil, err
			}
			// Refuse an oversized entry rather than truncating it: a silently
			// half-written file reported as written is worse than an error
			if header.Size > maxUploadSize {
				return nil, fmt.Errorf("%w: %q exceeds %d bytes", s.errInvalid, header.Name, maxUploadSize)
			}
			if err := root.MkdirAll(path.Dir(rel), 0o755); err != nil {
				return nil, err
			}
			if err := chownParents(root, rel, chown); err != nil {
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
			if err := chown(root, rel); err != nil {
				return nil, err
			}
			written = append(written, rel)
		default:
			// Symlinks and devices could point anywhere; refuse them outright
			return nil, fmt.Errorf("%w: archive entry %q has an unsupported type", s.errInvalid, header.Name)
		}
	}
}

// ReadCapped reads a file that a caller controls, refusing it beyond max rather
// than reading a prefix: half a YAML document is worse than none. It takes an open
// root so a caller can read a trusted internal path (below a protected directory)
// that safeRel would reject, while still resolving it inside the home.
func (s *Service) ReadCapped(root *os.Root, rel string, max int64) ([]byte, error) {
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
		return nil, fmt.Errorf("%w: %s is larger than %d bytes", s.errInvalid, rel, max)
	}
	return io.ReadAll(io.LimitReader(f, max))
}

// OpenRoot opens the app's home as a rooted filesystem. The app user owns that
// directory -- it is bind-mounted into their container and writable over scp --
// so every path the daemon touches as root must be resolved by the kernel with
// symlinks refused, not merely checked as a string.
func (s *Service) OpenRoot(home string) (*os.Root, error) {
	// CreateUser makes this directory; create it anyway so file operations still
	// work for an app whose Unix user was set up elsewhere (and in tests)
	if err := os.MkdirAll(home, homeMode); err != nil {
		return nil, err
	}
	return os.OpenRoot(home)
}

// safeRel validates a client-supplied path and returns it cleaned and relative,
// ready to hand to an OpenRoot. It rejects the obvious escapes early and with a
// useful message; containment itself is the root's job.
func (s *Service) safeRel(relPath string) (string, error) {
	raw := strings.TrimSpace(strings.ReplaceAll(relPath, `\`, "/"))
	// Refuse absolute paths and any ".." segment outright rather than quietly
	// normalizing them away: a caller asking for "../../etc/passwd" is confused
	// or hostile, and either way should hear about it
	if raw == "" || strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("%w: path must be relative to the app directory, got %q", s.errInvalid, relPath)
	}
	for _, segment := range strings.Split(raw, "/") {
		if segment == ".." {
			return "", fmt.Errorf("%w: path must not leave the app directory, got %q", s.errInvalid, relPath)
		}
	}
	cleaned := strings.TrimPrefix(path.Clean("/"+raw), "/")
	if cleaned == "" || cleaned == "." {
		return "", fmt.Errorf("%w: invalid path %q", s.errInvalid, relPath)
	}
	if isProtected(cleaned) {
		return "", fmt.Errorf("%w: %q is managed by hostit", s.errInvalid, relPath)
	}
	if strings.HasPrefix(path.Base(cleaned), tempPrefix) {
		return "", fmt.Errorf("%w: %q is reserved", s.errInvalid, relPath)
	}
	return cleaned, nil
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
