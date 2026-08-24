package control

import (
	"encoding/json"
	"errors"
	"log/slog"
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
// It answers with no tools. None of the assistant's tools are offered here: an app
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

// apiAssistantModel is one model an app may pick: the id it sends, a human label,
// and which backend answers (a claude-* id runs on the subscription, an
// anthropic-* id on the metered API). The upstream model string is deliberately
// not exposed -- the app names the id, hostit maps it.
type apiAssistantModel struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Backend string `json:"backend"`
}

// apiAssistantModels is the discovery response: the models this instance can
// actually run, and which one an empty choice resolves to.
type apiAssistantModels struct {
	Models  []apiAssistantModel `json:"models"`
	Default string              `json:"default"`
}

// handleSelfAssistantModels lists the models the calling app may pick, filtered
// to what this instance has configured. An app calls this to discover its
// choices before asking; the list is empty when no backend is available.
func (s *Server) handleSelfAssistantModels(w http.ResponseWriter, r *http.Request, a *store.App) {
	out := apiAssistantModels{Models: []apiAssistantModel{}}
	if s.assistant != nil {
		for _, o := range s.assistant.AskModels() {
			out.Models = append(out.Models, apiAssistantModel{ID: o.ID, Label: o.Label, Backend: o.Backend})
		}
		out.Default = s.assistant.AskDefaultModel() // authoritative, not re-derived from the list
	}
	writeJSON(w, http.StatusOK, &out)
}

// assistantOwnerKey is the identity a tenant's assistant call is rate-limited and
// metered against: the app's OWNER. A legacy (pre-ownership) app can have an
// empty OwnerID, and an empty key is reserveRun's UNLIMITED admin exemption -- so
// fall back to the app itself, which still bounds an otherwise-ownerless app
// rather than handing it the admin's no-limit status.
func assistantOwnerKey(a *store.App) string {
	if a.OwnerID != "" {
		return a.OwnerID
	}
	return "app:" + a.ID
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
	// Charged to the app's OWNER (or the app itself when ownerless), because it is
	// the operator's budget and their rate limit -- the same one an interactive
	// turn spends. Never an empty key: that is reserveRun's admin exemption.
	res, err := s.assistant.AskFor(r.Context(), assistantOwnerKey(a), a.Name, assistant.AskRequest{
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
		// A backend failure. Log the real error operator-side, but hand the tenant
		// a GENERIC one: a backend error can carry internals (sandbox stderr, an
		// upstream API's message) that are none of the app's business.
		slog.Warn("Assistant backend error on the container endpoint", "error", err)
		writeError(w, http.StatusBadGateway, errors.New("the assistant backend failed"))
	}
}
