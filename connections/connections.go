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
	"errors"
	"fmt"
	"regexp"
	"sort"
	"time"
)

var (
	// ErrInvalidCredential means the owner's input is wrong -- a missing field,
	// or a value that is not the shape the provider needs. Their mistake to
	// fix, so it must reach them as a 400 with the reason, not a 500.
	ErrInvalidCredential = errors.New("invalid credential")
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
	// AuthParams are the extra consent-URL parameters THIS provider needs --
	// Google's access_type=offline, Atlassian's audience. They are per provider
	// rather than global because copying Google's everywhere is how you end up
	// sending meaningless parameters to Slack.
	AuthParams map[string]string
	// LongLivedToken means the provider issues an access token that does not
	// expire and no refresh token (a Slack bot token, a GitHub OAuth App
	// token). hostit stores that token and hands it back as-is; there is
	// nothing to refresh, and demanding a refresh token would refuse a
	// perfectly good connection.
	LongLivedToken bool
	// Help is one line shown in the UI: where to get the credential.
	Help string
}

// Field is one input a static provider needs.
type Field struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Placeholder string `json:"placeholder,omitempty"`
	Secret      bool   `json:"secret,omitempty"`
	// Optional fields may be left blank; everything else is required. The
	// generic credential needs this -- its endpoint and note are context, not
	// part of the credential.
	Optional bool `json:"optional,omitempty"`
	// Multiline renders a textarea rather than an input. An SSH private key
	// does not fit on one line.
	Multiline bool `json:"multiline,omitempty"`
	// Pattern, when set, is a regex the value must match. It exists for the one
	// class of mistake worth catching at paste time -- an SSH PUBLIC key in the
	// private key box -- which otherwise validates as "some text" and then
	// fails much later inside an app, saying nothing useful.
	Pattern string `json:"-"`
	// PatternHint is what the owner is told when Pattern does not match.
	PatternHint string `json:"-"`
}

// Token is what an app gets from the token endpoint. A static provider returns
// its credential with no expiry; an OAuth one returns a fresh access token.
// The shape is the same so an app does not care which kind it was granted.
type Token struct {
	Provider    string `json:"provider"`
	AccessToken string `json:"access_token"`
	// ExpiresAt is nil when the credential does not expire (a pasted secret, a
	// Slack bot token). A POINTER because encoding/json ignores omitempty on a
	// time.Time: a zero value would serialise as 0001-01-01, and an app doing
	// the obvious thing would treat every static credential as long dead.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	Meta      string     `json:"meta,omitempty"`
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
		return fmt.Errorf("%w: %s takes no pasted fields", ErrInvalidCredential, p.Name)
	}
	for _, f := range p.Fields {
		value := values[f.Name]
		if value == "" {
			if f.Optional {
				continue
			}
			return fmt.Errorf("%w: %s is required", ErrInvalidCredential, f.Label)
		}
		if f.Pattern != "" {
			ok, err := regexp.MatchString(f.Pattern, value)
			if err != nil || !ok {
				return fmt.Errorf("%w: %s", ErrInvalidCredential, f.PatternHint)
			}
		}
	}
	return nil
}
