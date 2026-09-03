package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A small container gets the compiler tuned so `go build` stays under its cap
// instead of being OOM-killed. GOMEMLIMIT is a fraction of the cap (512 MiB *
// 80% = 409 MiB).
func TestGoBuildTuningLowMemory(t *testing.T) {
	got := goBuildTuning(512<<20, nil)
	require.Contains(t, got, "GOFLAGS=-p=1")
	require.Contains(t, got, "GOGC=30")
	require.Contains(t, got, "GOMEMLIMIT=409MiB")
}

// Ample memory (or an unknown cap) is left completely alone -- an untuned build
// is faster, and unknown means we cannot size GOMEMLIMIT safely.
func TestGoBuildTuningNoopWhenNotLowMemory(t *testing.T) {
	require.Nil(t, goBuildTuning(2<<30, nil), "2 GiB is ample, no tuning")
	require.Nil(t, goBuildTuning(0, nil), "unknown cap does not tune")
	require.Nil(t, goBuildTuning(-1, nil), "a bad cap does not tune")
}

// A var the app set in its own env: wins; only the ones it left unset are added.
func TestGoBuildTuningRespectsExistingEnv(t *testing.T) {
	got := goBuildTuning(512<<20, []string{"GOFLAGS=-mod=vendor", "GOMEMLIMIT=200MiB", "PATH=/usr/bin"})
	require.Equal(t, []string{"GOGC=30"}, got, "user's GOFLAGS and GOMEMLIMIT are untouched")
}
