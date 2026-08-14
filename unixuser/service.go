// Package unixuser manages the Unix user account (and matching group) hostit
// creates per app: creation without an /etc/skel copy, uid/gid lookup, rename,
// remap to a new id block, and a robust delete that reaps lingering processes. It
// also writes the app's initial home skeleton and hands home paths to the app user.
// Everything here shells out to the usual account tools and must run as root.
package unixuser

import (
	"errors"
	"fmt"
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
)

// Interface is the subset of user-account operations the app package depends on;
// the concrete *Service satisfies it, so a test can substitute a fake.
type Interface interface {
	Exists(username string) bool
	LookupUID(username string) (int, error)
	LookupIDs(username string) (uid, gid int, err error)
	Home(username string) (string, error)
	Create(username, home string, uid int) error
	SetHome(username, home string) error
	Rename(oldName, newName string) error
	KillProcesses(username string) error
	Delete(username string) error
	WriteSkeleton(username, home string, files map[string]string) error
	ChownIn(root *os.Root, username, rel string) error
}

// Service creates and removes app users. It carries the deployment settings a new
// account needs (login shell, supplementary group, home permissions), injected so
// the package holds no host-specific policy of its own.
type Service struct {
	shell    string
	group    string
	homeMode os.FileMode
}

var _ Interface = (*Service)(nil)

// New builds a unixuser Service. shell is the app users' login shell, group is the
// supplementary group that grants container entry, and homeMode is the app home's
// permissions.
func New(shell, group string, homeMode os.FileMode) *Service {
	return &Service{shell: shell, group: group, homeMode: homeMode}
}

// Exists reports whether a user with this name already exists.
func (s *Service) Exists(username string) bool {
	_, err := user.Lookup(username)
	return err == nil
}

// LookupUID returns a user's uid.
func (s *Service) LookupUID(username string) (int, error) {
	uid, _, err := lookupIDs(username)
	return uid, err
}

// LookupIDs returns a user's uid and gid.
func (s *Service) LookupIDs(username string) (uid, gid int, err error) {
	return lookupIDs(username)
}

// Home returns a user's passwd home directory. Being root-maintained state, it
// is trustworthy where paths inside an app's (tenant-owned) subvolume are not;
// the storage migrations key on it for that reason.
func (s *Service) Home(username string) (string, error) {
	u, err := user.Lookup(username)
	if err != nil {
		return "", err
	}
	return u.HomeDir, nil
}

// Create creates the app user and its home directory; 0750 so other apps cannot
// peek, and the hostit-shell login shell so SSH sessions land in the container.
//
// hostit makes the directory itself rather than letting useradd do it, because
// useradd would copy /etc/skel in with it. An app's directory should hold the
// app's files and hostit's own, not four dotfiles from the distribution that
// its owner never asked for and an agent has to look past.
func (s *Service) Create(username, home string, uid int) error {
	if err := os.MkdirAll(filepath.Dir(home), 0o755); err != nil {
		return err
	}
	// A prior app in this uid block can leave an orphan group at this gid: an app
	// that was renamed (usermod --login does not rename its group) and then deleted,
	// since userdel only auto-removes a group that still shares the user's name.
	// Remove any such group first, or groupadd collides ("GID already exists") the
	// moment the freed port is reused.
	if name := groupNameForGID(uid); name != "" && name != s.group {
		_ = run("groupdel", name)
	}
	// A group at the same id as the user, so the container's uid and gid blocks
	// are the one contiguous host range that lets podman idmap-mount the image.
	if err := run("groupadd", "--gid", strconv.Itoa(uid), username); err != nil {
		return err
	}
	args := createUserArgs(username, home, uid, s.shell, s.group)
	if err := run(args[0], args[1:]...); err != nil {
		return err
	}
	if err := os.MkdirAll(home, s.homeMode); err != nil {
		return err
	}
	if err := os.Lchown(home, uid, uid); err != nil {
		return err
	}
	return os.Chmod(home, s.homeMode)
}

// createUserArgs is the useradd command for a new app user, pinned to a specific
// uid/gid (its contiguous id block)
func createUserArgs(username, home string, uid int, shell, group string) []string {
	id := strconv.Itoa(uid)
	return []string{"useradd", "--no-create-home", "--home-dir", home, "--shell", shell,
		"--uid", id, "--gid", id, "--groups", group, "--comment", "hostit app", username}
}

