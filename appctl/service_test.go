package appctl

import (
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/app"
)

func TestLoadAppConfigAppMode(t *testing.T) {
	t.Parallel()
	c := writeAndLoadConfig(t, `
mode: app
run: ./server --debug
env:
  FOO: bar
`)
	assert.Equal(t, app.ModeApp, c.Mode)
	assert.Equal(t, "./server --debug", c.Run)
	assert.Equal(t, "bar", c.Env["FOO"])
}

func TestConfigRefusesTheRemovedContainerMode(t *testing.T) {
	t.Parallel()
	// "image:" let an app bring its own runtime, which meant a second execution
	// model with none of the agent's guarantees. Old configs must fail loudly
	// rather than quietly serving nothing.
	_, err := app.ParseConfigStrict([]byte("image: docker.io/library/nginx:alpine\ncontainer-port: 80\n"))
	require.Error(t, err)
}

func TestLoadAppConfigStaticMode(t *testing.T) {
	t.Parallel()
	c := writeAndLoadConfig(t, "mode: static\n")
	assert.Equal(t, app.ModeStatic, c.Mode)
	// There is nothing else to say: a static app serves public/
	assert.Equal(t, "/usr/bin/hostit static", c.Command("/usr/bin/hostit"))
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
			_, err := app.LoadConfig([]byte(tt.yaml))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestParseAppConfigLenient(t *testing.T) {
	t.Parallel()
	c, err := app.ParseConfig([]byte("# nothing"))
	require.NoError(t, err)
	assert.Empty(t, c.Run)
}

func TestControllerLifecycle(t *testing.T) {
	t.Parallel()
	ctl := newTestController(t, map[string]any{
		"GET /v1/self":           &SelfInfo{Name: "blog", Port: 10000, URL: "https://blog.apps.example.com"},
		"POST /v1/self/ensure":   map[string]string{"message": "workspace ready"},
		"POST /v1/self/deploy":   map[string]string{"message": "deployed"},
		"POST /v1/self/start":    map[string]string{"message": "app started"},
		"POST /v1/self/stop":     map[string]string{"message": "app stopped"},
		"POST /v1/self/restart":  map[string]string{"message": "app restarted"},
		"POST /v1/self/poweron":  map[string]string{"message": "workspace started"},
		"POST /v1/self/poweroff": map[string]string{"message": "powered off"},
		"POST /v1/self/reboot":   map[string]string{"message": "rebooting"},
		"GET /v1/self/status":    map[string]string{"output": "active (running)"},
		"GET /v1/self/logs":      map[string]string{"output": "log line\n"},
	})
	self, err := ctl.Self()
	require.NoError(t, err)
	assert.Equal(t, "blog", self.Name)
	assert.Equal(t, 10000, self.Port)
	msg, err := ctl.Deploy()
	require.NoError(t, err)
	assert.Equal(t, "deployed", msg)
	for _, tc := range []struct {
		call func() (string, error)
		want string
	}{
		{ctl.Start, "app started"},
		{ctl.Stop, "app stopped"},
		{ctl.Restart, "app restarted"},
		{ctl.PowerOn, "workspace started"},
		{ctl.PowerOff, "powered off"},
		{ctl.Reboot, "rebooting"},
	} {
		msg, err := tc.call()
		require.NoError(t, err)
		assert.Equal(t, tc.want, msg)
	}
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
	_, err := ctl.Deploy()
	require.Error(t, err)
}

func TestControllerDaemonUnreachable(t *testing.T) {
	t.Parallel()
	ctl := NewController("/nonexistent/hostit.sock")
	_, err := ctl.Self()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "daemon")
}

func writeAndLoadConfig(t *testing.T, content string) *app.Config {
	t.Helper()
	c, err := app.LoadConfig([]byte(content))
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
	c := &app.Config{Mode: app.ModeStatic}
	require.NoError(t, c.Validate())
	assert.Equal(t, "/usr/bin/hostit static", c.Command("/usr/bin/hostit"))

	// The old "static: <dir>" spelling carried a directory value; it is now an
	// unknown key whatever it points at, so a stale config is reported rather
	// than quietly served from the wrong place.
	for _, value := range []string{"public", ".", "site", "true"} {
		_, err := app.ParseConfigStrict([]byte("static: " + value + "\n"))
		assert.Error(t, err, "static: %q must be rejected", value)
	}
}

func TestPrepareRunsBeforeTheApp(t *testing.T) {
	t.Parallel()
	// Without a build step, an agent driving the API can only compile by baking
	// the build into "run:", which then rebuilds on every restart and turns a
	// broken compile into a crash loop
	c := &app.Config{Mode: app.ModeApp, Prepare: "go build -o bin/app ./src", Run: "./bin/app"}
	require.NoError(t, c.Validate())
	assert.Equal(t, app.ModeApp, c.Mode)
	assert.Equal(t, "go build -o bin/app ./src", c.Prepare)
	assert.Equal(t, "./bin/app", c.Command("/usr/bin/hostit"))

	// It is a step, not a mode: on its own it says nothing about how to serve
	assert.Error(t, (&app.Config{Prepare: "make"}).Validate())
	// And it works for static apps too (build a frontend into public/)
	assert.NoError(t, (&app.Config{Mode: app.ModeStatic, Prepare: "npm run build"}).Validate())
}

func TestModeIsExplicit(t *testing.T) {
	t.Parallel()
	// "mode: static" read as though the value mattered, when the directory is
	// always public/. The mode is a choice between two things, so it says so.
	c, err := app.ParseConfigStrict([]byte("mode: static\n"))
	require.NoError(t, err)
	require.NoError(t, c.Validate())
	assert.Equal(t, app.ModeStatic, c.Mode)
	assert.Equal(t, "/usr/bin/hostit static", c.Command("/usr/bin/hostit"))

	c, err = app.ParseConfigStrict([]byte("mode: app\nrun: ./bin/server\n"))
	require.NoError(t, err)
	require.NoError(t, c.Validate())
	assert.Equal(t, app.ModeApp, c.Mode)
	assert.Equal(t, "./bin/server", c.Command("/usr/bin/hostit"))

	for _, yaml := range []string{
		"mode: app\n",                       // nothing to run
		"mode: static\nrun: ./bin/server\n", // run: means nothing here
		"run: ./bin/server\n",               // no mode at all
		"mode: nonsense\n",
		"static: public\n", // the old spelling
	} {
		c, err := app.ParseConfigStrict([]byte(yaml))
		if err != nil {
			continue // strict parsing already rejected it
		}
		assert.Error(t, c.Validate(), "config %q must be refused", yaml)
	}
}
