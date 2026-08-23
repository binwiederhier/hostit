package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"heckel.io/hostit/assistant"
	"heckel.io/hostit/connections"
	"heckel.io/hostit/store"
)

const (
	// assistantReadCap bounds a file the assistant reads, so one huge file cannot
	// blow up the model's context
	assistantReadCap = 128 * 1024
	// mcpToolsTimeout bounds listing a server's tools while building a prompt.
	// Short: it is on the path of every turn, and a stale list is served when it
	// expires, so a slow server costs a pause and not the turn.
	mcpToolsTimeout = 10 * time.Second
	// mcpCallTimeout bounds one tool call. Longer, because the model asked for
	// it and is waiting on the answer.
	mcpCallTimeout = 60 * time.Second
)

// appTranscripts persists the assistant's per-app conversation in the registry as
// one JSON blob, adapting the SQLite store to assistant.Store.
type appTranscripts struct {
	store *store.Store
}

var _ assistant.Store = (*appTranscripts)(nil)

func (t *appTranscripts) Load(app string) ([]assistant.Message, error) {
	blob, err := t.store.LoadAssistantSession(app)
	if err != nil || blob == "" {
		return nil, err
	}
	var messages []assistant.Message
	if err := json.Unmarshal([]byte(blob), &messages); err != nil {
		return nil, err
	}
	return messages, nil
}

func (t *appTranscripts) Save(app string, messages []assistant.Message) error {
	blob, err := json.Marshal(messages)
	if err != nil {
		return err
	}
	return t.store.SaveAssistantSession(app, string(blob))
}

func (t *appTranscripts) Delete(app string) error {
	return t.store.DeleteAssistantSession(app)
}

// RecordUsage accumulates one turn's token usage onto the app's running totals.
func (t *appTranscripts) RecordUsage(app string, u assistant.Usage) error {
	return t.store.AddAssistantUsage(app, store.AssistantUsage{
		InputTokens:      int64(u.InputTokens),
		OutputTokens:     int64(u.OutputTokens),
		CacheWriteTokens: int64(u.CacheWriteTokens),
		CacheReadTokens:  int64(u.CacheReadTokens),
	})
}

// appOps adapts Manager to assistant.AppOps: it turns the assistant's generic
// tool calls into the app manager's operations, scoped to one app. This is the
// only place the assistant package meets the app lifecycle.
type appOps struct {
	apps *Manager  // Control-plane compositions (snapshot listing)
	node NodeAgent // The machine half: files, exec, deploy, snapshots
	// changed is called after every successful mutating tool call (file write,
	// exec, deploy, rollback); it feeds the debounced dashboard screenshot.
	// Optional: nil when nothing cares about changes.
	changed func(name string)
	// server is the control server, for the MCP tools: calling a granted MCP
	// server needs the credential custody that lives on it, and holding the
	// token anywhere closer to the model is the thing this design avoids.
	server *Server
}

// notifyChanged reports a successful mutation to the optional listener.
func (o *appOps) notifyChanged(name string) {
	if o.changed != nil {
		o.changed(name)
	}
}

var _ assistant.AppOps = (*appOps)(nil)

func (o *appOps) ListFiles(name, path string) (string, error) {
	listing, err := o.node.ListFiles(name, path)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s/\n", strings.TrimSuffix(listing.Path, "/"))
	for _, f := range listing.Files {
		if f.Type == FileTypeDir {
			fmt.Fprintf(&b, "  %s/\n", f.Path)
		} else {
			fmt.Fprintf(&b, "  %s (%d bytes)\n", f.Path, f.Size)
		}
	}
	if listing.Truncated {
		b.WriteString("  ... (truncated)\n")
	}
	return b.String(), nil
}

func (o *appOps) ReadFile(name, path string) (string, error) {
	b, err := o.node.ReadFile(name, path)
	if err != nil {
		return "", err
	}
	if len(b) > assistantReadCap {
		return string(b[:assistantReadCap]) + "\n... (truncated)", nil
	}
	return string(b), nil
}

func (o *appOps) WriteFile(name, path, content string) error {
	if err := o.node.WriteFile(name, path, []byte(content), 0); err != nil {
		return err
	}
	o.notifyChanged(name)
	return nil
}

func (o *appOps) Exec(name, command string, timeoutSeconds int) (assistant.ExecResult, error) {
	res, err := o.node.Exec(name, command, secondsToDuration(timeoutSeconds))
	if err != nil {
		return assistant.ExecResult{}, err
	}
	// A command may or may not have mutated anything; assume it did (the
	// debounce and rate limit make over-reporting cheap)
	o.notifyChanged(name)
	return assistant.ExecResult{
		Output:    res.Output,
		ExitCode:  res.ExitCode,
		Truncated: res.Truncated,
		TimedOut:  res.TimedOut,
	}, nil
}

