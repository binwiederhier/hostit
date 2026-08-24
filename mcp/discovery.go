package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// Discovery is what hostit learned about an MCP server by asking it: whether it
// wants authorization at all, and if so, where to arrange it.
//
// Nothing here is configured. That is the whole difference between MCP and every
// other provider in the catalog: a Google or a Slack is written down once with
// its endpoints baked in, while an MCP server is a URL somebody pastes, and the
// only way to know what it wants is to ask.
type Discovery struct {
	// NeedsAuth is true when the server refused an unauthenticated request.
	NeedsAuth bool
	// CanAuthorize is true when enough was advertised to run an OAuth flow. A
	// server can need auth and still not say how, which is a dead end worth
	// naming rather than half-attempting.
	CanAuthorize bool

	// Resource is the canonical identifier the token must be bound to
	// (RFC 8707). Sent on both the authorization and token requests so the
	// token cannot be replayed against a different server.
	Resource string
	Issuer   string

	AuthorizationEndpoint string
	TokenEndpoint         string
	RegistrationEndpoint  string
	// SupportsCIMD means the authorization server accepts an HTTPS URL as a
	// client_id and fetches the metadata document there, so hostit needs no
	// registration at all. MCP deprecated dynamic registration in its favour.
	SupportsCIMD bool
	Scopes       []string
}

// resourceMetadataRe pulls the resource_metadata URL out of a WWW-Authenticate
// challenge. A tiny regex rather than a full auth-param parser: this is the one
// parameter that matters and the header is not otherwise interpreted.
var (
	resourceMetadataRe = regexp.MustCompile(`resource_metadata="([^"]+)"`)
	challengeScopeRe   = regexp.MustCompile(`scope="([^"]+)"`)
)

// Discover asks an MCP server what it wants. An unauthenticated server is a
// perfectly good answer and reported as one.
func Discover(ctx context.Context, client *http.Client, serverURL string) (Discovery, error) {
	if _, err := url.Parse(serverURL); err != nil {
		return Discovery{}, fmt.Errorf("not a usable server URL: %w", err)
	}
	// A bare POST is what an MCP client would send; the point is only whether
	// it is refused, so the body does not matter.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	if err != nil {
		return Discovery{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := client.Do(req)
	if err != nil {
		return Discovery{}, fmt.Errorf("cannot reach %s: %w", serverURL, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))

	if resp.StatusCode != http.StatusUnauthorized {
		return Discovery{NeedsAuth: false}, nil
	}
	out := Discovery{NeedsAuth: true, Resource: serverURL}
	challenge := resp.Header.Get("WWW-Authenticate")
	if m := challengeScopeRe.FindStringSubmatch(challenge); m != nil {
		out.Scopes = strings.Fields(m[1])
	}

	// RFC 9728: the challenge names where the resource describes itself. A
	// server that omits it is not necessarily broken, so try the well-known
	// path on its own origin before giving up.
	metaURL := ""
	if m := resourceMetadataRe.FindStringSubmatch(challenge); m != nil {
		metaURL = m[1]
	} else if u, err := url.Parse(serverURL); err == nil {
		metaURL = u.Scheme + "://" + u.Host + "/.well-known/oauth-protected-resource"
	}
	var resource struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
		ScopesSupported      []string `json:"scopes_supported"`
	}
	if err := getJSON(ctx, client, metaURL, &resource); err != nil || len(resource.AuthorizationServers) == 0 {
		return out, nil // needs auth, cannot say how
	}
	if resource.Resource != "" {
		out.Resource = resource.Resource
	}
	if len(out.Scopes) == 0 {
		out.Scopes = resource.ScopesSupported
	}

	meta, err := AuthServerMetadata(ctx, client, resource.AuthorizationServers[0])
	if err != nil {
		return out, nil
	}
	out.Issuer = meta.Issuer
	out.AuthorizationEndpoint = meta.AuthorizationEndpoint
	out.TokenEndpoint = meta.TokenEndpoint
	out.RegistrationEndpoint = meta.RegistrationEndpoint
	out.SupportsCIMD = meta.SupportsCIMD
	out.CanAuthorize = meta.AuthorizationEndpoint != "" && meta.TokenEndpoint != ""
	return out, nil
}

// AuthServerMetadata reads an OAuth authorization server's own description of
// itself, which is how anything here learns where to send someone to consent
// without those URLs being written down by hand.
//
// BOTH well-known paths are tried. Servers publish one or the other -- GitHub
// answers only at the OpenID one, plenty of others only at the OAuth one -- and
// a client that knows a single path reports a perfectly good server as broken.
//
// Exported because custom OAuth providers use the same walk: an operator who
// gives an issuer instead of two endpoints is asking for exactly this.
func AuthServerMetadata(ctx context.Context, client *http.Client, issuer string) (Discovery, error) {
	as := strings.TrimSuffix(issuer, "/")
	var meta struct {
		Issuer                            string `json:"issuer"`
		AuthorizationEndpoint             string `json:"authorization_endpoint"`
		TokenEndpoint                     string `json:"token_endpoint"`
		RegistrationEndpoint              string `json:"registration_endpoint"`
		ClientIDMetadataDocumentSupported bool   `json:"client_id_metadata_document_supported"`
	}
	for _, path := range []string{"/.well-known/oauth-authorization-server", "/.well-known/openid-configuration"} {
		if err := getJSON(ctx, client, as+path, &meta); err == nil && meta.TokenEndpoint != "" {
			return Discovery{
				Issuer:                meta.Issuer,
				AuthorizationEndpoint: meta.AuthorizationEndpoint,
				TokenEndpoint:         meta.TokenEndpoint,
				RegistrationEndpoint:  meta.RegistrationEndpoint,
				SupportsCIMD:          meta.ClientIDMetadataDocumentSupported,
			}, nil
		}
	}
	return Discovery{}, fmt.Errorf("%s does not publish authorization server metadata at either well-known path", as)
}

func getJSON(ctx context.Context, client *http.Client, rawURL string, into any) error {
	if rawURL == "" {
		return fmt.Errorf("no URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered HTTP %d", rawURL, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(into)
}
