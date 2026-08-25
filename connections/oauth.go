package connections

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	q.Set(scopeParam, strings.Join(p.Scopes, " "))
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

// Exchange trades the consent code for tokens. The refresh token is what hostit
// keeps; the access token is discarded here because the first request will mint
// a fresh one anyway.
func (p Provider) Exchange(ctx context.Context, client *http.Client, clientID, clientSecret, redirectURL, code string) (refreshToken string, err error) {
	res, err := p.postToken(ctx, client, url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"code":          {code},
		"grant_type":    {"authorization_code"},
		"redirect_uri":  {redirectURL},
	})
	if err != nil {
		return "", err
	}
	// A provider whose token does not expire hands back nothing to refresh, so
	// the access token IS the thing worth keeping.
	if p.LongLivedToken {
		tok := p.keptToken(res)
		if tok == "" {
			return "", fmt.Errorf("%s returned no access token", p.Label)
		}
		return tok, nil
	}
	if res.RefreshToken == "" {
		// Without one, the connection would work for an hour and then die in a
		// way that looks like a hostit bug. Better to refuse the connection.
		return "", fmt.Errorf("%s returned no refresh token; disconnect it at the provider and connect again", p.Label)
	}
	return res.RefreshToken, nil
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
		return tokenResponse{}, fmt.Errorf("%s refused: %s %s", p.Label, res.Error, res.ErrorDesc)
	}
	if resp.StatusCode != http.StatusOK {
		return tokenResponse{}, fmt.Errorf("%s returned HTTP %d", p.Label, resp.StatusCode)
	}
	return res, nil
}
