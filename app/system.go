package app

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// userdelRetries is how often userdel is retried while user processes are
	// still dying; after userdelKillAfter attempts, survivors are SIGKILLed
	userdelRetries   = 10
	userdelKillAfter = 2
	userdelDelay     = time.Second
	// userShellFile is the login shell for app users; it execs the SSH session
	// into the app container (see cmd/shell.go)
	userShellFile = "/usr/bin/hostit-shell"
	// sshDir holds the app's authorized_keys, below the app's home
	sshDir = ".ssh"
	// AppsGroup owns the sudoers grant that lets app users enter their own
	// container (and nothing else); see /etc/sudoers.d/hostit
	AppsGroup = "hostit-apps"
	// subUIDFile and subGIDFile hold the subordinate ranges containers map into
	subUIDFile = "/etc/subuid"
	subGIDFile = "/etc/subgid"
)

// systemOps is the real SystemOps implementation; it shells out to the usual
// system tools and must run as root
type systemOps struct{}

var _ SystemOps = (*systemOps)(nil)

// NewSystemOps returns the real, root-requiring SystemOps implementation
func NewSystemOps() SystemOps {
	return &systemOps{}
}

func (o *systemOps) UserExists(username string) bool {
	_, err := user.Lookup(username)
	return err == nil
}

func (o *systemOps) LookupUID(username string) (int, error) {
	uid, _, err := lookupIDs(username)
	return uid, err
}

// CreateUser creates the app user with its home dir; 0750 so other apps cannot
// peek, and the hostit-shell login shell so SSH sessions land in the container
func (o *systemOps) CreateUser(username, home string) error {
	if err := os.MkdirAll(filepath.Dir(home), 0o755); err != nil {
		return err
	}
	err := run("useradd", "--create-home", "--home-dir", home, "--shell", userShellFile,
		"--groups", AppsGroup, "--comment", "hostit app", username)
	if err != nil {
		return err
	}
	return os.Chmod(home, homeMode)
}

// DeleteUser stops everything the user runs and removes the account including
// home. Processes can linger (container runtimes reparent, sessions close
// asynchronously), so escalate: ask systemd first, then kill what remains, and
// retry userdel while the stragglers die.
func (o *systemOps) DeleteUser(username string) error {
	_ = run("pkill", "--signal", "TERM", "--uid", username)
	var err error
	for i := 0; i < userdelRetries; i++ {
		if err = run("userdel", "--remove", username); err == nil {
			return nil
		}
		if i == userdelKillAfter {
			_ = run("pkill", "--signal", "KILL", "--uid", username)
		}
		time.Sleep(userdelDelay)
	}
	return err
}

// LookupIDs returns the identity ranges a container is mapped into: the user's
// own uid/gid (which become container root) plus their subordinate ranges
func (o *systemOps) LookupIDs(username string) (IDs, error) {
	uid, gid, err := lookupIDs(username)
	if err != nil {
		return IDs{}, err
	}
	subUID, count, err := lookupSubID(subUIDFile, username)
	if err != nil {
		return IDs{}, err
	}
	subGID, _, err := lookupSubID(subGIDFile, username)
	if err != nil {
		return IDs{}, err
	}
	return IDs{UID: uid, GID: gid, SubUID: subUID, SubGID: subGID, SubCount: count}, nil
}

// ImageExists reports whether the daemon's image store holds the given image
func (o *systemOps) ImageExists(tag string) bool {
	return run("podman", "image", "exists", tag) == nil
}

// BuildImage builds an image into the daemon's image store, shared by all apps
func (o *systemOps) BuildImage(contextDir, tag string) error {
	return run("podman", "build", "--tag", tag, contextDir)
}

// lookupSubID reads a user's subordinate id range from /etc/subuid or /etc/subgid
func lookupSubID(filename, username string) (start int, count int, err error) {
	b, err := os.ReadFile(filename)
	if err != nil {
		return 0, 0, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Split(strings.TrimSpace(line), ":")
		if len(fields) != 3 || fields[0] != username {
			continue
		}
		if start, err = strconv.Atoi(fields[1]); err != nil {
			return 0, 0, err
		}
		if count, err = strconv.Atoi(fields[2]); err != nil {
			return 0, 0, err
		}
		return start, count, nil
	}
	return 0, 0, fmt.Errorf("no subordinate id range for %s in %s", username, filename)
}

