package connections

import (
	"fmt"
	"regexp"
	"strings"
)

// Custom providers: catalog entries an operator writes for a service hostit has
// never heard of.
//
// This is cheap for one reason that was true from the start: a catalog entry is
// PURE DATA. There is no per-provider code anywhere -- Exchange and Refresh are
// plain OAuth 2.0, and the vendor quirks that looked like special cases (Google
// wanting access_type=offline, Atlassian wanting an audience, Slack issuing a
// token that never expires) are all fields. So an operator supplying the same
// data gets exactly the same behaviour, tested by the same tests.

// customNameRegex is what a provider name may be. It goes in a URL path and in
// a connection's provider column, so it stays boring.
var customNameRegex = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,30}[a-z0-9])$`)

// CustomSpec is one operator-written entry, as it appears in control.yml under
// connections:. Either Issuer or both AuthURL and TokenURL must be given.
type CustomSpec struct {
	Label  string
	Scopes []string
	// Issuer stands in for the two endpoints: hostit reads the service's own
	// authorization-server metadata to find them, the same walk it does for an
	// MCP server. Resolving needs the network, so it happens on first use
	// rather than here.
	Issuer         string
	AuthURL        string
	TokenURL       string
	AuthParams     map[string]string
	LongLivedToken bool
	Help           string
	NameHint       string
}

// CustomProvider builds a provider from an operator's entry, refusing a
// half-written one AT LOAD -- where the operator is looking -- rather than
// offering it and failing later on somebody's consent screen.
func CustomProvider(name string, spec CustomSpec) (Provider, error) {
	if !customNameRegex.MatchString(name) {
		return Provider{}, fmt.Errorf("%q is not a usable provider name: use lowercase letters, digits and dashes", name)
	}
	// A custom entry that shadowed a built-in would silently change what every
	// existing connection of that name means, which is not a thing an operator
	// should be able to do by accident.
	if _, taken := Lookup(name); taken {
		return Provider{}, fmt.Errorf("%q is a built-in provider; pick another name", name)
	}
	if strings.TrimSpace(spec.Label) == "" {
		return Provider{}, fmt.Errorf("%s needs a label: it is what a person reads in the Add menu", name)
	}
	if spec.Issuer == "" {
		if spec.AuthURL == "" {
			return Provider{}, fmt.Errorf("%s needs an auth-url (or an issuer to discover one from)", name)
		}
		if spec.TokenURL == "" {
			return Provider{}, fmt.Errorf("%s needs a token-url (or an issuer to discover one from)", name)
		}
	}
	for label, u := range map[string]string{"issuer": spec.Issuer, "auth-url": spec.AuthURL, "token-url": spec.TokenURL} {
		if u != "" && !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
			return Provider{}, fmt.Errorf("%s's %s must be a URL starting with https://", name, label)
		}
	}
	return Provider{
		Name:           name,
		Label:          strings.TrimSpace(spec.Label),
		Kind:           KindOAuth,
		Scopes:         spec.Scopes,
		Issuer:         spec.Issuer,
		AuthURL:        spec.AuthURL,
		TokenURL:       spec.TokenURL,
		AuthParams:     spec.AuthParams,
		LongLivedToken: spec.LongLivedToken,
		Help:           spec.Help,
		NameHint:       spec.NameHint,
		Custom:         true,
	}, nil
}

// NeedsDiscovery reports whether this provider's endpoints are still unknown --
// an operator gave an issuer instead of writing them out.
func (p Provider) NeedsDiscovery() bool {
	return p.Issuer != "" && (p.AuthURL == "" || p.TokenURL == "")
}

// WithEndpoints returns a copy carrying resolved endpoints.
func (p Provider) WithEndpoints(authURL, tokenURL string) Provider {
	p.AuthURL, p.TokenURL = authURL, tokenURL
	return p
}
