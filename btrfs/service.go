// Package btrfs wraps the btrfs subvolume and qgroup operations hostit uses for
// app-home snapshots and hard disk quotas. It shells out through an injected runner
// (so it can be faked in tests) and owns its own command timeout. Path layout --
// where an app's home and snapshots live -- stays with the caller; this package only
// operates on the paths it is given.
package btrfs

import (
	"strconv"
	"strings"
	"time"

	"heckel.io/hostit/run"
)

const (
	// timeout bounds a btrfs command; these are metadata operations (create,
	// snapshot, qgroup) and return quickly, but must never wedge a request.
	timeout = 30 * time.Second
	// bytesPerMB converts qgroup byte counts to the MB unit used everywhere else.
	bytesPerMB = 1024 * 1024
)

// Service performs btrfs subvolume and qgroup operations over a run.Runner.
type Service struct {
	runner run.Runner
}

// New builds a btrfs Service from a command runner.
func New(runner run.Runner) *Service {
	return &Service{runner: runner}
}

// Filesystem returns the filesystem type of dir (e.g. "btrfs", "ext2/ext3"), as
// reported by stat(1). The error is surfaced so callers can log why detection failed.
func (s *Service) Filesystem(dir string) (string, error) {
	out, err := s.runner.RunTimeout(timeout, "stat", "-f", "-c", "%T", dir)
	return strings.TrimSpace(out), err
}

// IsBtrfs reports whether dir lives on a btrfs filesystem, which is what makes
// subvolume snapshots and hard qgroup quotas possible.
func (s *Service) IsBtrfs(dir string) bool {
	fstype, err := s.Filesystem(dir)
	return err == nil && fstype == "btrfs"
}

// CreateSubvolume makes a new btrfs subvolume at path (the app's home).
func (s *Service) CreateSubvolume(path string) error {
	_, err := s.runner.RunTimeout(timeout, "btrfs", "subvolume", "create", path)
	return err
}

// DeleteSubvolume removes a subvolume (an app home or a snapshot). A read-only
// snapshot must have its flag cleared first, which the caller handles.
func (s *Service) DeleteSubvolume(path string) error {
	_, err := s.runner.RunTimeout(timeout, "btrfs", "subvolume", "delete", path)
	return err
}

// MoveSubvolume renames a subvolume within the app-homes filesystem. Same-fs, so
// it is a metadata rename (fast, atomic), used to swap a rollback's staged home in.
func (s *Service) MoveSubvolume(src, dst string) error {
	_, err := s.runner.Run("mv", src, dst)
	return err
}

// Snapshot snapshots src to dst; readonly makes a stable, immutable snapshot (what
// we keep) versus a writable copy (what a rollback or fork starts from).
func (s *Service) Snapshot(src, dst string, readonly bool) error {
	args := []string{"btrfs", "subvolume", "snapshot"}
	if readonly {
		args = append(args, "-r")
	}
	args = append(args, src, dst)
	_, err := s.runner.RunTimeout(timeout, args...)
	return err
}

// SetQuota limits the subvolume at home to diskMB via its qgroup; 0 clears the
// limit. Quota must already be enabled on the filesystem (done once at setup).
// This is the hard limit -- a write past it fails with EDQUOT rather than the app
// being stopped later by a background sweep.
func (s *Service) SetQuota(home string, diskMB int) error {
	limit := "none"
	if diskMB > 0 {
		limit = strconv.Itoa(diskMB) + "M"
	}
	_, err := s.runner.RunTimeout(timeout, "btrfs", "qgroup", "limit", limit, home)
	return err
}

// UsageMB returns how much the subvolume at home references, in MB, from its qgroup
// (accurate and cheap, no directory walk). Returns 0 if it cannot read it, so
// reporting degrades rather than fails.
func (s *Service) UsageMB(home string) int {
	out, err := s.runner.RunTimeout(timeout, "btrfs", "qgroup", "show", "-f", "--raw", home)
	if err != nil {
		return 0
	}
	return parseQgroupReferencedMB(out)
}

// parseQgroupReferencedMB reads the referenced bytes from `btrfs qgroup show -f
// --raw` output and returns whole megabytes. The table looks like:
//
//	Qgroupid         Referenced    Exclusive  Path
//	--------         ----------    ---------  ----
//	0/257            134217728     134217728  blog
//
// We take the Referenced column of the single data row.
func parseQgroupReferencedMB(out string) int {
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || !strings.Contains(fields[0], "/") {
			continue // header, separator, or blank
		}
		bytes, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			continue
		}
		return int(bytes / bytesPerMB)
	}
	return 0
}