// WriteAuthorizedKeys updates the hostit-managed block of the app's
// authorized_keys, leaving any key the user added by hand in place
func (o *systemOps) WriteAuthorizedKeys(username, home string, keys []string) error {
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
		return fmt.Errorf("%w: %s must be a directory", ErrInvalid, sshDir)
	}
	if err := root.MkdirAll(sshDir, 0o700); err != nil {
		return err
	}
	filename := sshDir + "/authorized_keys"
	existing, err := root.ReadFile(filename)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := root.WriteFile(filename, []byte(mergeAuthorizedKeys(string(existing), keys)), 0o600); err != nil {
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

// WriteScaffold writes initial files into the app home, never overwriting existing ones
func (o *systemOps) WriteScaffold(username, home string, files map[string]string) error {
	uid, gid, err := lookupIDs(username)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return err
	}
	defer root.Close()
	for name, content := range files {
		if _, err := root.Lstat(name); err == nil {
			continue
		}
		// Scaffold paths may be nested (public/index.html), and each directory
		// created on the way has to belong to the app user too
		if dir := path.Dir(name); dir != "." {
			if err := root.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			if err := root.Lchown(dir, uid, gid); err != nil {
				return err
			}
		}
		if err := root.WriteFile(name, []byte(content), 0o644); err != nil {
			return err
		}
		if err := root.Lchown(name, uid, gid); err != nil {
			return err
		}
	}
	return nil
}

// WriteUserFile writes a file (creating parent dirs) below the user's home,
// owned by the user; used for systemd user units
func (o *systemOps) WriteUserFile(username, home, relPath, content string, mode os.FileMode) error {
	uid, gid, err := lookupIDs(username)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return err
	}
	defer root.Close()

	// Create parent dirs one by one so each new one can be chowned to the user
	dir := path.Dir(filepath.ToSlash(relPath))
	var missing []string
	for d := dir; d != "." && d != "/"; d = path.Dir(d) {
		if _, err := root.Lstat(d); err != nil {
			missing = append([]string{d}, missing...)
		}
	}
	for _, d := range missing {
		if err := root.Mkdir(d, 0o755); err != nil {
			return err
		}
		if err := root.Lchown(d, uid, gid); err != nil {
			return err
		}
	}
	if err := root.WriteFile(relPath, []byte(content), mode); err != nil {
		return err
	}
	return root.Lchown(relPath, uid, gid)
}

// ChownToUser gives a path to the app user. Lchown, not Chown: the app user
// owns the directory this runs in, and chown(2) would follow a symlink they
// planted and hand its target away.
func (o *systemOps) ChownToUser(username, path string) error {
	uid, gid, err := lookupIDs(username)
	if err != nil {
		return err
	}
	return os.Lchown(path, uid, gid)
}

// ApplyPortRules atomically replaces the hostit nftables table: for each app
// port, loopback connects are only allowed for root (the proxy, which also owns
// the published container ports) and the app's own uid
func (o *systemOps) ApplyPortRules(rules []PortRule) error {
	var b strings.Builder
	b.WriteString("add table inet hostit\n")
	b.WriteString("flush table inet hostit\n")
	b.WriteString("add chain inet hostit output { type filter hook output priority filter ; policy accept ; }\n")
	for _, rule := range rules {
		fmt.Fprintf(&b, "add rule inet hostit output ip daddr 127.0.0.0/8 tcp dport %d meta skuid != { 0, %d } counter drop\n", rule.Port, rule.UID)
		fmt.Fprintf(&b, "add rule inet hostit output ip6 daddr ::1 tcp dport %d meta skuid != { 0, %d } counter drop\n", rule.Port, rule.UID)
	}
	f, err := os.CreateTemp("", "hostit-nft-*.conf")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(b.String()); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return run("nft", "-f", f.Name())
}

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

// run executes a command and returns a descriptive error including its output
func run(command string, args ...string) error {
	out, err := exec.Command(command, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s failed: %w: %s", command, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
