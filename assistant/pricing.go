package assistant

import "heckel.io/hostit/store"

// pricing is the per-million-token price of a model, in US dollars. Cache writes
// and reads are billed differently from fresh input.
type pricing struct {
	input      float64
	output     float64
	cacheWrite float64
	cacheRead  float64
}

// modelPricing maps an assistant model to its published per-million-token prices.
// An unknown model falls back to the Sonnet tier, which is the default model.
var modelPricing = map[string]pricing{
	"claude-sonnet-5": {input: 3, output: 15, cacheWrite: 3.75, cacheRead: 0.30},
}

// sonnetPricing is the fallback (and current default) tier.
var sonnetPricing = pricing{input: 3, output: 15, cacheWrite: 3.75, cacheRead: 0.30}

// CostUSD converts accumulated token usage into a dollar figure for the
// given model. It is an estimate: usage is summed over time and priced at current
// rates, so a rate change since the tokens were spent is not reflected.
func CostUSD(u store.AssistantUsage, model string) float64 {
	p, ok := modelPricing[model]
	if !ok {
		p = sonnetPricing
	}
	const perMillion = 1_000_000
	return (float64(u.InputTokens)*p.input +
		float64(u.OutputTokens)*p.output +
		float64(u.CacheWriteTokens)*p.cacheWrite +
		float64(u.CacheReadTokens)*p.cacheRead) / perMillion
}
