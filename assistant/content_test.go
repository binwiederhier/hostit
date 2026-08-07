package assistant

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildUserContentImage(t *testing.T) {
	t.Parallel()
	content, display := buildUserContent("look at this", []Attachment{
		{Path: "uploads/shot.png", MediaType: "image/png", Data: "QUJD"},
	})
	require.Len(t, content, 3) // image block + user text + note block
	assert.Equal(t, "image", content[0].Type)
	require.NotNil(t, content[0].Source)
	assert.Equal(t, "base64", content[0].Source.Type)
	assert.Equal(t, "image/png", content[0].Source.MediaType)
	assert.Equal(t, "QUJD", content[0].Source.Data)
	assert.Equal(t, "look at this", content[1].Text) // user's own text, no note
	assert.True(t, strings.HasPrefix(content[2].Text, attachmentNotePrefix))
	assert.Contains(t, content[2].Text, "uploads/shot.png")
	// The display is the user's text only -- the note never reaches the transcript.
	assert.Equal(t, "look at this", display)
	assert.NotContains(t, display, "uploads/shot.png")
}

func TestBuildUserContentNonImageIsReferencedOnly(t *testing.T) {
	t.Parallel()
	content, _ := buildUserContent("here is data", []Attachment{
		{Path: "uploads/data.csv", MediaType: "text/csv"},
	})
	require.Len(t, content, 2) // no image block: user text + note block
	assert.Equal(t, "here is data", content[0].Text)
	assert.True(t, strings.HasPrefix(content[1].Text, attachmentNotePrefix))
	assert.Contains(t, content[1].Text, "uploads/data.csv")
}

func TestBuildUserContentNoAttachments(t *testing.T) {
	t.Parallel()
	content, display := buildUserContent("hi", nil)
	require.Len(t, content, 1)
	assert.Equal(t, "hi", content[0].Text)
	assert.Equal(t, "hi", display)
}

// The attachment note is stripped from the reloaded transcript display.
func TestToItemsSkipsAttachmentNote(t *testing.T) {
	t.Parallel()
	content, _ := buildUserContent("look", []Attachment{{Path: "uploads/a.png", MediaType: "image/png", Data: "QQ=="}})
	items := toItems([]Message{{Role: "user", Content: content}})
	require.Len(t, items, 1)
	assert.Equal(t, "user", items[0].Kind)
	assert.Equal(t, "look", items[0].Text)
}
