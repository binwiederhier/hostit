package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The OAuth half, which differs from connections/oauth.go in three ways that
// are the whole reason MCP is its own package:
//
//   - No client secret. hostit cannot have one per server it has never met, so
//     it is a PUBLIC client and PKCE is what stands in for the secret.
//   - A client_id that is a URL (Client ID Metadata Documents). The server
//     fetches it to learn who is asking, which replaces the dynamic client
//     registration MCP used to require and has since deprecated.
//   - A resource parameter on every request (RFC 8707), so the token is minted
//     for one MCP server and is worthless if that server replays it elsewhere.

// OAuthToken is what comes back from the authorization server.
type OAuthToken struct {
	AccessToken  string
	RefreshToken string
	// ExpiresAt is nil when the server did not say. Treated as "assume short"
	// by the caller rather than "never expires", which is the safe way round.
	ExpiresAt *time.Time
}

// AuthCodeURL is where the owner is sent to consent.
func AuthCodeURL(d Discovery, clientID, redirectURL, state string, pkce PKCE) string {
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirectURL},
		"state":                 {state},
		"code_challenge":        {pkce.Challenge},
		"code_challenge_method": {pkce.Method},
	}
	if d.Resource != "" {
		q.Set("resource", d.Resource)
	}
	if len(d.Scopes) > 0 {
		q.Set("scope", strings.Join(d.Scopes, " "))
	}
	sep := "?"
	if strings.Contains(d.AuthorizationEndpoint, "?") {
		sep = "&"
	}
	return d.AuthorizationEndpoint + sep + q.Encode()
}

// Exchange trades the consent code for tokens, proving possession of the PKCE
// verifier.
func Exchange(ctx context.Context, client *http.Client, d Discovery, clientID, redirectURL, code string, pkce PKCE) (OAuthToken, error) {
	return postToken(ctx, client, d, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURL},
		"client_id":     {clientID},
		"code_verifier": {pkce.Verifier},
	})
}

// Refresh exchanges the stored refresh token for a new access token.
//
// Unlike the catalog providers, rotation is not an occasional quirk to tolerate
// here: MCP REQUIRES public clients to rotate, so the returned RefreshToken is
// normally a new one and the caller must store it. It is filled in with the old
// value when the server sent none, so a caller that always stores what it gets
// back cannot accidentally erase a working token.
func Refresh(ctx context.Context, client *http.Client, d Discovery, clientID, refreshToken string) (OAuthToken, error) {
	tok, err := postToken(ctx, client, d, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {clientID},
	})
	if err != nil {
		return OAuthToken{}, err
	}
	if tok.RefreshToken == "" {
		tok.RefreshToken = refreshToken
	}
	return tok, nil
}

func postToken(ctx context.Context, client *http.Client, d Discovery, form url.Values) (OAuthToken, error) {
	if d.TokenEndpoint == "" {
		return OAuthToken{}, fmt.Errorf("the server did not say where its token endpoint is")
	}
	if d.Resource != "" {
		form.Set("resource", d.Resource)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return OAuthToken{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return OAuthToken{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return OAuthToken{}, err
	}
	var res struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Error        string `json:"error"`
		ErrorDesc    string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return OAuthToken{}, fmt.Errorf("%w: the token endpoint did not answer with a token", ErrProtocol)
	}
	if res.Error != "" {
		// Wrapped as ErrUnauthorized because every one of these means the same
		// thing to whoever has to act: this connection needs authorizing again.
		return OAuthToken{}, fmt.Errorf("%w: %s %s", ErrUnauthorized, res.Error, res.ErrorDesc)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return OAuthToken{}, fmt.Errorf("the token endpoint answered HTTP %d", resp.StatusCode)
	}
	if res.AccessToken == "" {
		return OAuthToken{}, fmt.Errorf("%w: the token endpoint returned no access token", ErrProtocol)
	}
	out := OAuthToken{AccessToken: res.AccessToken, RefreshToken: res.RefreshToken}
	if res.ExpiresIn > 0 {
		expiry := time.Now().Add(time.Duration(res.ExpiresIn) * time.Second)
		out.ExpiresAt = &expiry
	}
	return out, nil
}

// ClientMetadata is the document an authorization server fetches at the
// client_id URL to learn who is asking. It must be served publicly and must
// list the redirect hostit really uses, or every consent is refused.
func ClientMetadata(redirectURL, clientID string) json.RawMessage {
	doc, _ := json.Marshal(map[string]any{
		"client_id":                  clientID,
		"client_name":                "hostit",
		"client_uri":                 strings.TrimSuffix(clientID, "/.well-known/oauth-client"),
		"redirect_uris":              []string{redirectURL},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	})
	return doc
}

// ClientIDFor works out what hostit calls itself to one authorization server.
// Three cases, in the order MCP prefers them:
//
//  1. The server accepts a URL as a client_id (CIMD). Nothing to do -- it
//     fetches the metadata document and that is the whole registration.
//  2. It offers dynamic registration (RFC 7591). Deprecated by MCP in favour of
//     the above, but still what a server written before it offers.
//  3. Neither. There is nothing to try: the server only knows clients somebody
//     registered by hand, which is a job for an operator and not for this
//     process. GitHub's MCP endpoint is a real example.
//
// The third case is REPORTED rather than attempted, because guessing produces a
// consent error at the provider that nobody can trace back to here.
func ClientIDFor(ctx context.Context, client *http.Client, d Discovery, redirectURL, metadataURL string) (string, error) {
	if d.SupportsCIMD {
		return metadataURL, nil
	}
	if d.RegistrationEndpoint != "" {
		return Register(ctx, client, d, redirectURL, metadataURL)
	}
	where := d.Issuer
	if where == "" {
		where = d.TokenEndpoint
	}
	return "", fmt.Errorf("%s does not accept a client id by URL and offers no registration, so hostit cannot introduce itself to it; it needs an OAuth client registered there by hand", where)
}

// Register performs dynamic client registration (RFC 7591), returning the
// client id the server issued.
//
// hostit registers as a PUBLIC client: it has no safe place to put a per-server
// secret, so a server that issues one anyway is refusing the only terms hostit
// can offer, and that is said plainly rather than half worked around.
func Register(ctx context.Context, client *http.Client, d Discovery, redirectURL, metadataURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.RegistrationEndpoint,
		bytes.NewReader(ClientMetadata(redirectURL, metadataURL)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	var res struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		Error        string `json:"error"`
		ErrorDesc    string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return "", fmt.Errorf("%w: the registration endpoint did not answer with a client", ErrProtocol)
	}
	if res.Error != "" {
		return "", fmt.Errorf("registration refused: %s %s", res.Error, res.ErrorDesc)
	}
	if res.ClientID == "" {
		return "", fmt.Errorf("%w: the registration endpoint returned no client id", ErrProtocol)
	}
	if res.ClientSecret != "" {
		return "", fmt.Errorf("that server issued a client secret, which hostit has nowhere safe to keep; it only registers as a public client")
	}
	return res.ClientID, nil
}
