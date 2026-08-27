package control

import "strings"

// The app-detail tabs a user may choose to show or hide. Order here is the
// canonical render order; the always-on tabs (connections, settings, snapshots)
// are not user-toggleable and are not in this set.
var toggleableTabs = []string{"assistant", "files", "terminal", "logs"}

// normalizeTabs cleans a comma-separated tab set to canonical form: only known
// keys, deduped, in canonical order. It enforces the one invariant the UI also
// promises -- there is always a primary pane: if the set names neither the
// assistant nor files, files is added; and when the instance has no assistant,
// the assistant tab cannot show, so it is dropped and files is guaranteed on.
// An empty input returns empty, meaning "no override -- use the built-in default".
func normalizeTabs(csv string, assistantEnabled bool) string {
	want := make(map[string]bool)
	explicit := false
	for _, part := range strings.Split(csv, ",") {
		key := strings.TrimSpace(part)
		if key != "" {
			explicit = true
		}
		want[key] = true
	}
	// A truly empty input is "no override" and stays empty; an input that named
	// something but resolves to nothing (e.g. only the assistant, which is off)
	// is still an explicit choice and gets the files fallback below.
	if !explicit {
		return ""
	}
	out := make([]string, 0, len(toggleableTabs))
	for _, key := range toggleableTabs {
		if !want[key] {
			continue
		}
		if key == "assistant" && !assistantEnabled {
			continue // no assistant configured -> the tab cannot exist
		}
		out = append(out, key)
	}
	// Guarantee a primary pane. The assistant counts as one only when enabled.
	hasPrimary := (assistantEnabled && contains(out, "assistant")) || contains(out, "files")
	if !hasPrimary {
		out = append([]string{"files"}, out...)
	}
	return strings.Join(out, ",")
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
