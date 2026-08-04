package appctl

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
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

func TestLoadAppConfigStaticMode(t *testing.T) {
	t.Parallel()
	c := writeAndLoadConfig(t, "static: public\n")
	assert.Equal(t, ModeStatic, c.Mode())
	assert.Equal(t, "public", c.Static)
	// A bare "static: ." serves the app directory itself
	c = writeAndLoadConfig(t, `static: "."`)
	assert.Equal(t, ModeStatic, c.Mode())
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
		{"static and run", "static: public\nrun: ./server", "either"},
		{"static and image", "static: public\nimage: nginx\ncontainer-port: 80", "either"},
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

func TestParseAppConfigLenient(t *testing.T) {
	t.Parallel()
	c, err := ParseAppConfig([]byte("# nothing"))
	require.NoError(t, err)
	assert.Empty(t, c.Run)
}

func TestControllerLifecycle(t *testing.T) {
	t.Parallel()
	ctl := newTestController(t, map[string]any{
		"GET /v1/self":          &SelfInfo{Name: "blog", Port: 10000, URL: "https://blog.apps.example.com"},
		"POST /v1/self/ensure":  map[string]string{"message": "workspace ready"},
		"POST /v1/self/up":      map[string]string{"message": "deployed"},
		"POST /v1/self/down":    map[string]string{"message": "stopped"},
		"POST /v1/self/restart": map[string]string{"message": "restarted"},
		"GET /v1/self/status":   map[string]string{"output": "active (running)"},
		"GET /v1/self/logs":     map[string]string{"output": "log line\n"},
	})
	self, err := ctl.Self()
	require.NoError(t, err)
	assert.Equal(t, "blog", self.Name)
	assert.Equal(t, 10000, self.Port)
	msg, err := ctl.Ensure()
	require.NoError(t, err)
	assert.Equal(t, "workspace ready", msg)
	msg, err = ctl.Up()
	require.NoError(t, err)
	assert.Equal(t, "deployed", msg)
	msg, err = ctl.Down()
	require.NoError(t, err)
	assert.Equal(t, "stopped", msg)
	msg, err = ctl.Restart()
	require.NoError(t, err)
	assert.Equal(t, "restarted", msg)
	out, err := ctl.Status()
	require.NoError(t, err)
	assert.Contains(t, out, "active")
	out, err = ctl.Logs(50)
	require.NoError(t, err)
	assert.Contains(t, out, "log line")
}

func TestControllerErrorPropagation(t *testing.T) {
	t.Parallel()
	ctl := newTestController(t, map[string]any{}) // No routes: everything 404s with an error body
	_, err := ctl.Up()
	require.Error(t, err)
}

func TestControllerDaemonUnreachable(t *testing.T) {
	t.Parallel()
	ctl := NewController("/nonexistent/hostit.sock")
	_, err := ctl.Self()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "daemon")
}

func writeAndLoadConfig(t *testing.T, content string) *AppConfig {
	t.Helper()
	filename := filepath.Join(t.TempDir(), "hostit.yml")
	require.NoError(t, os.WriteFile(filename, []byte(content), 0600))
	c, err := LoadAppConfig(filename)
	require.NoError(t, err)
	return c
}

// newTestController starts a fake daemon on a unix socket serving canned JSON
func newTestController(t *testing.T, routes map[string]any) *Controller {
	t.Helper()
	socketFile := filepath.Join(t.TempDir(), "hostit.sock")
	listener, err := net.Listen("unix", socketFile)
	require.NoError(t, err)
	mux := http.NewServeMux()
	for pattern, response := range routes {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(response)
		})
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "no such endpoint"})
	})
	server := &http.Server{Handler: mux}
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = server.Close()
	})
	return NewController(socketFile)
}

func TestValidateRefusesVolumesThatLeaveTheApp(t *testing.T) {
	t.Parallel()
	// hostit.yml is written by the app's own owner (or their agent), and these
	// strings become arguments to root podman. An absolute source would mount
	// any host directory into the container -- with ":U" it would also hand its
	// ownership to the tenant.
	for _, volume := range []string{
		"/:/hostfs",
		"/etc:/hostetc",
		"/etc:/hostetc:U",
		"../../etc:/hostetc",
		"./data:/data:U",
		"data:/data:rshared",
		"data:/data:nonsense",
	} {
		c := &AppConfig{Image: "nginx", ContainerPort: 80, Volumes: []string{volume}}
		assert.Error(t, c.Validate(), "volume %q must be refused", volume)
	}
	// The useful shapes still work
	for _, volume := range []string{"data:/data", "./data:/data", "data:/data:ro", "data:/data:rw", "data:/data:z"} {
		c := &AppConfig{Image: "nginx", ContainerPort: 80, Volumes: []string{volume}}
		assert.NoError(t, c.Validate(), "volume %q must be allowed", volume)
	}
}

func TestValidateRefusesABuildContextOutsideTheApp(t *testing.T) {
	t.Parallel()
	for _, build := range []string{"/etc/hostit", "/", "../..", "../sibling"} {
		c := &AppConfig{Build: build, ContainerPort: 80}
		assert.Error(t, c.Validate(), "build %q must be refused", build)
	}
	for _, build := range []string{".", "./docker", "docker"} {
		c := &AppConfig{Build: build, ContainerPort: 80}
		assert.NoError(t, c.Validate(), "build %q must be allowed", build)
	}
}
