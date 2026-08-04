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

func TestConfigRefusesTheRemovedContainerMode(t *testing.T) {
	t.Parallel()
	// "image:" let an app bring its own runtime, which meant a second execution
	// model with none of the agent's guarantees. Old configs must fail loudly
	// rather than quietly serving nothing.
	_, err := ParseAppConfigStrict([]byte("image: docker.io/library/nginx:alpine\ncontainer-port: 80\n"))
	require.Error(t, err)
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
		{"static and run", "static: public\nrun: ./server", "either"},
		// The removed container mode, and anything else hostit does not know
		{"image", "image: nginx\ncontainer-port: 80", "image"},
		{"build", "build: .", "build"},
		{"volumes", "run: ./x\nvolumes:\n  - ./data:/data", "volumes"},
		{"typo", "statik: public", "statik"},
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

func TestStaticModeAlwaysServesPublic(t *testing.T) {
	t.Parallel()
	// One place for the files an app puts on the web, whatever the config says:
	// an agent that writes to public/ is always right
	for _, static := range []string{"public", ".", "site", "true"} {
		c := &AppConfig{Static: static}
		require.NoError(t, c.Validate())
		assert.Equal(t, ModeStatic, c.Mode())
		assert.Contains(t, c.Command("/usr/bin/hostit"), `--dir "public"`, "static: %q", static)
	}
}

func TestPrepareRunsBeforeTheApp(t *testing.T) {
	t.Parallel()
	// Without a build step, an agent driving the API can only compile by baking
	// the build into "run:", which then rebuilds on every restart and turns a
	// broken compile into a crash loop
	c := &AppConfig{Prepare: "go build -o bin/app ./src", Run: "./bin/app"}
	require.NoError(t, c.Validate())
	assert.Equal(t, ModeProcess, c.Mode())
	assert.Equal(t, "go build -o bin/app ./src", c.Prepare)
	assert.Equal(t, "./bin/app", c.Command("/usr/bin/hostit"))

	// It is a step, not a mode: on its own it says nothing about how to serve
	assert.Error(t, (&AppConfig{Prepare: "make"}).Validate())
	// And it works for static apps too (build a frontend into public/)
	assert.NoError(t, (&AppConfig{Prepare: "npm run build", Static: PublicDir}).Validate())
}
