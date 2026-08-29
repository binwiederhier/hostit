// Package btrfs wraps the btrfs subvolume and qgroup operations hostit uses for
// app-subvolume snapshots and hard disk quotas. It shells out through an injected
// runner (so it can be faked in tests) and owns its own command timeout. Path layout
// -- where an app's subvolume and snapshots live -- stays with the caller; this
// package only operates on the paths it is given.
package btrfs

import (
	"strconv"
	"strings"
	"time"

	"heckel.io/hostit/system/run"
)

const (
	// BudgetGroupPrefix is the level-1 qgroup namespace app disk budgets live in
	// ("1/<uid>"). Exported so the node package builds group ids against the same
	// prefix this package's ListBudgetGroups filters on -- the reconcile sweep's
	// correctness depends on the two never drifting apart.
	BudgetGroupPrefix = "1/"
	// timeout bounds a btrfs command; these are metadata operations (create,
	// snapshot, qgroup) and return in well under a second on an idle pool -- but
	// they must wait for the current transaction, and on a small host the kernel
	// cleaner (processing deleted subvolumes) or a quota rescan can hold that
	// for tens of seconds. Generous on purpose: a slow create beats a failed
	// one; the point of the bound is only to never wedge a request forever.
	timeout = 2 * time.Minute
	// migrationTimeout bounds the one-time squota migration, which walks the
	// whole filesystem: on a pool with hundreds of subvolumes it runs for
	// minutes, and killing it on the ordinary deadline leaves the pool with no
	// quota at all -- every app uncapped -- while the kernel finishes the work
	// anyway. It is a backstop against a wedged command, not a service level.
	migrationTimeout = time.Hour
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
	Snapshot(src, dst string, readonly bool, qgroup string) error
	SetReadOnly(path string, readonly bool) error
	EnsureSimpleQuota(pool string) error
	RootID(path string) (string, error)
	QgroupCreate(pool, groupID string) error
	QgroupAssign(pool, subvolQgroup, groupID string) error
	QgroupLimitExclusive(pool, groupID string, diskMB int) error
	QgroupDestroy(pool, groupID string) error
	QgroupTryDestroy(pool, groupID string) error
	IsBtrfs(dir string) bool
	ListQgroups(pool string) ([]string, error)
	SubvolumeIDs(pool string) ([]string, error)
	ExclusiveUsageMB(pool, groupID string) int
	ListBudgetGroups(pool string) ([]string, error)
}

// Service performs btrfs subvolume and qgroup operations over a run.Runner.
type Service struct {
	runner run.Runner
	// enableSimpleQuota issues the quota-enable ioctl (no btrfs-progs version
	// dependency); a hook so tests can observe it without a real btrfs mount.
	enableSimpleQuota func(pool string) error
}

var _ Interface = (*Service)(nil)

// New builds a btrfs Service from a command runner.
func New(runner run.Runner) *Service {
	return &Service{runner: runner, enableSimpleQuota: enableSimpleQuotaIoctl}
}

// filesystem returns the filesystem type of dir (e.g. "btrfs", "ext2/ext3"), as
// reported by stat(1).
func (s *Service) filesystem(dir string) (string, error) {
	out, err := s.runner.RunTimeout(timeout, "stat", "-f", "-c", "%T", dir)
	return strings.TrimSpace(out), err
}

// IsBtrfs reports whether dir lives on a btrfs filesystem, which is what makes
// subvolume snapshots and hard qgroup quotas possible.
func (s *Service) IsBtrfs(dir string) bool {
	fstype, err := s.filesystem(dir)
	return err == nil && fstype == "btrfs"
}

// CreateSubvolume makes a new btrfs subvolume at path (a base staging subvolume).
func (s *Service) CreateSubvolume(path string) error {
	_, err := s.runner.RunTimeout(timeout, "btrfs", "subvolume", "create", path)
	return err
}

// DeleteSubvolume removes a subvolume (an app subvolume, a snapshot, a base). A read-only
// snapshot must have its flag cleared first, which the caller handles.
func (s *Service) DeleteSubvolume(path string) error {
	_, err := s.runner.RunTimeout(timeout, "btrfs", "subvolume", "delete", path)
	return err
}

// MoveSubvolume renames a subvolume within the apps filesystem. Same-fs, so it is
// a metadata rename (fast, atomic): what publishes a base export and swaps a
// rollback's or the migration's staged subvolume into place.
func (s *Service) MoveSubvolume(src, dst string) error {
	_, err := s.runner.Run("mv", src, dst)
	return err
}

