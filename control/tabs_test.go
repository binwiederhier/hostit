package control

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeTabs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name             string
		in               string
		assistantEnabled bool
		want             string
	}{
		{"empty stays empty (no override)", "", true, ""},
		{"canonical order + dedupe", "logs,files,files,assistant", true, "assistant,files,logs"},
		{"unknown keys dropped", "files,bogus,settings", true, "files"},
		{"neither assistant nor files forces files", "terminal,logs", true, "files,terminal,logs"},
		{"assistant alone is allowed when enabled", "assistant", true, "assistant"},
		{"assistant dropped and files forced when disabled", "assistant,terminal", false, "files,terminal"},
		{"assistant-only becomes files when disabled", "assistant", false, "files"},
		{"whitespace tolerated", " files , logs ", true, "files,logs"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, normalizeTabs(tc.in, tc.assistantEnabled))
		})
	}
}
