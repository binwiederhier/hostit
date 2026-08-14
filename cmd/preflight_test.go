package cmd

import (
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMissingBinaries(t *testing.T) {
	t.Parallel()
	// A lookPath that only "finds" podman and systemctl; everything else is missing.
	have := map[string]bool{"podman": true, "systemctl": true}
	lookPath := func(b string) (string, error) {
		if have[b] {
			return "/usr/bin/" + b, nil
		}
		return "", exec.ErrNotFound
	}
	missing := missingBinaries(lookPath)
	assert.NotContains(t, missing, "podman", "found binaries are not reported missing")
	assert.NotContains(t, missing, "systemctl")
	assert.Contains(t, missing, "btrfs", "an absent required binary is reported")
	assert.Contains(t, missing, "nft")
	assert.Contains(t, missing, "useradd")

	// With everything present, nothing is missing.
	assert.Empty(t, missingBinaries(func(string) (string, error) { return "/usr/bin/x", nil }))
}

func TestParseRuntimeVersion(t *testing.T) {
	t.Parallel()
	// Both tools print "<name> version X.Y.Z" on their first line; anything
	// unparseable must fail closed rather than pass a broken runtime.
	for _, tc := range []struct {
		out, name, want string
		ok              bool
	}{
		{"crun version 1.29.1\ncommit: xyz", "crun", "1.29.1", true},
		{"podman version 4.9.3", "podman", "4.9.3", true},
		{"crun version 1.14.1-1ubuntu1", "crun", "1.14.1", true},
		{"runc version 1.1.12", "crun", "", false}, // wrong runtime entirely
		{"garbage", "crun", "", false},
	} {
		got, err := parseRuntimeVersion(tc.out, tc.name)
		if tc.ok {
			require.NoError(t, err, tc.out)
			assert.Equal(t, tc.want, got, tc.out)
		} else {
			assert.Error(t, err, tc.out)
		}
	}
}

func TestVersionAtLeast(t *testing.T) {
	t.Parallel()
	// The launch gate: crun >= 1.29 (validated with the idmapped rootfs) and
	// podman >= 4.3 (--rootfs :idmap syntax).
	assert.True(t, versionAtLeast("1.29.1", 1, 29))
	assert.True(t, versionAtLeast("1.29", 1, 29))
	assert.True(t, versionAtLeast("2.0", 1, 29))
	assert.False(t, versionAtLeast("1.14.1", 1, 29))
	assert.False(t, versionAtLeast("1.28.9", 1, 29))
	assert.True(t, versionAtLeast("4.9.3", 4, 3))
	assert.False(t, versionAtLeast("4.2.1", 4, 3))
	assert.False(t, versionAtLeast("x.y", 1, 29), "unparseable fails closed")
}
