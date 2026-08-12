// Package agent implements the PID 1 process supervisor that runs inside every
// workspace container: it starts the hostit.yml "run" command, restarts it on
// exit, reloads it on SIGHUP (or Reload), mirrors its output to a log file, and
// reaps orphaned zombie processes as any init must.
package agent

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"heckel.io/hostit/appctl"
)

const (
	// hostitBinFile is where the hostit binary is mounted inside the container;
	// "mode: static" apps run its file server
	hostitBinFile = "/usr/bin/hostit"
	// Crash-loop supervision: a command that keeps exiting is restarted with an
	// exponential backoff (base doubling each time, capped), and after crashLimit
	// rapid crashes in a row the agent gives up and idles ("failed") instead of
	// restarting forever, so a doomed app stops pegging the box's one core.
	restartBackoffBase = 2 * time.Second
	restartBackoffMax  = 60 * time.Second
	// healthyRunTime is how long the command must run to count as healthy: a longer
	// run resets the backoff, so an app that runs fine then dies once is not treated
	// as a crash loop.
	healthyRunTime = 30 * time.Second
	crashLimit     = 5
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
	wake         chan struct{}
	stop         chan struct{}
	logFile      *appLog
	crashes      int // Consecutive rapid crashes, for the restart backoff
	stopOnce     sync.Once
	paused       atomic.Bool // App stopped by the owner; the container stays up
}

// New creates an Agent for the app living in home (usually $HOME)
func New(home string) *Agent {
	return &Agent{
		home:         home,
		restartDelay: restartBackoffBase,
		reap:         os.Getpid() == 1,
		exits:        make(chan childExit, 16),
		reloads:      make(chan struct{}, 1),
		wake:         make(chan struct{}, 1),
		stop:         make(chan struct{}),
	}
}

