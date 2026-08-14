package server

import (
	"io"
	"net/http"
	"strconv"

	"heckel.io/hostit/assistant"
	"heckel.io/hostit/store"
)

const (
	// defaultLogLines is returned by /v1/self/logs unless ?lines= is given
	defaultLogLines = 100
	// maxSelfToolBody caps a tool call's JSON arguments. write_file carries file
	// content, so it is generous, but a caller must not turn this into an
	// unbounded allocation on the daemon that shares a box with every app.
	maxSelfToolBody = 16 * 1024 * 1024
)

// The unix-socket "self" API handlers: an app (or the sandboxed assistant)
// acting on itself, scoped by the connection's peer UID. Router and peercred
// plumbing live in socket.go.

// handleSelfTool executes one assistant tool call against the calling app and
// returns its model-facing output. It shares DispatchTool with the built-in API
// loop, so a tool behaves identically whichever backend drives it; the socket's
// peer UID (SO_PEERCRED) is what scopes the call to this one app. This is the
// mediation boundary for the Claude Max backend: the sandbox container holds no
// app home and no podman -- every change it makes flows through here.
func (s *Server) handleSelfTool(w http.ResponseWriter, r *http.Request, a *store.App) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxSelfToolBody))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(body) == 0 {
		body = []byte("{}") // a no-argument tool (deploy, list_snapshots) sends nothing
	}
	// The changed hook feeds the debounced dashboard screenshot, same as when
	// the built-in API loop drives these tools.
	output, isErr := assistant.DispatchTool(&appOps{apps: s.apps, changed: s.assistantChanged}, a.Name, r.PathValue("name"), body)
	writeJSON(w, http.StatusOK, &apiToolResponse{Output: output, IsError: isErr})
}

// handleSelf tells the calling app who it is; this is how the CLI learns its
// name, port and URL without any token
func (s *Server) handleSelf(w http.ResponseWriter, r *http.Request, a *store.App) {
	writeJSON(w, http.StatusOK, s.appResponse(a, s.firstActiveDomain(a.Name)))
}

func (s *Server) handleSelfEnsure(w http.ResponseWriter, r *http.Request, a *store.App) {
	msg, err := s.apps.Ensure(a.Name)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: msg})
}

// handleSelfPowerOn is the explicit power-on verb: unlike Ensure (the login path)
// it clears a prior poweroff rather than refusing a powered-off app.
func (s *Server) handleSelfPowerOn(w http.ResponseWriter, r *http.Request, a *store.App) {
	msg, err := s.apps.PowerOn(a.Name)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: msg})
}

// handleSelfDeploy applies hostit.yml and (re)starts the app
func (s *Server) handleSelfDeploy(w http.ResponseWriter, r *http.Request, a *store.App) {
	msg, err := s.apps.Up(a.Name)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: msg})
}

// The app-process verbs: they move the run: command, leaving the container up.
func (s *Server) handleSelfStart(w http.ResponseWriter, r *http.Request, a *store.App) {
	s.selfAction(w, s.apps.StartApp(a.Name), "app started")
}

func (s *Server) handleSelfStop(w http.ResponseWriter, r *http.Request, a *store.App) {
	s.selfAction(w, s.apps.StopApp(a.Name), "app stopped")
}

func (s *Server) handleSelfRestart(w http.ResponseWriter, r *http.Request, a *store.App) {
	s.selfAction(w, s.apps.RestartApp(a.Name), "app restarted")
}

// The container verbs: they move the whole app container.
func (s *Server) handleSelfPowerOff(w http.ResponseWriter, r *http.Request, a *store.App) {
	s.selfAction(w, s.apps.Down(a.Name), "powered off")
}

func (s *Server) handleSelfReboot(w http.ResponseWriter, r *http.Request, a *store.App) {
	s.selfAction(w, s.apps.Restart(a.Name), "rebooting")
}

// selfAction writes a lifecycle result: the error, or a success message
func (s *Server) selfAction(w http.ResponseWriter, err error, ok string) {
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: ok})
}

func (s *Server) handleSelfStatus(w http.ResponseWriter, r *http.Request, a *store.App) {
	out, err := s.apps.Status(a.Name)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiOutputResponse{Output: out})
}

func (s *Server) handleSelfLogs(w http.ResponseWriter, r *http.Request, a *store.App) {
	lines := defaultLogLines
	if v := r.URL.Query().Get("lines"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			lines = n
		}
	}
	out, err := s.apps.Logs(a.Name, lines)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiOutputResponse{Output: out})
}
