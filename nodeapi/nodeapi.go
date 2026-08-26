// Package nodeapi is the contract between the control plane and a node: the
// NodeAgent verbs control calls, the ControlSink callbacks a node reports back
// through, the spec/state types that cross the wire, and the sentinel errors
// that must survive it. It is deliberately a low package -- it imports only the
// registry types (store) and the file-layer types (homefs), never the app
// orchestrator -- so the transport (package node) can speak the contract
// without depending on the implementation, and app implements it.
package nodeapi

import (
	"errors"
	"io"
	"os"
	"regexp"
	"time"

	"heckel.io/hostit/hoststats"

	"heckel.io/hostit/archive"
	"heckel.io/hostit/homefs"
	"heckel.io/hostit/store"
)

// FileInfo and Listing are the file-layer shapes the file verbs return; they
// live in homefs and are re-exported here so the contract is self-describing.
type (
	FileInfo = homefs.FileInfo
	Listing  = homefs.Listing
)

const (
	// AppNamePattern is what an app may be called: safe as a Unix username and as
	// a DNS label. Part of the contract because both halves enforce it: control
	// validates creates and renames, a node validates the login users it manages.
	AppNamePattern = `^[a-z]([a-z0-9-]{0,30}[a-z0-9])?$`
)

var (
	// appNameRegex is the compiled AppNamePattern behind ValidName
	appNameRegex = regexp.MustCompile(AppNamePattern)
)

// ValidName reports whether s is a valid hostit app name (safe as a Unix username
// and a DNS label). Format only -- create additionally rejects reserved names
// and duplicates.
func ValidName(s string) bool {
	return appNameRegex.MatchString(s)
}

var (
	// ErrAppExists is returned when an app (or its unix user) already exists.
	ErrAppExists = errors.New("app or user already exists")
	// ErrInvalid is returned for a malformed request.
	ErrInvalid = errors.New("invalid request")
	// ErrLimitReached is returned when a quota (app count, disk) is hit.
	ErrLimitReached = errors.New("limit reached")
)

// NodeAgent is the node-local half of the platform: every verb that must run
// on the machine an app physically lives on (its subvolume, unix user,
// container, ports, files). The control plane resolves an app to its node and
// calls these; on a single-box install the implementation is control.Manager
// itself, in-process. Nothing here may depend on reading the registry --
// inputs arrive as arguments or specs.
//
// Deliberately NOT here: registry bookkeeping (rows, tokens, limits records,
// snapshot metadata, retention policy), URL building, name validation, and
// compositions like Readme/Description/ListSnapshots -- those assemble on the
// control plane from these primitives.
type NodeAgent interface {
	// Provisioning: build/tear down the app on this machine. Fork is not a
	// separate verb: a fork is a provision whose spec carries the seed
	// subvolume (the reflink is node-local either way).
	Provision(spec *ProvisionSpec) error
	Deprovision(spec *DeprovisionSpec)
	Ensure(name string) (string, error)
	Up(name string) (string, error)
	Down(name string) error
	PowerOn(name string) (string, error)
	Restart(name string) error
	StartApp(name string) error
	StopApp(name string) error
	RestartApp(name string) error
	Status(name string) (string, error)
	Logs(name string, lines int) (string, error)
	Exec(name, command string, timeout time.Duration) (*ExecResult, error)
	// Terminal opens an interactive shell in the app's container, as a byte
	// stream with out-of-band resize. It replaces the old TerminalCommand,
	// which returned a command for the CALLER to exec -- correct only when the
	// caller was on the app's machine, which control no longer is: run on the
	// control host, "runuser <app>" named a user that only exists on the node.
	Terminal(name string) (TerminalSession, error)

	// Files: each resolves through os.OpenRoot on this machine's real path.
	ListFiles(name, dir string) (*Listing, error)
	ReadFile(name, relPath string) ([]byte, error)
	ReadFileMax(name, relPath string, max int64) ([]byte, error)
	WriteFile(name, relPath string, content []byte, mode os.FileMode) error
	WriteFileFrom(name, relPath string, r io.Reader, mode os.FileMode) error
	DeleteFile(name, relPath string) error
	MoveFile(name, fromRel, toRel string) error
	MakeDir(name, relPath string) error
	StatFile(name, relPath string) (*FileInfo, error)
	FileExists(name, relPath string) bool
	ExtractTar(name string, r io.Reader) ([]string, error)
	// ArchiveWorkspace streams the app's whole workspace (home/app) as an
	// archive in the given format, from a consistent read-only snapshot. The
	// caller closes the reader; closing it early aborts the archive and drops
	// the snapshot.
	ArchiveWorkspace(name string, format archive.Format) (io.ReadCloser, error)
	// ArchiveSnapshot streams an existing snapshot's workspace (home/app) as an
	// archive. The snapshot is already immutable, so no new snapshot is taken and
	// nothing is deleted; the caller closes the reader.
	ArchiveSnapshot(name, snapshotID string, format archive.Format) (io.ReadCloser, error)

	// Keys and limits, applied where the app user lives.
	// SetKeys writes the app's complete authorized_keys set. Control resolves
	// it (the app's own keys are registry state, absent from the node's
	// mirror), so there is deliberately no "sync from what you have" verb.
	SetKeys(name string, appKeys, profileKeys []string) error
	// Rename renames the app's Unix login (stopping and restarting the app
	// around the usermod) and carries the name-keyed caches over. The registry
	// flip is control's; on a failed flip control compensates by renaming back.
	Rename(oldName, newName, id string) error
	SetMemoryLimit(name string, memoryMB int)
	SetDiskLimit(name string, diskMB int)
	// SetCPULimit records the app's CPU cap in millicores (0 = uncapped);
	// like memory, it is applied on the next container (re)creation.
	SetCPULimit(name string, cpuMilli int)

	// Snapshots: the subvolume work; metadata stays on the control plane.
	// Snapshots reports the records this node holds for the apps it hosts.
	// Control reads them on rejoin, BEFORE pushing the mirror back, so a
	// record written while the connection was down is not overwritten by
	// control's older list (the subvolume would stay, unreferenced).
	Snapshots() ([]*store.Snapshot, error)
	TakeSnapshot(name, label string, auto bool) (*store.Snapshot, error)
	DeleteSnapshot(name, id string) error
	Rollback(name, id string) error

	// Sync replaces the node's registry mirror (the app and snapshot rows it
	// hosts); control pushes it on connect and after every registry mutation,
	// BEFORE any verb that reads rows on the node.
	Sync(state *SyncState) error

	// Reconcile converges this node to the desired state control asserts:
	// build what is missing, correct what has drifted (keys, limits, power),
	// tear down what is not in it. Control calls it on every connect
	// and on a timer, so a node that crashed, was rebuilt, or missed a
	// mutation heals from control's registry alone -- the registry is the
	// source, the node is a projection of it. A nil desired state falls back
	// to the node's mirror (an older control). Returns what it removed.
	Reconcile(desired *DesiredState) []string

	// Node-level: batch state for the control plane's cache, and the
	// heartbeat placement/health feed on.
	States(names []string) map[string]State
	Heartbeat() *Heartbeat
}

