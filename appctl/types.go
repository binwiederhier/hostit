package appctl

import (
	"time"
)

// Mode is how an app is run: as a plain process or as a container
type Mode string

const (
	// An app's home has a place for each kind of thing, so an agent (or a person)
	// never has to guess where something belongs, and so hostit knows where to
	// look without being told
	PublicDir = "public" // Files served on the web (static mode serves exactly this)
	BinDir    = "bin"    // Compiled binaries and scripts the app runs
	LogDir    = "log"    // The app's output, written by the hostit agent
	SrcDir    = "src"    // Source, if the app keeps its source on the host

	// ModeProcess runs the app's "run" command directly (must listen on $PORT)
	ModeProcess = Mode("process")
	// ModeContainer runs the app as a podman container of the app's own image
	ModeContainer = Mode("container")
	// ModeStatic serves a directory of files; hostit provides the web server
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
