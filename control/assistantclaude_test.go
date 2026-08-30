package control

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"heckel.io/hostit/assistant"
	"heckel.io/hostit/node/api"
)

// fakeClaudeNode is a NodeAgent that records the assistant specs it is handed and
// plays back a canned turn. Embedding the interface leaves every other verb a
// nil-panic, which these tests never call.
type fakeClaudeNode struct {
	NodeAgent
	gotTurn   *api.AssistantTurnSpec
	gotAnswer *api.AssistantAnswerSpec
}

func (f *fakeClaudeNode) RunAssistantTurn(_ context.Context, spec *api.AssistantTurnSpec, onEvent func(*api.AssistantEvent)) error {
	f.gotTurn = spec
	onEvent(&api.AssistantEvent{Type: "text", Text: "working"})
	onEvent(&api.AssistantEvent{Type: "tool_use", Tool: "write_file", Input: `{"path":"x"}`})
	onEvent(&api.AssistantEvent{Type: "tool_result", Output: "done"})
	onEvent(&api.AssistantEvent{Type: "result", Usage: &api.AssistantUsage{InputTokens: 10, OutputTokens: 5}})
	return nil
}

func (f *fakeClaudeNode) AnswerAssistant(_ context.Context, spec *api.AssistantAnswerSpec) (string, *api.AssistantUsage, error) {
	f.gotAnswer = spec
	return "the answer", &api.AssistantUsage{InputTokens: 3, OutputTokens: 7}, nil
}

// A turn is run on the app's NODE, carrying the subscription token control holds
// (never stored on a node) and named for the app so routing reaches the right
// machine -- so a turn works for an app on ANY node, without control needing a
// unix account for it. This replaces the old registry-identity test: identity is
// now resolved on the node, and this pins the control-side contract.
func TestClaudeBackendRoutesTurnToTheNode(t *testing.T) {
	t.Parallel()
	node := &fakeClaudeNode{}
	backend := &claudeBackend{node: node, token: "sk-subscription"}

	var events []assistant.Event
	usage, err := backend.RunTurn(context.Background(), "elsewhere", "do the thing", "you work on a hostit app",
		[]assistant.Attachment{{MediaType: "image/png", Data: "aGk="}},
		func(ev assistant.Event) { events = append(events, ev) })
	require.NoError(t, err)

	// The spec reaches the node with the app name, the prompt, the system prompt,
	// the image and the token control holds.
	require.NotNil(t, node.gotTurn)
	assert.Equal(t, "elsewhere", node.gotTurn.Name)
	assert.Equal(t, "do the thing", node.gotTurn.Prompt)
	assert.Equal(t, "you work on a hostit app", node.gotTurn.SystemPrompt)
	assert.Equal(t, "sk-subscription", node.gotTurn.OAuthToken, "control passes the token down per turn")
	require.Len(t, node.gotTurn.Images, 1)
	assert.Equal(t, "image/png", node.gotTurn.Images[0].MediaType)

	// The node's events map to the assistant's own events, and usage surfaces.
	var kinds []string
	for _, ev := range events {
		kinds = append(kinds, ev.Type)
	}
	assert.Equal(t, []string{"text", "tool_use", "tool_result", "usage"}, kinds)
	assert.Equal(t, 10, usage.InputTokens)
	assert.Equal(t, 5, usage.OutputTokens)
}

// Answer is the one-shot, tool-less path; it too runs on the node and carries the
// token, the model and the app's system prompt.
func TestClaudeBackendAnswerCarriesTokenAndModel(t *testing.T) {
	t.Parallel()
	node := &fakeClaudeNode{}
	backend := &claudeBackend{node: node, token: "sk-subscription"}

	text, usage, err := backend.Answer(context.Background(), "app1", "claude-opus-5", "be brief", "what is 2+2?")
	require.NoError(t, err)
	assert.Equal(t, "the answer", text)
	assert.Equal(t, 3, usage.InputTokens)

	require.NotNil(t, node.gotAnswer)
	assert.Equal(t, "app1", node.gotAnswer.Name)
	assert.Equal(t, "claude-opus-5", node.gotAnswer.Model)
	assert.Equal(t, "be brief", node.gotAnswer.System)
	assert.Equal(t, "what is 2+2?", node.gotAnswer.Prompt)
	assert.Equal(t, "sk-subscription", node.gotAnswer.OAuthToken)
}
