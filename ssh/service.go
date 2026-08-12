package ssh

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"strconv"

	cryptossh "golang.org/x/crypto/ssh"
)

const (
	// sshDir holds the app's authorized_keys, below the app's home
	sshDir = ".ssh"
)

// ErrNotDirectory is returned when the app's .ssh exists but is not a real
// directory (e.g. a symlink the tenant planted); hostit refuses to write into it.
var ErrNotDirectory = errors.New(".ssh must be a directory")

// Interface is the subset of ssh-key operations the app package depends on; the
// concrete *Service satisfies it, so a test can substitute a fake.
type Interface interface {
	WriteAuthorizedKeys(username, home string, keys []string) error
}

// Service writes app users' authorized_keys as root.
type Service struct{}

var _ Interface = (*Service)(nil)

// New builds an ssh key Service.
func New() *Service {
	return &Service{}
}

// ValidateKeys ensures every entry is a parseable authorized_keys line. It returns
// a plain error; callers that report request validation wrap it in their own
// sentinel.
func ValidateKeys(keys []string) error {
	for _, key := range keys {
		if _, _, _, _, err := cryptossh.ParseAuthorizedKey([]byte(key)); err != nil {
			return fmt.Errorf("invalid ssh key %q: %s", key, err.Error())
		}
	}
	return nil
}

// WriteAuthorizedKeys updates the hostit-managed block of the app's
// authorized_keys, leaving any key the user added by hand in place
func (s *Service) WriteAuthorizedKeys(username, home string, keys []string) error {
	uid, gid, err := lookupIDs(username)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return err
	}
	defer root.Close()
	return writeAuthorizedKeysIn(root, keys, uid, gid)
}

// writeAuthorizedKeysIn merges the managed keys into the app's authorized_keys.
// Everything goes through the root, and .ssh must be a real directory: the app
// user owns this home, and a link here would have root writing SSH keys (and
// handing out ownership) wherever they pointed it.
func writeAuthorizedKeysIn(root *os.Root, keys []string, uid, gid int) error {
	if stat, err := root.Lstat(sshDir); err == nil && !stat.IsDir() {
		return fmt.Errorf("%w: %s", ErrNotDirectory, sshDir)
	}
	if err := root.MkdirAll(sshDir, 0o700); err != nil {
		return err
	}
	filename := sshDir + "/authorized_keys"
	existing, err := root.ReadFile(filename)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := root.WriteFile(filename, []byte(MergeAuthorizedKeys(string(existing), keys)), 0o600); err != nil {
		return err
	}
	if err := root.Chmod(sshDir, 0o700); err != nil {
		return err
	}
	if err := root.Lchown(sshDir, uid, gid); err != nil {
		return err
	}
	return root.Lchown(filename, uid, gid)
}

// lookupIDs resolves an app user's uid/gid.
func lookupIDs(username string) (uid int, gid int, err error) {
	u, err := user.Lookup(username)
	if err != nil {
		return 0, 0, err
	}
	uid, err = strconv.Atoi(u.Uid)
	if err != nil {
		return 0, 0, err
	}
	gid, err = strconv.Atoi(u.Gid)
	if err != nil {
		return 0, 0, err
	}
	return uid, gid, nil
}
