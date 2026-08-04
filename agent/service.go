// Package agent implements the PID 1 process supervisor that runs inside every
// workspace container: it starts the hostit.yml "run" command, restarts it on
// exit, reloads it on SIGHUP (or Reload), mirrors its output to a log file, and
// reaps orphaned zombie processes as any init must.
package agent

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"heckel.io/hostit/appctl"
)

const (
	// hostitBinFile is where the hostit binary is mounted inside the container;
	// "static:" apps run its file server
	hostitBinFile = "/usr/bin/hostit"
	// defaultRestartDelay is the pause before restarting an exited command
	defaultRestartDelay = 2 * time.Second
	// killTimeout is how long a child gets after SIGTERM before SIGKILL
	killTimeout = 10 * time.Second
	// logMaxSize caps the app log; beyond it the log is rotated to .old
	logMaxSize = 10 * 1024 * 1024
)

// childExit reports the termination of a reaped or waited-for child
type childExit struct {
	pid    int
	status string
}

// Agent supervises the app command inside a workspace container
type Agent struct {
	home         string
	restartDelay time.Duration
	reap         bool // Reap orphan zombies; enabled when running as PID 1
	exits        chan childExit
	reloads      chan struct{}
	stop         chan struct{}
	logFile      *appLog
	stopOnce     sync.Once
}

// New creates an Agent for the app living in home (usually $HOME)
func New(home string) *Agent {
	return &Agent{
		home:         home,
		restartDelay: defaultRestartDelay,
		reap:         os.Getpid() == 1,
		exits:        make(chan childExit, 16),
		reloads:      make(chan struct{}, 1),
		stop:         make(chan struct{}),
	}
}

// Run supervises the configured command until Stop (or SIGTERM/SIGINT); it only
// returns on shutdown. Without a usable hostit.yml it idles, waiting for Reload.
func (a *Agent) Run() error {
	a.wireSignals()
	defer a.closeLog()
	for {
		conf := a.loadConfig()
		if conf == nil {
			slog.Info("No run command configured; idling until reload")
			select {
			case <-a.reloads:
				continue
			case <-a.stop:
				return nil
			}
		}
		if err := a.prepare(conf); err != nil {
			slog.Error("Prepare step failed; not starting the app", "error", err)
			if !a.sleepInterruptible() {
				return nil
			}
			continue
		}
		cmd, err := a.startChild(conf)
		if err != nil {
			slog.Error("Cannot start command", "error", err)
			if !a.sleepInterruptible() {
				return nil
			}
			continue
		}
		slog.Info("Started app command", "pid", cmd.Process.Pid, "command", conf.Command(hostitBinFile))
		exited := a.waitFor(cmd) // Exactly one waiter per child (Wait must not be called twice)

		// Wait for the child to exit, a reload request, or shutdown
		select {
		case exit := <-exited:
			slog.Warn("App command exited, restarting", "status", exit.status, "delay", a.restartDelay)
			if !a.sleepInterruptible() {
				return nil
			}
		case <-a.reloads:
			slog.Info("Reloading: stopping app command")
			a.killAndWait(cmd, exited)
		case <-a.stop:
			a.killAndWait(cmd, exited)
			return nil
		}
	}
}

// Reload asks the agent to re-read hostit.yml and restart the command
func (a *Agent) Reload() {
	select {
	case a.reloads <- struct{}{}:
	default:
	}
}

// Stop shuts the agent down, terminating the supervised command
func (a *Agent) Stop() {
	a.stopOnce.Do(func() {
		close(a.stop)
	})
}

// wireSignals maps SIGHUP to Reload, SIGTERM/SIGINT to Stop, and (as PID 1)
// starts the zombie reaper on SIGCHLD
func (a *Agent) wireSignals() {
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		for sig := range sigs {
			switch sig {
			case syscall.SIGHUP:
				a.Reload()
			default:
				a.Stop()
			}
		}
	}()
	if a.reap {
		chld := make(chan os.Signal, 16)
		signal.Notify(chld, syscall.SIGCHLD)
		go a.reapLoop(chld)
	}
}

// reapLoop waits for any terminated child (including orphans reparented to us
// as PID 1) and reports their exits; this doubles as the zombie reaper
func (a *Agent) reapLoop(chld chan os.Signal) {
	for range chld {
		for {
			var status syscall.WaitStatus
			pid, err := syscall.Wait4(-1, &status, syscall.WNOHANG, nil)
			if pid <= 0 || err != nil {
				break
			}
			a.reportExit(childExit{pid: pid, status: fmt.Sprintf("exit status %d", status.ExitStatus())})
		}
	}
}

// reportExit hands a reaped child to whoever is waiting for it, and drops it if
// nobody is. As PID 1 we reap orphans too, and while the app is idle (a stub, or
// a config that names no command) there is no waiter at all: blocking here would
// stop the reaper for good and leave the container collecting zombies.
func (a *Agent) reportExit(exit childExit) {
	select {
	case a.exits <- exit:
	default:
	}
}

