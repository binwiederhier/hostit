package node

import (
	"fmt"
	"strconv"
)

// systemLogLines is how many journal lines the admin logs view fetches by
// default; enough to see a recent incident without shipping the whole journal.
const systemLogLines = 400

// SystemLogs returns the node's own systemd journal (the hostit-node unit), for
// the admin logs view. It is the machine's log, distinct from an app's log
// (Logs), and needs the node's journal read privilege -- which the node has,
// running as root. A journalctl that is absent or refuses is surfaced as an
// error rather than an empty log, so the UI can say why.
func (m *Machine) SystemLogs(lines int) (string, error) {
	if lines <= 0 {
		lines = systemLogLines
	}
	unit := m.config.LogUnitName()
	out, err := m.runner.Run("journalctl", "-u", unit, "-n", strconv.Itoa(lines), "--no-pager", "-o", "short-iso")
	if err != nil {
		return "", fmt.Errorf("cannot read the node journal for unit %q: %w", unit, err)
	}
	return out, nil
}
