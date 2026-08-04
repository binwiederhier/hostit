package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"heckel.io/hostit/app"
	"heckel.io/hostit/store"
)

const (
	// agentLogLines is how much log an agent gets by default
	agentLogLines = 100
	// maxTarUpload caps a bulk upload
	maxTarUpload = 256 * 1024 * 1024
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

	// Actions are POST-only. Without these, a GET would fall through to the web
	// app's catch-all and answer with HTML, which is confusing for an agent.
	for _, action := range []string{"deploy", "start", "stop", "restart"} {
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
	writeJSON(w, http.StatusOK, s.agentGuide(""))
}

// agentGuide is the instruction set handed to agents. It is returned both by
// /api/info and inline by /api/{app}/info, because the prompt a user pastes
// points only at their app: whatever an agent needs must be reachable from
// that single URL.
func (s *Server) agentGuide(appName string) *apiAgentInfoResponse {
	base := "https://" + s.config.APIHostname()
	if s.config.TLS == "off" {
		base = "http://" + s.config.APIHostname()
	}
	name := appName
	if name == "" {
		name = "{app}"
	}
	return &apiAgentInfoResponse{
		Platform: "hostit",
		BaseURL:  base + "/api",
		WhatIsThis: "hostit hosts small web apps. Each app is an isolated container with its own " +
			"subdomain and HTTPS certificate. You manage an app entirely through this API: upload " +
			"files, describe how to run it in hostit.yml, then deploy. Your token is limited to one " +
			"app unless it is an account token.",
		Workflow: []string{
			"You are looking at /api/" + name + "/info: it tells you what the app currently is. Its README.md is the app's description and worklog. A new app is a stub serving a placeholder page.",
			"Upload files: PUT /api/" + name + "/files/{path} with the file body, or POST /api/" + name + "/files with a tar archive for many files at once.",
			"Write hostit.yml (upload it like any other file) to say how the app runs. See hostit_yml below.",
			"POST /api/" + name + "/deploy to apply hostit.yml and (re)start the app.",
			"GET /api/" + name + "/logs if it does not come up; the app must listen on 0.0.0.0:$PORT.",
			"PUT /api/" + name + "/readme to record what this app is and what you changed, for whoever comes next.",
		},
		HostitYml: "Three modes, pick one.\n\n" +
			"1. Static files (simplest, nothing to run):\n" +
			"     static: .          # or a subdirectory such as: static: public\n\n" +
			"2. Your own command, run in the workspace container:\n" +
			"     run: ./myapp       # MUST listen on 0.0.0.0:$PORT; $PORT is provided\n\n" +
			"3. Your own container image:\n" +
			"     image: docker.io/library/nginx:alpine   # or: build: .\n" +
			"     container-port: 80\n\n" +
			"Optional everywhere: env: {KEY: value}. Image mode also takes volumes: [./data:/data].",
		Runtimes: app.WorkspaceRuntimes + ". Install anything else inside the container with apt-get; " +
			"a new app starts as a stub serving a placeholder page.",
		SuggestedStack: "A single Go binary that embeds its frontend (go:embed) is the easiest thing to run here: " +
			"one file to upload, no runtime to install, instant start. Use run: ./myapp listening on 0.0.0.0:$PORT. " +
			"Python, Node and PHP work equally well, and a plain HTML site needs only static:.",
		Auth: "Send the token as: Authorization: Bearer <token>",
		Endpoints: []apiAgentEndpoint{
			{Method: "GET", Path: "/api/" + name + "/info", What: "This document plus the app's URL, state, README, file list and hostit.yml"},
			{Method: "GET", Path: "/api/" + name + "/logs", What: "Recent output; ?lines=N"},
			{Method: "GET", Path: "/api/" + name + "/files", What: "List the app's files"},
			{Method: "GET", Path: "/api/" + name + "/files/{path}", What: "Read one file"},
			{Method: "PUT", Path: "/api/" + name + "/files/{path}", What: "Write one file (raw body)"},
			{Method: "DELETE", Path: "/api/" + name + "/files/{path}", What: "Delete one file"},
			{Method: "POST", Path: "/api/" + name + "/files", What: "Upload a tar archive (Content-Type: application/x-tar)"},
			{Method: "PUT", Path: "/api/" + name + "/readme", What: `Replace README.md: {"readme": "..."}`},
			{Method: "POST", Path: "/api/" + name + "/deploy", What: "Apply hostit.yml and (re)start"},
			{Method: "POST", Path: "/api/" + name + "/start", What: "Start the app"},
			{Method: "POST", Path: "/api/" + name + "/stop", What: "Stop the app"},
			{Method: "POST", Path: "/api/" + name + "/restart", What: "Restart the app"},
		},
		Notes: []string{
			"Apps also accept SSH: the owner's SSH keys work, and you can scp/rsync into the app's home directory.",
			"Changing image/build/env/volumes recreates the container; changing only static:/run: restarts the process.",
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
		Guide: s.agentGuide(a.Name),
	})
}

func (s *Server) handleAgentLogs(w http.ResponseWriter, r *http.Request, c *caller, a *store.App) {
	lines := agentLogLines
	if v := r.URL.Query().Get("lines"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			lines = n
		}
	}
	out, err := s.apps.Logs(a.Name, lines)
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
	w.Header().Set("Content-Type", contentTypeFor(r.PathValue("path")))
	_, _ = w.Write(b)
}

func (s *Server) handleAgentFilePut(w http.ResponseWriter, r *http.Request, c *caller, a *store.App) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxTarUpload))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	path := r.PathValue("path")
	if err := s.apps.WriteFile(a.Name, path, body); err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, &apiMessageResponse{Message: "wrote " + path})
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

// contentTypeFor keeps file reads honest without pulling in a MIME database
func contentTypeFor(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".json":
		return "application/json"
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg":
		return "application/octet-stream"
	default:
		return "text/plain; charset=utf-8"
	}
}

var _ = app.ErrInvalid // Keep the error mapping in writeAppError meaningful here
