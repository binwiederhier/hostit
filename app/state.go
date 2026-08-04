package app

import (
	"encoding/json"
	"strconv"
	"strings"
)

// State is what an app is doing right now: whether its service is up and how
// much memory its container is using
type State struct {
	Running  bool `json:"running"`
	MemoryMB int  `json:"memory_mb"`
}

// States returns the live state of the given apps. Both podman and systemd are
// asked once for all of them rather than once per app, so listing a dashboard
// costs two commands regardless of how many apps exist.
func (m *Manager) States(names []string) map[string]State {
	states := make(map[string]State, len(names))
	if len(names) == 0 {
		return states
	}
	for name, running := range m.runningStates(names) {
		states[name] = State{Running: running}
	}
	for name, memoryMB := range m.memoryUsage() {
		state := states[name]
		state.MemoryMB = memoryMB
		states[name] = state
	}
	return states
}

// runningStates asks systemd about every app's unit in one call; "systemctl
// is-active" prints one line per unit, in order
func (m *Manager) runningStates(names []string) map[string]bool {
	args := []string{"systemctl", "is-active"}
	for _, name := range names {
		args = append(args, unitName(name))
	}
	out, _ := m.runner.Run(args...) // Non-zero exit just means "something is inactive"
	lines := strings.Split(strings.TrimSpace(out), "\n")
	running := make(map[string]bool, len(names))
	for i, name := range names {
		running[name] = i < len(lines) && strings.TrimSpace(lines[i]) == "active"
	}
	return running
}

// memoryUsage reads current container memory from one podman stats call
func (m *Manager) memoryUsage() map[string]int {
	usage := make(map[string]int)
	out, err := m.runner.Run("podman", "stats", "--no-stream", "--format", "json")
	if err != nil {
		return usage
	}
	var stats []struct {
		Name          string `json:"Name"`
		MemUsage      string `json:"MemUsage"` // e.g. "12.3MB / 512MB"
		MemUsageBytes any    `json:"MemUsageBytes"`
	}
	if err := json.Unmarshal([]byte(out), &stats); err != nil {
		return usage
	}
	for _, stat := range stats {
		name := strings.TrimPrefix(stat.Name, containerPrefix)
		if name == stat.Name {
			continue // Not one of ours
		}
		usage[name] = parseMemMB(stat.MemUsage)
	}
	return usage
}

// parseMemMB turns podman's "12.3MB / 512MB" into whole megabytes
func parseMemMB(memUsage string) int {
	value := strings.TrimSpace(strings.Split(memUsage, "/")[0])
	multiplier := 1.0
	switch {
	case strings.HasSuffix(value, "GB"):
		multiplier, value = 1024, strings.TrimSuffix(value, "GB")
	case strings.HasSuffix(value, "MB"):
		multiplier, value = 1, strings.TrimSuffix(value, "MB")
	case strings.HasSuffix(value, "kB"):
		multiplier, value = 1.0/1024, strings.TrimSuffix(value, "kB")
	case strings.HasSuffix(value, "B"):
		multiplier, value = 1.0/(1024*1024), strings.TrimSuffix(value, "B")
	}
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return 0
	}
	return int(parsed * multiplier)
}
