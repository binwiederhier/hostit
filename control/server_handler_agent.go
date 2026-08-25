package control

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"heckel.io/hostit/app"
	"heckel.io/hostit/assistant"
	"heckel.io/hostit/controlconf"
	"heckel.io/hostit/store"
	"heckel.io/hostit/workspace"
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
	route(mux, "GET", "/apps/{app}/export", s.requireApp(s.handleAppExport))
	route(mux, "GET", "/apps/{app}/snapshots/{id}/export", s.requireApp(s.handleSnapshotExport))
	route(mux, "DELETE", "/apps/{app}/files/{path...}", s.requireApp(s.handleAgentFileDelete))
	route(mux, "POST", "/apps/{app}/move", s.requireApp(s.handleAgentMove))
	route(mux, "POST", "/apps/{app}/mkdir", s.requireApp(s.handleAgentMkdir))
	route(mux, "POST", "/apps/{app}/files", s.requireApp(s.handleAgentFileUpload))
	route(mux, "PUT", "/apps/{app}/readme", s.requireApp(s.handleAgentReadmePut))
	route(mux, "POST", "/apps/{app}/deploy", s.requireApp(s.handleAgentDeploy))
	// The container ("power") verbs, and the app-process verbs, kept distinct
	route(mux, "POST", "/apps/{app}/poweron", s.requireApp(s.handleAgentPowerOn))
	route(mux, "POST", "/apps/{app}/poweroff", s.requireApp(s.handleAgentPowerOff))
	// Archiving is the app's own state, so an app-scoped token may set it. It is
	// reversible from the same token: an archived app keeps answering the file
	// and info endpoints, so nothing can strand itself here.
	route(mux, "POST", "/apps/{app}/archive", s.requireApp(s.handleAgentArchive))
	route(mux, "POST", "/apps/{app}/unarchive", s.requireApp(s.handleAgentUnarchive))
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
	files, err := s.node.ListFiles(a.Name, "")
	if err != nil {
		writeAppError(w, err)
		return
	}
	hostitYml, _ := s.node.ReadFile(a.Name, "hostit.yml") // Absent is fine; the agent writes one
	status, _ := s.node.Status(a.Name)
	memoryMB, diskMB, cpuMilli := s.appLimits(a.Name)
	writeJSON(w, http.StatusOK, &apiAgentAppResponse{
		Name:      a.Name,
		URL:       s.apps.URL(a),
		Running:   strings.Contains(status, "active (running)"),
		DiskMB:    a.DiskMB,
		Limits:    apiAgentLimits{MemoryMB: memoryMB, DiskMB: diskMB, CPUMilli: cpuMilli},
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
	res, err := s.node.Exec(a.Name, req.Command, time.Duration(req.TimeoutSeconds)*time.Second)
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
	out, err := s.node.Logs(a.Name, logLines(r.URL.Query().Get("lines")))
	if err != nil {
		writeJSON(w, http.StatusOK, &apiOutputResponse{Output: "(no logs yet: " + err.Error() + ")"})
		return
	}
	writeJSON(w, http.StatusOK, &apiOutputResponse{Output: out})
}

func (s *Server) handleAgentDeploy(w http.ResponseWriter, _ *http.Request, c *caller, a *store.App) {
	msg, err := s.node.Up(a.Name)
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
	msg, err := s.node.PowerOn(a.Name)
	if err != nil {
		writeAppError(w, err)
		return
	}
	s.logAction(c, a.Name, "poweron", "Powered on")
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: msg})
}

func (s *Server) handleAgentPowerOff(w http.ResponseWriter, _ *http.Request, c *caller, a *store.App) {
	if err := s.node.Down(a.Name); err != nil {
		writeAppError(w, err)
		return
	}
	s.logAction(c, a.Name, "poweroff", "Powered off")
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "powered off"})
}