// DesiredState is the complete configuration control asserts for one node:
// every app that belongs there, with everything needed to build it from
// nothing. It is the complete set, never a delta: applying it twice changes
// nothing, and applying it once after any outage is enough to converge.
type DesiredState struct {
	Apps []*AppDesired `json:"apps"`
	// Seq orders these the same way SyncState.Seq orders mirrors.
	Seq int64 `json:"seq"`
}

// AppDesired is one app's desired configuration. The embedded ProvisionSpec is
// exactly what creating the app from nothing needs, which is what makes
// re-provisioning a rebuilt node a matter of replaying this state.
type AppDesired struct {
	ProvisionSpec
	MemoryMB   int  `json:"memory_mb"`
	CPUMilli   int  `json:"cpu_milli"`
	PoweredOff bool `json:"powered_off"`
}

// ControlSink is the node's reverse channel to control.
// The app-socket relay: the node serves /run/hostit/hostit.sock, resolves the
// calling app by peer uid from its mirror, and forwards the request to control
// under this prefix with the app named in this header. The header is only
// meaningful on a node's authenticated duplex channel; nothing public reads it.
const (
	AppRelayPrefix = "/apprelay"
	AppRelayHeader = "X-Hostit-App"
)

// TerminalSession is one live browser terminal: Reads are the pty's output,
// Writes are keystrokes, Resize follows the browser window. Closing it ends
// the shell.
type TerminalSession interface {
	io.ReadWriteCloser
	Resize(cols, rows uint16) error
}

type ControlSink interface {
	// PowerChanged reports a poweroff/poweron the node's own verb performed.
	PowerChanged(name string, poweredOff bool)
	// UsageChanged reports a fresh disk usage measurement.
	UsageChanged(name string, usedMB int)
	// SnapshotsChanged carries the app's authoritative snapshot records after
	// any mutation; control replaces its rows with them.
	SnapshotsChanged(name string, snaps []*store.Snapshot)
}

