package control

import (
	"encoding/json"
	"errors"
	"net/http"

	"heckel.io/hostit/assistant"
	"heckel.io/hostit/store"
)

// An app asking the model a question, over its own socket.
//
// The interactive assistant BUILDS an app; this lets the app itself think. An
// app can summarise its own logs and decide whether they are worth waking
// somebody for, or be a chat that answers in the voice of a pirate -- without
// holding an API key, without a vendor account of its own, and without the
// owner pasting a secret into an environment variable that nothing can rotate.
//
// It is inference only. None of the assistant's tools are offered here: an app
// that could call write_file on itself through this is a self-modifying loop
// with nobody in the room.

// apiAskRequest is what an app sends. Either prompt (the easy case) or messages
// (a conversation), never both.
type apiAskRequest struct {
	// Prompt is the one-shot form: the shortest thing that can possibly work,
	// because most apps asking a question have exactly one to ask.
	Prompt   string                 `json:"prompt,omitempty"`
	System   string                 `json:"system,omitempty"`
	Messages []assistant.AskMessage `json:"messages,omitempty"`
	Model    string                 `json:"model,omitempty"`

	MaxTokens int `json:"max_tokens,omitempty"`
}

// apiAskResponse is the answer, plus what it cost -- an app that wants to stay
// within a budget can only do that if it is told.
type apiAskResponse struct {
	Text  string          `json:"text"`
	Model string          `json:"model"`
	Usage assistant.Usage `json:"usage"`
}

// handleSelfAssistantAsk answers one question for the calling app.
func (s *Server) handleSelfAssistantAsk(w http.ResponseWriter, r *http.Request, a *store.App) {
	if s.assistant == nil {
		writeError(w, http.StatusNotImplemented, assistant.ErrAskUnavailable)
		return
	}
	var req apiAskRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAskBody)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	messages := req.Messages
	if req.Prompt != "" {
		if len(messages) > 0 {
			writeError(w, http.StatusBadRequest,
				errors.New(`send "prompt" for one question or "messages" for a conversation, not both`))
			return
		}
		messages = []assistant.AskMessage{{Role: "user", Content: req.Prompt}}
	}
	// Charged to the app's OWNER, because it is their operator's budget and
	// their rate limit -- the same one an interactive turn spends.
	res, err := s.assistant.AskFor(r.Context(), a.OwnerID, a.Name, assistant.AskRequest{
		System:    req.System,
		Messages:  messages,
		Model:     req.Model,
		MaxTokens: req.MaxTokens,
	})
	if err != nil {
		writeAskError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiAskResponse{Text: res.Text, Model: res.Model, Usage: res.Usage})
}

// maxAskBody bounds the request body. Generous enough for a long conversation,
// small enough that a runaway app cannot post its whole disk.
const maxAskBody = 1 << 20

func writeAskError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, assistant.ErrAskInvalid), errors.Is(err, assistant.ErrAskTooLarge):
		writeError(w, http.StatusBadRequest, err)
	case errors.Is(err, assistant.ErrRateLimited), errors.Is(err, assistant.ErrTooManyRuns):
		// 429 with the reason: an app written to back off can only do that if it
		// can tell "too fast" from "broken".
		writeError(w, http.StatusTooManyRequests, err)
	case errors.Is(err, assistant.ErrAskUnavailable):
		writeError(w, http.StatusNotImplemented, err)
	default:
		writeError(w, http.StatusBadGateway, err)
	}
}
