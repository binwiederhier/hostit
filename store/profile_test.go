package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The self-service profile fields round-trip, and UpdateUserProfile writes ONLY
// them -- an admin's role/limit edit and a profile edit must not clobber each
// other.
func TestUserProfileRoundTrip(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.AddUser(&User{ID: "u1", Email: "a@example.com", Name: "A", Role: RoleUser, Status: StatusActive, CreatedAt: time.Now()}))

	// Defaults are empty/false on a fresh user.
	u, err := s.User("u1")
	require.NoError(t, err)
	assert.Equal(t, "", u.TechLevel)
	assert.Equal(t, "", u.AssistantPrompt)
	assert.Equal(t, "", u.DefaultTabs)
	assert.False(t, u.Onboarded)

	u.TechLevel = "novice"
	u.AssistantPrompt = "Explain like I am five."
	u.DefaultTabs = "assistant"
	u.Onboarded = true
	require.NoError(t, s.UpdateUserProfile(u))

	got, err := s.User("u1")
	require.NoError(t, err)
	assert.Equal(t, "novice", got.TechLevel)
	assert.Equal(t, "Explain like I am five.", got.AssistantPrompt)
	assert.Equal(t, "assistant", got.DefaultTabs)
	assert.True(t, got.Onboarded)

	// An admin limit edit via UpdateUser must not wipe the profile fields.
	got.Role = RoleAdmin
	require.NoError(t, s.UpdateUser(got))
	after, err := s.User("u1")
	require.NoError(t, err)
	assert.Equal(t, "novice", after.TechLevel, "admin edit preserved the profile")
	assert.Equal(t, RoleAdmin, after.Role)
}

func TestAppTabsRoundTrip(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.AddApp(&App{ID: "a1", Name: "dash", Port: 10000, Host: HostLocal, OwnerID: "u1"}))
	a, err := s.App("dash")
	require.NoError(t, err)
	assert.Equal(t, "", a.Tabs, "no override by default")

	require.NoError(t, s.SetAppTabs("dash", "assistant,logs"))
	a, err = s.App("dash")
	require.NoError(t, err)
	assert.Equal(t, "assistant,logs", a.Tabs)
}

func TestSettingSingleKey(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	v, err := s.Setting(SettingInfoPrompt)
	require.NoError(t, err)
	assert.Equal(t, "", v, "unset returns empty, not an error")

	require.NoError(t, s.SetSetting(SettingInfoPrompt, "House rules."))
	v, err = s.Setting(SettingInfoPrompt)
	require.NoError(t, err)
	assert.Equal(t, "House rules.", v)
}
