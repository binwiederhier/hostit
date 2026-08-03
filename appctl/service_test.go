package appctl

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadAppConfigProcessMode(t *testing.T) {
	t.Parallel()
	c := writeAndLoadConfig(t, `
run: ./server --debug
env:
  FOO: bar
`)
	assert.Equal(t, ModeProcess, c.Mode())
	assert.Equal(t, "./server --debug", c.Run)
	assert.Equal(t, "bar", c.Env["FOO"])
}

func TestLoadAppConfigContainerMode(t *testing.T) {
	t.Parallel()
	c := writeAndLoadConfig(t, `
image: docker.io/library/nginx:alpine
container-port: 80
volumes:
  - ./data:/data
`)
	assert.Equal(t, ModeContainer, c.Mode())
	assert.Equal(t, "docker.io/library/nginx:alpine", c.Image)
	assert.Equal(t, 80, c.ContainerPort)
}

func TestAppConfigValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{"empty", ``, "hostit.yml"},
		{"only comments", `# nothing here`, "hostit.yml"},
		{"run and image", "run: ./x\nimage: nginx\ncontainer-port: 80", "either"},
		{"image without port", `image: nginx`, "container-port"},
		{"build without port", `build: .`, "container-port"},
		{"build and image", "build: .\nimage: nginx\ncontainer-port: 80", "either"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			filename := filepath.Join(t.TempDir(), "hostit.yml")
			require.NoError(t, os.WriteFile(filename, []byte(tt.yaml), 0600))
			_, err := LoadAppConfig(filename)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestUnitFileProcessMode(t *testing.T) {
	t.Parallel()
	c := &AppConfig{Run: "./server --debug", Env: map[string]string{"FOO": "bar", "AAA": "first"}}
	require.NoError(t, c.Validate())
	unit := unitFile(c, &SelfInfo{Name: "blog", Port: 10000}, "/srv/hostit/apps/blog", "/usr/bin/podman")
	assert.Contains(t, unit, "Description=hostit app blog")
	assert.Contains(t, unit, "WorkingDirectory=/srv/hostit/apps/blog")
	assert.Contains(t, unit, `Environment="PORT=10000"`)
	assert.Contains(t, unit, `Environment="AAA=first"`)
	assert.Contains(t, unit, `Environment="FOO=bar"`)
	assert.Contains(t, unit, "ExecStart=/bin/sh -lc './server --debug'")
	assert.Contains(t, unit, "Restart=always")
	assert.Contains(t, unit, "WantedBy=default.target")
	// Env must be sorted for deterministic output
	assert.Less(t, strings.Index(unit, "AAA"), strings.Index(unit, "FOO"))
}

func TestUnitFileContainerMode(t *testing.T) {
	t.Parallel()
	c := &AppConfig{Image: "docker.io/library/nginx:alpine", ContainerPort: 80, Volumes: []string{"./data:/data"}}
	require.NoError(t, c.Validate())
	unit := unitFile(c, &SelfInfo{Name: "blog", Port: 10000}, "/srv/hostit/apps/blog", "/usr/bin/podman")
	assert.Contains(t, unit, "ExecStartPre=-/usr/bin/podman rm --force hostit-app")
	assert.Contains(t, unit, "ExecStart=/usr/bin/podman run --rm --name hostit-app")
	assert.Contains(t, unit, "--publish 127.0.0.1:10000:80")
	assert.Contains(t, unit, "--volume /srv/hostit/apps/blog/data:/data") // Relative volume resolved
	assert.Contains(t, unit, "docker.io/library/nginx:alpine")
	assert.Contains(t, unit, "ExecStop=/usr/bin/podman stop --time 5 hostit-app")
}

func TestUnitFileBuildMode(t *testing.T) {
	t.Parallel()
	c := &AppConfig{Build: ".", ContainerPort: 8080}
	require.NoError(t, c.Validate())
	unit := unitFile(c, &SelfInfo{Name: "blog", Port: 10000}, "/srv/hostit/apps/blog", "/usr/bin/podman")
	assert.Contains(t, unit, buildImageTag)
}

func TestControllerUp(t *testing.T) {
	t.Parallel()
	ctl, runner, appDir := newTestController(t)
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "hostit.yml"), []byte("run: ./server"), 0600))
	require.NoError(t, ctl.Up())
	unitFilename := filepath.Join(appDir, ".config", "systemd", "user", unitName+".service")
	b, err := os.ReadFile(unitFilename)
	require.NoError(t, err)
	assert.Contains(t, string(b), `Environment="PORT=10000"`)
	require.Len(t, runner.commands, 3)
	assert.Equal(t, "systemctl --user daemon-reload", runner.commands[0])
	assert.Equal(t, "systemctl --user enable "+unitName, runner.commands[1])
	assert.Equal(t, "systemctl --user restart "+unitName, runner.commands[2])
}

func TestControllerUpBuildMode(t *testing.T) {
	t.Parallel()
	ctl, runner, appDir := newTestController(t)
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "hostit.yml"), []byte("build: .\ncontainer-port: 8080"), 0600))
	require.NoError(t, ctl.Up())
	require.NotEmpty(t, runner.commands)
	assert.Contains(t, runner.commands[0], "podman build --tag "+buildImageTag)
}

func TestControllerDownRestart(t *testing.T) {
	t.Parallel()
	ctl, runner, _ := newTestController(t)
	require.NoError(t, ctl.Down())
	require.NoError(t, ctl.Restart())
	assert.Equal(t, "systemctl --user disable --now "+unitName, runner.commands[0])
	assert.Equal(t, "systemctl --user restart "+unitName, runner.commands[1])
}

func TestSelf(t *testing.T) {
	t.Parallel()
	socketFile := filepath.Join(t.TempDir(), "hostit.sock")
	listener, err := net.Listen("unix", socketFile)
	require.NoError(t, err)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/self", r.URL.Path)
		_ = json.NewEncoder(w).Encode(&SelfInfo{Name: "blog", Port: 10000, URL: "https://blog.apps.example.com"})
	})}
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = server.Close()
	})
	self, err := Self(socketFile)
	require.NoError(t, err)
	assert.Equal(t, "blog", self.Name)
	assert.Equal(t, 10000, self.Port)
	assert.Equal(t, "https://blog.apps.example.com", self.URL)
}

func writeAndLoadConfig(t *testing.T, content string) *AppConfig {
	t.Helper()
	filename := filepath.Join(t.TempDir(), "hostit.yml")
	require.NoError(t, os.WriteFile(filename, []byte(content), 0600))
	c, err := LoadAppConfig(filename)
	require.NoError(t, err)
	return c
}

func newTestController(t *testing.T) (*Controller, *fakeRunner, string) {
	t.Helper()
	appDir := t.TempDir()
	runner := &fakeRunner{}
	ctl := NewController(appDir, "/tmp/nonexistent.sock", runner)
	ctl.self = &SelfInfo{Name: "blog", Port: 10000, URL: "https://blog.apps.example.com"} // Skip socket lookup
	ctl.podman = "/usr/bin/podman"
	return ctl, runner, appDir
}

// fakeRunner records commands instead of executing them
type fakeRunner struct {
	commands []string
}

var _ Runner = (*fakeRunner)(nil)

func (f *fakeRunner) Run(name string, args ...string) (string, error) {
	f.commands = append(f.commands, name+" "+strings.Join(args, " "))
	return "", nil
}
