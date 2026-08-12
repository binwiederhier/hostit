package server

import (
	"heckel.io/hostit/app"
	"heckel.io/hostit/appctl"
	"heckel.io/hostit/config"
)

// agentGuide is the instruction set handed to agents. It is returned both by
// /api/info and inline by /api/apps/{app}/info, because the prompt a user pastes
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
	whereYouAre := "You are looking at " + appsPath(name) + "/info: it tells you what the app currently is. " +
		"Its README.md is the app's description and worklog. A new app is a stub serving a placeholder page."
	if description != "" {
		whereYouAre = "You are looking at " + appsPath(name) + "/info. This app is already built and live: " +
			description + ". Do not rebuild it from scratch. Read its README.md (the app's worklog) and its " +
			"files first, then make only the changes you were asked for."
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
			"Keep the app's own documentation in " + appctl.DocsDir + "/ -- how it works, why it is built the way it is, anything the next session would otherwise have to re-derive. Read it before you change anything, and update it after every change that matters. README.md is the summary and worklog; " + appctl.DocsDir + "/ is the detail.",
			"Compiling or installing dependencies: POST " + appsPath(name) + "/run with a shell command. It runs in the app's container, where the toolchains are, and returns the output and exit code -- so you can iterate on a build error without SSH. It is bounded (a minute by default, five at most): make the build a \"prepare:\" step in hostit.yml once it works, so it also runs on every deploy.",
			"Keep a one-line \"description:\" in hostit.yml saying what this app is. The owner's web page shows it, and the next session (or a different agent) starts from it instead of from a blank page.",
			"Snapshot as you go: POST " + appsPath(name) + "/snapshots at regular intervals -- before any risky change and after each chunk of working progress -- so there is always a recent point to roll back to. Always include a short one-line description of why, e.g. {\"label\": \"before rewriting the router\"}. (hostit also snapshots automatically before every deploy and hourly, but those are coarse; your own labelled snapshots are what make a mistake easy to undo.)",
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
		HostitYml: "\"mode:\" says what this app is. Two answers.\n\n" +
			"  mode: static     hostit serves " + appctl.PublicDir + "/ -- nothing to run, nothing else to set\n\n" +
			"  mode: app        your command serves it:\n" +
			"    prepare: cd " + appctl.SrcDir + " && go build -o ../" + appctl.BinDir + "/myapp .   # optional build step\n" +
			"    run: ./" + appctl.BinDir + "/myapp   # MUST listen on 0.0.0.0:$PORT; $PORT is provided\n" +
			"    (upload binaries with ?mode=755 so they are executable)\n\n" +
			"Optional in both: env: {KEY: value}, and description: a one-liner about the app. " +
			"Unknown keys are an error, so a typo is reported rather than ignored.",
		Runtimes: app.WorkspaceRuntimes + ". Install anything else inside the container with apt-get; " +
			"a new app starts as a stub serving a placeholder page.",
		SuggestedStack: "A single Go binary that embeds its frontend (go:embed) is the easiest thing to run here: " +
			"one file, no runtime to install, instant start. Use run: ./" + appctl.BinDir + "/myapp listening on 0.0.0.0:$PORT. " +
			"Python, Node.js (with npm) and PHP work out of the box, a plain HTML site needs only mode: static, and anything else installs with apt-get.\n\n" +
			"Prefer keeping the source here: upload it to " + appctl.SrcDir + "/ and build it in the container with a " +
			"\"prepare:\" step in hostit.yml, e.g. prepare: cd " + appctl.SrcDir + " && go build -o ../" + appctl.BinDir + "/myapp . " +
			"Prepare runs before the app starts, on every deploy; if it fails the app is not started and the error is in the logs. " +
			"That way the build happens on the machine that runs it (no cross-compiling, no toolchain needed on your side), " +
			"and the next session can still change the app. Uploading a prebuilt binary to " + appctl.BinDir + "/ also works and is " +
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
			{Method: "POST", Path: "" + appsPath(name) + "/snapshots/{id}/restore", What: "Roll back to a snapshot (a safety snapshot of the current state is taken first)"},
			{Method: "DELETE", Path: "" + appsPath(name) + "/snapshots/{id}", What: "Delete one snapshot"},
			{Method: "POST", Path: "" + appsPath(name) + "/start|stop|restart", What: "The run: command: start, stop, or restart it (fast; container stays up)"},
			{Method: "POST", Path: "" + appsPath(name) + "/poweron|poweroff|reboot", What: "The container: power it on, off, or reboot it"},
		},
		Notes: []string{
			"Apps also accept SSH: the owner's SSH keys work, and you can scp/rsync into the app's home directory.",
			"Changing env: recreates the container (which ends any SSH session in it); changing mode:, prepare: or run: only restarts the app inside it.",
			"/run is bounded: a minute by default, five at most, and its output is capped. Anything longer belongs in \"prepare:\". A command you background (with & and its output redirected) keeps running after /run returns -- useful, but nothing will stop it except POST /reboot, which replaces the container.",
			"Your app has 512 processes and its memory limit to work with, and the disk quota is shared with everything else in the app. A build that fans out past that fails rather than taking the host with it.",
			"Deleting an app, renaming it, and attaching a custom domain are done by the owner in the web app, not through this API. A rename keeps the app running and changes none of its files, so nothing you build here is affected.",
			"The owner's live preview always shows your latest deploy: hostit tags preview requests with a ?hostit_preview=<n> query parameter and serves them with caching disabled, so you do not need to do anything special. Just do not 404 or error on an unknown query string.",
		},
	}
}