// Snapshot snapshots src to dst; readonly makes a stable, immutable snapshot (what
// we keep) versus a writable copy (what a rollback or fork starts from). A
// non-empty qgroup adds the new subvolume to that group AT CREATION (-i):
// membership is atomic and accounting stays consistent, so the budget enforces
// from the first byte -- a later "qgroup assign" marks the group inconsistent
// and the kernel does not enforce limits until a rescan completes.
func (s *Service) Snapshot(src, dst string, readonly bool, qgroup string) error {
	args := []string{"btrfs", "subvolume", "snapshot"}
	if readonly {
		args = append(args, "-r")
	}
	if qgroup != "" {
		args = append(args, "-i", qgroup)
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
	// An existing membership ("File exists") is success: every budget path is
	// ensure-style and re-runs after partial failures, and the migration retries
	// whole apps -- treating a re-assign as an error would keep it from converging.
	if err != nil && strings.Contains(out+err.Error(), "File exists") {
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
// is deleted (its member subvolumes are already gone by then). Right after the
// member subvolumes were deleted, destroy fails with "Device or resource busy"
// until the btrfs transaction commits -- a filesystem sync forces that commit,
// so one sync+retry turns the common app-delete case into a clean destroy.
// QgroupTryDestroy is a single gentle destroy attempt for background
// teardowns: no filesystem sync (which forces a full transaction commit and
// stalls every concurrent operation on the pool -- an app create's snapshot
// waited many seconds behind one), no member surgery. The caller polls it;
// the startup reconcile still runs the full ladder for stragglers.
func (s *Service) QgroupTryDestroy(pool, groupID string) error {
	_, err := s.runner.RunTimeout(timeout, "btrfs", "qgroup", "destroy", groupID, pool)
	return err
}

func (s *Service) QgroupDestroy(pool, groupID string) error {
	out, err := s.runner.RunTimeout(timeout, "btrfs", "qgroup", "destroy", groupID, pool)
	if err == nil || !strings.Contains(out+err.Error(), "busy") {
		return err
	}
	_, _ = s.runner.RunTimeout(timeout, "btrfs", "filesystem", "sync", pool)
	out, err = s.runner.RunTimeout(timeout, "btrfs", "qgroup", "destroy", groupID, pool)
	if err == nil || !strings.Contains(out+err.Error(), "busy") {
		return err
	}
	// Still busy after the sync: the group has members whose subvolumes are long
	// deleted (they linger as stale 0/<id> qgroups), and btrfs refuses to destroy
	// a group that has members. Remove them from the group, then destroy.
	for _, member := range s.groupMembers(pool, groupID) {
		_, _ = s.runner.RunTimeout(timeout, "btrfs", "qgroup", "remove", member, groupID, pool)
	}
	_, err = s.runner.RunTimeout(timeout, "btrfs", "qgroup", "destroy", groupID, pool)
	return err
}

// groupMembers parses the group's child list from `btrfs qgroup show -pc`. The
// child list is the FIFTH column (qgroupid, referenced, exclusive, parent,
// child); a trailing path column follows and can itself contain spaces ("<0
// member qgroups>"), so the last field is NOT the child list.
func (s *Service) groupMembers(pool, groupID string) []string {
	out, err := s.runner.RunTimeout(timeout, "btrfs", "qgroup", "show", "-pc", pool)
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[0] != groupID {
			continue
		}
		children := fields[4]
		if children == "-" || children == "" {
			return nil
		}
		return strings.Split(children, ",")
	}
	return nil
}

// ListBudgetGroups returns the level-1 qgroup ids ("1/<id>") that exist on the
// pool -- the app budget groups. Used by the orphan reconcile to sweep groups
// whose app is gone (a destroy that stayed busy leaves one behind).
func (s *Service) ListBudgetGroups(pool string) ([]string, error) {
	groups, err := s.ListQgroups(pool)
	if err != nil {
		return nil, err
	}
	budgets := make([]string, 0, len(groups))
	for _, g := range groups {
		if strings.HasPrefix(g, BudgetGroupPrefix) {
			budgets = append(budgets, g)
		}
	}
	return budgets, nil
}

// ListQgroups returns every qgroup id on the pool ("0/256", "1/1000", ...).
func (s *Service) ListQgroups(pool string) ([]string, error) {
	out, err := s.runner.RunTimeout(timeout, "btrfs", "qgroup", "show", pool)
	if err != nil {
		return nil, err
	}
	groups := make([]string, 0)
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && strings.Contains(fields[0], "/") {
			groups = append(groups, fields[0])
		}
	}
	return groups, nil
}

// SubvolumeIDs returns the root id of every subvolume on the pool; a
// subvolume's own qgroup is "0/<rootid>".
func (s *Service) SubvolumeIDs(pool string) ([]string, error) {
	out, err := s.runner.RunTimeout(timeout, "btrfs", "subvolume", "list", pool)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "ID" {
			ids = append(ids, fields[1])
		}
	}
	return ids, nil
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
