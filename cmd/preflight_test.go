package cmd

import (
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
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
