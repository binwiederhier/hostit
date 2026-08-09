package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"heckel.io/hostit/app"
	"heckel.io/hostit/assistant"
	"heckel.io/hostit/store"
)

const (
	// assistantReadCap bounds a file the assistant reads, so one huge file cannot
	// blow up the model's context
	assistantReadCap = 128 * 1024
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

// appOps adapts app.Manager to assistant.AppOps: it turns the assistant's generic
// tool calls into the app manager's operations, scoped to one app. This is the
// only place the assistant package meets the app package.
type appOps struct {
	apps *app.Manager
}

var _ assistant.AppOps = (*appOps)(nil)

func (o *appOps) ListFiles(name, path string) (string, error) {
	listing, err := o.apps.ListFiles(name, path)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s/\n", strings.TrimSuffix(listing.Path, "/"))
	for _, f := range listing.Files {
		if f.Type == app.FileTypeDir {
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
	b, err := o.apps.ReadFile(name, path)
	if err != nil {
		return "", err
	}
	if len(b) > assistantReadCap {
		return string(b[:assistantReadCap]) + "\n... (truncated)", nil
	}
	return string(b), nil
}

func (o *appOps) WriteFile(name, path, content string) error {
	return o.apps.WriteFile(name, path, []byte(content), 0)
}

func (o *appOps) Exec(name, command string, timeoutSeconds int) (assistant.ExecResult, error) {
	res, err := o.apps.Exec(name, command, secondsToDuration(timeoutSeconds))
	if err != nil {
		return assistant.ExecResult{}, err
	}
	return assistant.ExecResult{
		Output:    res.Output,
		ExitCode:  res.ExitCode,
		Truncated: res.Truncated,
		TimedOut:  res.TimedOut,
	}, nil
}

func (o *appOps) Logs(name string, lines int) (string, error) {
	return o.apps.Logs(name, lines)
}

func (o *appOps) Deploy(name string) (string, error) {
	return o.apps.Up(name)
}

func (o *appOps) Snapshot(name, label string) (string, error) {
	snap, err := o.apps.TakeSnapshot(name, label, false)
	if err != nil {
		return "", err
	}
	return "saved snapshot " + snap.ID, nil
}

func (o *appOps) Rollback(name, id string) (string, error) {
	if err := o.apps.Rollback(name, id); err != nil {
		return "", err
	}
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