// loadConfig reads hostit.yml leniently; nil means "nothing to run"
func (a *Agent) loadConfig() *appctl.AppConfig {
	b, err := os.ReadFile(filepath.Join(a.home, "hostit.yml"))
	if err != nil {
		return nil
	}
	conf, err := appctl.ParseAppConfig(b)
	if err != nil {
		slog.Error("Cannot parse hostit.yml", "error", err)
		return nil
	}
	if conf.Command(hostitBinFile) == "" {
		return nil
	}
	return conf
}

// prepare runs the app's build step, if it has one, before the app starts. Its
// output goes to the app log like everything else, so a failed build is visible
// where its owner (or their agent) is already looking.
func (a *Agent) prepare(conf *appctl.AppConfig) error {
	if conf.Prepare == "" {
		return nil
	}
	out, err := a.openLog()
	if err != nil {
		return err
	}
	slog.Info("Running prepare step", "command", conf.Prepare)
	cmd := exec.Command("/bin/sh", "-lc", conf.Prepare)
	cmd.Dir = a.home
	cmd.Env = os.Environ()
	cmd.Stdout = io.MultiWriter(os.Stdout, out)
	cmd.Stderr = io.MultiWriter(os.Stderr, out)
	return cmd.Run()
}

// startChild launches the run command in its own process group with output
// mirrored to stdout (podman logs) and the app log file
func (a *Agent) startChild(conf *appctl.AppConfig) (*exec.Cmd, error) {
	out, err := a.openLog()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("/bin/sh", "-lc", conf.Command(hostitBinFile))
	cmd.Dir = a.home
	cmd.Env = os.Environ()
	cmd.Stdout = io.MultiWriter(os.Stdout, out)
	cmd.Stderr = io.MultiWriter(os.Stderr, out)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

// waitFor returns a channel that yields the exit of the given child. As PID 1
// the reap loop owns wait(); otherwise we wait directly.
func (a *Agent) waitFor(cmd *exec.Cmd) <-chan childExit {
	pid := cmd.Process.Pid
	out := make(chan childExit, 1)
	if a.reap {
		go func() {
			for exit := range a.exits {
				if exit.pid == pid {
					out <- exit
					return
				}
			}
		}()
	} else {
		go func() {
			err := cmd.Wait()
			status := "exit status 0"
			if err != nil {
				status = err.Error()
			}
			out <- childExit{pid: pid, status: status}
		}()
	}
	return out
}

// killAndWait terminates the child's process group, escalating to SIGKILL
func (a *Agent) killAndWait(cmd *exec.Cmd, exited <-chan childExit) {
	pid := cmd.Process.Pid
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	select {
	case <-exited:
		return
	case <-time.After(killTimeout):
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
	select {
	case <-exited:
	case <-time.After(killTimeout):
		slog.Error("Child did not die even after SIGKILL", "pid", pid)
	}
}

// sleepInterruptible pauses before a restart; returns false on shutdown
func (a *Agent) sleepInterruptible() bool {
	select {
	case <-time.After(a.restartDelay):
		return true
	case <-a.reloads:
		return true
	case <-a.stop:
		return false
	}
}

// openLog (re)opens the app log. The returned writer rotates as it writes: an
// app that starts once and runs for months never comes back through here, so
// checking the size only on start would let the log grow without bound.
func (a *Agent) openLog() (*appLog, error) {
	a.closeLog()
	dir := filepath.Join(a.home, appctl.LogDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	log := &appLog{filename: filepath.Join(dir, "app.log")}
	if err := log.open(); err != nil {
		return nil, err
	}
	a.logFile = log
	return log, nil
}

// appLog is the app's log file, rotated to .old once it passes logMaxSize
type appLog struct {
	filename string
	file     *os.File
	size     int64

	mu sync.Mutex // Protects file and size; stdout and stderr both write here
}

// open attaches to the log file, rotating first if it is already too big
func (l *appLog) open() error {
	if stat, err := os.Stat(l.filename); err == nil && stat.Size() > logMaxSize {
		_ = os.Rename(l.filename, l.filename+".old")
	}
	f, err := os.OpenFile(l.filename, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	stat, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	l.file, l.size = f, stat.Size()
	return nil
}

// Write appends to the log, rotating when it has grown past the cap
func (l *appLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return len(p), nil // Closed; the app's output is not worth an error
	}
	if l.size+int64(len(p)) > logMaxSize {
		_ = l.file.Close()
		if err := os.Rename(l.filename, l.filename+".old"); err != nil {
			return 0, err
		}
		if err := l.open(); err != nil {
			return 0, err
		}
	}
	n, err := l.file.Write(p)
	l.size += int64(n)
	return n, err
}

// Close releases the file; further writes are discarded
func (l *appLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

func (a *Agent) closeLog() {
	if a.logFile != nil {
		_ = a.logFile.Close()
		a.logFile = nil
	}
}
