package app

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/appctl"
	"heckel.io/hostit/store"
)

var testIDs = IDs{UID: 1001, GID: 1001, SubUID: 165536, SubGID: 165536, SubCount: 65536}

func TestContainerCreateArgsWorkspaceMode(t *testing.T) {
	t.Parallel()
	conf := &appctl.AppConfig{Run: "python3 -m http.server $PORT"}
	require.NoError(t, conf.Validate())
	a := &store.App{Name: "blog", Port: 10000}
	args := containerCreateArgs(conf, a, "/srv/hostit/apps/blog", "/run/hostit/hostit.sock", "/usr/bin/hostit", 0, testIDs)
	cmd := strings.Join(args, " ")
	assert.Contains(t, cmd, "create --name hostit-app-blog")
	assert.Contains(t, cmd, "--hostname blog")
	assert.Contains(t, cmd, "--env PORT=10000")
	assert.Contains(t, cmd, "--env HOME=/home/blog")
	assert.Contains(t, cmd, "--workdir /home/blog")
	assert.Contains(t, cmd, "--publish 127.0.0.1:10000:10000")
	assert.Contains(t, cmd, "--volume /srv/hostit/apps/blog:/home/blog")
	assert.Contains(t, cmd, "--volume /usr/bin/hostit:/usr/bin/hostit:ro")
	assert.Contains(t, cmd, "--volume /run/hostit/hostit.sock:/run/hostit/hostit.sock")
	assert.Contains(t, cmd, workspaceImage)
	// The agent supervises the run command as PID 1
	assert.True(t, strings.HasSuffix(cmd, workspaceImage+" /usr/bin/hostit agent"), cmd)
}

func TestContainerCreateArgsImageMode(t *testing.T) {
	t.Parallel()
	conf := &appctl.AppConfig{Image: "docker.io/library/nginx:alpine", ContainerPort: 80, Env: map[string]string{"FOO": "bar"}, Volumes: []string{"./data:/data"}}
	require.NoError(t, conf.Validate())
	a := &store.App{Name: "blog", Port: 10000}
	args := containerCreateArgs(conf, a, "/srv/hostit/apps/blog", "/run/hostit/hostit.sock", "/usr/bin/hostit", 0, testIDs)
	cmd := strings.Join(args, " ")
	assert.Contains(t, cmd, "--publish 127.0.0.1:10000:80")
	assert.Contains(t, cmd, "--env PORT=80")
	assert.Contains(t, cmd, "--env FOO=bar")
	assert.Contains(t, cmd, "--volume /srv/hostit/apps/blog/data:/data")
	// No command override: the image's own entrypoint runs
	assert.True(t, strings.HasSuffix(cmd, "docker.io/library/nginx:alpine"), cmd)
	// The home dir is NOT mounted in image mode, but CLI plumbing is
	assert.NotContains(t, cmd, ":/home/blog")
	assert.Contains(t, cmd, "--volume /usr/bin/hostit:/usr/bin/hostit:ro")
}

func TestContainerCreateArgsBuildMode(t *testing.T) {
	t.Parallel()
	conf := &appctl.AppConfig{Build: ".", ContainerPort: 8080}
	require.NoError(t, conf.Validate())
	a := &store.App{Name: "blog", Port: 10000}
	args := containerCreateArgs(conf, a, "/srv/hostit/apps/blog", "/run/hostit/hostit.sock", "/usr/bin/hostit", 0, testIDs)
	assert.Equal(t, buildImageTag("blog"), args[len(args)-1])
}

func TestContainerConfigHashChanges(t *testing.T) {
	t.Parallel()
	a := &store.App{Name: "blog", Port: 10000}
	conf1 := &appctl.AppConfig{Run: "./server"}
	conf2 := &appctl.AppConfig{Run: "./server"}
	conf3 := &appctl.AppConfig{Run: "./other"}
	conf4 := &appctl.AppConfig{Image: "nginx", ContainerPort: 80}
	hash1 := containerConfigHash(containerCreateArgs(conf1, a, "/h", "/s", "/b", 0, testIDs))
	hash2 := containerConfigHash(containerCreateArgs(conf2, a, "/h", "/s", "/b", 0, testIDs))
	hash3 := containerConfigHash(containerCreateArgs(conf3, a, "/h", "/s", "/b", 0, testIDs))
	hash4 := containerConfigHash(containerCreateArgs(conf4, a, "/h", "/s", "/b", 0, testIDs))
	assert.Equal(t, hash1, hash2)
	// run: changes are handled by the agent (SIGHUP), not by recreating the container,
	// so the hash must NOT depend on the run command ...
	assert.Equal(t, hash1, hash3)
	// ... but image/port changes must recreate
	assert.NotEqual(t, hash1, hash4)
}

func TestWorkspaceContainerfile(t *testing.T) {
	t.Parallel()
	// The workspace image must contain the bits that make ssh/scp/sftp/rsync work
	assert.Contains(t, workspaceContainerfile, "openssh-sftp-server")
	assert.Contains(t, workspaceContainerfile, "rsync")
	assert.Contains(t, workspaceContainerfile, "FROM docker.io/library/debian")
}
