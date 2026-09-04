package control

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"heckel.io/hostit/store"
)

// The gallery gate: off by default, control.yml can turn it on, and the admin DB
// override wins over control.yml either way.
func TestAppListingGate(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	assert.False(t, s.appListingEnabled(), "off by default")

	s.config.AppListing = true
	assert.True(t, s.appListingEnabled(), "control.yml turns it on")

	require.NoError(t, s.apps.Store().SetSetting(store.SettingAppListing, "false"))
	assert.False(t, s.appListingEnabled(), "admin override wins")

	require.NoError(t, s.apps.Store().SetSetting(store.SettingAppListing, "true"))
	s.config.AppListing = false
	assert.True(t, s.appListingEnabled(), "admin override wins the other way")
}

// Listing is refused for a private app, and refused entirely when the gallery is
// off; unlisting is always allowed.
func TestSetListedRequiresPublicAppAndEnabledGallery(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	token, _, err := s.users.CreateToken(u.ID, "t")
	require.NoError(t, err)
	st := s.apps.Store()
	require.NoError(t, st.AddApp(&store.App{Name: "pub", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))
	require.NoError(t, st.AddApp(&store.App{Name: "priv", Port: 10001, Host: store.HostLocal, OwnerID: u.ID}))
	require.NoError(t, st.SetAppPrivate("priv", true))

	// Gallery off: even a public app cannot be listed.
	rr := request(t, s.API(), "PUT", "/api/apps/pub/listed", `{"listed":true}`, token)
	assert.Equal(t, http.StatusForbidden, rr.Code)

	s.config.AppListing = true
	// A private app cannot be listed.
	rr = request(t, s.API(), "PUT", "/api/apps/priv/listed", `{"listed":true}`, token)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	// A public app can.
	rr = request(t, s.API(), "PUT", "/api/apps/pub/listed", `{"listed":true}`, token)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	a, _ := st.App("pub")
	assert.True(t, a.Listed)
	// Unlisting is always fine.
	rr = request(t, s.API(), "PUT", "/api/apps/pub/listed", `{"listed":false}`, token)
	require.Equal(t, http.StatusOK, rr.Code)
}

// The visibility endpoint folds listing in ("Publicly listed"): public + listed
// lists the app, and making it private coerces listing off.
func TestVisibilitySetsAndCoercesListed(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	s.config.AppListing = true
	u := newActiveTestUser(t, s, "owner@example.com")
	token, _, err := s.users.CreateToken(u.ID, "t")
	require.NoError(t, err)
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "app", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))

	rr := request(t, s.API(), "PUT", "/api/apps/app/visibility", `{"private":false,"listed":true}`, token)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	a, _ := s.apps.Store().App("app")
	assert.False(t, a.Private)
	assert.True(t, a.Listed, "public + listed")

	rr = request(t, s.API(), "PUT", "/api/apps/app/visibility", `{"private":true,"listed":true}`, token)
	require.Equal(t, http.StatusOK, rr.Code)
	a, _ = s.apps.Store().App("app")
	assert.True(t, a.Private)
	assert.False(t, a.Listed, "a private app is never listed, even if asked")
}

// Explore shows only public, listed, live apps -- and nothing when the gallery
// is off.
func TestExploreListsOnlyPublicListedApps(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	s.config.AppListing = true
	u := newActiveTestUser(t, s, "owner@example.com")
	token, _, err := s.users.CreateToken(u.ID, "t")
	require.NoError(t, err)
	st := s.apps.Store()
	require.NoError(t, st.AddApp(&store.App{Name: "shown", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))
	require.NoError(t, st.SetAppListed("shown", true))
	require.NoError(t, st.AddApp(&store.App{Name: "hiddenpriv", Port: 10001, Host: store.HostLocal, OwnerID: u.ID}))
	require.NoError(t, st.SetAppPrivate("hiddenpriv", true))
	require.NoError(t, st.SetAppListed("hiddenpriv", true))
	require.NoError(t, st.AddApp(&store.App{Name: "hiddenunlisted", Port: 10002, Host: store.HostLocal, OwnerID: u.ID}))

	get := func() apiExploreResponse {
		rr := request(t, s.API(), "GET", "/api/explore", "", token)
		require.Equal(t, http.StatusOK, rr.Code)
		var resp apiExploreResponse
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
		return resp
	}

	resp := get()
	require.True(t, resp.Enabled)
	names := []string{}
	for _, a := range resp.Apps {
		names = append(names, a.Name)
	}
	assert.Equal(t, []string{"shown"}, names, "only the public, listed app")

	// Gallery off: disabled and empty.
	s.config.AppListing = false
	require.NoError(t, st.SetSetting(store.SettingAppListing, ""))
	resp = get()
	assert.False(t, resp.Enabled)
	assert.Empty(t, resp.Apps)
}
