package app

import (
	"archive/tar"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"heckel.io/hostit/appctl"
)

const (
	// readmeFile is the app's own README: the agent reads it to learn what the
	// app is and writes back what it changed, and the owner sees it in the web
	// app. hostit's own instructions live in HOSTIT.txt, so the two never fight.
	readmeFile = "README.md"
	// maxUploadSize caps a single uploaded file
	maxUploadSize = 64 * 1024 * 1024
	// defaultFileMode is what an upload gets when it does not ask for a mode
	defaultFileMode = 0o644
)

var (
	// protectedDirs hold hostit's own state and the account's SSH keys: callers
	// may neither read them through the API nor overwrite them. Other dotfiles
	// are merely hidden from listings (useradd copies shell dotfiles from
	// /etc/skel, which are noise for an agent) but stay writable, so an app can
	// still have its own .env or .dockerignore.
	protectedDirs = []string{".hostit/", ".ssh/", ".config/", ".local/", ".cache/"}
)

// FileInfo describes one file in an app's home directory
type FileInfo struct {
	Path     string    `json:"path"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

// WriteFile writes a file below the app's home directory, creating parent
// directories, and gives it to the app user. A zero mode means the default;
// anything else is used as-is, so a binary or script can arrive executable.
func (m *Manager) WriteFile(name, relPath string, content []byte, mode os.FileMode) error {
	full, err := m.safePath(name, relPath)
	if err != nil {
		return err
	}
	if len(content) > maxUploadSize {
		return fmt.Errorf("%w: file exceeds %d bytes", ErrInvalid, maxUploadSize)
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(full, content, fileMode(mode)); err != nil {
		return err
	}
	// WriteFile only applies the mode when it creates the file, so an overwrite
	// that changes the mode needs saying twice
	if err := os.Chmod(full, fileMode(mode)); err != nil {
		return err
	}
	return m.chownToApp(name, full)
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
	full, err := m.safePath(name, relPath)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(full)
}

// DeleteFile removes a file from the app's home directory
func (m *Manager) DeleteFile(name, relPath string) error {
	full, err := m.safePath(name, relPath)
	if err != nil {
		return err
	}
	return os.Remove(full)
}

// ListFiles returns the app's own files, skipping hostit's internal state
func (m *Manager) ListFiles(name string) ([]*FileInfo, error) {
	home := m.appHome(name)
	files := make([]*FileInfo, 0)
	err := filepath.WalkDir(home, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // Unreadable entries must not abort the listing
		}
		rel, relErr := filepath.Rel(home, p)
		if relErr != nil || rel == "." {
			return nil
		}
		if d.IsDir() {
			if isHiddenFromListing(rel + "/") {
				return filepath.SkipDir
			}
			return nil
		}
		if isHiddenFromListing(rel) {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil
		}
		files = append(files, &FileInfo{Path: rel, Size: info.Size(), Modified: info.ModTime()})
		return nil
	})
	return files, err
}

// ExtractTar unpacks an uploaded tar archive into the app's home directory and
// returns the paths it wrote. Entries that would escape the home are refused.
func (m *Manager) ExtractTar(name string, r io.Reader) ([]string, error) {
	written := make([]string, 0)
	tr := tar.NewReader(r)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return written, nil
		} else if err != nil {
			return nil, fmt.Errorf("%w: broken archive: %s", ErrInvalid, err.Error())
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if _, err := m.safePath(name, header.Name); err != nil {
				return nil, err
			}
			continue
		case tar.TypeReg:
			full, err := m.safePath(name, header.Name)
			if err != nil {
				return nil, err
			}
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return nil, err
			}
			mode := fileMode(os.FileMode(header.Mode))
			f, err := os.OpenFile(full, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
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
			if err := os.Chmod(full, mode); err != nil {
				return nil, err
			}
			if err := m.chownToApp(name, full); err != nil {
				return nil, err
			}
			written = append(written, path.Clean(header.Name))
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
	if os.IsNotExist(err) {
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
// builds the app is asked to keep current
func (m *Manager) Description(name string) string {
	conf, err := appctl.LoadAppConfig(filepath.Join(m.appHome(name), "hostit.yml"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(conf.Description)
}

// safePath resolves a client-supplied relative path inside the app's home and
// refuses anything that would escape it
func (m *Manager) safePath(name, relPath string) (string, error) {
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
	home := m.appHome(name)
	full := filepath.Join(home, filepath.FromSlash(cleaned))
	if !strings.HasPrefix(full, home+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: invalid path %q", ErrInvalid, relPath)
	}
	return full, nil
}

// chownToApp gives a written file to the app user, so it is theirs inside the
// container (where their uid is root) and over SSH
func (m *Manager) chownToApp(name, full string) error {
	return m.ops.ChownToUser(name, full)
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
	for _, segment := range strings.Split(rel, "/") {
		if strings.HasPrefix(segment, ".") {
			return true
		}
	}
	return false
}
