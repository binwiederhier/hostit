package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"heckel.io/hostit/assistant"
	"heckel.io/hostit/store"
)

const (
	// agentLogLines is how much log an agent gets by default
	agentLogLines = 100
	// maxTarUpload caps a bulk upload
	maxTarUpload = 256 * 1024 * 1024
	// maxLogLines caps what "?lines=" may ask for
	maxLogLines = 10000
)

// newAgentRoutes registers the per-app API. Everything here lives under
// /api/apps/{app}/ because that prefix is exactly what an app-scoped token
// may reach: the shape of the URL is the shape of the permission.
func (s *Server) newAgentRoutes(mux *http.ServeMux) {
	route(mux, "GET", "/info", s.requireActive(s.handleAgentInfo))
	route(mux, "GET", "/apps/{app}/info", s.requireApp(s.handleAgentAppInfo))
	route(mux, "GET", "/apps/{app}/logs", s.requireApp(s.handleAgentLogs))
	route(mux, "GET", "/apps/{app}/assistant/transcript", s.requireApp(s.handleAgentAssistant))
	route(mux, "GET", "/apps/{app}/files", s.requireApp(s.handleAgentFileList))
	route(mux, "PUT", "/apps/{app}/files/{path...}", s.requireApp(s.handleAgentFilePut))
	route(mux, "GET", "/apps/{app}/files/{path...}", s.requireApp(s.handleAgentFileGet))
	route(mux, "DELETE", "/apps/{app}/files/{path...}", s.requireApp(s.handleAgentFileDelete))
	route(mux, "POST", "/apps/{app}/move", s.requireApp(s.handleAgentMove))
	route(mux, "POST", "/apps/{app}/mkdir", s.requireApp(s.handleAgentMkdir))
	route(mux, "POST", "/apps/{app}/files", s.requireApp(s.handleAgentFileUpload))
	route(mux, "PUT", "/apps/{app}/readme", s.requireApp(s.handleAgentReadmePut))
	route(mux, "POST", "/apps/{app}/deploy", s.requireApp(s.handleAgentDeploy))
	// The container ("power") verbs, and the app-process verbs, kept distinct
	route(mux, "POST", "/apps/{app}/poweron", s.requireApp(s.handleAgentPowerOn))
	route(mux, "POST", "/apps/{app}/poweroff", s.requireApp(s.handleAgentPowerOff))
	route(mux, "POST", "/apps/{app}/reboot", s.requireApp(s.handleAgentReboot))
	route(mux, "POST", "/apps/{app}/start", s.requireApp(s.handleAgentStart))
	route(mux, "POST", "/apps/{app}/stop", s.requireApp(s.handleAgentStop))
	route(mux, "POST", "/apps/{app}/restart", s.requireApp(s.handleAgentRestart))
	route(mux, "POST", "/apps/{app}/run", s.requireApp(s.handleAgentRun))
	route(mux, "GET", "/apps/{app}/snapshots", s.requireApp(s.handleAgentSnapshotList))
	route(mux, "POST", "/apps/{app}/snapshots", s.requireApp(s.handleAgentSnapshotTake))
	route(mux, "POST", "/apps/{app}/snapshots/{id}/restore", s.requireApp(s.handleAgentRestore))
	route(mux, "DELETE", "/apps/{app}/snapshots/{id}", s.requireApp(s.handleAgentSnapshotDelete))

	// Actions are POST-only. Without these, a GET would fall through to the web
	// app's catch-all and answer with HTML, which is confusing for an agent.
	for _, action := range []string{"deploy", "poweron", "poweroff", "reboot", "start", "stop", "restart", "run"} {
		route(mux, "GET", "/apps/{app}/"+action, methodNotAllowed(action))
	}
}

// methodNotAllowed explains that an action needs POST
func methodNotAllowed(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, errors.New("use POST for /"+action+", not GET"))
	}
}

// handleAgentInfo explains the platform to an agent that has never seen it
func (s *Server) handleAgentInfo(w http.ResponseWriter, _ *http.Request, c *caller) {
	writeJSON(w, http.StatusOK, s.agentGuide("", ""))
}

// appsPath is where one app's endpoints live, which is also exactly what its
// token may reach
func appsPath(name string) string {
	return apiPrefix + "/apps/" + name
}