func (s *Server) handleAgentReboot(w http.ResponseWriter, _ *http.Request, c *caller, a *store.App) {
	if err := s.node.Restart(a.Name); err != nil {
		writeAppError(w, err)
		return
	}
	s.logAction(c, a.Name, "reboot", "Rebooted")
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "rebooted"})
}

func (s *Server) handleAgentStart(w http.ResponseWriter, _ *http.Request, c *caller, a *store.App) {
	if err := s.node.StartApp(a.Name); err != nil {
		writeAppError(w, err)
		return
	}
	s.logAction(c, a.Name, "start", "Started the app")
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "app started"})
}

func (s *Server) handleAgentStop(w http.ResponseWriter, _ *http.Request, c *caller, a *store.App) {
	if err := s.node.StopApp(a.Name); err != nil {
		writeAppError(w, err)
		return
	}
	s.logAction(c, a.Name, "stop", "Stopped the app")
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "app stopped"})
}

func (s *Server) handleAgentRestart(w http.ResponseWriter, _ *http.Request, c *caller, a *store.App) {
	if err := s.node.RestartApp(a.Name); err != nil {
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
// sizes a tail of the agent log file, so an absurd number would be an absurd
// allocation
func logLines(raw string) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return agentLogLines
	}
	return min(n, maxLogLines)
}

