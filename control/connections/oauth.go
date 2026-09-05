package connections

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The OAuth half. Only control ever runs this: the refresh token never leaves
// it, and an app only ever sees the short-lived access token that comes out.

// AuthCodeURL is where the owner is sent to consent. state carries hostit's own
// nonce so the callback can tell a connection from a login -- the PoC shares
// the login callback path, see plans/260819-connections.md.
func (p Provider) AuthCodeURL(clientID, redirectURL, state string) string {
	q := url.Values{
		"client_id":     {clientID},
		"redirect_uri":  {redirectURL},
		"response_type": {"code"},
		"state":         {state},
	}
	// A user-token provider (Slack personal) asks in user_scope, so Slack issues
	// a token that acts as the person; every other provider uses the bot/app scope.
	scopeParam := "scope"
	if p.UserToken {
		scopeParam = "user_scope"
	}
	// A provider can legitimately ask for nothing: a GitHub App's permissions are
	// configured on the app and chosen at install, so it has no scopes to name.
	// Send no parameter rather than an empty one.
	if len(p.Scopes) > 0 {
		q.Set(scopeParam, strings.Join(p.Scopes, " "))
	}
	// Whatever THIS provider needs on top. Google wants offline access or it
	// never issues a refresh token; Atlassian wants its audience named; Slack
	// and GitHub want none of it.
	for k, v := range p.AuthParams {
		q.Set(k, v)
	}
	return p.AuthURL + "?" + q.Encode()
}

// tokenResponse is the subset of an OAuth token response hostit uses.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
	// AuthedUser carries the USER token when the request asked in user_scope
	// (Slack). It is separate from the top-level access_token, which is the bot
	// token a user-token connection never uses.
	AuthedUser struct {
		AccessToken string `json:"access_token"`
	} `json:"authed_user"`
}

// keptToken is the access token worth storing from a response: a user-token
// provider's lives under authed_user, everyone else's at the top level.
func (p Provider) keptToken(res tokenResponse) string {
	if p.UserToken {
		return res.AuthedUser.AccessToken
	}
	return res.AccessToken
}

// Exchange trades the consent code for tokens. It returns the credential to
// store and whether that credential is REFRESHABLE (a refresh token, exchanged
// for access tokens over time) rather than a long-lived access token kept and
// handed back as-is. The mode is decided here, from what the token endpoint
// returns, and the caller remembers it per connection.
func (p Provider) Exchange(ctx context.Context, client *http.Client, clientID, clientSecret, redirectURL, code string) (credential string, refreshable bool, err error) {
	res, err := p.postToken(ctx, client, url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"code":          {code},
		"grant_type":    {"authorization_code"},
		"redirect_uri":  {redirectURL},
	})
	if err != nil {
		return "", false, err
	}
	// A provider whose token does not expire hands back nothing to refresh, so
	// the access token IS the thing worth keeping.
	if p.LongLivedToken {
		// Invariant check: a genuinely long-lived provider returns neither a
		// refresh token nor an expiry. If it does, the provider is MISclassified
		// -- the stored access token will die and, with refresh skipped, only a
		// manual reconnect brings it back. Log it (no credential is exposed) so
		// the cause is visible instead of surfacing as a mystery needs-reconnect.
		// (GitHub, which does exactly this, is a HybridToken provider now.)
		if res.RefreshToken != "" || res.ExpiresIn != 0 {
			slog.Warn("Long-lived provider returned refreshable-token fields; it is misclassified and its token will expire",
				"provider", p.Name, "has_refresh_token", res.RefreshToken != "", "expires_in", res.ExpiresIn)
		}
		tok := p.keptToken(res)
		if tok == "" {
			return "", false, fmt.Errorf("%s returned no access token", p.Label)
		}
		return tok, false, nil
	}
	// A hybrid provider issues a refresh token only when the operator's app is
	// configured for expiring tokens. With one, keep it and refresh. Without
	// one, the app issues a permanent token, so keep that and probe instead --
	// refusing (as a pure refreshing provider does) would lock out a valid
	// classic OAuth App.
	if p.HybridToken {
		if res.RefreshToken != "" {
			return res.RefreshToken, true, nil
		}
		tok := p.keptToken(res)
		if tok == "" {
			return "", false, fmt.Errorf("%s returned neither a refresh token nor an access token", p.Label)
		}
		return tok, false, nil
	}
	if res.RefreshToken == "" {
		// Without one, the connection would work for an hour and then die in a
		// way that looks like a hostit bug. Better to refuse the connection.
		return "", false, fmt.Errorf("%s returned no refresh token; disconnect it at the provider and connect again", p.Label)
	}
	return res.RefreshToken, true, nil
}

