// Package container wraps the podman operations hostit uses to run app containers
// and manage their images. It shells out through an injected runner (so it can be
// faked in tests) and operates on container/image names it is given -- naming (the
// hostit-app-<id> convention) is app-identity policy and stays with the caller.
package container

import "time"

// podman is the container runtime binary; centralized so the wrappers don't each
// repeat the literal.
const podman = "podman"

// Runner executes a system command and returns its combined output; the daemon's
// root command runner satisfies it.
type Runner interface {
	Run(args ...string) (string, error)
	RunTimeout(timeout time.Duration, args ...string) (string, error)
}

// Service drives podman over a Runner.
type Service struct {
	runner Runner
}

// New builds a container Service from a command runner.
func New(runner Runner) *Service {
	return &Service{runner: runner}
}

// Inspect returns a container's inspect output formatted with the given Go
// template; the caller uses the error to detect a missing container.
func (s *Service) Inspect(name, format string) (string, error) {
	return s.runner.Run(podman, "container", "inspect", name, "--format", format)
}

// RemoveForce removes a container, killing it first if it is running. A missing
// container is not usually an error the caller cares about.
func (s *Service) RemoveForce(name string) error {
	_, err := s.runner.Run(podman, "rm", "--force", name)
	return err
}

// Kill sends a signal to a container's PID 1.
func (s *Service) Kill(name, signal string) error {
	_, err := s.runner.Run(podman, "kill", "--signal", signal, name)
	return err
}

// Create creates a container from a full argument list (everything after "podman",
// i.e. starting with "create"); the caller builds the args from its config.
func (s *Service) Create(args ...string) error {
	_, err := s.runner.Run(append([]string{podman}, args...)...)
	return err
}

// Names lists container names, one per line; all includes stopped containers.
func (s *Service) Names(all bool) (string, error) {
	args := []string{podman, "ps"}
	if all {
		args = append(args, "--all")
	}
	args = append(args, "--format", "{{.Names}}")
	return s.runner.Run(args...)
}

// RunningStartTimes lists "<name>|<startedAt>" for running containers, bounded by
// timeout so a state sweep never wedges.
func (s *Service) RunningStartTimes(timeout time.Duration) (string, error) {
	return s.runner.RunTimeout(timeout, podman, "ps", "--format", "{{.Names}}|{{.StartedAt}}")
}

// Stats returns one podman stats snapshot as JSON, bounded by timeout.
func (s *Service) Stats(timeout time.Duration) (string, error) {
	return s.runner.RunTimeout(timeout, podman, "stats", "--no-stream", "--format", "json")
}

// Exec runs a command inside a container's workdir. timeout is an outer backstop for
// podman itself hanging; real per-command enforcement is the caller's business
// (e.g. wrapping the command in timeout(1) inside the container).
func (s *Service) Exec(timeout time.Duration, name, workdir string, args ...string) (string, error) {
	full := append([]string{podman, "exec", "--workdir", workdir, name}, args...)
	return s.runner.RunTimeout(timeout, full...)
}

// Images lists the image store's "repository:tag" strings, one per line.
func (s *Service) Images() (string, error) {
	return s.runner.Run(podman, "images", "--format", "{{.Repository}}:{{.Tag}}")
}

// RemoveImage removes an image by tag.
func (s *Service) RemoveImage(image string) error {
	_, err := s.runner.Run(podman, "rmi", image)
	return err
}

// ImageOf returns the image a container was created from.
func (s *Service) ImageOf(name string) (string, error) {
	return s.runner.Run(podman, "inspect", name, "--format", "{{.ImageName}}")
}

// ImageExists reports whether the image store holds the given tag.
func (s *Service) ImageExists(tag string) bool {
	_, err := s.runner.Run(podman, "image", "exists", tag)
	return err == nil
}

// Build builds an image tag from a context directory into the shared image store.
func (s *Service) Build(tag, contextDir string) error {
	_, err := s.runner.Run(podman, "build", "--tag", tag, contextDir)
	return err
}
