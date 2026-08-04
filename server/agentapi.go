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

	"heckel.io/hostit/app"
	"heckel.io/hostit/appctl"
	"heckel.io/hostit/config"
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

// newAgentRoutes registers the agent-facing API. It is deliberately separate
// from /v1 (which serves the web app and the CLI): everything here is shaped so
// that an AI agent handed one token and one URL can discover the rest.
func (s *Server) newAgentRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/info", s.requireActive(s.handleAgentInfo))
	mux.Handle("GET /api/{app}/info", s.requireApp(s.handleAgentAppInfo))
	mux.Handle("GET /api/{app}/logs", s.requireApp(s.handleAgentLogs))
	mux.Handle("GET /api/{app}/files", s.requireApp(s.handleAgentFileList))
	mux.Handle("PUT /api/{app}/files/{path...}", s.requireApp(s.handleAgentFilePut))
	mux.Handle("GET /api/{app}/files/{path...}", s.requireApp(s.handleAgentFileGet))
	mux.Handle("DELETE /api/{app}/files/{path...}", s.requireApp(s.handleAgentFileDelete))
	mux.Handle("POST /api/{app}/files", s.requireApp(s.handleAgentFileUpload))
	mux.Handle("PUT /api/{app}/readme", s.requireApp(s.handleAgentReadmePut))
	mux.Handle("POST /api/{app}/deploy", s.requireApp(s.handleAgentDeploy))
	mux.Handle("POST /api/{app}/start", s.requireApp(s.handleAgentStart))
	mux.Handle("POST /api/{app}/stop", s.requireApp(s.handleAgentStop))
	mux.Handle("POST /api/{app}/restart", s.requireApp(s.handleAgentRestart))
	mux.Handle("POST /api/{app}/run", s.requireApp(s.handleAgentRun))

	// Actions are POST-only. Without these, a GET would fall through to the web
	// app's catch-all and answer with HTML, which is confusing for an agent.
	for _, action := range []string{"deploy", "start", "stop", "restart", "run"} {
		mux.HandleFunc("GET /api/{app}/"+action, methodNotAllowed(action))
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

// agentGuide is the instruction set handed to agents. It is returned both by
// /api/info and inline by /api/{app}/info, because the prompt a user pastes
// points only at their app: whatever an agent needs must be reachable from
// that single URL.
func (s *Server) agentGuide(appName, description string) *apiAgentInfoResponse {
	base := "https://" + s.config.APIHostname()
	if s.config.TLS == config.TLSOff {
		base = "http://" + s.config.APIHostname()
	}
	name := appName
	if name == "" {
		name = "{app}"
	}
	// An app that describes itself is finished work someone came back to. Say so
	// first: an agent handed only a build-shaped prompt will otherwise start over
	// and overwrite it.
	whereYouAre := "You are looking at /api/" + name + "/info: it tells you what the app currently is. " +
		"Its README.md is the app's description and worklog. A new app is a stub serving a placeholder page."
	if description != "" {
		whereYouAre = "You are looking at /api/" + name + "/info. This app is already built and live: " +
			description + ". Do not rebuild it from scratch. Read its README.md (the app's worklog) and its " +
			"files first, then make only the changes you were asked for."
	}
	return &apiAgentInfoResponse{
		Platform: "hostit",
		BaseURL:  base + "/api",
		WhatIsThis: "hostit hosts small web apps. Each app is an isolated container with its own " +
			"subdomain and HTTPS certificate. You manage an app entirely through this API: upload " +
			"files, describe how to run it in hostit.yml, then deploy. Your token is limited to one " +
			"app unless it is an account token.",
		Workflow: []string{
			whereYouAre,
			"Upload files: PUT /api/" + name + "/files/{path} with the file body, or POST /api/" + name + "/files with a tar archive for many files at once. Put them where they belong (see layout below).",
			"Write hostit.yml (upload it like any other file) to say how the app runs. See hostit_yml below.",
			"POST /api/" + name + "/deploy to apply hostit.yml and (re)start the app.",
			"GET /api/" + name + "/logs if it does not come up; the app must listen on 0.0.0.0:$PORT.",
			"PUT /api/" + name + "/readme to record what this app is and what you changed, for whoever comes next.",
			"Keep the app's own documentation in " + appctl.DocsDir + "/ -- how it works, why it is built the way it is, anything the next session would otherwise have to re-derive. Read it before you change anything, and update it after every change that matters. README.md is the summary and worklog; " + appctl.DocsDir + "/ is the detail.",
			"Compiling or installing dependencies: POST /api/" + name + "/run with a shell command. It runs in the app's container, where the toolchains are, and returns the output and exit code -- so you can iterate on a build error without SSH. It is bounded (a minute by default, five at most): make the build a \"prepare:\" step in hostit.yml once it works, so it also runs on every deploy.",
			"Keep a one-line \"description:\" in hostit.yml saying what this app is. The owner's web page shows it, and the next session (or a different agent) starts from it instead of from a blank page.",
		},
		Layout: "The app's home directory has a place for each kind of thing:\n\n" +
			"  " + appctl.PublicDir + "/   files served on the web -- static mode serves exactly this directory\n" +
			"  " + appctl.BinDir + "/      binaries and scripts the app runs (run: ./" + appctl.BinDir + "/myapp)\n" +
			"  " + appctl.LogDir + "/      the app's output, written by hostit; read it with GET /logs\n" +
			"  " + appctl.SrcDir + "/      source, if you keep the app's source here\n" +
			"  " + appctl.DocsDir + "/     the app's own documentation -- how it works, why it is built that way\n\n" +
			"hostit.yml and README.md live at the top. Directories are created as you write into them.\n\n" +
			"If your app serves files itself, point it at " + appctl.PublicDir + "/ and never at the home directory: " +
			"the home also holds hostit.yml (which may carry env values) and .ssh/, and serving it puts them on the " +
			"open internet. For example: python3 -m http.server $PORT --bind 0.0.0.0 --directory " + appctl.PublicDir + "",
		HostitYml: "Two modes, pick one.\n\n" +
			"1. Static files (simplest, nothing to run):\n" +
			"     static: " + appctl.PublicDir + "   # always serves " + appctl.PublicDir + "/, whatever this says\n\n" +
			"2. Your own command, run in the workspace container:\n" +
			"     prepare: cd " + appctl.SrcDir + " && go build -o ../" + appctl.BinDir + "/myapp .   # optional build step\n" +
			"     run: ./" + appctl.BinDir + "/myapp   # MUST listen on 0.0.0.0:$PORT; $PORT is provided\n" +
			"     (upload binaries with ?mode=755 so they are executable)\n\n" +
			"Optional in both: env: {KEY: value}, and description: a one-liner about the app. " +
			"Unknown keys are an error, so a typo is reported rather than ignored.",
		Runtimes: app.WorkspaceRuntimes + ". Install anything else inside the container with apt-get; " +
			"a new app starts as a stub serving a placeholder page.",
		SuggestedStack: "A single Go binary that embeds its frontend (go:embed) is the easiest thing to run here: " +
			"one file, no runtime to install, instant start. Use run: ./" + appctl.BinDir + "/myapp listening on 0.0.0.0:$PORT. " +
			"Python, Node and PHP work equally well, and a plain HTML site needs only static:.\n\n" +
			"Prefer keeping the source here: upload it to " + appctl.SrcDir + "/ and build it in the container with a " +
			"\"prepare:\" step in hostit.yml, e.g. prepare: cd " + appctl.SrcDir + " && go build -o ../" + appctl.BinDir + "/myapp . " +
			"Prepare runs before the app starts, on every deploy; if it fails the app is not started and the error is in the logs. " +
			"That way the build happens on the machine that runs it (no cross-compiling, no toolchain needed on your side), " +
			"and the next session can still change the app. Uploading a prebuilt binary to " + appctl.BinDir + "/ also works and is " +
			"faster -- build it with CGO_ENABLED=0 GOOS=linux GOARCH=amd64 -- but then only the binary is here, and whoever " +
			"comes next has nothing to edit.",
		Auth: "Send the token as: Authorization: Bearer <token>",
		Endpoints: []apiAgentEndpoint{
			{Method: "GET", Path: "/api/" + name + "/info", What: "This document plus the app's URL, state, README, file list and hostit.yml"},
			{Method: "GET", Path: "/api/" + name + "/logs", What: "Recent output; ?lines=N"},
			{Method: "GET", Path: "/api/" + name + "/files", What: "List the app's files"},
			{Method: "GET", Path: "/api/" + name + "/files/{path}", What: "Read one file"},
			{Method: "PUT", Path: "/api/" + name + "/files/{path}", What: "Write one file (raw body); add ?mode=755 for something executable"},
			{Method: "DELETE", Path: "/api/" + name + "/files/{path}", What: "Delete one file"},
			{Method: "POST", Path: "/api/" + name + "/files", What: "Upload a tar archive (Content-Type: application/x-tar)"},
			{Method: "PUT", Path: "/api/" + name + "/readme", What: `Replace README.md: {"readme": "..."}`},
			{Method: "POST", Path: "/api/" + name + "/run", What: `Run one shell command in the app's container: {"command": "cd src && go build ./..."} -- returns its output and exit code`},
			{Method: "POST", Path: "/api/" + name + "/deploy", What: "Apply hostit.yml and (re)start"},
			{Method: "POST", Path: "/api/" + name + "/start", What: "Start the app"},
			{Method: "POST", Path: "/api/" + name + "/stop", What: "Stop the app"},
			{Method: "POST", Path: "/api/" + name + "/restart", What: "Restart the app"},
		},
		Notes: []string{
			"Apps also accept SSH: the owner's SSH keys work, and you can scp/rsync into the app's home directory.",
			"Changing image/build/env/volumes recreates the container; changing only static:/run: restarts the process.",
			"/run is bounded: a minute by default, five at most, and its output is capped. Anything longer belongs in \"prepare:\". A command you background (with & and its output redirected) keeps running after /run returns -- useful, but nothing will stop it except POST /restart, which replaces the container.",
			"Your app has 512 processes and its memory limit to work with, and the disk quota is shared with everything else in the app. A build that fans out past that fails rather than taking the host with it.",
			"Deleting or renaming apps is done by the owner in the web app, not through this API.",
		},
	}
}

func (s *Server) handleAgentAppInfo(w http.ResponseWriter, _ *http.Request, c *caller, a *store.App) {
	readme, err := s.apps.Readme(a.Name)
	if err != nil {
		writeAppError(w, err)
		return
	}
	files, err := s.apps.ListFiles(a.Name)
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
		OverQuota: a.OverQuota,
		Readme:    readme,
		HostitYml: string(hostitYml),
		Files:     files,
		SSH: apiSSHInfo{
			User:    a.Name,
			Host:    s.config.SSHHostname(),
			Command: "ssh " + a.Name + "@" + s.config.SSHHostname(),
		},
		Hint:  "Upload files, write hostit.yml, then POST /api/" + a.Name + "/deploy. Everything you need is in this response.",
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

func (s *Server) handleAgentLogs(w http.ResponseWriter, r *http.Request, c *caller, a *store.App) {
	out, err := s.apps.Logs(a.Name, logLines(r.URL.Query().Get("lines")))
	if err != nil {
		writeJSON(w, http.StatusOK, &apiOutputResponse{Output: "(no logs yet: " + err.Error() + ")"})
		return
	}
	writeJSON(w, http.StatusOK, &apiOutputResponse{Output: out})
}

func (s *Server) handleAgentFileList(w http.ResponseWriter, _ *http.Request, c *caller, a *store.App) {
	files, err := s.apps.ListFiles(a.Name)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, files)
}

func (s *Server) handleAgentFileGet(w http.ResponseWriter, r *http.Request, c *caller, a *store.App) {
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

func (s *Server) handleAgentStart(w http.ResponseWriter, _ *http.Request, c *caller, a *store.App) {
	msg, err := s.apps.Ensure(a.Name)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: msg})
}

func (s *Server) handleAgentStop(w http.ResponseWriter, _ *http.Request, c *caller, a *store.App) {
	if err := s.apps.Down(a.Name); err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "stopped"})
}

func (s *Server) handleAgentRestart(w http.ResponseWriter, _ *http.Request, c *caller, a *store.App) {
	if err := s.apps.Restart(a.Name); err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "restarted"})
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