// ProvisionSpec is everything the node half needs to build an app on this
// machine, resolved by the control side.
type ProvisionSpec struct {
	// Host is the target node id; the control plane's routing agent sends the
	// spec there (the row does not exist yet when provisioning starts).
	Host    string   `json:"host"`
	ID      string   `json:"id"`       // Stable app id; subvolume and container are keyed on it
	Name    string   `json:"name"`     // Unix account name (today: the app name)
	Port    int      `json:"port"`     // Loopback port; the uid block derives from it
	SSHKeys []string `json:"ssh_keys"` // Full authorized_keys set (request + profile keys)
	// SeedAppID/SeedSnapshotID name the fork seed (the source app's subvolume,
	// or one of its snapshots); empty SeedAppID builds a fresh app with the
	// skeleton. IDs, not paths: the NODE resolves them against its own pool --
	// a control-computed absolute path is wrong on any node whose apps-dir is
	// not control's.
	SeedAppID      string `json:"seed_app_id"`
	SeedSnapshotID string `json:"seed_snapshot_id"`
	URL            string `json:"url"`     // The app's public URL, for the skeleton's welcome page
	DiskMB         int    `json:"disk_mb"` // Resolved disk cap; the budget qgroup is created and capped BEFORE the subvolume
}

// DeprovisionSpec is everything the teardown needs, captured by the control
// side BEFORE the registry rows are gone: once they are, name-keyed lookups
// (paths, ids, snapshots) resolve nothing.
type DeprovisionSpec struct {
	// Host is the node the app lives on, captured before the row is removed.
	Host string `json:"host"`
	// ID keys everything on-disk (subvolume, snapshots dir); the node resolves
	// the paths against its own pool.
	ID        string `json:"id"`
	Name      string `json:"name"`
	Port      int    `json:"port"`
	UID       int    `json:"uid"` // The budget qgroup key; UIDKnown guards a failed lookup
	UIDKnown  bool   `json:"uid_known"`
	Unit      string `json:"unit"`
	Container string `json:"container"`
}

// SyncState is the registry slice a node mirrors.
type SyncState struct {
	Apps      []*store.App      `json:"apps"`
	Snapshots []*store.Snapshot `json:"snapshots"`
	// Seq orders the pushes on one control<->node connection. A payload is
	// built by reading the registry and sent afterwards, so two concurrent
	// mutations can reach the node in the opposite order to the one they were
	// built in; the node applies only a payload newer than the last one it
	// took, and ignores the rest. Without it, an older snapshot landing last
	// drops a just-created app from the mirror and every verb for that app
	// fails with "app not found" until the next mutation pushes again.
	Seq int64 `json:"seq"`
}

// State is one app's measured runtime state, as a node reports it.
type State struct {
	Running    bool `json:"running"`     // The container's systemd unit is active
	AppRunning bool `json:"app_running"` // The run: command inside it is up
	// AppState is the agent's breadcrumb verbatim ("running"/"crashed"/"failed"/... or
	// "" when the container is down), so the UI can tell a crashed give-up from a stop.
	AppState     string `json:"app_state"`
	MemoryMB     int    `json:"memory_mb"`      // Current container memory use in MB
	CPUPercent   int    `json:"cpu_percent"`    // Current container CPU use in whole percent (may exceed 100 on multiple cores)
	StartedAt    int64  `json:"started_at"`     // Unix seconds the container last started (0 if down)
	AppStartedAt int64  `json:"app_started_at"` // Unix millis the run: process last changed state (0 if never)
}

// ExecResult is the output of one in-container command.
type ExecResult struct {
	Output    string `json:"output"`
	ExitCode  int    `json:"exit_code"`
	Truncated bool   `json:"truncated"`
	TimedOut  bool   `json:"timed_out"`
}

// Heartbeat is what a node reports about itself: the inputs for placement and
// health. Grown in later phases (free memory/disk, app count).
type Heartbeat struct {
	Version      string `json:"version"`
	BtrfsCapable bool   `json:"btrfs_capable"`
	// Stats is the machine's own resource state, so the admin page can tell a
	// healthy box from a full one without anyone SSHing in.
	Stats hoststats.Stats `json:"stats"`
	// Address is where this node's published app ports are reachable -- what
	// the proxy dials. The node reports it because the node is what knows it
	// (it is the address its containers publish on); control records it so
	// routing needs no operator-supplied flag to be correct.
	Address string `json:"address"`
	// SSHHost is the hostname clients use to SSH to apps on this node. The node
	// reports it for the same reason as Address: it knows its own reachable
	// address. Empty leaves control advertising its base domain.
	SSHHost string `json:"ssh_host"`
	// SSHHostKey is this node's sshd public host key (one authorized-keys-format
	// line), reported so control can write the relay gateway's known_hosts.
	SSHHostKey string `json:"ssh_host_key"`
}
