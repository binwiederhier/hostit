package app

import "heckel.io/hostit/nodeapi"

// The node RPC contract lives in package nodeapi so the transport layer can
// speak it without importing this orchestrator. The names are re-exported here
// as aliases, so app internals (and server/assistant/cmd) keep referring to
// app.NodeAgent, app.ProvisionSpec, app.ErrInvalid, ... unchanged.
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

// The Manager is the in-process NodeAgent of a single-box install.
var _ NodeAgent = (*Manager)(nil)
