package node

import (
	"context"

	"heckel.io/hostit/node/api"
)

// RunAssistantTurn runs one Claude Max assistant turn on this machine, streaming
// its events through onEvent. It is thin: the sandbox Engine owns the container
// and the claude stream; control owns the transcript, the SSE fan-out and the
// accounting around it. Cancelling ctx kills the container.
func (m *Machine) RunAssistantTurn(ctx context.Context, spec *api.AssistantTurnSpec, onEvent func(*api.AssistantEvent)) error {
	return m.sandbox.RunTurn(ctx, spec, onEvent)
}

// AnswerAssistant runs a one-shot, tool-less answer on the subscription and
// returns the answer text and usage.
func (m *Machine) AnswerAssistant(ctx context.Context, spec *api.AssistantAnswerSpec) (string, *api.AssistantUsage, error) {
	return m.sandbox.Answer(ctx, spec)
}
