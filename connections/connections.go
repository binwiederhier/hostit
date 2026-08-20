// Package connections is the accounts an owner connects once -- Google, GitHub,
// an IMAP mailbox -- and then grants to their apps.
//
// The design decision this package exists to express: hostit brokers the
// CREDENTIAL, not the API. It holds the refresh token and hands an app a
// short-lived access token on request, so the app uses the vendor's own SDK and
// hostit never grows an API translation layer per vendor. That is safe here
// precisely because the credential is the app owner's own -- unlike the
// operator's assistant key, there is no third party to keep it from.
//
// See plans/260819-connections.md.
package connections

import (
	"fmt"
	"sort"
	"time"
)

const (
	// KindOAuth: hostit stores a refresh token and exchanges it for access
	// tokens. KindStatic: the owner pasted a credential used as-is.
	KindOAuth  = "oauth"
	KindStatic = "static"
)

// Provider is one connectable service.
type Provider struct {
	// Name is the id used everywhere: the URL, the grant, the token endpoint.
	Name string
	// Label is what a person reads.
	Label string
	Kind  string
	// Scopes are requested at connect time (OAuth only). Asked for per
	// capability rather than all at once, so connecting Calendar does not
	// demand Gmail.
	Scopes []string
	// Fields are what a static provider asks the owner to paste. The one named
	// by SecretField is the credential; the rest are non-secret context (an
	// IMAP host) and are stored in the clear as meta.
	Fields      []Field
	SecretField string
	// AuthURL/TokenURL are the OAuth endpoints (OAuth only).
	AuthURL  string
	TokenURL string
	// Help is one line shown in the UI: where to get the credential.
	Help string
}

// Field is one input a static provider needs.
type Field struct {
	Name        string
	Label       string
	Placeholder string
	Secret      bool
}

// Token is what an app gets from the token endpoint. A static provider returns
// its credential with no expiry; an OAuth one returns a fresh access token.
// The shape is the same so an app does not care which kind it was granted.
type Token struct {
	Provider    string    `json:"provider"`
	AccessToken string    `json:"access_token"`
	ExpiresAt   time.Time `json:"expires_at,omitempty"`
	Meta        string    `json:"meta,omitempty"`
}

// registry holds the providers hostit knows how to connect.
var registry = map[string]Provider{}

// Register adds a provider. Called from init() in each provider's file, so the
// set of providers is the set of files that define one.
func Register(p Provider) { registry[p.Name] = p }

// Lookup returns a provider by name.
func Lookup(name string) (Provider, bool) {
	p, ok := registry[name]
	return p, ok
}

// All returns every known provider, ordered so the UI is stable.
func All() []Provider {
	out := make([]Provider, 0, len(registry))
	for _, p := range registry {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Configured reports whether this provider can actually be offered here. An
// OAuth provider needs the instance to have a client; a static one needs
// nothing, so it is always available.
func (p Provider) Configured(clientID, clientSecret string) bool {
	if p.Kind != KindOAuth {
		return true
	}
	return clientID != "" && clientSecret != ""
}

// Validate checks a static provider's submitted fields.
func (p Provider) Validate(values map[string]string) error {
	if p.Kind != KindStatic {
		return fmt.Errorf("%s is not a static provider", p.Name)
	}
	for _, f := range p.Fields {
		if values[f.Name] == "" {
			return fmt.Errorf("%s is required", f.Label)
		}
	}
	return nil
}
