package app

import (
	"io"
	"os"
	"time"

	"heckel.io/hostit/store"
)

// NodeAgent is the node-local half of the platform: every verb that must run
// on the machine an app physically lives on (its subvolume, unix user,
// container, ports, files). The control plane resolves an app to its node and
// calls these; on a single-box install the implementation is the Manager
// itself, in-process. This is the seam the multi-node split grows along
// (plans/260815-hostit-nodeagent.md): hostit-node will serve exactly this
// surface over the dial-in RPC, so nothing here may depend on reading the
// registry -- inputs arrive as arguments or specs.
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
	TerminalCommand(name string) (string, []string, error)

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

	// Keys and limits, applied where the app user lives.
	SetKeys(name string, appKeys, profileKeys []string) error
	SyncKeys(name string, profileKeys []string) error
	SetMemoryLimit(name string, memoryMB int)
	SetDiskLimit(name string, diskMB int)

	// Snapshots: the subvolume work; metadata stays on the control plane.
	TakeSnapshot(name, label string, auto bool) (*store.Snapshot, error)
	DeleteSnapshot(name, id string) error
	Rollback(name, id string) error

	// Sync replaces the node's registry mirror (the app and snapshot rows it
	// hosts); control pushes it on connect and after every registry mutation,
	// BEFORE any verb that reads rows on the node.
	Sync(state *SyncState) error

	// Reconcile converges the node's machine state to its (freshly synced)
	// mirror: tear down apps deleted while it was disconnected, re-assert port
	// rules. Run on every rejoin. Returns the orphan ids it removed.
	Reconcile() []string

	// Node-level: batch state for the control plane's cache, and the
	// heartbeat placement/health feed on.
	States(names []string) map[string]State
	Heartbeat() *Heartbeat
}

// Heartbeat is what a node reports about itself: the inputs for placement and
// health. Grown in later phases (free memory/disk, app count).
type Heartbeat struct {
	Version      string `json:"version"`
	BtrfsCapable bool   `json:"btrfs_capable"`
}

// The Manager is the in-process NodeAgent of a single-box install.
var _ NodeAgent = (*Manager)(nil)
