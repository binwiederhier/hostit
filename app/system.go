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

// CreateUser creates the app user and its home directory; 0750 so other apps
// cannot peek, and the hostit-shell login shell so SSH sessions land in the
// container.
//
// hostit makes the directory itself rather than letting useradd do it, because
// useradd would copy /etc/skel in with it. An app's directory should hold the
// app's files and hostit's own, not four dotfiles from the distribution that
// its owner never asked for and an agent has to look past.
func (o *systemOps) CreateUser(username, home string, uid int) error {
	if err := os.MkdirAll(filepath.Dir(home), 0o755); err != nil {
		return err
	}
	// A prior app in this uid block can leave an orphan group at this gid: an app
	// that was renamed (usermod --login does not rename its group) and then deleted,
	// since userdel only auto-removes a group that still shares the user's name.
	// Remove any such group first, or groupadd collides ("GID already exists") the
	// moment the freed port is reused.
	if name := groupNameForGID(uid); name != "" && name != AppsGroup {
		_ = run("groupdel", name)
	}
	// A group at the same id as the user, so the container's uid and gid blocks
	// are the one contiguous host range that lets podman idmap-mount the image.
	if err := run("groupadd", "--gid", strconv.Itoa(uid), username); err != nil {
		return err
	}
	args := createUserArgs(username, home, uid)
	if err := run(args[0], args[1:]...); err != nil {
		return err
	}
	if err := os.MkdirAll(home, homeMode); err != nil {
		return err
	}
	if err := os.Lchown(home, uid, uid); err != nil {
		return err
	}
	return os.Chmod(home, homeMode)
}

// createUserArgs is the useradd command for a new app user, pinned to a specific
// uid/gid (its contiguous id block; see IDs)
func createUserArgs(username, home string, uid int) []string {
	id := strconv.Itoa(uid)
	return []string{"useradd", "--no-create-home", "--home-dir", home, "--shell", userShellFile,
		"--uid", id, "--gid", id, "--groups", AppsGroup, "--comment", "hostit app", username}
}

// RemapUser moves an existing app user and its home to a new uid/gid block. Used
// only by the one-off migration to contiguous blocks; the app must be stopped
// first, since usermod refuses a uid change while the user has live processes.
func (o *systemOps) RemapUser(username, home string, uid int) error {
	id := strconv.Itoa(uid)
	if err := run("groupmod", "--gid", id, username); err != nil {
		return err
	}
	if err := run("usermod", "--uid", id, "--gid", username, username); err != nil {
		return err
	}
	// Flatten every file in the home to the new base: usermod only rechowns files
	// still owned by the old primary uid, not ones a container process wrote as a
	// subordinate uid.
	return run("chown", "-R", id+":"+id, home)
}

// SetUserHome repoints a user at an already-moved home. No --move-home: the
// migration renamed the directory itself, so usermod must only update the record.
func (o *systemOps) SetUserHome(username, home string) error {
	return run("usermod", "--home", home, username)
}

// RenameUser changes a user's login name; uid, home and files are untouched, so
// this is cheap and safe to do while the app keeps running. It also renames the
// user's primary group to match, so a later delete (which removes the group by the
// user's name) does not leave an orphan gid behind that would block reusing the
// freed port.
func (o *systemOps) RenameUser(oldName, newName string) error {
	if err := run("usermod", "--login", newName, oldName); err != nil {
		return err
	}
	if group := primaryGroupName(newName); group != "" && group != newName && group != AppsGroup {
		_ = run("groupmod", "--new-name", newName, group) // best-effort; cosmetic if it fails
	}
	return nil
}

// KillUserProcesses SIGKILLs every process owned by the user. pkill exits 1 when
// there was nothing to kill, which is success here, so only a worse code is an
// error.
func (o *systemOps) KillUserProcesses(username string) error {
	err := run("pkill", "-KILL", "-u", username)
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return nil // no matching processes
	}
	return err
}

// DeleteUser stops everything the user runs and removes the account including
// home. Processes can linger (container runtimes reparent, sessions close
// asynchronously), so escalate: ask systemd first, then kill what remains, and
// retry userdel while the stragglers die.
func (o *systemOps) DeleteUser(username string) error {
	// Resolve the primary group now, before the account is gone, so it can be
	// removed too. userdel only auto-removes a group named like the user; a rename
	// (usermod --login) leaves the group under its old name, so without this it
	// becomes an orphan gid that blocks reusing the freed port.
	group := primaryGroupName(username)
	_ = run("pkill", "--signal", "TERM", "--uid", username)
	var err error
	for i := 0; i < userdelRetries; i++ {
		if err = run("userdel", "--remove", username); err == nil {
			o.removeGroup(group)
			return nil
		}
		// userdel can remove the account yet still exit non-zero (e.g. it could not
		// remove the home directory), and the next retry then reports "no such user".
		// Once the account is gone the delete has succeeded, so stop treating the
		// leftover error as failure -- otherwise a half-removed app is undeletable.
		if !o.UserExists(username) {
			o.removeGroup(group)
			return nil
		}
		if i == userdelKillAfter {
			_ = run("pkill", "--signal", "KILL", "--uid", username)
		}
		time.Sleep(userdelDelay)
	}
	return err
}

// removeGroup deletes an app's primary group, best-effort: userdel may already
// have removed it (when its name still matched the user's), and the shared apps
// group is never an app's primary group, but guard it anyway.
func (o *systemOps) removeGroup(name string) {
	if name != "" && name != AppsGroup {
		_ = run("groupdel", name)
	}
}

// primaryGroupName returns a user's primary group name, or "" if it cannot be
// resolved. It reads the group by the user's gid, so it is correct even when a
// rename left the group under a name that differs from the user's.
func primaryGroupName(username string) string {
	u, err := user.Lookup(username)
	if err != nil {
		return ""
	}
	g, err := user.LookupGroupId(u.Gid)
	if err != nil {
		return ""
	}
	return g.Name
}

// groupNameForGID returns the name of the group at gid, or "" if there is none.
func groupNameForGID(gid int) string {
	g, err := user.LookupGroupId(strconv.Itoa(gid))
	if err != nil {
		return ""
	}
	return g.Name
}

// LookupIDs returns the app's contiguous id block: its uid/gid (which become
// container root) and the block size that runs up from there. No /etc/subuid
// lookup: the block is [uid, uid+uidBlockSize), mapped explicitly by root.
func (o *systemOps) LookupIDs(username string) (IDs, error) {
	uid, gid, err := lookupIDs(username)
	if err != nil {
		return IDs{}, err
	}
	return IDs{UID: uid, GID: gid, Count: uidBlockSize}, nil
}

// ImageExists reports whether the daemon's image store holds the given image
func (o *systemOps) ImageExists(tag string) bool {
	return run("podman", "image", "exists", tag) == nil
}

// BuildImage builds an image into the daemon's image store, shared by all apps
func (o *systemOps) BuildImage(contextDir, tag string) error {
	return run("podman", "build", "--tag", tag, contextDir)
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

// ChownToUserIn gives a path (relative to the app's home) to the app user,
// chowning *through the app's os.Root* so an app owner cannot swap an
// intermediate directory for a symlink and redirect the root daemon's chown onto
// a host path. Lchown, not Chown, so the final component's symlink is not
// followed either.
func (o *systemOps) ChownToUserIn(root *os.Root, username, rel string) error {
	uid, gid, err := lookupIDs(username)
	if err != nil {
		return err
	}
	return root.Lchown(rel, uid, gid)
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
