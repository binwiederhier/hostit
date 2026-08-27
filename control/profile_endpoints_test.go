package control

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"heckel.io/hostit/store"
)

func TestAccountProfileUpdate(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	token, _, err := s.users.CreateToken(u.ID, "t")
	require.NoError(t, err)

	// Assistant is off in the test server (no key), so a set that names only the
	// assistant must fall back to files -- normalizeTabs, enforced server-side.
	body := `{"tech_level":"novice","assistant_prompt":"  Keep it simple.  ","default_tabs":"assistant,terminal","onboarded":true}`
	rr := request(t, s.API(), "PATCH", "/api/account", body, token)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var got apiAccountResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &got))
	assert.Equal(t, "novice", got.TechLevel)
	assert.Equal(t, "Keep it simple.", got.AssistantPrompt, "trimmed")
	assert.Equal(t, "files,terminal", got.DefaultTabs, "assistant dropped (off), files forced")
	assert.True(t, got.Onboarded)

	// It persisted: a fresh GET sees the same.
	rr = request(t, s.API(), "GET", "/api/account", "", token)
	require.Equal(t, http.StatusOK, rr.Code)
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &got))
	assert.Equal(t, "Keep it simple.", got.AssistantPrompt)
}

func TestInfoPromptFlowsIntoGuide(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	require.NoError(t, s.apps.Store().SetSetting(store.SettingInfoPrompt, "Deploy to prod only on Fridays."))

	guide := s.agentGuide("", "", "")
	assert.Equal(t, "Deploy to prod only on Fridays.", guide.AdditionalAdminPrompt, "instance prompt is its own field")
}

func TestGuideCarriesOwnerAssistantPrompt(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	u.AssistantPrompt = "Always write tests."
	require.NoError(t, s.apps.Store().UpdateUserProfile(u))

	guide := s.agentGuide("blog", "", u.ID)
	assert.Equal(t, "Always write tests.", guide.AdditionalUserPrompt, "the owner's note is its own field")
}
