package assistant

import (
	"encoding/json"
	"fmt"
	"strings"
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
}

// toolDefs describes the tools to the model. The schemas are deliberately small:
// a path, some content, a command. Everything else the model discovers by using
// them (list_files, then read hostit.yml).
func toolDefs() []Tool {
	return []Tool{
		{
			Name:        "list_files",
			Description: "List the files in a directory of the app's home (relative path, defaults to the root). Use this to see the app's layout before reading or writing.",
			InputSchema: schema(`{"type":"object","properties":{"path":{"type":"string","description":"Directory relative to the app home, e.g. \"public\". Empty for the root."}}}`),
		},
		{
			Name:        "read_file",
			Description: "Read a UTF-8 text file from the app's home, by path relative to the home (e.g. \"hostit.yml\" or \"public/index.html\").",
			InputSchema: schema(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
		},
		{
			Name:        "write_file",
			Description: "Create or overwrite a text file in the app's home. Parent directories are created. This is how you change the app; a static app serves public/ and a run: app is described by hostit.yml.",
			InputSchema: schema(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"]}`),
		},
		{
			Name:        "run_command",
			Description: "Run a shell command inside the app's container (as the app user, in its home). Use it to build, install dependencies, or inspect. Returns stdout+stderr, the exit code, and whether it timed out.",
			InputSchema: schema(`{"type":"object","properties":{"command":{"type":"string"},"timeout_seconds":{"type":"integer","description":"Optional; 0 for the default."}},"required":["command"]}`),
		},
		{
			Name:        "read_logs",
			Description: "Read the tail of the app's runtime log (the output of its run: command). Use this to debug why a running app misbehaves.",
			InputSchema: schema(`{"type":"object","properties":{"lines":{"type":"integer","description":"How many trailing lines; 0 for the default."}}}`),
		},
		{
			Name:        "deploy",
			Description: "Apply hostit.yml and (re)start the app: build if needed, recreate the container if its configuration (mode, run command, env) changed, and serve the latest files. Run this after you change hostit.yml or a run: app's code, so the change goes live. A static app already serving public/ does not need this for content edits.",
			InputSchema: schema(`{"type":"object","properties":{}}`),
		},
	}
}

// dispatch runs one tool call against the app and returns the result text the
// model sees, plus whether it was an error. A tool error is reported to the model
// (isError), not returned up: the model is expected to read it and adapt, exactly
// as it would a failed shell command.
func (m *Manager) dispatch(app, name string, input json.RawMessage) (string, bool) {
	switch name {
	case "list_files":
		var in struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(input, &in)
		out, err := m.ops.ListFiles(app, in.Path)
		return orError(out, err)
	case "read_file":
		var in struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(input, &in); err != nil || in.Path == "" {
			return "path is required", true
		}
		out, err := m.ops.ReadFile(app, in.Path)
		return orError(out, err)
	case "write_file":
		var in struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(input, &in); err != nil || in.Path == "" {
			return "path is required", true
		}
		if err := m.ops.WriteFile(app, in.Path, in.Content); err != nil {
			return err.Error(), true
		}
		return fmt.Sprintf("wrote %d bytes to %s", len(in.Content), in.Path), false
	case "run_command":
		var in struct {
			Command        string `json:"command"`
			TimeoutSeconds int    `json:"timeout_seconds"`
		}
		if err := json.Unmarshal(input, &in); err != nil || strings.TrimSpace(in.Command) == "" {
			return "command is required", true
		}
		res, err := m.ops.Exec(app, in.Command, in.TimeoutSeconds)
		if err != nil {
			return err.Error(), true
		}
		return formatExec(res), res.ExitCode != 0
	case "read_logs":
		var in struct {
			Lines int `json:"lines"`
		}
		_ = json.Unmarshal(input, &in)
		out, err := m.ops.Logs(app, in.Lines)
		return orError(out, err)
	case "deploy":
		out, err := m.ops.Deploy(app)
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
