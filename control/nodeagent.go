package control

import "heckel.io/hostit/node/api"

// The node RPC contract lives in package nodeapi so the transport layer can
// speak it without importing this orchestrator. The names are re-exported here
// as aliases, so app internals (and server/assistant/cmd) keep referring to
// NodeAgent, ProvisionSpec, ErrInvalid, ... unchanged.
type (
	NodeAgent           = api.NodeAgent
	ControlSink         = api.ControlSink
	ProvisionSpec       = api.ProvisionSpec
	DeprovisionSpec     = api.DeprovisionSpec
	ScreenshotSpec      = api.ScreenshotSpec
	AssistantTurnSpec   = api.AssistantTurnSpec
	AssistantAnswerSpec = api.AssistantAnswerSpec
	AssistantImage      = api.AssistantImage
	AssistantEvent      = api.AssistantEvent
	AssistantUsage      = api.AssistantUsage
	SyncState           = api.SyncState
	State               = api.State
	ExecResult          = api.ExecResult
	Heartbeat           = api.Heartbeat
)

var (
	// ErrAppExists is returned when an app or its unix user already exists.
	ErrAppExists = api.ErrAppExists
	// ErrInvalid is returned for a malformed request.
	ErrInvalid = api.ErrInvalid
	// ErrLimitReached is returned when a quota is hit.
	ErrLimitReached = api.ErrLimitReached
)

// The Manager is NOT a NodeAgent. It used to be, by embedding a Machine, which
// is what made a fused daemon possible; machine work now always crosses the
// cluster link to a node, even one sharing the host.
