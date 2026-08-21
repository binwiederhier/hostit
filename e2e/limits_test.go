//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAppLimitsEndToEnd proves the three per-app caps on a live instance: the
// PATCH lands, a reboot converges the container, and each cap actually BITES
// where the tenant would hit it -- dd refused by the disk quota, an
// over-allocation killed by the memory cap, and the cgroup stating the CPU
// cap inside the container. Also pins the create-time CPU stamp and that an
// app's own agent token cannot edit limits.
func TestAppLimitsEndToEnd(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	name := uniqueName("e2e-limit")
	app := e.createApp(name)
	t.Cleanup(func() { e.deleteApp(name) })
	token := fmt.Sprint(app["agent_token"])

	// A fresh app carries the stamped CPU default (0.5 cores) as its override.
	var got map[string]any
	e.get("/api/apps/"+name, e.token, &got)
	assert.EqualValues(t, 500, got["cpu_milli"], "new apps get the default CPU cap stamped")

	// The agent token may not edit limits: an assistant must not raise its own caps.
	req, err := http.NewRequest("PATCH", e.host+"/api/apps/"+name+"/limits", strings.NewReader(`{"memory_mb":4096}`))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode, "the app's own token cannot resize it")

	// Tight caps, then a reboot: disk applies live, memory and CPU converge on
	// the recreate the reboot performs.
	e.doJSON("PATCH", "/api/apps/"+name+"/limits", e.token,
		map[string]int{"memory_mb": 128, "disk_mb": 300, "cpu_milli": 500}, nil, http.StatusOK)
	e.post("/api/apps/"+name+"/reboot", e.token, nil)

	// The cgroup inside the container states the enforced caps: 128 MB memory
	// and a 0.5-core quota (50000 of every 100000 microseconds).
	out, code := e.runEventually(name, token, "cat /sys/fs/cgroup/memory.max /sys/fs/cgroup/cpu.max 2>&1")
	require.Zero(t, code, "cgroup read: %s", truncate(out, 300))
	assert.Contains(t, out, "134217728", "memory.max is the 128 MB cap")
	assert.Contains(t, out, "50000", "cpu.max carries the half-core quota")

	// Disk: writing past the 300 MB budget must fail with a quota error, not
	// silently succeed. dd reports "Disk quota exceeded" (EDQUOT).
	out, _ = e.runEventually(name, token, "dd if=/dev/zero of=big.bin bs=1M count=400 2>&1; rm -f big.bin")
	assert.Contains(t, strings.ToLower(out), "quota", "a write past the disk budget is refused: %s", truncate(out, 300))

	// Memory: allocating far past the cap must die (OOM kill or MemoryError),
	// never print success.
	out, _ = e.runEventually(name, token,
		`python3 -c "a = bytearray(400*1024*1024); print('allocated-fine')" 2>&1 || echo "alloc-died"`)
	assert.NotContains(t, out, "allocated-fine", "an allocation past the memory cap must not succeed")
}
