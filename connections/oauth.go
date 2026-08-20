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
		"scope":         {strings.Join(p.Scopes, " ")},
		"state":         {state},
		// offline + consent because hostit needs a REFRESH token, and Google
		// only returns one on the first consent unless it is asked for again.
		"access_type": {"offline"},
		"prompt":      {"consent"},
		// So adding a capability later does not silently drop scopes already
		// granted.
		"include_granted_scopes": {"true"},
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
	if res.RefreshToken == "" {
		// Without one, the connection would work for an hour and then die in a
		// way that looks like a hostit bug. Better to refuse the connection.
		return "", fmt.Errorf("%s returned no refresh token; disconnect it at the provider and connect again", p.Label)
	}
	return res.RefreshToken, nil
}

// Refresh exchanges the stored refresh token for an access token.
func (p Provider) Refresh(ctx context.Context, client *http.Client, clientID, clientSecret, refreshToken string) (Token, error) {
	res, err := p.postToken(ctx, client, url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"refresh_token": {refreshToken},
		"grant_type":    {"refresh_token"},
	})
	if err != nil {
		return Token{}, err
	}
	expiry := time.Now().Add(time.Duration(res.ExpiresIn) * time.Second)
	if res.ExpiresIn == 0 {
		expiry = time.Now().Add(time.Hour)
	}
	return Token{Provider: p.Name, AccessToken: res.AccessToken, ExpiresAt: expiry}, nil
}

func (p Provider) postToken(ctx context.Context, client *http.Client, form url.Values) (tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
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
