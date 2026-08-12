package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAssistantDefaultsGet(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	rr := request(t, s.API(), "GET", "/api/assistant-defaults", "", testToken)
	require.Equal(t, http.StatusOK, rr.Code)
	var resp apiAssistantDefaults
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	// The API model catalog is surfaced so the admin UI can offer them
	require.NotEmpty(t, resp.Models)
	assert.Equal(t, "claude-sonnet-5", resp.Models[0].ID)
	assert.Empty(t, resp.AllowedModels, "no allowlist configured means all models")
}

func TestAssistantDefaultsUpdate(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)

	// The update echoes the resulting defaults back, so one round-trip both writes
	// and reads. A partial body changes only the fields it names.
	body := `{"external_allowed":true,"allowed_models":["claude-opus-5"],"default_mode":"claude-opus-5"}`
	rr := request(t, s.API(), "PUT", "/api/assistant-defaults", body, testToken)
	require.Equal(t, http.StatusOK, rr.Code)
	var resp apiAssistantDefaults
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.True(t, resp.ExternalAllowed)
	assert.Equal(t, []string{"claude-opus-5"}, resp.AllowedModels)
	assert.Equal(t, "claude-opus-5", resp.DefaultMode)

	// And it persisted: a fresh GET reads the same defaults
	rr = request(t, s.API(), "GET", "/api/assistant-defaults", "", testToken)
	require.Equal(t, http.StatusOK, rr.Code)
	var reread apiAssistantDefaults
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &reread))
	assert.True(t, reread.ExternalAllowed)
	assert.Equal(t, []string{"claude-opus-5"}, reread.AllowedModels)
	assert.Equal(t, "claude-opus-5", reread.DefaultMode)
}

func TestAssistantDefaultsUpdateRejectsUnknownMode(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	rr := request(t, s.API(), "PUT", "/api/assistant-defaults", `{"default_mode":"no-such-model"}`, testToken)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestAssistantDefaultsUpdateRejectsGarbage(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	rr := request(t, s.API(), "PUT", "/api/assistant-defaults", `not json`, testToken)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}
