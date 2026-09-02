package connections

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type revokeRecorder struct {
	*httptest.Server
	auth string
	form url.Values
}

func newRevokeRecorder(t *testing.T, status int, body string) *revokeRecorder {
	t.Helper()
	rec := &revokeRecorder{}
	rec.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.auth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		rec.form, _ = url.ParseQuery(string(raw))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(rec.Close)
	return rec
}

func TestRevokeSendsTheTokenAsBearerForSlack(t *testing.T) {
	rec := newRevokeRecorder(t, http.StatusOK, `{"ok":true}`)
	p := Provider{Name: "slack-user", RevokeURL: rec.URL, RevokeAuth: "bearer"}
	require.NoError(t, p.Revoke(context.Background(), http.DefaultClient, "cid", "csecret", "tok-abc"))
	assert.Equal(t, "Bearer tok-abc", rec.auth)
	assert.Equal(t, "tok-abc", rec.form.Get("token"))
}

func TestRevokeSendsClientCredentialsForDiscord(t *testing.T) {
	rec := newRevokeRecorder(t, http.StatusOK, "")
	p := Provider{Name: "discord", RevokeURL: rec.URL}
	require.NoError(t, p.Revoke(context.Background(), http.DefaultClient, "cid", "csecret", "tok-abc"))
	assert.Empty(t, rec.auth)
	assert.Equal(t, "cid", rec.form.Get("client_id"))
	assert.Equal(t, "csecret", rec.form.Get("client_secret"))
	assert.Equal(t, "tok-abc", rec.form.Get("token"))
}

func TestRevokeReportsARefusalAndAnHTTPError(t *testing.T) {
	rec := newRevokeRecorder(t, http.StatusOK, `{"ok":false,"error":"token_revoked"}`)
	p := Provider{Name: "slack-user", RevokeURL: rec.URL, RevokeAuth: "bearer"}
	require.ErrorContains(t, p.Revoke(context.Background(), http.DefaultClient, "cid", "cs", "t"), "token_revoked")

	bad := newRevokeRecorder(t, http.StatusBadRequest, "")
	require.Error(t, Provider{RevokeURL: bad.URL}.Revoke(context.Background(), http.DefaultClient, "cid", "cs", "t"))
}

func TestRevokeIsANoOpWithoutURLOrToken(t *testing.T) {
	require.NoError(t, Provider{}.Revoke(context.Background(), http.DefaultClient, "c", "s", "t"))
	require.NoError(t, Provider{RevokeURL: "https://x/revoke"}.Revoke(context.Background(), http.DefaultClient, "c", "s", ""))
}

func TestBuiltinSlackAndDiscordCarryRevokeEndpoints(t *testing.T) {
	slack, ok := Lookup("slack-user")
	require.True(t, ok)
	assert.Equal(t, "https://slack.com/api/auth.revoke", slack.RevokeURL)
	assert.Equal(t, "bearer", slack.RevokeAuth)
	discord, ok := Lookup("discord")
	require.True(t, ok)
	assert.Equal(t, "https://discord.com/api/oauth2/token/revoke", discord.RevokeURL)
	assert.Empty(t, discord.RevokeAuth)
}
