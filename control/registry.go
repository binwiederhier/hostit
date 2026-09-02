package control

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"sync"
	"time"

	"heckel.io/hostit/archive"
	"heckel.io/hostit/node/api"
	"heckel.io/hostit/store"
)

// NodeRegistry tracks the CONNECTED node agents by node id. Registration
// happens on every dial-in; a redial supersedes the previous agent, so an
// unregister only removes the entry if it still holds that same agent.
type NodeRegistry struct {
	agents map[string]NodeAgent
	mu     sync.Mutex // Protects agents
}

// NewNodeRegistry creates an empty registry.
func NewNodeRegistry() *NodeRegistry {
	return &NodeRegistry{agents: make(map[string]NodeAgent)}
}

// Register records the node's live agent, superseding a previous connection.
func (r *NodeRegistry) Register(id string, agent NodeAgent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agents[id] = agent
}

// Unregister removes the node's agent -- only if it still is this agent, so a
// dead connection's cleanup never removes its successor.
func (r *NodeRegistry) Unregister(id string, agent NodeAgent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.agents[id] == agent {
		delete(r.agents, id)
	}
}

// Agent returns the node's live agent, or nil if it is not connected.
func (r *NodeRegistry) Agent(id string) NodeAgent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.agents[id]
}

// IDs lists the connected node ids, sorted.
func (r *NodeRegistry) IDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]string, 0, len(r.agents))
	for id := range r.agents {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// RecordNodeStatus stores what a node reports about itself on connect: the
// address its apps are reachable at (the proxy's routing target), the build it
// runs, and the fact that it just answered. The address comes from the node
// rather than an operator flag, so a node cannot be registered with the wrong
// one -- and routing simply waits until a node has said where it is.
func (m *Manager) RecordNodeStatus(nodeID string, hb *api.Heartbeat) error {
	if hb.Address != "" {
		if err := m.store.EnsureNode(nodeID, hb.Address); err != nil {
			return err
		}
	}
	if hb.SSHHost != "" {
		if err := m.store.SetNodeSSHHost(nodeID, hb.SSHHost); err != nil {
			slog.Warn("Cannot record a node's SSH host", "node", nodeID, "error", err)
		}
	}
	if hb.SSHHostKey != "" {
		if err := m.store.SetNodeHostKey(nodeID, hb.SSHHostKey); err != nil {
			slog.Warn("Cannot record a node's SSH host key", "node", nodeID, "error", err)
		}
	}
	if blob, err := json.Marshal(hb.Stats); err == nil {
		if err := m.store.SetNodeStats(nodeID, string(blob)); err != nil {
			slog.Warn("Cannot record a node's machine stats", "node", nodeID, "error", err)
		}
	}
	if hb.Version != "" {
		if err := m.store.SetNodeVersion(nodeID, hb.Version); err != nil {
			slog.Warn("Cannot record a node's version", "node", nodeID, "error", err)
		}
	}
	// A node's ssh host/key or the fleet's apps may have changed -> the frontend
	// (control itself) re-reconciles its routes, keys and stubs.
	m.refreshSSHRelay()
	return m.store.SetNodeSeen(nodeID, time.Now())
}

// NodeAgent is where this manager sends machine work: the routing agent that
// resolves each app to its hosting node.
func (m *Manager) NodeAgent() NodeAgent {
	return m.node
}

// NodeRegistry is where connected nodes register. It exists from construction
// and is simply empty until one dials in.
func (m *Manager) NodeRegistry() *NodeRegistry {
	return m.registry
}

// placeNode picks the node for a new app: the connected node hosting the
// fewest apps (names sort breaks ties). With nothing connected the app lands on
// the local node (which then dials in and hosts it).
func (m *Manager) placeNode() string {
	if m.registry == nil {
		return store.HostLocal
	}
	ids := m.registry.IDs()
	if len(ids) == 0 {
		return store.HostLocal
	}
	counts := make(map[string]int, len(ids))
	if apps, err := m.store.Apps(); err == nil {
		for _, a := range apps {
			counts[a.Host]++
		}
	}
	best := ids[0]
	for _, id := range ids[1:] {
		if counts[id] < counts[best] {
			best = id
		}
	}
	return best
}

// hostOrLocal normalizes an app row's host ("" predates the node registry).
func hostOrLocal(host string) string {
	if host == "" {
		return store.HostLocal
	}
	return host
}

// routingAgent implements NodeAgent by resolving each call to the app's
// hosting node: name-keyed verbs look the host up in the registry rows,
// spec-keyed verbs carry it. A verb against a disconnected node fails loudly
// -- silently doing machine work on the wrong machine is the one unforgivable
// outcome here.
type routingAgent struct {
	store *store.Store
	reg   *NodeRegistry
}

// NewRoutingAgent builds the control plane's fan-out NodeAgent.
func NewRoutingAgent(st *store.Store, reg *NodeRegistry) NodeAgent {
	return &routingAgent{store: st, reg: reg}
}

var _ NodeAgent = (*routingAgent)(nil)

// agentFor resolves a connected agent by node id.
func (ra *routingAgent) agentFor(host string) (NodeAgent, error) {
	agent := ra.reg.Agent(hostOrLocal(host))
	if agent == nil {
		return nil, fmt.Errorf("node %q is not connected", hostOrLocal(host))
	}
	return agent, nil
}

// route resolves the app's hosting node's agent.
func (ra *routingAgent) route(name string) (NodeAgent, error) {
	a, err := ra.store.App(name)
	if err != nil {
		return nil, err
	}
	return ra.agentFor(a.Host)
}

// routeRunnable is route for the verbs that would make an app RUN, and refuses
// an archived one. Every such verb goes through here rather than each handler
// checking, so a verb added later cannot quietly forget: stopping and reading
// stay on plain route, since an archived app must still be inspectable and
// windable-down.
func (ra *routingAgent) routeRunnable(name string) (NodeAgent, error) {
	a, err := ra.store.App(name)
	if err != nil {
		return nil, err
	}
	if a.Archived {
		return nil, ErrArchived
	}
	return ra.agentFor(a.Host)
}

func (ra *routingAgent) Provision(spec *ProvisionSpec) error {
	agent, err := ra.agentFor(spec.Host)
	if err != nil {
		return err
	}
	return agent.Provision(spec)
}

func (ra *routingAgent) Deprovision(spec *DeprovisionSpec) {
	agent, err := ra.agentFor(spec.Host)
	if err != nil {
		slog.Warn("Cannot deprovision; node not connected", "app", spec.Name, "node", spec.Host)
		return
	}
	agent.Deprovision(spec)
}

func (ra *routingAgent) Ensure(name string) (string, error) {
	agent, err := ra.routeRunnable(name)
	if err != nil {
		return "", err
	}
	return agent.Ensure(name)
}

func (ra *routingAgent) Up(name string) (string, error) {
	agent, err := ra.routeRunnable(name)
	if err != nil {
		return "", err
	}
	return agent.Up(name)
}

func (ra *routingAgent) Down(name string) error {
	agent, err := ra.route(name)
	if err != nil {
		return err
	}
	return agent.Down(name)
}

func (ra *routingAgent) PowerOn(name string) (string, error) {
	agent, err := ra.routeRunnable(name)
	if err != nil {
		return "", err
	}
	return agent.PowerOn(name)
}

func (ra *routingAgent) Restart(name string) error {
	agent, err := ra.routeRunnable(name)
	if err != nil {
		return err
	}
	return agent.Restart(name)
}

func (ra *routingAgent) StartApp(name string) error {
	agent, err := ra.routeRunnable(name)
	if err != nil {
		return err
	}
	return agent.StartApp(name)
}

func (ra *routingAgent) StopApp(name string) error {
	agent, err := ra.route(name)
	if err != nil {
		return err
	}
	return agent.StopApp(name)
}

func (ra *routingAgent) RestartApp(name string) error {
	agent, err := ra.routeRunnable(name)
	if err != nil {
		return err
	}
	return agent.RestartApp(name)
}

func (ra *routingAgent) Status(name string) (string, error) {
	agent, err := ra.route(name)
	if err != nil {
		return "", err
	}
	return agent.Status(name)
}

func (ra *routingAgent) Logs(name string, lines int) (string, error) {
	agent, err := ra.route(name)
	if err != nil {
		return "", err
	}
	return agent.Logs(name, lines)
}

func (ra *routingAgent) Exec(name, command string, timeout time.Duration) (*ExecResult, error) {
	agent, err := ra.routeRunnable(name)
	if err != nil {
		return nil, err
	}
	return agent.Exec(name, command, timeout)
}

func (ra *routingAgent) Screenshot(spec *api.ScreenshotSpec) ([]byte, error) {
	agent, err := ra.routeRunnable(spec.Name)
	if err != nil {
		return nil, err
	}
	return agent.Screenshot(spec)
}

func (ra *routingAgent) RunAssistantTurn(ctx context.Context, spec *api.AssistantTurnSpec, onEvent func(*api.AssistantEvent)) error {
	agent, err := ra.routeRunnable(spec.Name)
	if err != nil {
		return err
	}
	return agent.RunAssistantTurn(ctx, spec, onEvent)
}

func (ra *routingAgent) AnswerAssistant(ctx context.Context, spec *api.AssistantAnswerSpec) (string, *api.AssistantUsage, error) {
	agent, err := ra.routeRunnable(spec.Name)
	if err != nil {
		return "", nil, err
	}
	return agent.AnswerAssistant(ctx, spec)
}

func (ra *routingAgent) Terminal(name string) (api.TerminalSession, error) {
	agent, err := ra.routeRunnable(name)
	if err != nil {
		return nil, err
	}
	return agent.Terminal(name)
}

func (ra *routingAgent) ListFiles(name, dir string) (*Listing, error) {
	agent, err := ra.route(name)
	if err != nil {
		return nil, err
	}
	return agent.ListFiles(name, dir)
}

func (ra *routingAgent) ReadFile(name, relPath string) ([]byte, error) {
	agent, err := ra.route(name)
	if err != nil {
		return nil, err
	}
	return agent.ReadFile(name, relPath)
}

func (ra *routingAgent) ReadFileMax(name, relPath string, max int64) ([]byte, error) {
	agent, err := ra.route(name)
	if err != nil {
		return nil, err
	}
	return agent.ReadFileMax(name, relPath, max)
}

func (ra *routingAgent) WriteFile(name, relPath string, content []byte, mode os.FileMode) error {
	agent, err := ra.route(name)
	if err != nil {
		return err
	}
	return agent.WriteFile(name, relPath, content, mode)
}

func (ra *routingAgent) WriteFileFrom(name, relPath string, r io.Reader, mode os.FileMode) error {
	agent, err := ra.route(name)
	if err != nil {
		return err
	}
	return agent.WriteFileFrom(name, relPath, r, mode)
}

// ArchiveWorkspace routes a live-workspace export to the node holding the app.
func (ra *routingAgent) ArchiveWorkspace(name string, format archive.Format) (io.ReadCloser, error) {
	agent, err := ra.route(name)
	if err != nil {
		return nil, err
	}
	return agent.ArchiveWorkspace(name, format)
}

// ArchiveSnapshot routes an existing-snapshot export to the node holding the app.
func (ra *routingAgent) ArchiveSnapshot(name, snapshotID string, format archive.Format) (io.ReadCloser, error) {
	agent, err := ra.route(name)
	if err != nil {
		return nil, err
	}
	return agent.ArchiveSnapshot(name, snapshotID, format)
}

func (ra *routingAgent) DeleteFile(name, relPath string) error {
	agent, err := ra.route(name)
	if err != nil {
		return err
	}
	return agent.DeleteFile(name, relPath)
}

func (ra *routingAgent) MoveFile(name, fromRel, toRel string) error {
	agent, err := ra.route(name)
	if err != nil {
		return err
	}
	return agent.MoveFile(name, fromRel, toRel)
}

func (ra *routingAgent) MakeDir(name, relPath string) error {
	agent, err := ra.route(name)
	if err != nil {
		return err
	}
	return agent.MakeDir(name, relPath)
}

func (ra *routingAgent) StatFile(name, relPath string) (*FileInfo, error) {
	agent, err := ra.route(name)
	if err != nil {
		return nil, err
	}
	return agent.StatFile(name, relPath)
}

func (ra *routingAgent) FileExists(name, relPath string) bool {
	agent, err := ra.route(name)
	if err != nil {
		return false
	}
	return agent.FileExists(name, relPath)
}

func (ra *routingAgent) ExtractTar(name string, r io.Reader) ([]string, error) {
	agent, err := ra.route(name)
	if err != nil {
		return nil, err
	}
	return agent.ExtractTar(name, r)
}

func (ra *routingAgent) SetKeys(name string, appKeys, profileKeys []string) error {
	agent, err := ra.route(name)
	if err != nil {
		return err
	}
	return agent.SetKeys(name, appKeys, profileKeys)
}

// Snapshots has no app to route by: it is a node-level read, and control asks
// a SPECIFIC node's agent directly on rejoin (IngestNodeSnapshots), never
// through the router. Routing it would have to pick a node arbitrarily.
func (ra *routingAgent) Snapshots() ([]*store.Snapshot, error) {
	return nil, fmt.Errorf("%w: snapshots must be read from a specific node's agent", api.ErrInvalid)
}

func (ra *routingAgent) Rename(oldName, newName, id string) error {
	// Route by the OLD name: the registry still holds it while the machine half runs.
	agent, err := ra.route(oldName)
	if err != nil {
		return err
	}
	return agent.Rename(oldName, newName, id)
}

func (ra *routingAgent) SetMemoryLimit(name string, memoryMB int) {
	agent, err := ra.route(name)
	if err != nil {
		slog.Warn("Cannot set memory limit; node not connected", "app", name)
		return
	}
	agent.SetMemoryLimit(name, memoryMB)
}

func (ra *routingAgent) SetCPULimit(name string, cpuMilli int) {
	agent, err := ra.route(name)
	if err != nil {
		slog.Warn("Cannot set cpu limit; node not connected", "app", name)
		return
	}
	agent.SetCPULimit(name, cpuMilli)
}

func (ra *routingAgent) SetDiskLimit(name string, diskMB int) {
	agent, err := ra.route(name)
	if err != nil {
		slog.Warn("Cannot set disk limit; node not connected", "app", name)
		return
	}
	agent.SetDiskLimit(name, diskMB)
}

func (ra *routingAgent) TakeSnapshot(name, label string, auto bool) (*store.Snapshot, error) {
	agent, err := ra.routeRunnable(name)
	if err != nil {
		return nil, err
	}
	return agent.TakeSnapshot(name, label, auto)
}

func (ra *routingAgent) DeleteSnapshot(name, id string) error {
	agent, err := ra.route(name)
	if err != nil {
		return err
	}
	return agent.DeleteSnapshot(name, id)
}

func (ra *routingAgent) Rollback(name, id string) error {
	agent, err := ra.route(name)
	if err != nil {
		return err
	}
	return agent.Rollback(name, id)
}

// States fans out to every connected node and merges; each node only knows
// its own apps, so names it cannot answer just stay absent.
func (ra *routingAgent) States(names []string) map[string]State {
	merged := make(map[string]State)
	for _, id := range ra.reg.IDs() {
		if agent := ra.reg.Agent(id); agent != nil {
			for name, state := range agent.States(names) {
				merged[name] = state
			}
		}
	}
	return merged
}

// Sync broadcasts; PushMirror pushes per-node filtered states instead, so
// this only exists to satisfy the interface for direct uses.
func (ra *routingAgent) Sync(state *SyncState) error {
	for _, id := range ra.reg.IDs() {
		if agent := ra.reg.Agent(id); agent != nil {
			if err := agent.Sync(state); err != nil {
				return err
			}
		}
	}
	return nil
}

// Reconcile fans out to every connected node; rejoin calls a node's agent
// directly, so this is only for interface completeness.
// Reconcile fans the desired state out, SLICED per node: a node must only
// ever be handed the apps it hosts. Handing every node the whole fleet would
// have each one try to build and configure apps that live elsewhere -- and on
// a colocated pair, where /etc/passwd is shared, it would look like the app
// already exists and quietly apply another node's limits to it.
func (ra *routingAgent) Reconcile(desired *api.DesiredState) []string {
	for _, id := range ra.reg.IDs() {
		agent := ra.reg.Agent(id)
		if agent == nil {
			continue
		}
		agent.Reconcile(sliceDesired(desired, id))
	}
	return nil
}

// sliceDesired narrows a fleet-wide desired state to one node's apps.
func sliceDesired(desired *api.DesiredState, nodeID string) *api.DesiredState {
	if desired == nil {
		return nil
	}
	out := &api.DesiredState{Seq: desired.Seq, Apps: make([]*api.AppDesired, 0, len(desired.Apps))}
	for _, app := range desired.Apps {
		if hostOrLocal(app.Host) == nodeID {
			out.Apps = append(out.Apps, app)
		}
	}
	return out
}

// Heartbeat is per-node data; meaningless on the fan-out.
func (ra *routingAgent) Heartbeat() *Heartbeat {
	return nil
}
