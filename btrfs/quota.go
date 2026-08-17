package btrfs

import (
	"fmt"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

// hostit uses btrfs SIMPLE quotas (squota, kernel 6.7+), not full qgroups.
// Full qgroups cannot enforce app budgets here: seeding an app subvolume as a
// CoW snapshot of the shared base -- even with -i -- marks the whole
// filesystem's quota state inconsistent, and the kernel stops enforcing every
// limit until a full rescan completes (reproduced on stage: 300MB written
// straight past a 200MB cap). Squota accounts extents to their creating
// subvolume at allocation time: no rescans, never inconsistent, enforcement
// from the first byte, and shared base extents are charged to the base (i.e.
// to nobody), which is exactly the exclusive-bytes budget semantic.

const (
	// quotaModeSquota/quotaModeQgroup are the values of
	// /sys/fs/btrfs/<uuid>/qgroups/mode; the file is absent when quota is off.
	quotaModeSquota = "squota"
	quotaModeQgroup = "qgroup"
	quotaModeOff    = "off"
	// btrfsIocQuotaCtl is BTRFS_IOC_QUOTA_CTL: _IOWR(0x94, 40, struct
	// btrfs_ioctl_quota_ctl_args{u64 cmd; u64 status}) = 3<<30 | 16<<16 |
	// 0x94<<8 | 40.
	btrfsIocQuotaCtl = 0xC0109428
	// btrfsQuotaCtlEnableSimple is BTRFS_QUOTA_CTL_ENABLE_SIMPLE_QUOTA. Issued
	// directly (not via btrfs-progs) because `quota enable --simple` needs
	// progs 6.7+ while every other operation works with much older tools.
	btrfsQuotaCtlEnableSimple = 4
)

// quotaCtlArgs mirrors struct btrfs_ioctl_quota_ctl_args.
type quotaCtlArgs struct {
	cmd    uint64
	status uint64
}

// EnsureSimpleQuota puts the pool in simple-quota mode; idempotent, called at
// every node start. A pool still on full qgroups (an install predating squota)
// is migrated: disable, then enable simple. The switch drops all qgroups and
// stops counting pre-existing extents -- the caller must re-ensure every app's
// budget group afterwards (the startup limit sweep does), and usage readings
// restart from the apps' next writes.
func (s *Service) EnsureSimpleQuota(pool string) error {
	mode, err := s.quotaMode(pool)
	if err != nil {
		return err
	}
	switch mode {
	case quotaModeSquota:
		return nil
	case quotaModeQgroup:
		// Generous on purpose: see migrationTimeout.
		if _, err := s.runner.RunTimeout(migrationTimeout, "btrfs", "quota", "disable", pool); err != nil {
			return fmt.Errorf("cannot disable full qgroups for the squota migration: %w", err)
		}
	}
	return s.enableSimpleQuota(pool)
}

// quotaMode reads the pool's quota mode from sysfs ("squota", "qgroup", or
// "off" when the qgroups directory does not exist). Read through the runner
// like every other probe, so tests can fake it.
func (s *Service) quotaMode(pool string) (string, error) {
	uuid, err := s.runner.RunTimeout(timeout, "findmnt", "-no", "UUID", "--target", pool)
	if err != nil {
		return "", fmt.Errorf("cannot resolve the filesystem of %s: %w", pool, err)
	}
	out, err := s.runner.RunTimeout(timeout, "cat", "/sys/fs/btrfs/"+strings.TrimSpace(uuid)+"/qgroups/mode")
	if err != nil {
		if strings.Contains(out+err.Error(), "No such file") {
			return quotaModeOff, nil
		}
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// enableSimpleQuotaIoctl issues BTRFS_IOC_QUOTA_CTL(ENABLE_SIMPLE_QUOTA) on
// the pool directory; the Service default for its enableSimpleQuota hook.
func enableSimpleQuotaIoctl(pool string) error {
	dir, err := os.Open(pool)
	if err != nil {
		return err
	}
	defer dir.Close()
	args := quotaCtlArgs{cmd: btrfsQuotaCtlEnableSimple}
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, dir.Fd(), btrfsIocQuotaCtl, uintptr(unsafe.Pointer(&args))); errno != 0 {
		return fmt.Errorf("cannot enable simple quotas on %s: %w", pool, errno)
	}
	return nil
}
