package assistant

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildUserContentImage(t *testing.T) {
	t.Parallel()
	content, display := buildUserContent("look at this", []Attachment{
		{Path: "uploads/shot.png", MediaType: "image/png", Data: "QUJD"},
	})
	require.Len(t, content, 2) // image block + text block
	assert.Equal(t, "image", content[0].Type)
	require.NotNil(t, content[0].Source)
	assert.Equal(t, "base64", content[0].Source.Type)
	assert.Equal(t, "image/png", content[0].Source.MediaType)
	assert.Equal(t, "QUJD", content[0].Source.Data)
	assert.Equal(t, "text", content[1].Type)
	assert.Contains(t, content[1].Text, "look at this")
	assert.Contains(t, content[1].Text, "uploads/shot.png")
	assert.Contains(t, display, "uploads/shot.png")
}

func TestBuildUserContentNonImageIsReferencedOnly(t *testing.T) {
	t.Parallel()
	content, _ := buildUserContent("here is data", []Attachment{
		{Path: "uploads/data.csv", MediaType: "text/csv"},
	})
	require.Len(t, content, 1) // no image block, only text
	assert.Equal(t, "text", content[0].Type)
	assert.Contains(t, content[0].Text, "uploads/data.csv")
}

func TestBuildUserContentNoAttachments(t *testing.T) {
	t.Parallel()
	content, display := buildUserContent("hi", nil)
	require.Len(t, content, 1)
	assert.Equal(t, "hi", content[0].Text)
	assert.Equal(t, "hi", display)
}