func (o *appOps) Logs(name string, lines int) (string, error) {
	return o.node.Logs(name, lines)
}

func (o *appOps) Deploy(name string) (string, error) {
	out, err := o.node.Up(name)
	if err != nil {
		return "", err
	}
	o.notifyChanged(name)
	return out, nil
}

func (o *appOps) Snapshot(name, label string) (string, error) {
	snap, err := o.node.TakeSnapshot(name, label, false)
	if err != nil {
		return "", err
	}
	return "saved snapshot " + snap.ID, nil
}

func (o *appOps) Rollback(name, id string) (string, error) {
	if err := o.node.Rollback(name, id); err != nil {
		return "", err
	}
	o.notifyChanged(name)
	return "rolled back to " + id, nil
}

func (o *appOps) ListSnapshots(name string) (string, error) {
	snaps, err := o.apps.ListSnapshots(name)
	if err != nil {
		return "", err
	}
	if len(snaps) == 0 {
		return "no snapshots yet", nil
	}
	var b strings.Builder
	for _, s := range snaps {
		kind := "manual"
		if s.Auto {
			kind = "auto"
		}
		fmt.Fprintf(&b, "%s  %s  %s", s.ID, s.CreatedAt.Format("2006-01-02 15:04"), kind)
		if s.Label != "" {
			fmt.Fprintf(&b, "  %q", s.Label)
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

func secondsToDuration(seconds int) time.Duration {
	return time.Duration(seconds) * time.Second
}

// Archived reports whether the app is shelved, so the assistant's prompt can
// say so before the model plans work that will be refused.
func (o *appOps) Archived(name string) bool {
	return o.apps.archived(name)
}

// Connections lists what this app has been granted, so the assistant's prompt
// can name them. It carries no secret -- the model is told the name to ask for,
// and the app reads the credential from its own socket at runtime.
func (o *appOps) Connections(name string) []assistant.Connection {
	a, err := o.apps.App(name)
	if err != nil {
		return nil
	}
	granted, err := o.apps.Store().AppConnections(a.ID)
	if err != nil {
		return nil
	}
	out := make([]assistant.Connection, 0, len(granted))
	for _, c := range granted {
		label := c.Provider
		if p, ok := connections.Lookup(c.Provider); ok {
			label = p.Label
		}
		out = append(out, assistant.Connection{
			Slug: c.Slug, Provider: c.Provider, ProviderLabel: label,
			MCP: c.Kind == store.ConnectionMCP,
		})
	}
	return out
}

// MCPTools are the tools on the MCP servers this app was granted, so the model
// gets them as real tools with the servers' own schemas rather than a paragraph
// telling it to make HTTP requests it cannot see the shape of.
//
// A server that will not answer is skipped rather than failing the turn: one
// unreachable connection must not cost the assistant every other tool it has.
func (o *appOps) MCPTools(name string) []assistant.MCPTool {
	conns, srv := o.mcpConnections(name)
	if srv == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), mcpToolsTimeout)
	defer cancel()
	out := make([]assistant.MCPTool, 0)
	for _, conn := range conns {
		tools, err := srv.connections.mcpTools(ctx, conn, srv.selfMCPClientID())
		if err != nil {
			slog.Warn("Cannot list an MCP server's tools for the assistant", "slug", conn.Slug, "error", err)
			continue
		}
		for _, t := range tools {
			out = append(out, assistant.MCPTool{
				Connection: conn.Slug, Name: t.Name,
				Description: t.Description, InputSchema: t.InputSchema,
			})
		}
	}
	return out
}

// CallMCPTool runs one tool on a granted server. Resolved against the grant
// again here rather than trusting the name the model produced.
func (o *appOps) CallMCPTool(name, connection, tool string, args map[string]any) (string, bool, error) {
	conns, srv := o.mcpConnections(name)
	if srv == nil {
		return "", false, errors.New("connections are not available on this server")
	}
	for _, conn := range conns {
		if conn.Slug != connection {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), mcpCallTimeout)
		defer cancel()
		res, err := srv.connections.mcpCall(ctx, conn, srv.selfMCPClientID(), tool, args)
		if err != nil {
			return "", false, err
		}
		return res.Text, res.IsError, nil
	}
	return "", false, fmt.Errorf("this app has not been granted an MCP server called %q", connection)
}

// mcpConnections is the app's granted MCP connections, and the server that can
// talk to them.
func (o *appOps) mcpConnections(name string) ([]*store.Connection, *Server) {
	if o.server == nil || o.server.connections == nil {
		return nil, nil
	}
	a, err := o.apps.App(name)
	if err != nil {
		return nil, nil
	}
	granted, err := o.apps.Store().AppConnections(a.ID)
	if err != nil {
		return nil, nil
	}
	out := make([]*store.Connection, 0)
	for _, c := range granted {
		if c.Kind == store.ConnectionMCP {
			out = append(out, c)
		}
	}
	return out, o.server
}
