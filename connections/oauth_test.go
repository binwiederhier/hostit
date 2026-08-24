package connections

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tokenServer stands in for a provider's token endpoint, recording what was
// sent and answering with whatever the test needs.
func tokenServer(t *testing.T, body map[string]any, seen *http.Request) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if seen != nil {
			*seen = *r
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A provider that issues a refresh token: hostit keeps that, and exchanges it
// for a short-lived access token whenever an app asks.
func TestExchangeAndRefreshForARefreshingProvider(t *testing.T) {
	t.Parallel()
	srv := tokenServer(t, map[string]any{"access_token": "at-1", "refresh_token": "rt-1", "expires_in": 3600}, nil)
	p := Provider{Name: "google-calendar", Label: "Google Calendar", Kind: KindOAuth, TokenURL: srv.URL}

	secret, err := p.Exchange(context.Background(), srv.Client(), "cid", "sec", "https://cb", "code")
	require.NoError(t, err)
	assert.Equal(t, "rt-1", secret, "the refresh token is what gets stored")

	tok, _, err := p.Refresh(context.Background(), srv.Client(), "cid", "sec", "rt-1")
	require.NoError(t, err)
	assert.Equal(t, "at-1", tok.AccessToken)
	assert.NotNil(t, tok.ExpiresAt, "and it expires")
}

// A refreshing provider that returns no refresh token is refused at connect
// time: it would work for an hour and then fail in a way that looks like a
// hostit bug.
func TestARefreshingProviderWithoutARefreshTokenIsRefused(t *testing.T) {
	t.Parallel()
	srv := tokenServer(t, map[string]any{"access_token": "at-1", "expires_in": 3600}, nil)
	p := Provider{Name: "google-calendar", Label: "Google Calendar", Kind: KindOAuth, TokenURL: srv.URL}

	_, err := p.Exchange(context.Background(), srv.Client(), "cid", "sec", "https://cb", "code")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refresh token")
}

// Slack and GitHub hand back a token that does not expire and no refresh
// token. That is a perfectly good connection: hostit stores the access token
// and hands it straight back.
func TestALongLivedProviderStoresTheAccessTokenItself(t *testing.T) {
	t.Parallel()
	srv := tokenServer(t, map[string]any{"access_token": "xoxb-abc", "ok": true}, nil)
	p := Provider{Name: "slack", Label: "Slack", Kind: KindOAuth, TokenURL: srv.URL, LongLivedToken: true}

	secret, err := p.Exchange(context.Background(), srv.Client(), "cid", "sec", "https://cb", "code")
	require.NoError(t, err)
	assert.Equal(t, "xoxb-abc", secret, "the access token is the thing worth keeping")

	// Refreshing one is a no-op that returns what is stored, so the token
	// endpoint an app calls behaves identically for both kinds.
	tok, _, err := p.Refresh(context.Background(), srv.Client(), "cid", "sec", "xoxb-abc")
	require.NoError(t, err)
	assert.Equal(t, "xoxb-abc", tok.AccessToken)
	assert.Nil(t, tok.ExpiresAt, "nothing to expire, so nothing is promised")
}

// GitHub answers form-encoded unless asked otherwise, which would otherwise
// parse as "not a token response".
func TestTheTokenRequestAsksForJSON(t *testing.T) {
	t.Parallel()
	var seen http.Request
	srv := tokenServer(t, map[string]any{"access_token": "gho_x"}, &seen)
	p := Provider{Name: "github", Label: "GitHub", Kind: KindOAuth, TokenURL: srv.URL, LongLivedToken: true}

	_, err := p.Exchange(context.Background(), srv.Client(), "cid", "sec", "https://cb", "code")
	require.NoError(t, err)
	assert.Equal(t, "application/json", seen.Header.Get("Accept"))
}

// A provider's own error text beats a generic failure: "invalid_grant" almost
// always means the owner revoked access at the provider.
func TestAProviderRefusalSaysWhy(t *testing.T) {
	t.Parallel()
	srv := tokenServer(t, map[string]any{"error": "invalid_grant", "error_description": "Token has been expired or revoked."}, nil)
	p := Provider{Name: "google-calendar", Label: "Google Calendar", Kind: KindOAuth, TokenURL: srv.URL}

	_, _, err := p.Refresh(context.Background(), srv.Client(), "cid", "sec", "rt-dead")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid_grant")
	assert.Contains(t, err.Error(), "revoked")
}

// A credential that does not expire must not claim to have expired in year 1.
// encoding/json ignores omitempty on a time.Time, so the zero value would
// serialise as 0001-01-01 and an app doing the obvious thing would treat every
// static credential as long dead.
func TestATokenWithNoExpiryOmitsTheField(t *testing.T) {
	t.Parallel()
	b, err := json.Marshal(Token{Provider: "imap", AccessToken: "pw"})
	require.NoError(t, err)
	assert.NotContains(t, string(b), "expires_at")
	assert.NotContains(t, string(b), "0001-01-01")

	at := time.Now().Add(time.Hour)
	b, err = json.Marshal(Token{Provider: "google-calendar", AccessToken: "at", ExpiresAt: &at})
	require.NoError(t, err)
	assert.Contains(t, string(b), "expires_at")
}

// Some providers ROTATE the refresh token: every refresh returns a new one and
// invalidates the old. Discord does. Discarding the new one means the first
// refresh works and the second fails with invalid_grant, which reads like the
// user revoked access when nothing of the sort happened.
func TestARotatedRefreshTokenIsReturnedSoItCanBeStored(t *testing.T) {
	t.Parallel()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		given := r.Form.Get("refresh_token")
		seen = append(seen, given)
		w.Header().Set("Content-Type", "application/json")
		// Only the newest refresh token is accepted, the way a rotating
		// provider behaves.
		if len(seen) > 1 && given != "rt-"+fmt.Sprint(len(seen)-1) {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at", "refresh_token": "rt-" + fmt.Sprint(len(seen)), "expires_in": 3600,
		})
	}))
	defer srv.Close()
	p := Provider{Name: "discord", Label: "Discord", Kind: KindOAuth, TokenURL: srv.URL}

	tok, rotated, err := p.Refresh(context.Background(), srv.Client(), "cid", "sec", "rt-0")
	require.NoError(t, err)
	assert.Equal(t, "at", tok.AccessToken)
	assert.Equal(t, "rt-1", rotated, "the caller has to be told, or the next refresh fails")

	// Using the rotated one works; using the original would not.
	_, _, err = p.Refresh(context.Background(), srv.Client(), "cid", "sec", rotated)
	assert.NoError(t, err)
}

// A provider that does NOT rotate (Google) returns nothing new, and the caller
// must not overwrite a good refresh token with an empty string.
func TestANonRotatingProviderReturnsNoNewRefreshToken(t *testing.T) {
	t.Parallel()
	srv := tokenServer(t, map[string]any{"access_token": "at", "expires_in": 3600}, nil)
	p := Provider{Name: "google-calendar", Label: "Google Calendar", Kind: KindOAuth, TokenURL: srv.URL}

	_, rotated, err := p.Refresh(context.Background(), srv.Client(), "cid", "sec", "rt-keep")
	require.NoError(t, err)
	assert.Empty(t, rotated, "nothing to store")
}