// Run supervises the configured command until Stop (or SIGTERM/SIGINT); it only
// returns on shutdown. Without a usable hostit.yml it idles, waiting for Reload.
func (a *Agent) Run() error {
	a.wireSignals()
	defer a.closeLog()
	for {
		// The owner stopped the app but left the container up: idle, keeping the
		// container alive, until start (or reload) wakes us.
		if a.paused.Load() {
			slog.Info("App stopped; container stays up until it is started")
			a.writeState(appctl.AppStateStopped)
			if !a.waitWake() {
				return nil
			}
			continue
		}
		conf := a.loadConfig()
		if conf == nil {
			slog.Info("No run command configured; idling until reload")
			a.writeState(appctl.AppStateIdle)
			if !a.waitWake() {
				return nil
			}
			continue
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
		startedAt := time.Now()
		slog.Info("Started app command", "pid", cmd.Process.Pid, "command", conf.Command(hostitBinFile))
		a.writeState(appctl.AppStateRunning)
		exited := a.waitFor(cmd) // Exactly one waiter per child (Wait must not be called twice)

		// Wait for the child to exit, a reload request, or shutdown
		select {
		case exit := <-exited:
			ranFor := time.Since(startedAt)
			var delay time.Duration
			var giveUp bool
			a.crashes, delay, giveUp = restartPlan(a.crashes, ranFor)
			if giveUp {
				// The command keeps crashing on start. Stop restarting rather than
				// hammer the box forever; the owner fixes it and redeploys/starts.
				slog.Error("App keeps crashing; stopping restarts until it is redeployed or started",
					"crashes", a.crashes, "lastStatus", exit.status)
				a.logNotice("App crashed %d times in a row; giving up on automatic restarts. Fix the app, then redeploy or start it.", crashLimit)
				a.writeState(appctl.AppStateFailed)
				a.crashes = 0
				if !a.waitWake() {
					return nil
				}
				continue
			}
			slog.Warn("App command exited, restarting", "status", exit.status,
				"ranFor", ranFor.Round(time.Second), "delay", delay)
			a.writeState(appctl.AppStateCrashed)
			if !a.sleepFor(delay) {
				return nil
			}
		case <-a.wake:
			// Paused or resumed: stop the command and let the loop top decide. Owner
			// action, so forget prior crashes and start the backoff fresh.
			a.crashes = 0
			a.killAndWait(cmd, exited)
		case <-a.reloads:
			slog.Info("Reloading: stopping app command")
			a.crashes = 0
			a.killAndWait(cmd, exited)
		case <-a.stop:
			a.killAndWait(cmd, exited)
			return nil
		}
	}
}

// waitWake blocks in an idle state (paused, or no run command) until something
// asks the agent to re-evaluate; it returns false only on shutdown.
func (a *Agent) waitWake() bool {
	select {
	case <-a.wake:
		return true
	case <-a.reloads:
		return true
	case <-a.stop:
		return false
	}
}

// Reload asks the agent to re-read hostit.yml and restart the command. Reloading
// implies the app should run, so it clears a pause.
func (a *Agent) Reload() {
	a.paused.Store(false)
	select {
	case a.reloads <- struct{}{}:
	default:
	}
}

// Pause stops the app command but leaves the container running ("stop app"); the
// supervisor idles until Resume. Start/stop of the container is a separate thing.
func (a *Agent) Pause() {
	a.paused.Store(true)
	a.signalWake()
}

// Resume restarts the app command after a Pause ("start app")
func (a *Agent) Resume() {
	a.paused.Store(false)
	a.signalWake()
}

func (a *Agent) signalWake() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

// writeState records the run: process state for the daemon to read. The daemon
// cannot see inside the container, so this file is how it tells a stopped or
// crashed app (container up, nothing serving) from a healthy one. Best-effort.
func (a *Agent) writeState(state string) {
	path := filepath.Join(a.home, appctl.AppStateFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
		_ = os.WriteFile(path, []byte(state), 0o644)
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
	signal.Notify(sigs, syscall.SIGHUP, syscall.SIGUSR1, syscall.SIGUSR2, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		for sig := range sigs {
			switch sig {
			case syscall.SIGHUP:
				a.Reload()
			case syscall.SIGUSR1:
				a.Pause()
			case syscall.SIGUSR2:
				a.Resume()
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
	// Timestamp the app log; leave the podman-logs stream (os.Stdout) raw.
	ts := newTimestampWriter(out)
	cmd.Stdout = io.MultiWriter(os.Stdout, ts)
	cmd.Stderr = io.MultiWriter(os.Stderr, ts)
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
	// Timestamp the app log; leave the podman-logs stream (os.Stdout) raw.
	ts := newTimestampWriter(out)
	cmd.Stdout = io.MultiWriter(os.Stdout, ts)
	cmd.Stderr = io.MultiWriter(os.Stderr, ts)
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
	return a.sleepFor(a.restartDelay)
}

// sleepFor waits d, or returns early if a reload/wake arrives; it returns false
// only on shutdown, so callers can bail out of a backoff the moment the owner acts.
func (a *Agent) sleepFor(d time.Duration) bool {
	select {
	case <-time.After(d):
		return true
	case <-a.reloads:
		return true
	case <-a.wake:
		return true
	case <-a.stop:
		return false
	}
}

// restartPlan decides what to do after the run command exits. crashes is the count
// of consecutive rapid crashes so far; ranFor is how long the command just ran. It
// returns the new crash count, the backoff to wait before the next start, and
// whether to give up restarting because the app keeps crashing.
func restartPlan(crashes int, ranFor time.Duration) (newCrashes int, delay time.Duration, giveUp bool) {
	if ranFor >= healthyRunTime {
		// Ran long enough to be healthy: an isolated exit, not a crash loop.
		return 0, restartBackoffBase, false
	}
	crashes++
	if crashes >= crashLimit {
		return crashes, 0, true
	}
	delay = restartBackoffBase << (crashes - 1)
	if delay > restartBackoffMax {
		delay = restartBackoffMax
	}
	return crashes, delay, false
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

// logNotice writes a hostit-generated line into the app log, timestamped like the
// app's own output and tagged [hostit], so operational events the owner needs to
// see (a crash-loop give-up) show up in the Logs tab next to the output that caused
// them. A closed log is a silent no-op.
func (a *Agent) logNotice(format string, args ...any) {
	if a.logFile == nil {
		return
	}
	line := time.Now().Format(logTimeFormat) + " [hostit] " + fmt.Sprintf(format, args...) + "\n"
	_, _ = a.logFile.Write([]byte(line))
}

// logTimeFormat stamps each app-log line. Space-separated, no brackets, so it
// stays readable and is trivial to strip; the container clock is UTC.
const logTimeFormat = "2006-01-02 15:04:05"

// maxLineBuffer bounds how much a single unterminated line may buffer before it is
// flushed anyway, so an app that never prints a newline cannot grow memory without
// bound.
const maxLineBuffer = 64 * 1024

// timestampWriter prefixes each complete line with a wall-clock timestamp before
// writing it to the log, so the app's output is readable after the fact rather
// than a bare undated stream. Partial lines are held until their newline arrives.
// The stdout and stderr copiers share one writer, so it guards its buffer.
type timestampWriter struct {
	w   io.Writer
	buf []byte
	mu  sync.Mutex // Protects buf
}

func newTimestampWriter(w io.Writer) *timestampWriter {
	return &timestampWriter{w: w}
}

func (t *timestampWriter) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	for {
		i := bytes.IndexByte(t.buf, '\n')
		if i < 0 {
			break
		}
		if err := t.emit(t.buf[:i+1]); err != nil {
			return len(p), err
		}
		t.buf = t.buf[i+1:]
	}
	// An over-long line with no newline yet is flushed so it cannot pin memory.
	if len(t.buf) > maxLineBuffer {
		if err := t.emit(t.buf); err != nil {
			return len(p), err
		}
		t.buf = t.buf[:0]
	}
	return len(p), nil
}

// emit writes one line prefixed with the current time. Called with the mutex held.
func (t *timestampWriter) emit(line []byte) error {
	stamp := time.Now().Format(logTimeFormat) + " "
	out := make([]byte, 0, len(stamp)+len(line))
	out = append(out, stamp...)
	out = append(out, line...)
	_, err := t.w.Write(out)
	return err
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
