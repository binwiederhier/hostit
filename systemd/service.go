// Package systemd wraps the systemctl verbs hostit uses to run each app as a
// systemd unit. It shells out through an injected runner (so it can be faked in
// tests) and operates only on unit names it is given -- unit naming (the
// hostit-app@<id> template instances) is app-identity policy and stays with the
// caller.
package systemd

import (
	"time"

	"heckel.io/hostit/run"
)

// systemctl is the control binary; centralized so the verb wrappers don't each
// repeat the literal.
const systemctl = "systemctl"

// Interface is the subset of systemctl operations the app and snapshot packages
// depend on; the concrete *Service satisfies it, so a test can substitute a fake.
type Interface interface {
	EnableNow(unit string) error
	DisableNow(unit string) error
	Start(unit string) error
	Stop(unit string) error
	Restart(unit string) error
	ResetFailed(unit string) error
	Status(unit string) (string, error)
	ListUnits(pattern string) (string, error)
	IsActive(timeout time.Duration, units ...string) (string, error)
}

// Service drives systemctl over a run.Runner.
type Service struct {
	runner run.Runner
}

var _ Interface = (*Service)(nil)

// New builds a systemd Service from a command runner.
func New(runner run.Runner) *Service {
	return &Service{runner: runner}
}

// EnableNow enables a unit and starts it immediately (systemctl enable --now).
func (s *Service) EnableNow(unit string) error {
	_, err := s.runner.Run(systemctl, "enable", "--now", unit)
	return err
}

// DisableNow disables a unit and stops it immediately (systemctl disable --now).
func (s *Service) DisableNow(unit string) error {
	_, err := s.runner.Run(systemctl, "disable", "--now", unit)
	return err
}

// Start starts a unit.
func (s *Service) Start(unit string) error {
	_, err := s.runner.Run(systemctl, "start", unit)
	return err
}

// Stop stops a unit.
func (s *Service) Stop(unit string) error {
	_, err := s.runner.Run(systemctl, "stop", unit)
	return err
}

// Restart restarts a unit.
func (s *Service) Restart(unit string) error {
	_, err := s.runner.Run(systemctl, "restart", unit)
	return err
}

// ResetFailed clears a unit's failed state, so a Restart=always unit systemd still
// knows about does not keep retrying a container that is gone.
func (s *Service) ResetFailed(unit string) error {
	_, err := s.runner.Run(systemctl, "reset-failed", unit)
	return err
}

// Status returns a unit's human-readable status (systemctl status --no-pager).
func (s *Service) Status(unit string) (string, error) {
	return s.runner.Run(systemctl, "status", "--no-pager", unit)
}

// ListUnits lists units matching a glob pattern, one per plain line, headers and
// legend suppressed for easy parsing.
func (s *Service) ListUnits(pattern string) (string, error) {
	return s.runner.Run(systemctl, "list-units", pattern, "--all", "--no-legend", "--plain")
}

// IsActive queries one or more units in a single call; systemctl prints one line
// per unit, in the order given. A non-zero exit just means something is inactive,
// so the returned error is for the caller to ignore or surface as it sees fit.
// timeout bounds the call (0 means no timeout), so a state sweep never wedges on a
// stuck systemctl.
func (s *Service) IsActive(timeout time.Duration, units ...string) (string, error) {
	args := append([]string{systemctl, "is-active"}, units...)
	if timeout > 0 {
		return s.runner.RunTimeout(timeout, args...)
	}
	return s.runner.Run(args...)
}
