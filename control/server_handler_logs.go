package control

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"time"
)

// Admin-only logs: the control process's own journal and each node's journal,
// so an operator can look at both from the admin page. These are distinct from
// an app's log (server_handler_agent.go): those are the app's output; these are
// hostit's own machine logs.

// systemLogLines is the default number of journal lines returned.
const controlLogLines = 400

// readJournal returns the last `lines` of a systemd unit's journal. It shells
// out to journalctl (bounded), so a host without systemd, or a unit that does
// not exist, surfaces as an error the UI can show rather than an empty log.
func readJournal(unit string, lines int) (string, error) {
	if lines <= 0 {
		lines = controlLogLines
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "journalctl", "-u", unit, "-n", strconv.Itoa(lines), "--no-pager", "-o", "short-iso").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("cannot read the journal for unit %q: %w (%s)", unit, err, string(out))
	}
	return string(out), nil
}

func logLinesParam(r *http.Request) int {
	if n, err := strconv.Atoi(r.URL.Query().Get("lines")); err == nil && n > 0 {
		return n
	}
	return controlLogLines
}

// handleControlLogs returns control's own journal (the hostit-control unit).
func (s *Server) handleControlLogs(w http.ResponseWriter, r *http.Request, _ *caller) {
	text, err := readJournal(s.config.LogUnitName(), logLinesParam(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiLogsResponse{Source: "control", Unit: s.config.LogUnitName(), Text: text})
}

// handleNodeLogs returns one node's own journal, routed to that node's agent
// (local or remote) through the registry.
func (s *Server) handleNodeLogs(w http.ResponseWriter, r *http.Request, _ *caller) {
	node := r.PathValue("node")
	agent := s.apps.NodeRegistry().Agent(node)
	if agent == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("node %q is not connected", node))
		return
	}
	sl, ok := agent.(interface {
		SystemLogs(lines int) (string, error)
	})
	if !ok {
		writeError(w, http.StatusNotImplemented, fmt.Errorf("node %q does not support system logs", node))
		return
	}
	text, err := sl.SystemLogs(logLinesParam(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiLogsResponse{Source: "node", Node: node, Text: text})
}
