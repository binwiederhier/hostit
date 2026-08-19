package assistant

// The metered Anthropic API: pay per token against the operator's key.

func init() { Register(anthropicBackend{}) }

type anthropicBackend struct{}

func (anthropicBackend) Name() string { return BackendAnthropic }

func (anthropicBackend) Configured(creds Credentials) bool {
	return creds.AnthropicAPIKey != ""
}

// Models are ordered strongest-first, matching the subscription group above it:
// the menu shows both groups, and two lists sorted in opposite directions read
// as a mistake even when each one is defensible on its own.
func (anthropicBackend) Models() []Model {
	return []Model{
		{Slug: "opus-5", Label: "Opus 5", Model: "claude-opus-5"},
		{Slug: "sonnet-5", Label: "Sonnet 5", Model: "claude-sonnet-5"},
		{Slug: "haiku-4-5", Label: "Haiku 4.5", Model: "claude-haiku-4-5-20251001"},
	}
}
