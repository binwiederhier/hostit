package container

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRunner records commands and can be primed to return canned output for a
// command whose joined args start with a given prefix.
type fakeRunner struct {
	ran     []string
	timed   []string
	outputs map[string]string
}

func newFakeRunner() *fakeRunner { return &fakeRunner{outputs: map[string]string{}} }

func (f *fakeRunner) output(joined string) string {
	for prefix, out := range f.outputs {
		if strings.HasPrefix(joined, prefix) {
			return out
		}
	}
	return ""
}

func (f *fakeRunner) Run(args ...string) (string, error) {
	joined := strings.Join(args, " ")
	f.ran = append(f.ran, joined)
	return f.output(joined), nil
}

func (f *fakeRunner) RunTimeout(_ time.Duration, args ...string) (string, error) {
	joined := strings.Join(args, " ")
	f.timed = append(f.timed, joined)
	return f.output(joined), nil
}

func TestContainerVerbs(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	s := New(r)

	_, _ = s.Inspect("hostit-app-abc", "{{.Config.Labels.hash}}")
	require.NoError(t, s.RemoveForce("hostit-app-abc"))
	require.NoError(t, s.Kill("hostit-app-abc", "HUP"))
	require.NoError(t, s.Create("create", "--name", "hostit-app-abc", "img"))
	require.NoError(t, s.RemoveImage("localhost/hostit-workspace:old"))
	_, _ = s.Images()
	_, _ = s.ImageOf("hostit-app-abc")

	assert.Equal(t, []string{
		"podman container inspect hostit-app-abc --format {{.Config.Labels.hash}}",
		"podman rm --force hostit-app-abc",
		"podman kill --signal HUP hostit-app-abc",
		"podman create --name hostit-app-abc img",
		"podman rmi localhost/hostit-workspace:old",
		"podman images --format {{.Repository}}:{{.Tag}}",
		"podman inspect hostit-app-abc --format {{.ImageName}}",
	}, r.ran)
}

func TestContainerNames(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	s := New(r)

	_, _ = s.Names(true)
	_, _ = s.Names(false)
	assert.Equal(t, []string{
		"podman ps --all --format {{.Names}}",
		"podman ps --format {{.Names}}",
	}, r.ran)
}

func TestContainerImages(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	s := New(r)

	assert.True(t, s.ImageExists("localhost/hostit-workspace:abc")) // fake returns nil err
	require.NoError(t, s.Build("localhost/hostit-workspace:abc", "/build/ctx"))
	assert.Equal(t, []string{
		"podman image exists localhost/hostit-workspace:abc",
		"podman build --tag localhost/hostit-workspace:abc /build/ctx",
	}, r.ran)
}

func TestCreateFromReturnsTheContainerID(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.outputs["podman create"] = "abc123def\n"
	s := New(r)

	id, err := s.CreateFrom("localhost/hostit-workspace:abc", "true")
	require.NoError(t, err)
	assert.Equal(t, "abc123def", id)
	assert.Equal(t, []string{"podman create localhost/hostit-workspace:abc true"}, r.ran)
}

func TestExportRootfsPipesIntoTheTargetDirectory(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	s := New(r)

	require.NoError(t, s.ExportRootfs(15*time.Minute, "abc123def", "/apps/.bases/tag1"))
	require.Len(t, r.timed, 1)
	// The export streams straight into tar: an ~860 MB rootfs must never hit a
	// temp file on a host this small. xattrs (file capabilities) must survive.
	assert.Equal(t, "sh -c podman export abc123def | tar -xpf - --xattrs --xattrs-include='*' -C /apps/.bases/tag1", r.timed[0])
	assert.Empty(t, r.ran, "the export is bounded by a timeout")
}

func TestContainerTimedCalls(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	s := New(r)

	_, _ = s.RunningStartTimes(5 * time.Second)
	_, _ = s.Stats(5 * time.Second)
	_, _ = s.Exec(10*time.Second, "hostit-app-abc", "/app", "/bin/sh", "-lc", "ls")

	assert.Equal(t, []string{
		"podman ps --format {{.Names}}|{{.StartedAt}}",
		"podman stats --no-stream --format json",
		"podman exec --workdir /app hostit-app-abc /bin/sh -lc ls",
	}, r.timed)
	assert.Empty(t, r.ran, "timed calls must not go through the untimed path")
}
