package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChatRoomKeepsOnlyTheLastMessages(t *testing.T) {
	t.Parallel()
	room := &chatRoom{}
	for i := 0; i < maxChatMessages+50; i++ {
		room.post("u", "msg")
	}
	msgs := room.list()
	assert.Len(t, msgs, maxChatMessages)
}

func TestChatRoomTruncatesAndRejectsEmpty(t *testing.T) {
	t.Parallel()
	room := &chatRoom{}
	_, ok := room.post("someone", "   ")
	assert.False(t, ok, "a blank message is refused")

	msg, ok := room.post(strings.Repeat("n", 200), strings.Repeat("t", 999))
	require.True(t, ok)
	assert.Len(t, []rune(msg.Name), maxChatName)
	assert.Len(t, []rune(msg.Text), maxChatText)

	blank, ok := room.post("", "hi")
	require.True(t, ok)
	assert.Equal(t, "anon", blank.Name, "a missing name defaults to anon")
}

func TestPlaceholderHandlerServesPageAndChat(t *testing.T) {
	t.Parallel()
	h := placeholderHandler()

	// The page is served at the root
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Header().Get("Content-Type"), "text/html")
	assert.Contains(t, rr.Body.String(), "placeholder app")

	// Posting a message, then reading it back
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/chat", strings.NewReader(`{"name":"ann","text":"hello"}`)))
	require.Equal(t, http.StatusOK, rr.Code)

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/chat", nil))
	require.Equal(t, http.StatusOK, rr.Code)
	var msgs []chatMessage
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &msgs))
	require.Len(t, msgs, 1)
	assert.Equal(t, "ann", msgs[0].Name)
	assert.Equal(t, "hello", msgs[0].Text)

	// An empty message is a bad request, not a stored blank
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/chat", strings.NewReader(`{"name":"x","text":""}`)))
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}
