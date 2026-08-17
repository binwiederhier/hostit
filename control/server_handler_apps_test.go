package control

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/controlconf"
	"heckel.io/hostit/preview"
	"heckel.io/hostit/store"
)

// appScopedToken mints a token limited to a single app, as one would paste into
// an agent running inside that app.
func appScopedToken(t *testing.T, s *Server, u *store.User, appName string) string {
	t.Helper()
	token, _, err := s.users.CreateAppToken(u.ID, appName, "agent")
	require.NoError(t, err)
	return token
}

// A rename keys on the app id, so nothing durable moves: the store row simply
// answers to the new name and the old name is gone.
func TestAppRename(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))

	rr := request(t, s.API(), "POST", "/api/apps/blog/rename", `{"new_name":"journal"}`, token)
	require.Equal(t, http.StatusOK, rr.Code)
	var resp apiAppResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, "journal", resp.Name)

	// The row now answers to the new name, and the old one is gone
	_, err := s.apps.Store().App("journal")
	require.NoError(t, err)
	_, err = s.apps.Store().App("blog")
	assert.ErrorIs(t, err, store.ErrAppNotFound)
}

func TestAppRenameRejections(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "wiki", Port: 10001, Host: store.HostLocal, OwnerID: u.ID}))

	// A malformed new name is a bad request, not a 500
	assert.Equal(t, http.StatusBadRequest, request(t, s.API(), "POST", "/api/apps/blog/rename", `{"new_name":"NOPE!"}`, token).Code)
	// A name already taken is a conflict
	assert.Equal(t, http.StatusConflict, request(t, s.API(), "POST", "/api/apps/blog/rename", `{"new_name":"wiki"}`, token).Code)
	// Renaming an app that does not exist is a 404
	assert.Equal(t, http.StatusNotFound, request(t, s.API(), "POST", "/api/apps/ghost/rename", `{"new_name":"x"}`, token).Code)
}

func TestAppSetDescription(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))
	seedAppSubvolume(t, s, "blog")

	rr := request(t, s.API(), "PUT", "/api/apps/blog/description", `{"description":"A tiny blog"}`, token)
	require.Equal(t, http.StatusOK, rr.Code)
	var resp apiAppResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, "A tiny blog", resp.Description)

	// And it is persisted: a fresh GET reads the same description back
	rr = request(t, s.API(), "GET", "/api/apps/blog", "", token)
	require.Equal(t, http.StatusOK, rr.Code)
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, "A tiny blog", resp.Description)
}

func TestAppSetDescriptionUnknownApp(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)
	rr := request(t, s.API(), "PUT", "/api/apps/ghost/description", `{"description":"x"}`, token)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestAppRotateToken(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))

	before, err := s.users.AppToken(u.ID, "blog")
	require.NoError(t, err)

	rr := request(t, s.API(), "POST", "/api/apps/blog/token", "", token)
	require.Equal(t, http.StatusOK, rr.Code)
	var resp apiAppResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.AgentToken)
	assert.NotEqual(t, before, resp.AgentToken, "rotation must mint a fresh token")

	// And the new token is the one now stored for the app
	after, err := s.users.AppToken(u.ID, "blog")
	require.NoError(t, err)
	assert.Equal(t, resp.AgentToken, after)
}

func TestAppSetKeysUnknownApp(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)
	body := fmt.Sprintf(`{"ssh_keys":[%q]}`, testPublicKey)
	assert.Equal(t, http.StatusNotFound, request(t, s.API(), "PUT", "/api/apps/ghost/keys", body, token).Code)
}

