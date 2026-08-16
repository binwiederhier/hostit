package control

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The assistant defaults are folded into the global /api/settings call rather than
// living on their own /api/assistant-defaults endpoint.
func TestAssistantDefaultsGet(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	rr := request(t, s.API(), "GET", "/api/settings", "", testToken)
	require.Equal(t, http.StatusOK, rr.Code)
	var resp apiSettingsResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.NotNil(t, resp.Assistant, "settings carries the assistant defaults")
	// The API model catalog is surfaced so the admin UI can offer them
	require.NotEmpty(t, resp.Assistant.Models)
	assert.Equal(t, "claude-sonnet-5", resp.Assistant.Models[0].ID)
	assert.Empty(t, resp.Assistant.AllowedModels, "no allowlist configured means all models")
}

func TestAssistantDefaultsUpdate(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)

	// The update echoes the resulting settings back, so one round-trip both writes
	// and reads. A partial body changes only the fields it names.
	body := `{"assistant":{"external_allowed":true,"allowed_models":["claude-opus-5"],"default_mode":"claude-opus-5"}}`
	rr := request(t, s.API(), "PATCH", "/api/settings", body, testToken)
	require.Equal(t, http.StatusOK, rr.Code)
	var resp apiSettingsResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.NotNil(t, resp.Assistant)
	assert.True(t, resp.Assistant.ExternalAllowed)
	assert.Equal(t, []string{"claude-opus-5"}, resp.Assistant.AllowedModels)
	assert.Equal(t, "claude-opus-5", resp.Assistant.DefaultMode)

	// And it persisted: a fresh GET reads the same defaults
	rr = request(t, s.API(), "GET", "/api/settings", "", testToken)
	require.Equal(t, http.StatusOK, rr.Code)
	var reread apiSettingsResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &reread))
	require.NotNil(t, reread.Assistant)
	assert.True(t, reread.Assistant.ExternalAllowed)
	assert.Equal(t, []string{"claude-opus-5"}, reread.Assistant.AllowedModels)
	assert.Equal(t, "claude-opus-5", reread.Assistant.DefaultMode)
}

func TestAssistantDefaultsUpdateRejectsUnknownMode(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	rr := request(t, s.API(), "PATCH", "/api/settings", `{"assistant":{"default_mode":"no-such-model"}}`, testToken)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}
