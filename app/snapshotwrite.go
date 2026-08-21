package app

import "strings"

// Writing back into a tenant's hostit.yml. The file belongs to whoever builds
// the app -- their key order, their comments -- so edits are line surgery on
// the document they wrote, never parse-and-remarshal, which would return a
// tidy file with every comment deleted. Same reasoning as SetDescription, one
// level deeper because snapshot: is a block.

const (
	snapshotKey = "snapshot:"
)

// SetSnapshotConfig writes the snapshot block: it updates the keys that have a
// value, drops the ones that do not, and removes the block entirely when
// nothing is left. Everything outside the block is untouched.
func SetSnapshotConfig(content string, hooks SnapshotHooks) string {
	fields := []struct{ key, value string }{
		{"interval", hooks.Interval},
		{"pre", hooks.Pre},
		{"post", hooks.Post},
	}

	lines := strings.Split(content, "\n")
	start, end := snapshotBlock(lines)
	var block []string
	if start >= 0 {
		// Copied, not sliced: appending to a sub-slice of lines would write
		// through into the lines that follow the block and corrupt them.
		block = append(block, lines[start+1:end]...)
	}

	// Update in place where the key already exists, so its position in the
	// block (and any comment above it) is kept.
	for _, f := range fields {
		block = setBlockKey(block, f.key, f.value)
	}

	if len(nonBlank(block)) == 0 {
		if start < 0 {
			return content
		}
		return strings.Join(append(append([]string{}, lines[:start]...), lines[end:]...), "\n")
	}

	rebuilt := append([]string{snapshotKey}, block...)
	if start < 0 {
		// No block yet: append one, keeping the document's trailing newline.
		trimmed := strings.TrimRight(content, "\n")
		if trimmed == "" {
			return strings.Join(rebuilt, "\n") + "\n"
		}
		return trimmed + "\n" + strings.Join(rebuilt, "\n") + "\n"
	}
	out := append([]string{}, lines[:start]...)
	out = append(out, rebuilt...)
	out = append(out, lines[end:]...)
	return strings.Join(out, "\n")
}

// snapshotBlock locates the snapshot: block, returning the index of its header
// and of the first line after it. The block ends at the next line that starts
// in column zero, which is the next top-level key.
func snapshotBlock(lines []string) (start, end int) {
	start = -1
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), snapshotKey) && !strings.HasPrefix(l, " ") {
			start = i
			break
		}
	}
	if start < 0 {
		return -1, -1
	}
	for end = start + 1; end < len(lines); end++ {
		l := lines[end]
		if strings.TrimSpace(l) == "" {
			continue // a blank line inside the block does not end it
		}
		if !strings.HasPrefix(l, " ") && !strings.HasPrefix(l, "\t") {
			break
		}
	}
	// Blank lines trailing the block belong to whatever follows, not to it.
	for end > start+1 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	return start, end
}

// setBlockKey sets (or removes, when value is empty) one key inside a block's
// lines, leaving the rest of the block alone.
func setBlockKey(block []string, key, value string) []string {
	line := "  " + key + ": " + yamlScalar(value)
	for i, l := range block {
		if !strings.HasPrefix(strings.TrimSpace(l), key+":") {
			continue
		}
		if value == "" {
			return append(append([]string{}, block[:i]...), block[i+1:]...)
		}
		block[i] = line
		return block
	}
	if value == "" {
		return block
	}
	return append(block, line)
}

// yamlScalar renders a value as YAML, quoting only when a plain scalar would
// not survive the round trip. Hook commands are shell, and quoting every one of
// them would leave the tenant's file full of escaping they did not write.
func yamlScalar(s string) string {
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s[:1], `-?:,[]{}#&*!|>'"%@`+"`") ||
		strings.Contains(s, ": ") || strings.Contains(s, " #") ||
		strings.TrimSpace(s) != s {
		return yamlQuote(s)
	}
	return s
}

// nonBlank drops empty lines, so "is this block empty" ignores whitespace.
func nonBlank(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}
