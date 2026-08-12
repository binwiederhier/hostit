package assistant

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	// Tool registry names: each tool the model may call. Defined in toolDefs and
	// matched in DispatchTool's switch; the names also tag which tool ran on the
	// event stream, so they are a stable contract shared by both backends.
	toolListFiles      = "list_files"
	toolReadFile       = "read_file"
	toolWriteFile      = "write_file"
	toolRunCommand     = "run_command"
	toolReadLogs       = "read_logs"
	toolDeploy         = "deploy"
	toolRefreshPreview = "refresh_preview"
	toolSnapshot       = "snapshot"
	toolListSnapshots  = "list_snapshots"
	toolRollback       = "rollback"
)

// AppOps is what the assistant can do to an app. It is exactly the app's own REST
// surface -- read and write files, run a command in the container, read logs --
// so the model is confined to the one app the same way a pasted agent token is.
// The server implements this over app.Manager; tests use a fake.
type AppOps interface {
	ListFiles(app, path string) (string, error)
	ReadFile(app, path string) (string, error)
	WriteFile(app, path, content string) error
	Exec(app, command string, timeoutSeconds int) (ExecResult, error)
	Logs(app string, lines int) (string, error)
	Deploy(app string) (string, error)
	Snapshot(app, label string) (string, error)
	ListSnapshots(app string) (string, error)
	Rollback(app, id string) (string, error)
}

// ToolDefs exposes the tool definitions for another driver of the same app
// operations -- the sandboxed Claude Max backend advertises these same tools over
// MCP, so the model sees an identical surface whichever backend runs it.
func ToolDefs() []Tool {
	return toolDefs()
}

