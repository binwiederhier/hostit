package podman

import (
	"encoding/json"
	"strconv"
	"strings"
)

// Stat is one container's live resource consumption, from a single stats call.
type Stat struct {
	Name       string
	MemoryMB   int
	CPUPercent int
}

// ParseStats decodes one "podman stats" JSON snapshot (see Stats) into whole-MB
// memory and whole-percent CPU per container; the caller maps the container
// names back onto whatever it keys on.
//
// podman prints lowercase snake_case keys, e.g.
//
//	{"name":"hostit-app-blog","cpu_percent":"3.70%","mem_usage":"4.633MB / 536.9MB"}
func ParseStats(out string) ([]Stat, error) {
	var stats []struct {
		Name       string `json:"name"`
		MemUsage   string `json:"mem_usage"`
		CPUPercent string `json:"cpu_percent"`
	}
	if err := json.Unmarshal([]byte(out), &stats); err != nil {
		return nil, err
	}
	parsed := make([]Stat, 0, len(stats))
	for _, stat := range stats {
		parsed = append(parsed, Stat{Name: stat.Name, MemoryMB: parseMemMB(stat.MemUsage), CPUPercent: parseCPUPercent(stat.CPUPercent)})
	}
	return parsed, nil
}

// parseCPUPercent turns podman's "3.70%" into whole percent, rounded. It can be
// over 100 for a container using more than one core, which is fine to report.
func parseCPUPercent(cpu string) int {
	value := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(cpu), "%"))
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0
	}
	return int(parsed + 0.5)
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
