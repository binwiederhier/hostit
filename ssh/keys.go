// Package ssh manages an app user's authorized_keys (the hostit-managed block) and
// validates SSH public keys. It writes into the app's home through an os.Root so a
// tenant cannot redirect the root daemon's writes with a symlink.
package ssh

import "strings"

const (
	// managedBeginMarker and managedEndMarker delimit the block of authorized_keys
	// that hostit owns. Anything outside it was put there by hand and is kept.
	managedBeginMarker = "# BEGIN hostit-managed keys -- edits inside this block are overwritten"
	managedEndMarker   = "# END hostit-managed keys"
)

// MergeAuthorizedKeys replaces the hostit-managed block in an authorized_keys
// file while preserving everything around it, so a key someone scp'd in by hand
// survives every profile change hostit makes
func MergeAuthorizedKeys(existing string, managed []string) string {
	isManaged := make(map[string]bool, len(managed))
	for _, key := range managed {
		isManaged[keyMaterial(key)] = true
	}
	var kept []string
	inManaged := false
	for _, line := range strings.Split(existing, "\n") {
		switch {
		case strings.HasPrefix(line, managedBeginMarker):
			inManaged = true
		case strings.HasPrefix(line, managedEndMarker):
			inManaged = false
		case inManaged || strings.TrimSpace(line) == "":
			// Inside the block, or blank: dropped and rewritten below
		case isManaged[keyMaterial(line)]:
			// A key hostit manages, left over from before the block existed or
			// pasted in by hand; keep only the managed copy
		default:
			kept = append(kept, line)
		}
	}
	var b strings.Builder
	for _, line := range kept {
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString(managedBeginMarker)
	b.WriteString("\n")
	for _, key := range managed {
		if strings.TrimSpace(key) == "" {
			continue
		}
		b.WriteString(strings.TrimSpace(key))
		b.WriteString("\n")
	}
	b.WriteString(managedEndMarker)
	b.WriteString("\n")
	return b.String()
}

// keyMaterial reduces an authorized_keys line to type+key, so the same key with
// a different comment is recognised as the same key
func keyMaterial(line string) string {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) < 2 {
		return strings.TrimSpace(line)
	}
	return fields[0] + " " + fields[1]
}