// toolDefs describes the tools to the model. The schemas are deliberately small:
// a path, some content, a command. Everything else the model discovers by using
// them (list_files, then read hostit.yml).
func toolDefs() []Tool {
	return []Tool{
		{
			Name:        toolListFiles,
			Description: "List the files in a directory of the app's home (relative path, defaults to the root). Use this to see the app's layout before reading or writing.",
			InputSchema: schema(`{"type":"object","properties":{"path":{"type":"string","description":"Directory relative to the app home, e.g. \"public\". Empty for the root."}}}`),
		},
		{
			Name:        toolReadFile,
			Description: "Read a UTF-8 text file from the app's home, by path relative to the home (e.g. \"hostit.yml\" or \"public/index.html\").",
			InputSchema: schema(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
		},
		{
			Name:        toolWriteFile,
			Description: "Create or overwrite a text file in the app's home. Parent directories are created. This is how you change the app; a static app serves public/ and a run: app is described by hostit.yml.",
			InputSchema: schema(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"]}`),
		},
		{
			Name:        toolRunCommand,
			Description: "Run a shell command inside the app's container (as the app user, in its home). Use it to build, install dependencies, or inspect. Returns stdout+stderr, the exit code, and whether it timed out.",
			InputSchema: schema(`{"type":"object","properties":{"command":{"type":"string"},"timeout_seconds":{"type":"integer","description":"Optional; 0 for the default."}},"required":["command"]}`),
		},
		{
			Name:        toolReadLogs,
			Description: "Read the tail of the app's runtime log (the output of its run: command). Use this to debug why a running app misbehaves.",
			InputSchema: schema(`{"type":"object","properties":{"lines":{"type":"integer","description":"How many trailing lines; 0 for the default."}}}`),
		},
		{
			Name:        toolDeploy,
			Description: "Apply hostit.yml and (re)start the app: build if needed, recreate the container if its configuration (mode, run command, env) changed, and serve the latest files. Run this after you change hostit.yml or a run: app's code, so the change goes live. A static app already serving public/ does not need this for content edits.",
			InputSchema: schema(`{"type":"object","properties":{}}`),
		},
		{
			Name:        toolRefreshPreview,
			Description: "Reload the live preview shown next to this chat in the owner's browser. Call this after a content change that does not need a deploy (e.g. editing a static file the app already serves) so the owner sees the result. A deploy reloads the preview on its own; use this only when nothing else would.",
			InputSchema: schema(`{"type":"object","properties":{}}`),
		},
		{
			Name:        toolSnapshot,
			Description: "Save a restorable snapshot of the app's files right now. Take one before any risky change, and periodically as you make progress, so there is always a recent point to roll back to. Always pass a short one-line label saying why you took it (what you are about to do, or what you just finished). (Snapshots are also taken automatically before every deploy and hourly.)",
			InputSchema: schema(`{"type":"object","properties":{"label":{"type":"string","description":"Short one-line reason for this snapshot, e.g. \"before rewriting the router\" or \"finished the login page\""}},"required":["label"]}`),
		},
		{
			Name:        toolListSnapshots,
			Description: "List the app's snapshots (id, time and label), newest first, so you can pick one to roll back to.",
			InputSchema: schema(`{"type":"object","properties":{}}`),
		},
		{
			Name:        toolRollback,
			Description: "Roll the app back to a snapshot, restoring its files to that point. A snapshot of the current state is taken first, so this is reversible (you can roll forward again). Get the id from list_snapshots first.",
			InputSchema: schema(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`),
		},
	}
}

// dispatch runs one tool call against the app and returns the result text the
// model sees, plus whether it was an error. A tool error is reported to the model
// (isError), not returned up: the model is expected to read it and adapt, exactly
// as it would a failed shell command.
func (m *Manager) dispatch(app, name string, input json.RawMessage) (string, bool) {
	return DispatchTool(m.ops, app, name, input)
}

// DispatchTool runs one tool call against an app through the given AppOps and
// returns the model-facing result text plus whether it was an error. It is the
// single place tool calls are executed, shared by the built-in API loop (via
// Manager.dispatch) and the sandboxed Claude Max backend (via the MCP server), so
// both backends behave identically. refresh_preview is a UI-only signal handled
// by the caller that has a UI; a headless backend simply will not advertise it.
func DispatchTool(ops AppOps, app, name string, input json.RawMessage) (string, bool) {
	switch name {
	case toolListFiles:
		var in struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(input, &in)
		out, err := ops.ListFiles(app, in.Path)
		return orError(out, err)
	case toolReadFile:
		var in struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(input, &in); err != nil || in.Path == "" {
			return "path is required", true
		}
		out, err := ops.ReadFile(app, in.Path)
		return orError(out, err)
	case toolWriteFile:
		var in struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(input, &in); err != nil || in.Path == "" {
			return "path is required", true
		}
		if err := ops.WriteFile(app, in.Path, in.Content); err != nil {
			return err.Error(), true
		}
		return fmt.Sprintf("wrote %d bytes to %s", len(in.Content), in.Path), false
	case toolRunCommand:
		var in struct {
			Command        string `json:"command"`
			TimeoutSeconds int    `json:"timeout_seconds"`
		}
		if err := json.Unmarshal(input, &in); err != nil || strings.TrimSpace(in.Command) == "" {
			return "command is required", true
		}
		res, err := ops.Exec(app, in.Command, in.TimeoutSeconds)
		if err != nil {
			return err.Error(), true
		}
		return formatExec(res), res.ExitCode != 0
	case toolReadLogs:
		var in struct {
			Lines int `json:"lines"`
		}
		_ = json.Unmarshal(input, &in)
		out, err := ops.Logs(app, in.Lines)
		return orError(out, err)
	case toolDeploy:
		out, err := ops.Deploy(app)
		return orError(out, err)
	case toolRefreshPreview:
		// A UI-only signal: the tool call itself, carried on the event stream, tells
		// the owner's browser to reload the live preview. Nothing to do server-side.
		return "the live preview has been reloaded in the owner's browser", false
	case toolSnapshot:
		var in struct {
			Label string `json:"label"`
		}
		_ = json.Unmarshal(input, &in)
		out, err := ops.Snapshot(app, in.Label)
		return orError(out, err)
	case toolListSnapshots:
		out, err := ops.ListSnapshots(app)
		return orError(out, err)
	case toolRollback:
		var in struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(input, &in); err != nil || in.ID == "" {
			return "id is required", true
		}
		out, err := ops.Rollback(app, in.ID)
		return orError(out, err)
	default:
		return "unknown tool: " + name, true
	}
}

// formatExec renders a command result for the model: the output, then a footer
// with the exit code and any timeout, so the model knows whether it succeeded.
func formatExec(res ExecResult) string {
	var b strings.Builder
	b.WriteString(res.Output)
	if !strings.HasSuffix(res.Output, "\n") && res.Output != "" {
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "[exit code %d", res.ExitCode)
	if res.TimedOut {
		b.WriteString(", timed out")
	}
	if res.Truncated {
		b.WriteString(", output truncated")
	}
	b.WriteString("]")
	return b.String()
}

func orError(out string, err error) (string, bool) {
	if err != nil {
		return err.Error(), true
	}
	return out, false
}

// schema is a tiny helper to keep the tool definitions readable
func schema(s string) json.RawMessage {
	return json.RawMessage(s)
}
