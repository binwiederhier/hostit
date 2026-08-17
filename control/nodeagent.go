package control

import "heckel.io/hostit/nodeapi"

// The node RPC contract lives in package nodeapi so the transport layer can
// speak it without importing this orchestrator. The names are re-exported here
// as aliases, so app internals (and server/assistant/cmd) keep referring to
// NodeAgent, ProvisionSpec, ErrInvalid, ... unchanged.
type (
	NodeAgent       = nodeapi.NodeAgent
	ControlSink     = nodeapi.ControlSink
	ProvisionSpec   = nodeapi.ProvisionSpec
	DeprovisionSpec = nodeapi.DeprovisionSpec
	SyncState       = nodeapi.SyncState
	State           = nodeapi.State
	ExecResult      = nodeapi.ExecResult
	Heartbeat       = nodeapi.Heartbeat
)

var (
	// ErrAppExists is returned when an app or its unix user already exists.
	ErrAppExists = nodeapi.ErrAppExists
	// ErrInvalid is returned for a malformed request.
	ErrInvalid = nodeapi.ErrInvalid
	// ErrLimitReached is returned when a quota is hit.
	ErrLimitReached = nodeapi.ErrLimitReached
)

// The Manager is NOT a NodeAgent. It used to be, by embedding a Machine, which
// is what made a fused daemon possible; machine work now always crosses the
// cluster link to a node, even one sharing the host.