func (s *Server) handleAgentAppInfo(w http.ResponseWriter, _ *http.Request, c *caller, a *store.App) {
	readme, err := s.apps.Readme(a.Name)
	if err != nil {
		writeAppError(w, err)
		return
	}
	files, err := s.apps.ListFiles(a.Name, "")
	if err != nil {
		writeAppError(w, err)
		return
	}
	hostitYml, _ := s.apps.ReadFile(a.Name, "hostit.yml") // Absent is fine; the agent writes one
	status, _ := s.apps.Status(a.Name)
	writeJSON(w, http.StatusOK, &apiAgentAppResponse{
		Name:      a.Name,
		URL:       s.apps.URL(a),
		Running:   strings.Contains(status, "active (running)"),
		DiskMB:    a.DiskMB,
		Readme:    readme,
		HostitYml: string(hostitYml),
		Files:     files,
		SSH: apiSSHInfo{
			User:    a.Name,
			Host:    s.config.SSHHostname(),
			Command: "ssh " + a.Name + "@" + s.config.SSHHostname(),
		},
		Hint:  "Upload files, write hostit.yml, then POST " + appsPath(a.Name) + "/deploy. Everything you need is in this response.",
		Guide: s.agentGuide(a.Name, s.apps.Description(a.Name)),
	})
}

// handleAgentRun runs one command inside the app's container and returns what
// it printed. It is how an agent compiles without SSH; see app.Exec for why it
// grants nothing an app token did not already have.
func (s *Server) handleAgentRun(w http.ResponseWriter, r *http.Request, c *caller, a *store.App) {
	var req apiRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	res, err := s.apps.Exec(a.Name, req.Command, time.Duration(req.TimeoutSeconds)*time.Second)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiRunResponse{
		Output:    res.Output,
		ExitCode:  res.ExitCode,
		Truncated: res.Truncated,
		TimedOut:  res.TimedOut,
	})
}

// handleAgentAssistant hands an external agent the built-in assistant's session
// for this app, rendered as markdown, so an agent the owner switches to picks up
// with the full history of what was already tried instead of starting cold. It
// answers cleanly (enabled: false) when the server has no assistant configured.
func (s *Server) handleAgentAssistant(w http.ResponseWriter, _ *http.Request, c *caller, a *store.App) {
	if s.assistant == nil {
		writeJSON(w, http.StatusOK, &apiAgentAssistantResponse{Enabled: false})
		return
	}
	items, err := s.assistant.Transcript(a.Name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiAgentAssistantResponse{
		Enabled:    true,
		Running:    s.assistant.Running(a.Name),
		Messages:   len(items),
		Transcript: assistant.RenderTranscript(items),
	})
}

func (s *Server) handleAgentLogs(w http.ResponseWriter, r *http.Request, c *caller, a *store.App) {
	out, err := s.apps.Logs(a.Name, logLines(r.URL.Query().Get("lines")))
	if err != nil {
		writeJSON(w, http.StatusOK, &apiOutputResponse{Output: "(no logs yet: " + err.Error() + ")"})
		return
	}
	writeJSON(w, http.StatusOK, &apiOutputResponse{Output: out})
}

// handleAgentFileList lists one directory, named by ?path= and defaulting to the
// app's root. It is not the whole tree: an app with dependencies installed would
// otherwise answer with tens of thousands of entries.
func (s *Server) handleAgentFileList(w http.ResponseWriter, r *http.Request, c *caller, a *store.App) {
	listing, err := s.apps.ListFiles(a.Name, r.URL.Query().Get("path"))
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listing)
}

func (s *Server) handleAgentFileGet(w http.ResponseWriter, r *http.Request, c *caller, a *store.App) {
	// ?stat=1 returns metadata (size, modtime, MIME) instead of the bytes, so the
	// editor can tell text from binary without downloading the whole file.
	if r.URL.Query().Has("stat") {
		info, err := s.apps.StatFile(a.Name, r.PathValue("path"))
		if err != nil {
			writeAppError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, info)
		return
	}
	b, err := s.apps.ReadFile(a.Name, r.PathValue("path"))
	if err != nil {
		writeAppError(w, err)
		return
	}
	// Served from the web app's own origin, and an admin may read any user's
	// files: never let one tenant's HTML run here. Downloading is the only thing
	// this endpoint is for.
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(path.Base(r.PathValue("path"))))
	_, _ = w.Write(b)
}

func (s *Server) handleAgentFilePut(w http.ResponseWriter, r *http.Request, c *caller, a *store.App) {
	mode, err := uploadMode(r.URL.Query().Get("mode"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	// Straight from the socket to disk: a body big enough to matter must never
	// be held in the daemon, which shares a small box with every app container
	relPath := r.PathValue("path")
	if err := s.apps.WriteFileFrom(a.Name, relPath, r.Body, mode); err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, &apiMessageResponse{Message: "wrote " + relPath})
}

// uploadMode parses an octal ?mode= such as 755, so a binary or script can be
// uploaded ready to run; empty means the default
func uploadMode(raw string) (os.FileMode, error) {
	if raw == "" {
		return 0, nil
	}
	parsed, err := strconv.ParseUint(raw, 8, 32)
	if err != nil || parsed > 0o777 {
		return 0, fmt.Errorf("invalid mode %q: use octal permissions such as 644 or 755", raw)
	}
	return os.FileMode(parsed), nil
}

// handleAgentMove renames or moves a file within the app's home (used by the web
// file browser to drag a file into a folder or rename it).
func (s *Server) handleAgentMove(w http.ResponseWriter, r *http.Request, c *caller, a *store.App) {
	var req apiMoveRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.From == "" || req.To == "" {
		writeError(w, http.StatusBadRequest, errors.New("both from and to are required"))
		return
	}
	if err := s.apps.MoveFile(a.Name, req.From, req.To); err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "moved " + req.From + " to " + req.To})
}

