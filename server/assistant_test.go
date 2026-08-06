package server

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAssistantEndpointDisabledWhenUnconfigured(t *testing.T) {
	t.Parallel()
	s := newTestServer(t) // no Anthropic API key -> assistant is nil
	// Authenticated (admin token), but the feature is off: a clear 501, not a panic.
	rr := request(t, s.API(), "POST", "/api/apps/blog/assistant", `{"message":"hi"}`, testToken)
	assert.Equal(t, http.StatusNotImplemented, rr.Code)
}

func TestAssistantEndpointRequiresAuth(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	rr := request(t, s.API(), "POST", "/api/apps/blog/assistant", `{"message":"hi"}`, "")
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}
