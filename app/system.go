package app

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
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
	err := run("useradd", "--create-home", "--home-dir", home, "--shell", userShellFile, "--comment", "hostit app", username)
	if err != nil {
		return err
	}
	return os.Chmod(home, 0o750)
}

// DeleteUser stops everything the user runs and removes the account including
// home. Processes can linger (container runtimes reparent, sessions close
// asynchronously), so escalate: ask systemd first, then kill what remains, and
// retry userdel while the stragglers die.
func (o *systemOps) DeleteUser(username string) error {
	_ = run("loginctl", "disable-linger", username)
	_ = run("loginctl", "terminate-user", username)
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

// EnableLinger makes the user's systemd user units run at boot and survive logout
func (o *systemOps) EnableLinger(username string) error {
	return run("loginctl", "enable-linger", username)
}

func (o *systemOps) WriteAuthorizedKeys(username, home string, keys []string) error {
	uid, gid, err := lookupIDs(username)
	if err != nil {
		return err
	}
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		return err
	}
	filename := filepath.Join(sshDir, "authorized_keys")
	if err := os.WriteFile(filename, []byte(strings.Join(keys, "\n")+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.Chown(sshDir, uid, gid); err != nil {
		return err
	}
	return os.Chown(filename, uid, gid)
}

// WriteScaffold writes initial files into the app home, never overwriting existing ones
func (o *systemOps) WriteScaffold(username, home string, files map[string]string) error {
	uid, gid, err := lookupIDs(username)
	if err != nil {
		return err
	}
	for name, content := range files {
		filename := filepath.Join(home, name)
		if _, err := os.Stat(filename); err == nil {
			continue
		}
		if err := os.WriteFile(filename, []byte(content), 0o644); err != nil {
			return err
		}
		if err := os.Chown(filename, uid, gid); err != nil {
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
	filename := filepath.Join(home, relPath)

	// Create parent dirs one by one so each new one can be chowned to the user
	dir := filepath.Dir(filename)
	var missing []string
	for d := dir; strings.HasPrefix(d, home) && d != home; d = filepath.Dir(d) {
		if _, err := os.Stat(d); err != nil {
			missing = append([]string{d}, missing...)
		}
	}
	for _, d := range missing {
		if err := os.Mkdir(d, 0o755); err != nil {
			return err
		}
		if err := os.Chown(d, uid, gid); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filename, []byte(content), mode); err != nil {
		return err
	}
	return os.Chown(filename, uid, gid)
}

// SharedImageExists reports whether the shared read-only store already holds the
// given image
func (o *systemOps) SharedImageExists(storeDir, tag string) bool {
	return run("podman", "--root", storeDir, "image", "exists", tag) == nil
}

// BuildSharedImage builds an image into the shared store as root and makes it
// world-readable, so unprivileged app users can use it as an additional image
// store without copying or rebuilding it
func (o *systemOps) BuildSharedImage(storeDir, contextDir, tag string) error {
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		return err
	}
	err := run("podman", "--root", storeDir, "build", "--tag", tag, contextDir)
	if err != nil {
		return err
	}
	return makeWorldReadable(storeDir)
}

// makeWorldReadable grants read (and traverse) access to everyone below dir;
// rootless users can only use the shared layers if they can read the files
func makeWorldReadable(dir string) error {
	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip unreadable entries rather than failing the build
		}
		mode := info.Mode().Perm()
		newMode := mode | 0o004 // Others may read
		if info.IsDir() || mode&0o100 != 0 {
			newMode |= 0o001 // ... and traverse/execute where the owner can
		}
		if newMode == mode {
			return nil
		}
		return os.Chmod(path, newMode)
	})
}

// ApplyPortRules atomically replaces the hostit nftables table: for each app
// port, loopback connects are only allowed for root (the proxy) and the owner
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
