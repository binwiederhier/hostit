package agent

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	// lowMemBuildThreshold is the container memory at or below which the Go
	// compiler gets tuned to fit. Above it a build has ample headroom and runs
	// faster untuned, so tuning only kicks in for small apps -- the 512 MB apps
	// that OOM-kill `go build` are well under it.
	lowMemBuildThreshold = 768 << 20 // 768 MiB
	// goBuildMemFraction is the percent of the container's memory GOMEMLIMIT is
	// set to: enough headroom under the hard cap that the compiler collects
	// before the kernel kills it, while still giving the build most of the RAM.
	goBuildMemFraction = 80
	// goBuildMemFloorMiB keeps GOMEMLIMIT sane on a very small cap.
	goBuildMemFloorMiB = 64
)

// containerMemLimitBytes reads this container's memory cap from the cgroup v2
// interface. It returns 0 when the cap is unset ("max"), unreadable, or not a
// number -- callers treat 0 as "unknown, do not tune".
func containerMemLimitBytes() int64 {
	b, err := os.ReadFile("/sys/fs/cgroup/memory.max")
	if err != nil {
		return 0
	}
	s := strings.TrimSpace(string(b))
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil { // "max" (unlimited) or anything unexpected
		return 0
	}
	return n
}

// goBuildTuning returns environment additions (KEY=VALUE) that let `go build`
// fit a small container: -p=1 compiles one package at a time, and GOMEMLIMIT
// plus a low GOGC make the compiler collect aggressively and stay under the
// cap. The Go RUNTIME is tiny; the COMPILER is the memory hog, so this matters
// only at build time -- hostit applies it to the prepare step alone, never to
// the running app. It is a no-op above lowMemBuildThreshold (a big box builds
// faster untuned) or when the cap is unknown, harmless to non-Go builds (these
// vars only touch Go tools), and it NEVER overrides a var the app already set.
func goBuildTuning(memLimitBytes int64, existing []string) []string {
	if memLimitBytes <= 0 || memLimitBytes > lowMemBuildThreshold {
		return nil
	}
	set := make(map[string]bool, len(existing))
	for _, kv := range existing {
		if i := strings.IndexByte(kv, '='); i > 0 {
			set[kv[:i]] = true
		}
	}
	var out []string
	if !set["GOFLAGS"] {
		out = append(out, "GOFLAGS=-p=1")
	}
	if !set["GOGC"] {
		out = append(out, "GOGC=30")
	}
	if !set["GOMEMLIMIT"] {
		limitMiB := memLimitBytes / (1 << 20) * goBuildMemFraction / 100
		if limitMiB < goBuildMemFloorMiB {
			limitMiB = goBuildMemFloorMiB
		}
		out = append(out, fmt.Sprintf("GOMEMLIMIT=%dMiB", limitMiB))
	}
	return out
}
