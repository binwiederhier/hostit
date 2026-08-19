package assistant

// The metered Anthropic API: pay per token against the operator's key.

func init() { Register(anthropicBackend{}) }

type anthropicBackend struct{}

func (anthropicBackend) Name() string { return BackendAnthropic }

func (anthropicBackend) Configured(creds Credentials) bool {
	return creds.AnthropicAPIKey != ""
}

// Models are ordered cheapest-first: the API bills per token, so the small model
// is the one to reach for by default.
func (anthropicBackend) Models() []Model {
	return []Model{
		{Slug: "haiku-4-5", Label: "Haiku 4.5", Model: "claude-haiku-4-5-20251001"},
		{Slug: "sonnet-5", Label: "Sonnet 5", Model: "claude-sonnet-5"},
		{Slug: "opus-5", Label: "Opus 5", Model: "claude-opus-5"},
	}
}