// A non-admin sees other people's apps as if they did not exist: a 404, never a
// 403, so they cannot even confirm the name is taken.
func TestNonAdminCannotSeeAnothersApp(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	owner := newActiveTestUser(t, s, "owner@example.com")
	stranger := newActiveTestUser(t, s, "stranger@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "secret", Port: 10000, Host: store.HostLocal, OwnerID: owner.ID}))
	strangerToken := accountToken(t, s, stranger)

	for _, tc := range []struct {
		method, path, body string
	}{
		{"GET", "/api/apps/secret", ""},
		{"DELETE", "/api/apps/secret", ""},
		{"POST", "/api/apps/secret/rename", `{"new_name":"mine"}`},
		{"PUT", "/api/apps/secret/description", `{"description":"x"}`},
		{"POST", "/api/apps/secret/token", ""},
	} {
		rr := request(t, s.API(), tc.method, tc.path, tc.body, strangerToken)
		assert.Equal(t, http.StatusNotFound, rr.Code, "%s %s must be 404, not 403", tc.method, tc.path)
	}
}

// An app-scoped token exists to be pasted into one app's agent, so it may reach
// that app's endpoints and nothing else -- not even a sibling app the same owner
// holds. Reaching outside its app is a 403.
func TestAppScopedTokenCannotTouchAnotherApp(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	owner := newActiveTestUser(t, s, "owner@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal, OwnerID: owner.ID}))
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "wiki", Port: 10001, Host: store.HostLocal, OwnerID: owner.ID}))
	scoped := appScopedToken(t, s, owner, "blog")

	// Its own app is reachable
	assert.Equal(t, http.StatusOK, request(t, s.API(), "GET", "/api/apps/blog", "", scoped).Code)
	// The sibling app, though owned by the same user, is out of scope
	assert.Equal(t, http.StatusForbidden, request(t, s.API(), "GET", "/api/apps/wiki", "", scoped).Code)
	// And so is the account surface, which would reveal the owner
	assert.Equal(t, http.StatusForbidden, request(t, s.API(), "GET", "/api/account/keys", "", scoped).Code)
}

func TestAppPreviewScreenshotServed(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	s.config.AppPreview = controlconf.AppPreviewScreenshot
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)
	a := &store.App{Name: "blog", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}
	require.NoError(t, s.apps.Store().AddApp(a))

	// No shot yet: not found
	assert.Equal(t, http.StatusNotFound, request(t, s.API(), "GET", "/api/apps/blog/preview.png", "", token).Code)

	// With a stored shot, the bytes come back as a PNG
	dir := preview.Dir(s.config.DataDir)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, a.ID+".png"), []byte("fakepng"), 0o600))
	rr := request(t, s.API(), "GET", "/api/apps/blog/preview.png", "", token)
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "image/png", rr.Header().Get("Content-Type"))
	assert.Equal(t, "fakepng", rr.Body.String())

	// A stranger gets a 404, same as any app they don't own
	stranger := accountToken(t, s, newActiveTestUser(t, s, "other@example.com"))
	assert.Equal(t, http.StatusNotFound, request(t, s.API(), "GET", "/api/apps/blog/preview.png", "", stranger).Code)
}

func TestAppPreviewHiddenOutsideScreenshotMode(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)
	a := &store.App{Name: "blog", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}
	require.NoError(t, s.apps.Store().AddApp(a))
	dir := preview.Dir(s.config.DataDir)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, a.ID+".png"), []byte("fakepng"), 0o600))

	// Default mode is live: the endpoint does not exist for callers
	assert.Equal(t, http.StatusNotFound, request(t, s.API(), "GET", "/api/apps/blog/preview.png", "", token).Code)
}

func TestAppPreviewRefreshQueuesOrRejects(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	s.config.AppPreview = controlconf.AppPreviewScreenshot
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))

	// Not wired (no preview manager in the test server) -> treated as not-found
	assert.Equal(t, http.StatusNotFound, request(t, s.API(), "POST", "/api/apps/blog/preview", "", token).Code)

	// A stranger cannot even see the app
	stranger := accountToken(t, s, newActiveTestUser(t, s, "other@example.com"))
	assert.Equal(t, http.StatusNotFound, request(t, s.API(), "POST", "/api/apps/blog/preview", "", stranger).Code)

	// With a preview manager wired, the owner's refresh is accepted
	s.SetPreviews(preview.New(nil, t.TempDir(), func() ([]preview.App, error) { return nil, nil }))
	assert.Equal(t, http.StatusAccepted, request(t, s.API(), "POST", "/api/apps/blog/preview", "", token).Code)
}

func TestAppResponseCarriesPreviewMode(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	s.config.AppPreview = controlconf.AppPreviewScreenshot
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))

	rr := request(t, s.API(), "GET", "/api/apps/blog", "", token)
	require.Equal(t, http.StatusOK, rr.Code)
	var resp apiAppResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, "screenshot", resp.PreviewMode)
}
