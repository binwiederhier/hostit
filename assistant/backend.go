package assistant

import "sort"

// Credentials is what the catalog needs to know, and nothing more. It is a type
// of this package's own rather than control's config, because the sandbox
// already imports controlconf -- inlining an assistant.Config into
// controlconf.Config would be an import cycle. controlconf builds one of these
// from its keys, so the config FILE is unchanged and the assistant still owns
// what a backend is.
type Credentials struct {
	AnthropicAPIKey      string
	ClaudeCodeOAuthToken string
}

// A backend is a place turns can run: the operator's Claude subscription, the
// metered Anthropic API, and whatever comes next. The assistant owns what a
// backend IS, so adding one is a single implementation here rather than an edit
// in the config package, the mode dropdown, the validator and the admin view.
//
// The catalog follows from credentials, never from a hand-written list: an
// operator who configures a key gets exactly the models that key can serve, and
// cannot list one it cannot.

const (
	// BackendClaude runs on the operator's Claude subscription (a
	// claude-code-oauth-token), BackendAnthropic on the metered API (an
	// anthropic-api-key). The names are the id prefixes users see.
	BackendClaude    = "claude"
	BackendAnthropic = "anthropic"
)

// Backend is one place a turn can run.
type Backend interface {
	// Name is the id prefix and the icon key ("claude", "anthropic").
	Name() string
	// Configured reports whether this backend's credential is present. An
	// unconfigured backend contributes nothing to the catalog and cannot be
	// selected -- it is as if it did not exist.
	Configured(creds Credentials) bool
	// Models are the models this backend serves, in the order they should be
	// offered. Each carries the backend's own model string, which is NOT the
	// option id: the API and the subscription both serve Opus, so
	// "anthropic-opus-5" and "claude-opus-5" are different choices that bill
	// differently while naming the same model upstream.
	Models() []Model
}

// Model is one model a backend serves.
type Model struct {
	// Slug is the id suffix ("opus-5"); Label is what a person reads ("Opus 5");
	// Model is what the backend is actually asked for ("claude-opus-5").
	Slug  string
	Label string
	Model string
}

// Option is one entry in the mode dropdown: a backend and a model together,
// because neither alone says what runs or who pays.
type Option struct {
	ID      string `json:"id"`      // "claude-opus-5", "anthropic-haiku-4-5"
	Label   string `json:"label"`   // "Opus 5"
	Backend string `json:"backend"` // "claude", "anthropic" -- the icon, too
	Model   string `json:"-"`       // what the backend is asked for
}

// registry holds the known backends in the order their groups are offered.
var registry []Backend

// Register adds a backend to the catalog. Called from init() in each backend's
// file, so the set of backends is the set of files that implement one.
func Register(b Backend) {
	registry = append(registry, b)
	sort.SliceStable(registry, func(i, j int) bool {
		// The subscription first: it is the one an operator pays for up front,
		// so it is the one they mean by default.
		return registry[i].Name() == BackendClaude && registry[j].Name() != BackendClaude
	})
}

// Backends returns the registered backends, configured or not.
func Backends() []Backend {
	return append([]Backend(nil), registry...)
}

// Catalog is every option this instance can actually run, grouped by backend in
// registration order. Empty when nothing is configured, which is also when the
// assistant hides itself.
func Catalog(creds Credentials) []Option {
	opts := make([]Option, 0, 8)
	for _, b := range registry {
		if !b.Configured(creds) {
			continue
		}
		for _, m := range b.Models() {
			opts = append(opts, Option{
				ID:      b.Name() + "-" + m.Slug,
				Label:   m.Label,
				Backend: b.Name(),
				Model:   m.Model,
			})
		}
	}
	return opts
}

// Lookup resolves a selected id to its option; ok is false when the id names a
// backend that is not configured, which is what makes a stale per-app choice
// fall back instead of failing a turn.
func Lookup(creds Credentials, id string) (Option, bool) {
	for _, o := range Catalog(creds) {
		if o.ID == id {
			return o, true
		}
	}
	return Option{}, false
}

// Default is the option a turn uses when the app has never chosen one, or when
// its choice names something this instance no longer offers: the subscription's
// Opus when that backend is configured, else the API's Sonnet. Deliberately not
// configurable -- an operator who wants something else sets it per app, and a
// config key here only ever named a model someone had to keep in sync.
func Default(creds Credentials) (Option, bool) {
	catalog := Catalog(creds)
	for _, want := range []string{BackendClaude + "-opus-5", BackendAnthropic + "-sonnet-5"} {
		for _, o := range catalog {
			if o.ID == want {
				return o, true
			}
		}
	}
	// Neither preferred model is available: any configured option beats none.
	if len(catalog) > 0 {
		return catalog[0], true
	}
	return Option{}, false
}