// handleAgentMkdir creates an empty directory in the app's home (used by the web
// file browser's "new folder" button).
func (s *Server) handleAgentMkdir(w http.ResponseWriter, r *http.Request, c *caller, a *store.App) {
	var req apiMkdirRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Path == "" {
		writeError(w, http.StatusBadRequest, errors.New("path is required"))
		return
	}
	if err := s.apps.MakeDir(a.Name, req.Path); err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "created " + req.Path})
}

func (s *Server) handleAgentFileDelete(w http.ResponseWriter, r *http.Request, c *caller, a *store.App) {
	if err := s.apps.DeleteFile(a.Name, r.PathValue("path")); err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "deleted " + r.PathValue("path")})
}

func (s *Server) handleAgentFileUpload(w http.ResponseWriter, r *http.Request, c *caller, a *store.App) {
	written, err := s.apps.ExtractTar(a.Name, io.LimitReader(r.Body, maxTarUpload))
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, &apiFilesWrittenResponse{Written: written})
}

func (s *Server) handleAgentReadmePut(w http.ResponseWriter, r *http.Request, c *caller, a *store.App) {
	var req apiReadmeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.apps.WriteReadme(a.Name, req.Readme); err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "README.md updated"})
}

func (s *Server) handleAgentDeploy(w http.ResponseWriter, _ *http.Request, c *caller, a *store.App) {
	msg, err := s.apps.Up(a.Name)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: msg + " -- " + s.apps.URL(a)})
}

// The "power" verbs act on the container (the machine): power it on, off, or
// reboot it. The "app" verbs act on the run: process inside a running container.
// Keeping them separate is the point -- restarting your app should not mean
// tearing the whole container down.

func (s *Server) handleAgentPowerOn(w http.ResponseWriter, _ *http.Request, c *caller, a *store.App) {
	msg, err := s.apps.Ensure(a.Name)
	if err != nil {
		writeAppError(w, err)
		return
	}
	s.logAction(c, a.Name, "poweron", "Powered on")
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: msg})
}

func (s *Server) handleAgentPowerOff(w http.ResponseWriter, _ *http.Request, c *caller, a *store.App) {
	if err := s.apps.Down(a.Name); err != nil {
		writeAppError(w, err)
		return
	}
	s.logAction(c, a.Name, "poweroff", "Powered off")
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "powered off"})
}

func (s *Server) handleAgentReboot(w http.ResponseWriter, _ *http.Request, c *caller, a *store.App) {
	if err := s.apps.Restart(a.Name); err != nil {
		writeAppError(w, err)
		return
	}
	s.logAction(c, a.Name, "reboot", "Rebooted")
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "rebooted"})
}

func (s *Server) handleAgentStart(w http.ResponseWriter, _ *http.Request, c *caller, a *store.App) {
	if err := s.apps.StartApp(a.Name); err != nil {
		writeAppError(w, err)
		return
	}
	s.logAction(c, a.Name, "start", "Started the app")
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "app started"})
}

func (s *Server) handleAgentStop(w http.ResponseWriter, _ *http.Request, c *caller, a *store.App) {
	if err := s.apps.StopApp(a.Name); err != nil {
		writeAppError(w, err)
		return
	}
	s.logAction(c, a.Name, "stop", "Stopped the app")
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "app stopped"})
}

func (s *Server) handleAgentRestart(w http.ResponseWriter, _ *http.Request, c *caller, a *store.App) {
	if err := s.apps.RestartApp(a.Name); err != nil {
		writeAppError(w, err)
		return
	}
	s.logAction(c, a.Name, "restart", "Restarted the app")
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "app restarted"})
}

// requireApp resolves {app} and enforces both ownership and token scope: an
// app-scoped token may only ever act on the app it was minted for
func (s *Server) requireApp(next func(http.ResponseWriter, *http.Request, *caller, *store.App)) http.HandlerFunc {
	return s.requireActive(func(w http.ResponseWriter, r *http.Request, c *caller) {
		name := r.PathValue("app")
		if c.appScope != "" && c.appScope != name {
			writeError(w, http.StatusForbidden, errors.New("this token is limited to the app "+c.appScope))
			return
		}
		a, err := s.ownedApp(c, name)
		if err != nil {
			writeAppError(w, err)
			return
		}
		next(w, r, c, a)
	})
}

// logLines turns the "?lines=" parameter into a bounded line count; the value
// reaches "podman logs --tail" and a tail of the log file, so an absurd number
// would be an absurd allocation
func logLines(raw string) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return agentLogLines
	}
	return min(n, maxLogLines)
}