// Refresh exchanges the stored refresh token for an access token. For a
// provider whose token does not expire there is nothing to exchange: the
// stored secret is already the usable token, and it is returned unchanged so
// the endpoint an app calls behaves the same for both kinds.
//
// The second return is a ROTATED refresh token, empty unless the provider
// issued a new one. Some do on every refresh -- Discord does -- and the old one
// is dead the moment this returns. The caller MUST store it: dropping it makes
// the first refresh work and the second fail with invalid_grant, which reads
// like the owner revoked access when nothing of the sort happened.
func (p Provider) Refresh(ctx context.Context, client *http.Client, clientID, clientSecret, refreshToken string) (Token, string, error) {
	if p.LongLivedToken {
		return Token{Provider: p.Name, AccessToken: refreshToken}, "", nil
	}
	res, err := p.postToken(ctx, client, url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"refresh_token": {refreshToken},
		"grant_type":    {"refresh_token"},
	})
	if err != nil {
		return Token{}, "", err
	}
	expiry := time.Now().Add(time.Duration(res.ExpiresIn) * time.Second)
	if res.ExpiresIn == 0 {
		expiry = time.Now().Add(time.Hour)
	}
	// Only report a rotation when the provider actually sent a different token,
	// so a non-rotating provider never causes a pointless write.
	rotated := ""
	if res.RefreshToken != "" && res.RefreshToken != refreshToken {
		rotated = res.RefreshToken
	}
	return Token{Provider: p.Name, AccessToken: res.AccessToken, ExpiresAt: &expiry}, rotated, nil
}

func (p Provider) postToken(ctx context.Context, client *http.Client, form url.Values) (tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// GitHub answers form-encoded unless asked otherwise, which would parse as
	// "not a token response"; everyone else already sends JSON.
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return tokenResponse{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return tokenResponse{}, err
	}
	var res tokenResponse
	if err := json.Unmarshal(body, &res); err != nil {
		return tokenResponse{}, fmt.Errorf("%s returned something that is not a token response", p.Label)
	}
	if res.Error != "" {
		// The provider's own words: "invalid_grant" almost always means the
		// owner revoked access, and saying so beats a generic failure.
		return tokenResponse{}, fmt.Errorf("%w: %s refused: %s %s", ErrReauthRequired, p.Label, res.Error, res.ErrorDesc)
	}
	if resp.StatusCode != http.StatusOK {
		return tokenResponse{}, fmt.Errorf("%s returned HTTP %d", p.Label, resp.StatusCode)
	}
	return res, nil
}

// Revoke tells the provider to invalidate a token hostit is discarding (a
// removed connection, or the old token after a reconnect issued a new one). It
// is best-effort hygiene: killing a credential that would otherwise stay valid.
// A provider with no RevokeURL has nothing to revoke and returns nil.
func (p Provider) Revoke(ctx context.Context, client *http.Client, clientID, clientSecret, token string) error {
	if p.RevokeURL == "" || token == "" {
		return nil
	}
	form := url.Values{"token": {token}, "token_type_hint": {"access_token"}}
	// Slack authenticates auth.revoke with the token itself (a Bearer header);
	// RFC 7009 servers (Discord) authenticate the CLIENT and take the token in
	// the body.
	if p.RevokeAuth != "bearer" {
		form.Set("client_id", clientID)
		form.Set("client_secret", clientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.RevokeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if p.RevokeAuth == "bearer" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<16))
	if res.StatusCode >= 400 {
		return fmt.Errorf("%s revoke returned %s", p.Label, res.Status)
	}
	// Slack answers 200 with {"ok":false,"error":...} on a bad token; a plain RFC
	// 7009 server answers 200 with an empty body. Read ok only when it is there.
	var out struct {
		OK    *bool  `json:"ok"`
		Error string `json:"error"`
	}
	if len(body) > 0 && json.Unmarshal(body, &out) == nil && out.OK != nil && !*out.OK {
		return fmt.Errorf("%s revoke refused: %s", p.Label, out.Error)
	}
	return nil
}
