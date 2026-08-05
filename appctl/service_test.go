package appctl

import (
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadAppConfigAppMode(t *testing.T) {
	t.Parallel()
	c := writeAndLoadConfig(t, `
mode: app
run: ./server --debug
env:
  FOO: bar
`)
	assert.Equal(t, ModeApp, c.Mode)
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
	c := writeAndLoadConfig(t, "mode: static\n")
	assert.Equal(t, ModeStatic, c.Mode)
	// There is nothing else to say: a static app serves public/
	assert.Contains(t, c.Command("/usr/bin/hostit"), `--dir "public"`)
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
		{"no mode", "run: ./server", "mode: static"},
		{"static with a run command", "mode: static\nrun: ./server", "only applies"},
		{"app without a run command", "mode: app", "needs a"},
		{"unknown mode", "mode: nonsense", "unknown mode"},
		// The removed container mode, and anything else hostit does not know
		{"image", "image: nginx\ncontainer-port: 80", "image"},
		{"build", "build: .", "build"},
		{"volumes", "run: ./x\nvolumes:\n  - ./data:/data", "volumes"},
		{"typo", "statik: public", "statik"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := LoadAppConfig([]byte(tt.yaml))
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
	c, err := LoadAppConfig([]byte(content))
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
	// A static app serves exactly public/, so there is no directory to configure
	c := &AppConfig{Mode: ModeStatic}
	require.NoError(t, c.Validate())
	assert.Contains(t, c.Command("/usr/bin/hostit"), `--dir "public"`)

	// The old "static: <dir>" spelling carried a directory value; it is now an
	// unknown key whatever it points at, so a stale config is reported rather
	// than quietly served from the wrong place.
	for _, value := range []string{"public", ".", "site", "true"} {
		_, err := ParseAppConfigStrict([]byte("static: " + value + "\n"))
		assert.Error(t, err, "static: %q must be rejected", value)
	}
}

func TestPrepareRunsBeforeTheApp(t *testing.T) {
	t.Parallel()
	// Without a build step, an agent driving the API can only compile by baking
	// the build into "run:", which then rebuilds on every restart and turns a
	// broken compile into a crash loop
	c := &AppConfig{Mode: ModeApp, Prepare: "go build -o bin/app ./src", Run: "./bin/app"}
	require.NoError(t, c.Validate())
	assert.Equal(t, ModeApp, c.Mode)
	assert.Equal(t, "go build -o bin/app ./src", c.Prepare)
	assert.Equal(t, "./bin/app", c.Command("/usr/bin/hostit"))

	// It is a step, not a mode: on its own it says nothing about how to serve
	assert.Error(t, (&AppConfig{Prepare: "make"}).Validate())
	// And it works for static apps too (build a frontend into public/)
	assert.NoError(t, (&AppConfig{Mode: ModeStatic, Prepare: "npm run build"}).Validate())
}

func TestModeIsExplicit(t *testing.T) {
	t.Parallel()
	// "mode: static" read as though the value mattered, when the directory is
	// always public/. The mode is a choice between two things, so it says so.
	c, err := ParseAppConfigStrict([]byte("mode: static\n"))
	require.NoError(t, err)
	require.NoError(t, c.Validate())
	assert.Equal(t, ModeStatic, c.Mode)
	assert.Contains(t, c.Command("/usr/bin/hostit"), `--dir "public"`)

	c, err = ParseAppConfigStrict([]byte("mode: app\nrun: ./bin/server\n"))
	require.NoError(t, err)
	require.NoError(t, c.Validate())
	assert.Equal(t, ModeApp, c.Mode)
	assert.Equal(t, "./bin/server", c.Command("/usr/bin/hostit"))

	for _, yaml := range []string{
		"mode: app\n",                       // nothing to run
		"mode: static\nrun: ./bin/server\n", // run: means nothing here
		"run: ./bin/server\n",               // no mode at all
		"mode: nonsense\n",
		"static: public\n", // the old spelling
	} {
		c, err := ParseAppConfigStrict([]byte(yaml))
		if err != nil {
			continue // strict parsing already rejected it
		}
		assert.Error(t, c.Validate(), "config %q must be refused", yaml)
	}
}
