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

// Interface is the subset of btrfs operations the app, snapshot and workspace
// packages depend on; the concrete *Service satisfies it, so a test can
// substitute a fake.
type Interface interface {
	CreateSubvolume(path string) error
	DeleteSubvolume(path string) error
	MoveSubvolume(src, dst string) error
	Snapshot(src, dst string, readonly bool) error
	SetReadOnly(path string, readonly bool) error
	SetQuota(home string, diskMB int) error
	UsageMB(home string) int
	QuotaEnable(pool string) error
	RootID(path string) (string, error)
	QgroupCreate(pool, groupID string) error
	QgroupAssign(pool, subvolQgroup, groupID string) error
	QgroupLimitExclusive(pool, groupID string, diskMB int) error
	QgroupDestroy(pool, groupID string) error
	ExclusiveUsageMB(pool, groupID string) int
}

// Service performs btrfs subvolume and qgroup operations over a run.Runner.
type Service struct {
	runner run.Runner
}

var _ Interface = (*Service)(nil)

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

// SetReadOnly flips a subvolume's read-only property; used to seal an exported
// base rootfs so nothing (and nobody) can dirty what every app snapshot shares.
func (s *Service) SetReadOnly(path string, readonly bool) error {
	_, err := s.runner.RunTimeout(timeout, "btrfs", "property", "set", path, "ro", strconv.FormatBool(readonly))
	return err
}

// QuotaEnable turns on quota accounting for the pool; idempotent, so it is safe
// to run at every daemon start.
func (s *Service) QuotaEnable(pool string) error {
	_, err := s.runner.RunTimeout(timeout, "btrfs", "quota", "enable", pool)
	return err
}

// RootID returns a subvolume's numeric id (as printed by inspect-internal
// rootid); a subvolume's own level-0 qgroup is "0/<rootid>".
func (s *Service) RootID(path string) (string, error) {
	out, err := s.runner.RunTimeout(timeout, "btrfs", "inspect-internal", "rootid", path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// QgroupCreate creates a higher-level qgroup (e.g. "1/<uid>") on the pool, the
// container for an app's combined disk budget.
func (s *Service) QgroupCreate(pool, groupID string) error {
	_, err := s.runner.RunTimeout(timeout, "btrfs", "qgroup", "create", groupID, pool)
	return err
}

// QgroupAssign makes a subvolume's own qgroup ("0/<rootid>") a member of a
// higher-level group. btrfs exits non-zero when it merely recommends a quota
// rescan after the assign; that is advice, not a failure, so it is tolerated
// (with a best-effort rescan kicked off) rather than surfaced.
func (s *Service) QgroupAssign(pool, subvolQgroup, groupID string) error {
	out, err := s.runner.RunTimeout(timeout, "btrfs", "qgroup", "assign", subvolQgroup, groupID, pool)
	if err != nil && strings.Contains(out+err.Error(), "rescan") {
		_, _ = s.runner.RunTimeout(timeout, "btrfs", "quota", "rescan", pool)
		return nil
	}
	return err
}

// QgroupLimitExclusive hard-caps a group's EXCLUSIVE bytes; 0 clears the limit.
// Exclusive is deliberate: an app's subvolumes share most of their extents with
// the base rootfs (and with each other via snapshots), and a referenced limit
// would count those shared bytes and wedge the container the moment it starts.
func (s *Service) QgroupLimitExclusive(pool, groupID string, diskMB int) error {
	limit := "none"
	if diskMB > 0 {
		limit = strconv.Itoa(diskMB) + "M"
	}
	_, err := s.runner.RunTimeout(timeout, "btrfs", "qgroup", "limit", "-e", limit, groupID, pool)
	return err
}

// QgroupDestroy removes a higher-level qgroup; best-effort cleanup when an app
// is deleted (its member subvolumes are already gone by then).
func (s *Service) QgroupDestroy(pool, groupID string) error {
	_, err := s.runner.RunTimeout(timeout, "btrfs", "qgroup", "destroy", groupID, pool)
	return err
}

// ExclusiveUsageMB returns a group's exclusive bytes in MB from `btrfs qgroup
// show --raw` -- the app's true pinned bytes, i.e. what deleting it would free.
// Returns 0 if the group or output cannot be read, so reporting degrades rather
// than fails.
func (s *Service) ExclusiveUsageMB(pool, groupID string) int {
	out, err := s.runner.RunTimeout(timeout, "btrfs", "qgroup", "show", "--raw", pool)
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] != groupID {
			continue
		}
		bytes, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil {
			return 0
		}
		return int(bytes / bytesPerMB)
	}
	return 0
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
