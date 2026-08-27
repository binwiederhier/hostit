package app

// The app-home contract: where an app's things live, and the states the
// in-container agent records for the daemon to read. Both sides of the
// boundary reference these rather than the bare strings.

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
	DataDir   = "data"   // Persistent app data by convention: sqlite databases, state files

	// ConfigFile is the app's hostit.yml, the one file that describes how it runs
	ConfigFile = "hostit.yml"
	// LogFile is where the agent writes the app's output, relative to the home;
	// the agent, the daemon and the CLI's "logs -f" all resolve the same path here
	LogFile = LogDir + "/app.log"
	// StateFile is where the agent records the run: process state (one of the
	// AppState* values below) for the daemon to read: the daemon cannot see inside
	// the container, so the agent leaves this breadcrumb
	StateFile = LogDir + "/state"

	// AppState* are the values the agent writes to StateFile and the daemon reads
	// back. They are the contract between the in-container agent and the daemon, so
	// both sides reference these constants rather than the bare strings.
	StateRunning = "running" // The run: command is up and serving
	StateStopped = "stopped" // Stopped by the owner; the container stays up
	StateCrashed = "crashed" // Exited and is being restarted with a backoff
	StateFailed  = "failed"  // Crash-looped; the agent gave up restarting it
	StateIdle    = "idle"    // No run: command configured (e.g. a static app)

	// ModeApp runs the app's own "run" command, which must listen on $PORT
	ModeApp = Mode("app")
	// ModeStatic serves PublicDir; hostit provides the web server
	ModeStatic = Mode("static")
)