// SetHome points an account's home directory at a new path. Only the passwd
// entry changes (no --move-home): the caller moves the files itself, which is
// what the storage-unification migration does.
func (s *Service) SetHome(username, home string) error {
	args := setHomeArgs(username, home)
	return run(args[0], args[1:]...)
}

// setHomeArgs is the usermod command SetHome runs, split out so the flag shape
// is testable without root.
func setHomeArgs(username, home string) []string {
	return []string{"usermod", "--home", home, username}
}

// Rename changes a user's login name; uid, home and files are untouched, so this
// is cheap and safe to do while the app keeps running. It also renames the user's
// primary group to match, so a later delete (which removes the group by the user's
// name) does not leave an orphan gid behind that would block reusing the freed port.
func (s *Service) Rename(oldName, newName string) error {
	if err := run("usermod", "--login", newName, oldName); err != nil {
		return err
	}
	_ = s.SyncGroupName(newName) // best-effort; cosmetic if it fails
	return nil
}

// SyncGroupName renames a user's primary group to match its login name, when the
// two have drifted apart (an app renamed before group-rename shipped kept its old
// group name). Aligning them keeps a deleted app from leaving an orphan group, and
// frees the old name so a new app can take it. Idempotent and a no-op when the
// names already agree or the user is on the shared app group.
func (s *Service) SyncGroupName(username string) error {
	group := primaryGroupName(username)
	newName, ok := groupNeedsRename(group, username, s.group)
	if !ok {
		return nil
	}
	return run("groupmod", "--new-name", newName, group)
}

// groupNeedsRename reports whether an app user's primary group should be renamed to
// match the user's login name, and to what. A group that is empty, already the
// user's name, or the shared app group is left alone.
func groupNeedsRename(currentGroup, username, sharedGroup string) (string, bool) {
	if currentGroup == "" || currentGroup == username || currentGroup == sharedGroup {
		return "", false
	}
	return username, true
}

// KillProcesses SIGKILLs every process owned by the user. pkill exits 1 when there
// was nothing to kill, which is success here, so only a worse code is an error.
func (s *Service) KillProcesses(username string) error {
	err := run("pkill", "-KILL", "-u", username)
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return nil // no matching processes
	}
	return err
}

// Delete stops everything the user runs and removes the account including home.
// Processes can linger (container runtimes reparent, sessions close
// asynchronously), so escalate: ask systemd first, then kill what remains, and
// retry userdel while the stragglers die.
func (s *Service) Delete(username string) error {
	// Resolve the primary group now, before the account is gone, so it can be
	// removed too. userdel only auto-removes a group named like the user; a rename
	// (usermod --login) leaves the group under its old name, so without this it
	// becomes an orphan gid that blocks reusing the freed port.
	group := primaryGroupName(username)
	_ = run("pkill", "--signal", "TERM", "--uid", username)
	var err error
	for i := 0; i < userdelRetries; i++ {
		if err = run("userdel", "--remove", username); err == nil {
			s.removeGroup(group)
			return nil
		}
		// userdel can remove the account yet still exit non-zero (e.g. it could not
		// remove the home directory), and the next retry then reports "no such user".
		// Once the account is gone the delete has succeeded, so stop treating the
		// leftover error as failure -- otherwise a half-removed app is undeletable.
		if !s.Exists(username) {
			s.removeGroup(group)
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
func (s *Service) removeGroup(name string) {
	if name != "" && name != s.group {
		_ = run("groupdel", name)
	}
}

// WriteSkeleton writes initial files into the app home, never overwriting existing ones
func (s *Service) WriteSkeleton(username, home string, files map[string]string) error {
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
		// Skeleton paths may be nested (public/index.html), and each directory
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

// ChownIn gives a path (relative to the app's home) to the app user, chowning
// *through the app's os.Root* so an app owner cannot swap an intermediate directory
// for a symlink and redirect the root daemon's chown onto a host path. Lchown, not
// Chown, so the final component's symlink is not followed either.
func (s *Service) ChownIn(root *os.Root, username, rel string) error {
	uid, gid, err := lookupIDs(username)
	if err != nil {
		return err
	}
	return root.Lchown(rel, uid, gid)
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
