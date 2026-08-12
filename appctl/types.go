package appctl

import (
	"time"
)

// Mode is how an app is run: hostit serves its files, or it runs a command
type Mode string

const (
	// An app's home has a place for each kind of thing, so an agent (or a person)
	// never has to guess where something belongs, and so hostit knows where to
	// look without being told
	PublicDir = "public" // Files served on the web (static mode serves exactly this)
	BinDir    = "bin"    // Compiled binaries and scripts the app runs
	LogDir    = "log"    // The app's output, written by the hostit agent
	SrcDir    = "src"    // Source, if the app keeps its source on the host
	DocsDir   = "docs"   // The app's own documentation, for whoever works on it next

	// ConfigFile is the app's hostit.yml, the one file that describes how it runs
	ConfigFile = "hostit.yml"
	// AppLogFile is where the agent writes the app's output, relative to the home;
	// the agent, the daemon and the CLI's "logs -f" all resolve the same path here
	AppLogFile = LogDir + "/app.log"
	// AppStateFile is where the agent records the run: process state (one of the
	// AppState* values below) for the daemon to read: the daemon cannot see inside
	// the container, so the agent leaves this breadcrumb
	AppStateFile = LogDir + "/state"

	// AppState* are the values the agent writes to AppStateFile and the daemon reads
	// back. They are the contract between the in-container agent and the daemon, so
	// both sides reference these constants rather than the bare strings.
	AppStateRunning = "running" // The run: command is up and serving
	AppStateStopped = "stopped" // Stopped by the owner; the container stays up
	AppStateCrashed = "crashed" // Exited and is being restarted with a backoff
	AppStateFailed  = "failed"  // Crash-looped; the agent gave up restarting it
	AppStateIdle    = "idle"    // No run: command configured (e.g. a static app)

	// ModeApp runs the app's own "run" command, which must listen on $PORT
	ModeApp = Mode("app")
	// ModeStatic serves PublicDir; hostit provides the web server
	ModeStatic = Mode("static")
)

// SelfInfo is what the daemon's unix socket /v1/self endpoint returns about the
// calling app; field names match the server's app response
type SelfInfo struct {
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	Port      int       `json:"port"`
	CreatedAt time.Time `json:"created_at"`
}

// messageResponse mirrors the daemon's lifecycle action responses
type messageResponse struct {
	Message string `json:"message"`
}

// outputResponse mirrors the daemon's status/log responses
type outputResponse struct {
	Output string `json:"output"`
}

// errorResponse mirrors the daemon's error responses
type errorResponse struct {
	Error string `json:"error"`
}

// toolResponse mirrors the daemon's /v1/self/tool result: one tool call's
// model-facing output and whether the tool reported an error
type toolResponse struct {
	Output  string `json:"output"`
	IsError bool   `json:"is_error"`
}