// agentGuide is the instruction set handed to agents. It is returned both by
// /api/info and inline by /api/apps/{app}/info, because the prompt a user pastes
// points only at their app: whatever an agent needs must be reachable from
// that single URL.
func (s *Server) agentGuide(appName, description string) *apiAgentInfoResponse {
	base := "https://" + s.config.APIHostname()
	if s.config.TLS == controlconf.TLSOff {
		base = "http://" + s.config.APIHostname()
	}
	name := appName
	if name == "" {
		name = "{app}"
	}
	// An app that describes itself is finished work someone came back to. Say so
	// first: an agent handed only a build-shaped prompt will otherwise start over
	// and overwrite it.
	whereYouAre := "You are looking at " + appsPath(name) + "/info: it tells you what the app currently is. " +
		"Its README.md is the app's description and worklog. A new app is a stub serving a placeholder page."
	if description != "" {
		whereYouAre = "You are looking at " + appsPath(name) + "/info. This app is already built and live: " +
			description + ". Do not rebuild it from scratch. Read its README.md (the app's worklog) and its " +
			"files first, then make only the changes you were asked for."
	}
	// An archived app refuses most of what follows, so say it before the workflow
	// rather than letting the agent discover it one 409 at a time.
	if s.apps.archived(name) {
		whereYouAre = "This app is ARCHIVED: it is powered off and refuses to run. Deploy, power on, run and " +
			"snapshot are all rejected, and its subdomain serves nothing; you can still read and write its " +
			"files. POST " + appsPath(name) + "/unarchive brings it back (it stays powered off; power it on " +
			"after). Everything below applies once it is unarchived. " + whereYouAre
	}
	// The owner may have built this app with hostit's own assistant before switching
	// to you. Reading that session first is the difference between continuing their
	// work and starting over.
	readSession := "Read the built-in assistant's session for this app first: GET " + appsPath(name) +
		"/assistant/transcript. If the owner already worked on this app with hostit's assistant, that transcript " +
		"is the history of what was asked, tried and decided -- pick up from it rather than starting cold. It is " +
		"empty (or {\"enabled\":false}) if there is nothing to read; then rely on README.md and the files."
	return &apiAgentInfoResponse{
		Platform: "hostit",
		BaseURL:  base + apiPrefix,
		WhatIsThis: "hostit hosts small web apps. Each app is an isolated container with its own " +
			"subdomain and HTTPS certificate. You manage an app entirely through this API: upload " +
			"files, describe how to run it in hostit.yml, then deploy. Your token is limited to one " +
			"app unless it is an account token.",
		Workflow: []string{
			whereYouAre,
			readSession,
			"Upload files: PUT " + appsPath(name) + "/files/{path} with the file body, or POST " + appsPath(name) + "/files with a tar archive for many files at once. Put them where they belong (see layout below).",
			"Write hostit.yml (upload it like any other file) to say how the app runs. See hostit_yml below.",
			"POST " + appsPath(name) + "/deploy to apply hostit.yml and (re)start the app.",
			"GET " + appsPath(name) + "/logs if it does not come up; the app must listen on 0.0.0.0:$PORT.",
			"PUT " + appsPath(name) + "/readme to record what this app is and what you changed, for whoever comes next.",
			"Keep the app's own documentation in " + app.DocsDir + "/ -- how it works, why it is built the way it is, anything the next session would otherwise have to re-derive. Read it before you change anything, and update it after every change that matters. README.md is the summary and worklog; " + app.DocsDir + "/ is the detail.",
			"Compiling or installing dependencies: POST " + appsPath(name) + "/run with a shell command. It runs in the app's container, where the toolchains are, and returns the output and exit code -- so you can iterate on a build error without SSH. It is bounded (a minute by default, five at most): make the build a \"prepare:\" step in hostit.yml once it works, so it also runs on every deploy.",
			"Keep a one-line \"description:\" in hostit.yml saying what this app is. The owner's web page shows it, and the next session (or a different agent) starts from it instead of from a blank page.",
			"Connected accounts: if the owner has connected Google, GitHub or an IMAP mailbox and granted it to this app, GET /api/container/connections over the app's unix socket lists them and GET /api/container/connections/{provider}/token returns a usable, short-lived credential. Ask for it per request and do not store it -- it expires. The app acts as its owner.",
			"Snapshot as you go: POST " + appsPath(name) + "/snapshots at regular intervals -- before any risky change and after each chunk of working progress -- so there is always a recent point to roll back to. A snapshot captures the container's whole filesystem, your files AND anything you installed, so a broken apt-get or system change rolls back too. Always include a short one-line description of why, e.g. {\"label\": \"before rewriting the router\"}. (hostit also snapshots automatically before every deploy and every few hours, but those are coarse; your own labelled snapshots are what make a mistake easy to undo.)",
		},
		Layout: "The app's home directory has a place for each kind of thing:\n\n" +
			"  " + app.PublicDir + "/   files served on the web -- static mode serves exactly this directory\n" +
			"  " + app.BinDir + "/      binaries and scripts the app runs (run: ./" + app.BinDir + "/myapp)\n" +
			"  " + app.LogDir + "/      the app's output, written by hostit; read it with GET /logs\n" +
			"  " + app.SrcDir + "/      source, if you keep the app's source here\n" +
			"  " + app.DocsDir + "/     the app's own documentation -- how it works, why it is built that way\n\n" +
			"hostit.yml and README.md live at the top. Directories are created as you write into them.\n\n" +
			"If your app serves files itself, point it at " + app.PublicDir + "/ and never at the home directory: " +
			"the home also holds hostit.yml (which may carry env values) and .ssh/, and serving it puts them on the " +
			"open internet. For example: python3 -m http.server $PORT --bind 0.0.0.0 --directory " + app.PublicDir + "",
		HostitYml: "\"mode:\" says what this app is. Two answers.\n\n" +
			"  mode: static     hostit serves " + app.PublicDir + "/ -- nothing to run, nothing else to set\n\n" +
			"  mode: app        your command serves it:\n" +
			"    prepare: cd " + app.SrcDir + " && go build -o ../" + app.BinDir + "/myapp .   # optional build step\n" +
			"    run: ./" + app.BinDir + "/myapp   # MUST listen on 0.0.0.0:$PORT; $PORT is provided\n" +
			"    (upload binaries with ?mode=755 so they are executable)\n\n" +
			"Optional in both: env: {KEY: value}, and description: a one-liner about the app. " +
			"Unknown keys are an error, so a typo is reported rather than ignored.",
		Runtimes: workspace.Runtimes + ". Install anything else inside the container with apt-get; " +
			"installed packages persist across restarts and redeploys (the container's filesystem is the app's " +
			"own durable disk) and count against the app's disk budget. A new app starts as a stub serving a placeholder page.",
		SuggestedStack: "A single Go binary that embeds its frontend (go:embed) is the easiest thing to run here: " +
			"one file, no runtime to install, instant start. Use run: ./" + app.BinDir + "/myapp listening on 0.0.0.0:$PORT. " +
			"Python, Node.js (with npm) and PHP work out of the box, a plain HTML site needs only mode: static, and anything else installs with apt-get.\n\n" +
			"Prefer keeping the source here: upload it to " + app.SrcDir + "/ and build it in the container with a " +
			"\"prepare:\" step in hostit.yml, e.g. prepare: cd " + app.SrcDir + " && go build -o ../" + app.BinDir + "/myapp . " +
			"Prepare runs before the app starts, on every deploy; if it fails the app is not started and the error is in the logs. " +
			"That way the build happens on the machine that runs it (no cross-compiling, no toolchain needed on your side), " +
			"and the next session can still change the app. Uploading a prebuilt binary to " + app.BinDir + "/ also works and is " +
			"faster -- build it with CGO_ENABLED=0 GOOS=linux GOARCH=amd64 -- but then only the binary is here, and whoever " +
			"comes next has nothing to edit.",
		Auth: "Send the token as: Authorization: Bearer <token>",
		Endpoints: []apiAgentEndpoint{
			{Method: "GET", Path: "" + appsPath(name) + "/info", What: "This document plus the app's URL, state, README, file list and hostit.yml"},
			{Method: "GET", Path: "" + appsPath(name) + "/assistant/transcript", What: "The built-in assistant's chat history for this app, rendered as markdown. Read it to continue prior work with full context; enabled:false if the server has no assistant"},
			{Method: "GET", Path: "" + appsPath(name) + "/logs", What: "Recent output; ?lines=N"},
			{Method: "GET", Path: "" + appsPath(name) + "/files", What: "List the app's files"},
			{Method: "GET", Path: "" + appsPath(name) + "/files/{path}", What: "Read one file"},
			{Method: "PUT", Path: "" + appsPath(name) + "/files/{path}", What: "Write one file (raw body); add ?mode=755 for something executable"},
			{Method: "DELETE", Path: "" + appsPath(name) + "/files/{path}", What: "Delete one file"},
			{Method: "POST", Path: "" + appsPath(name) + "/move", What: `Rename or move a file or directory: {"from": "src/old.go", "to": "src/new.go"}`},
			{Method: "POST", Path: "" + appsPath(name) + "/mkdir", What: `Create an empty directory: {"path": "src/handlers"}`},
			{Method: "POST", Path: "" + appsPath(name) + "/files", What: "Upload a tar archive (Content-Type: application/x-tar)"},
			{Method: "PUT", Path: "" + appsPath(name) + "/readme", What: `Replace README.md: {"readme": "..."}`},
			{Method: "POST", Path: "" + appsPath(name) + "/run", What: `Run one shell command in the app's container: {"command": "cd src && go build ./..."} -- returns its output and exit code`},
			{Method: "POST", Path: "" + appsPath(name) + "/deploy", What: "Apply hostit.yml and (re)start"},
			{Method: "GET", Path: "" + appsPath(name) + "/snapshots", What: "List restorable snapshots (id, time, label, auto), newest first"},
			{Method: "POST", Path: "" + appsPath(name) + "/snapshots", What: `Take a snapshot now: {"label": "short reason"}. Take them at regular intervals with a one-line description of why`},
			{Method: "POST", Path: "" + appsPath(name) + "/snapshots/{id}/restore", What: "Roll back to a snapshot -- files and installed packages together (a safety snapshot of the current state is taken first)"},
			{Method: "DELETE", Path: "" + appsPath(name) + "/snapshots/{id}", What: "Delete one snapshot"},
			{Method: "POST", Path: "" + appsPath(name) + "/start|stop|restart", What: "The run: command: start, stop, or restart it (fast; container stays up)"},
			{Method: "POST", Path: "" + appsPath(name) + "/poweron|poweroff|reboot", What: "The container: power it on, off, or reboot it"},
			{Method: "POST", Path: "" + appsPath(name) + "/archive|unarchive", What: "Shelve the app (powered off, refuses to run, takes no new snapshots) or bring it back"},
			{Method: "PUT", Path: "" + appsPath(name) + "/visibility", What: `Who may open the app's URL: {"private": true} restricts it to the owner, its collaborators and admins; {"private": false} publishes it. Owner only`},
			{Method: "GET|POST|DELETE", Path: "" + appsPath(name) + "/viewers", What: `Who else may OPEN a private app (and nothing more -- no files, no terminal, no deploys): POST {"email": "..."} to add an existing active user, DELETE .../viewers/{id} to remove one. Owner only`},
			{Method: "GET", Path: "/api/container/connections", What: "Over the app's unix socket: the connections and credentials this app was granted, each with the slug its owner named it"},
			{Method: "GET", Path: "/api/container/connections/{slug}/token", What: "Over the app's unix socket: a usable, short-lived credential for one of them, e.g. curl --unix-socket $HOSTIT_SOCKET http://x/api/container/connections/work-cal/token"},
		},
		Notes: []string{
			"Apps also accept SSH: the owner's SSH keys work, and you can scp/rsync into the app's home directory.",
			"Changing env: recreates the container (which ends any SSH session in it, but keeps all files and installed packages); changing mode:, prepare: or run: only restarts the app inside it.",
			"/run is bounded: a minute by default, five at most, and its output is capped. Anything longer belongs in \"prepare:\". A command you background (with & and its output redirected) keeps running after /run returns -- useful, but nothing will stop it except POST /reboot, which replaces the container.",
			"The app runs inside enforced resource caps (see \"limits\" in the per-app info): allocating past the RAM cap gets the process OOM-killed, writing past the disk budget fails with \"Disk quota exceeded\" (the quota covers everything in the app, installed packages included), and a CPU cap throttles rather than kills. Plus a 512-process ceiling. Size builds accordingly -- a compile that fans out past the caps fails rather than taking the host with it.",
			"The caps are not yours to change: the app's owner edits them in the web app (Settings -> Resources), within a per-user resource pool. This API refuses limit changes on an app token by design.",
			"Deleting an app, renaming it, and attaching a custom domain are done by the owner in the web app, not through this API. A rename keeps the app running and changes none of its files, so nothing you build here is affected.",
			"The owner's live preview always shows your latest deploy: hostit tags preview requests with a ?hostit_preview=<n> query parameter and serves them with caching disabled, so you do not need to do anything special. Just do not 404 or error on an unknown query string.",
		},
	}
}

// handleAgentArchive shelves the app: powered off, refusing to run, and taking
// no new snapshots until it is unarchived.
func (s *Server) handleAgentArchive(w http.ResponseWriter, _ *http.Request, c *caller, a *store.App) {
	if err := s.apps.Archive(a.Name); err != nil {
		writeAppError(w, err)
		return
	}
	s.logAction(c, a.Name, "archive", "Archived the app")
	// The app AFTER the change: the row the caller authorized against predates it.
	writeJSON(w, http.StatusOK, s.appResponseFor(c, s.reread(a), s.firstActiveDomain(a.Name)))
}

// handleAgentUnarchive brings the app back as an ordinary powered-off app.
func (s *Server) handleAgentUnarchive(w http.ResponseWriter, _ *http.Request, c *caller, a *store.App) {
	if err := s.apps.Unarchive(a.Name); err != nil {
		writeAppError(w, err)
		return
	}
	s.logAction(c, a.Name, "unarchive", "Brought the app back from the archive")
	writeJSON(w, http.StatusOK, s.appResponseFor(c, s.reread(a), s.firstActiveDomain(a.Name)))
}
