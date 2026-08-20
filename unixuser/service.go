// Package unixuser manages the Unix user account (and matching group) hostit
// creates per app: creation without an /etc/skel copy, uid/gid lookup, rename,
// and a robust delete that reaps lingering processes. It also writes the app's
// initial home skeleton and hands home paths to the app user.
// Everything here shells out to the usual account tools and must run as root.
package unixuser

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path"
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

// Account is one app's Unix account as the host knows it: its login name and
// its home directory, which is the only durable link back to the app id (the
// home is the files path inside the id-keyed app subvolume).
type Account struct {
	Name string
	Home string
}

// Interface is the subset of user-account operations the machine half depends on;
// the concrete *Service satisfies it, so a test can substitute a fake.
type Interface interface {
	Exists(username string) bool
	// List returns the app accounts on this host (the members of the app
	// group). Homes are what the caller filters on: colocated nodes share one
	// /etc/passwd, so only the homes under a node's own pool are its business.
	List() ([]Account, error)
	LookupUID(username string) (int, error)
	LookupIDs(username string) (uid, gid int, err error)
	Create(username, home string, uid int) error
	Rename(oldName, newName string) error
	KillProcesses(username string) error
	Delete(username string) error
	WriteSkeleton(home string, files map[string]string) error
}

// Service creates and removes app users. It carries the deployment settings a new
// account needs (login shell, supplementary group), injected so the package
// holds no host-specific policy of its own.
type Service struct {
	shell string
	group string
}

var _ Interface = (*Service)(nil)

// New builds a unixuser Service. shell is the app users' login shell and group
// is the supplementary group that grants container entry.
func New(shell, group string) *Service {
	return &Service{shell: shell, group: group}
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

// List returns the app accounts on this host: every member of the app group,
// with its home. The group is how an app account is recognizable at all --
// uids are a range, names are arbitrary -- and the home carries the app id.
func (s *Service) List() ([]Account, error) {
	g, err := user.LookupGroup(s.group)
	if err != nil {
		return nil, err
	}
	names, err := groupMembers(g)
	if err != nil {
		return nil, err
	}
	accounts := make([]Account, 0, len(names))
	for _, name := range names {
		u, err := user.Lookup(name)
		if err != nil {
			continue // a member that no longer resolves is nothing to clean up
		}
		accounts = append(accounts, Account{Name: u.Username, Home: u.HomeDir})
	}
	return accounts, nil
}

// groupMembers lists a group's members. os/user has no enumeration, so this
// reads the group's member list with getent -- the same source useradd
// --groups writes to, NSS included.
func groupMembers(g *user.Group) ([]string, error) {
	out, err := exec.Command("getent", "group", g.Name).Output()
	if err != nil {
		return nil, fmt.Errorf("getent group %s failed: %w", g.Name, err)
	}
	// name:x:gid:member,member,...
	fields := strings.Split(strings.TrimSpace(string(out)), ":")
	if len(fields) < 4 || strings.TrimSpace(fields[3]) == "" {
		return nil, nil
	}
	return strings.Split(fields[3], ","), nil
}

// Create creates the app user. The home directory itself is NOT touched: it is
// the files dir inside the app's (root-owned, idmap-mounted) subvolume, created
// by the workspace service before this runs; useradd gets --no-create-home so it
// neither makes it nor copies /etc/skel into it.
func (s *Service) Create(username, home string, uid int) error {
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
	return run(args[0], args[1:]...)
}

// createUserArgs is the useradd command for a new app user, pinned to a specific
// uid/gid (its contiguous id block)
func createUserArgs(username, home string, uid int, shell, group string) []string {
	id := strconv.Itoa(uid)
	return []string{"useradd", "--no-create-home", "--home-dir", home, "--shell", shell,
		"--uid", id, "--gid", id, "--groups", group, "--comment", "hostit app", username}
}

// Rename changes a user's login name; uid, home and files are untouched, so this
// is cheap and safe to do while the app keeps running. It also renames the user's
// primary group to match, so a later delete (which removes the group by the user's
// name) does not leave an orphan gid behind that would block reusing the freed port.
func (s *Service) Rename(oldName, newName string) error {
	if err := run("usermod", "--login", newName, oldName); err != nil {
		return err
	}
	_ = s.syncGroupName(newName) // best-effort; cosmetic if it fails
	return nil
}

// syncGroupName renames a user's primary group to match its login name, when the
// two have drifted apart (an app renamed before group-rename shipped kept its old
// group name). Aligning them keeps a deleted app from leaving an orphan group, and
// frees the old name so a new app can take it. Idempotent and a no-op when the
// names already agree or the user is on the shared app group.
func (s *Service) syncGroupName(username string) error {
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
func (s *Service) WriteSkeleton(home string, files map[string]string) error {
	root, err := os.OpenRoot(home)
	if err != nil {
		return err
	}
	defer root.Close()
	for name, content := range files {
		if _, err := root.Lstat(name); err == nil {
			continue
		}
		// Skeleton paths may be nested (public/index.html); root-owned like the
		// whole idmapped tree
		if dir := path.Dir(name); dir != "." {
			if err := root.MkdirAll(dir, 0o755); err != nil {
				return err
			}
		}
		if err := root.WriteFile(name, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
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

// SweepShellPaths migrates app users whose login shell is still oldShell to
// this service's (new) shell path, and returns who was changed. The shell is
// what sshd execs on login, so the ordering here is load-bearing: the new file
// must exist BEFORE any passwd entry names it -- a wrong path is not an error
// message, it is every owner locked out of SSH. Users on any other shell
// (humans, already-migrated apps) are never touched.
func (s *Service) SweepShellPaths(oldShell string) ([]string, error) {
	if _, err := os.Stat(s.shell); err != nil {
		return nil, fmt.Errorf("refusing shell migration: %s is not installed: %w", s.shell, err)
	}
	f, err := os.Open("/etc/passwd")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	targets, err := sweepTargets(f, oldShell)
	if err != nil {
		return nil, err
	}
	changed := make([]string, 0, len(targets))
	for _, name := range targets {
		if out, err := exec.Command("usermod", "--shell", s.shell, name).CombinedOutput(); err != nil {
			// Keep going: the user stays on the old path, which this release
			// still ships, so nothing is locked out by a partial sweep.
			return changed, fmt.Errorf("usermod %s: %w: %s", name, err, strings.TrimSpace(string(out)))
		}
		changed = append(changed, name)
	}
	return changed, nil
}

// sweepTargets picks the users to migrate from a passwd stream: exactly those
// whose shell field equals the old path. Malformed lines are skipped, not
// fatal -- refusing the whole sweep over one bad line would strand everyone.
func sweepTargets(passwd io.Reader, oldShell string) ([]string, error) {
	var targets []string
	scanner := bufio.NewScanner(passwd)
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), ":")
		if len(fields) < 7 {
			continue
		}
		if fields[6] == oldShell {
			targets = append(targets, fields[0])
		}
	}
	return targets, scanner.Err()
}
