// Package hoststats measures the machine a hostit component runs on: memory,
// disk and load. Every cluster member reports it in its heartbeat, so the
// admin page can answer "is this box healthy" without anyone SSHing in.
//
// It is a leaf on purpose: both wire contracts (nodeapi, proxyapi) embed the
// same shape, and one definition beats two that drift.
package hoststats

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	// procMeminfo and procLoadavg are the kernel's own accounting; on a
	// container-less host (which every hostit component runs on) they describe
	// the machine itself.
	procMeminfo = "/proc/meminfo"
	procLoadavg = "/proc/loadavg"
	kbPerMB     = 1024
)

// Stats is one machine's resource state at heartbeat time. Zero values mean
// "not reported" (see Known), not "an idle machine".
type Stats struct {
	MemoryUsedMB  int `json:"memory_used_mb"`
	MemoryTotalMB int `json:"memory_total_mb"`
	DiskUsedMB    int `json:"disk_used_mb"`
	DiskTotalMB   int `json:"disk_total_mb"`
	// Load1 is the 1-minute load average, and CPUCount the cores it is spread
	// over -- load alone says nothing without knowing how many cores carry it.
	Load1    float64 `json:"load1"`
	CPUCount int     `json:"cpu_count"`
}

// Known reports whether these stats were ever measured, so a member that has
// not reported reads as "--" rather than as a machine with no memory.
func (s Stats) Known() bool {
	return s.MemoryTotalMB > 0 || s.DiskTotalMB > 0
}

// MemoryPercent and DiskPercent are what the UI colours on; both answer 0
// rather than dividing by an unreported total.
func (s Stats) MemoryPercent() int {
	return percent(s.MemoryUsedMB, s.MemoryTotalMB)
}

func (s Stats) DiskPercent() int {
	return percent(s.DiskUsedMB, s.DiskTotalMB)
}

func percent(used, total int) int {
	if total <= 0 {
		return 0
	}
	return int(float64(used) / float64(total) * 100)
}

// Measure reads the machine's current state. diskPath picks the filesystem
// that matters to the caller -- a node's apps pool, a proxy's cache dir --
// since that is the one whose filling up breaks the component.
//
// A failed reading leaves its fields zero rather than failing the whole
// heartbeat: telemetry must never take a member offline.
func Measure(diskPath string) Stats {
	var s Stats
	s.CPUCount = runtime.NumCPU()
	if f, err := os.Open(procMeminfo); err == nil {
		if total, available, err := parseMeminfo(f); err == nil {
			s.MemoryTotalMB, s.MemoryUsedMB = total, total-available
		}
		_ = f.Close()
	}
	if f, err := os.Open(procLoadavg); err == nil {
		if load, err := parseLoadavg(f); err == nil {
			s.Load1 = load
		}
		_ = f.Close()
	}
	var fs unix.Statfs_t
	if err := unix.Statfs(diskPath, &fs); err == nil {
		mb := uint64(fs.Bsize) / (1024 * 1024)
		if mb == 0 { // the usual 4k block: compute in bytes instead
			s.DiskTotalMB = int(fs.Blocks * uint64(fs.Bsize) / (1024 * 1024))
			s.DiskUsedMB = int((fs.Blocks - fs.Bavail) * uint64(fs.Bsize) / (1024 * 1024))
		} else {
			s.DiskTotalMB = int(fs.Blocks * mb)
			s.DiskUsedMB = int((fs.Blocks - fs.Bavail) * mb)
		}
	}
	return s
}

// parseMeminfo returns total and available memory in MB. Available, not free:
// see the test for why the distinction is the whole point.
func parseMeminfo(r io.Reader) (totalMB, availableMB int, err error) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		key, value, found := strings.Cut(scanner.Text(), ":")
		if !found {
			continue
		}
		kb, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(value), " kB"))
		if err != nil {
			continue
		}
		switch key {
		case "MemTotal":
			totalMB = kb / kbPerMB
		case "MemAvailable":
			availableMB = kb / kbPerMB
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, 0, err
	}
	if totalMB == 0 {
		return 0, 0, errors.New("no MemTotal in meminfo")
	}
	return totalMB, availableMB, nil
}

// parseLoadavg returns the 1-minute load average.
func parseLoadavg(r io.Reader) (float64, error) {
	var line string
	scanner := bufio.NewScanner(r)
	if !scanner.Scan() {
		return 0, errors.New("empty loadavg")
	}
	line = scanner.Text()
	first, _, found := strings.Cut(line, " ")
	if !found {
		return 0, fmt.Errorf("cannot parse loadavg %q", line)
	}
	return strconv.ParseFloat(first, 64)
}
