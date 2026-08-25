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
	"sync"
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
	// tokens. KindStatic: the owner pasted a credential used as-is. KindMCP:
	// hostit holds the token and CALLS the server, so the app gets tool results
	// rather than a credential.
	KindOAuth  = "oauth"
	KindStatic = "static"
	KindMCP    = "mcp"
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
	// demand Gmail. When a provider offers ScopeOptions, Scopes is the BASELINE
	// always granted on top of whatever the owner picks.
	Scopes []string
	// ScopeOptions are the read choices the add dialog offers as checkboxes, each
	// a labelled bundle of scopes the owner can grant or withhold. Empty means
	// the grant is fixed (Scopes exactly). The client sends back the chosen
	// KEYS, never raw scopes, and ResolveScopes maps them against this allowlist.
	ScopeOptions []ScopeOption
	// UserToken means the OAuth dance asks for a token that acts AS the person,
	// not a bot: the scopes go in user_scope (not scope) and the token comes back
	// under authed_user.access_token. Slack's personal connection is the one case
	// -- it reads the channels the owner is already in with no bot to invite.
	UserToken bool
	// Fields are what a static provider asks the owner to paste. The one named
	// by SecretField is the credential; the rest are non-secret context (an
	// IMAP host) and are stored in the clear as meta.
	Fields      []Field
	SecretField string
	// AuthURL/TokenURL are the OAuth endpoints (OAuth only).
	AuthURL  string
	TokenURL string
	// Issuer is set only on a custom provider whose operator gave one instead
	// of the two endpoints; they are then discovered from its metadata.
	Issuer string
	// Custom marks a provider an operator wrote rather than one hostit ships.
	// Personal narrows that further: one a USER defined, visible only to them.
	// Neither changes behaviour -- they are for the UI, so a person can tell
	// whose entries are whose.
	Custom   bool
	Personal bool
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
	// NameHint is what the add dialog suggests calling this connection. It is
	// provider knowledge rather than form knowledge: the form asking every
	// credential to be called "OpenAI key" is how Home Assistant ended up
	// suggesting that.
	NameHint string
}

// ScopeOption is one read choice the add dialog offers as a checkbox: a labelled
// bundle of provider scopes the owner can grant or withhold at connect time.
type ScopeOption struct {
	// Key is the stable id the client checks and sends back; the scopes
	// themselves never cross from the client, so a crafted request cannot
	// over-grant.
	Key string `json:"key"`
	// Label and Help are what the checkbox reads.
	Label string `json:"label"`
	Help  string `json:"help,omitempty"`
	// Scopes is what granting this option adds to the baseline. Not sent to the
	// client -- the raw scopes are the server's business; the client only names keys.
	Scopes []string `json:"-"`
	// Default is whether the box is checked when the dialog opens.
	Default bool `json:"default,omitempty"`
}

// ResolveScopes turns the option keys the dialog sent into the effective scope
// list: the baseline Scopes plus every selected option's scopes, in order and
// deduplicated. An unknown key is an error rather than a silent drop, so a
// crafted request cannot smuggle in a scope this provider never offered.
func (p Provider) ResolveScopes(keys []string) ([]string, error) {
	byKey := make(map[string]ScopeOption, len(p.ScopeOptions))
	for _, o := range p.ScopeOptions {
		byKey[o.Key] = o
	}
	seen := make(map[string]bool)
	var out []string
	add := func(scopes []string) {
		for _, scope := range scopes {
			if !seen[scope] {
				seen[scope] = true
				out = append(out, scope)
			}
		}
	}
	add(p.Scopes) // the baseline is always granted, first
	for _, key := range keys {
		o, ok := byKey[key]
		if !ok {
			return nil, fmt.Errorf("unknown scope option %q", key)
		}
		add(o.Scopes)
	}
	return out, nil
}

// DefaultScopeKeys are the options checked when the dialog opens.
func (p Provider) DefaultScopeKeys() []string {
	var keys []string
	for _, o := range p.ScopeOptions {
		if o.Default {
			keys = append(keys, o.Key)
		}
	}
	return keys
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

// registry holds the providers hostit knows how to connect. Register is called
// from init() in production, so writes finish before serving; the mutex makes it
// safe anyway (tests register at runtime, concurrent with request-path reads).
var (
	registry   = map[string]Provider{}
	registryMu sync.RWMutex // Protects registry
)

// Register adds a provider. Called from init() in each provider's file, so the
// set of providers is the set of files that define one.
func Register(p Provider) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[p.Name] = p
}

// Lookup returns a provider by name.
func Lookup(name string) (Provider, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	p, ok := registry[name]
	return p, ok
}

// All returns every known provider, ordered so the UI is stable.
func All() []Provider {
	registryMu.RLock()
	out := make([]Provider, 0, len(registry))
	for _, p := range registry {
		out = append(out, p)
	}
	registryMu.RUnlock()
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

// Validate checks the fields an owner submitted. OAuth providers take none --
// their credential comes from a consent round trip, not a form.
func (p Provider) Validate(values map[string]string) error {
	if len(p.Fields) == 0 {
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
