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
	// userdelRetries is how often userdel is retried while user processes are still dying
	userdelRetries = 5
	userdelDelay   = 500 * time.Millisecond
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

// CreateUser creates the app user with its home dir; 0750 so other apps cannot peek
func (o *systemOps) CreateUser(username, home string) error {
	if err := os.MkdirAll(filepath.Dir(home), 0o755); err != nil {
		return err
	}
	err := run("useradd", "--create-home", "--home-dir", home, "--shell", "/bin/bash", "--comment", "hostit app", username)
	if err != nil {
		return err
	}
	return os.Chmod(home, 0o750)
}

// DeleteUser stops everything the user runs and removes the account including home.
// userdel can transiently fail while the user's processes are still terminating,
// so it is retried a few times.
func (o *systemOps) DeleteUser(username string) error {
	_ = run("loginctl", "disable-linger", username)
	_ = run("loginctl", "terminate-user", username)
	var err error
	for i := 0; i < userdelRetries; i++ {
		if err = run("userdel", "--remove", username); err == nil {
			return nil
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
