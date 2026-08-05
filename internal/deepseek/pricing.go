package deepseek

// Usage is token accounting normalized across all four wire formats, so
// one pricing function and one ledger row serve chat, FIM, Responses and
// Anthropic alike.
//
// InputTokens is always the full prompt, cache hits included:
// InputTokens == CacheHitTokens + CacheMissTokens.
type Usage struct {
	InputTokens     int `json:"input_tokens"`
	CacheHitTokens  int `json:"cache_hit_tokens"`
	CacheMissTokens int `json:"cache_miss_tokens"`
	OutputTokens    int `json:"output_tokens"`
	ReasoningTokens int `json:"reasoning_tokens"`
	TotalTokens     int `json:"total_tokens"`
}

// Empty reports whether the API returned no accounting at all.
func (u Usage) Empty() bool { return u.TotalTokens == 0 && u.InputTokens == 0 && u.OutputTokens == 0 }

// CacheHitRate is the fraction of the prompt served from the context
// cache, in [0,1]. Zero-prompt requests report 0.
func (u Usage) CacheHitRate() float64 {
	if u.InputTokens <= 0 {
		return 0
	}
	return float64(u.CacheHitTokens) / float64(u.InputTokens)
}

// Price is the published per-million-token rate card for one model, in
// USD. Source: https://api-docs.deepseek.com/quick_start/pricing
type Price struct {
	CacheHitInput  float64
	CacheMissInput float64
	Output         float64
}

// prices as published on 2026-08-02. DeepSeek adjusts these; `deepseek
// usage` labels every figure an estimate for that reason, and the raw
// token counts are kept in the ledger so any row can be repriced later.
//
// A peak/off-peak policy has been announced (2x on all billing items
// during 09:00-12:00 and 14:00-18:00 Beijing time) but has no effective
// date yet, so it is deliberately NOT applied: guessing that a request
// was billed double would be inventing data. When it lands, multiply here.
var prices = map[string]Price{
	ModelFlash: {CacheHitInput: 0.0028, CacheMissInput: 0.14, Output: 0.28},
	ModelPro:   {CacheHitInput: 0.003625, CacheMissInput: 0.435, Output: 0.87},
}

// PriceFor returns the rate card for a model. Unknown models — including
// the Claude names the Anthropic endpoint remaps server-side — resolve
// through ResolveModel first.
func PriceFor(model string) (Price, bool) {
	p, ok := prices[ResolveModel(model)]
	return p, ok
}

// ResolveModel maps whatever the caller asked for onto the model that
// actually ran, so cost lands on the right rate card.
//
// The Anthropic-format endpoint accepts Claude model names and remaps
// them: claude-opus* becomes pro, claude-haiku*/claude-sonnet* become
// flash, and anything else unrecognised falls back to flash.
func ResolveModel(model string) string {
	switch {
	case model == ModelFlash || model == ModelPro:
		return model
	case hasPrefix(model, "claude-opus"):
		return ModelPro
	case hasPrefix(model, "claude-haiku"), hasPrefix(model, "claude-sonnet"):
		return ModelFlash
	default:
		return ModelFlash
	}
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// Cost estimates what a request cost, in USD, from its token counts.
// ok is false for a model with no published price, in which case callers
// should report tokens without a figure rather than print a zero.
func Cost(model string, u Usage) (usd float64, ok bool) {
	p, ok := PriceFor(model)
	if !ok {
		return 0, false
	}
	const perMillion = 1_000_000.0
	usd = float64(u.CacheHitTokens)*p.CacheHitInput/perMillion +
		float64(u.CacheMissTokens)*p.CacheMissInput/perMillion +
		float64(u.OutputTokens)*p.Output/perMillion
	return usd, true
}

// CacheSavings is what the cached part of the prompt would have cost at
// the cache-miss rate, minus what it did cost. This is the number that
// justifies structuring prompts for cache reuse, and no other DeepSeek
// tool surfaces it.
func CacheSavings(model string, u Usage) (usd float64, ok bool) {
	p, ok := PriceFor(model)
	if !ok {
		return 0, false
	}
	const perMillion = 1_000_000.0
	return float64(u.CacheHitTokens) * (p.CacheMissInput - p.CacheHitInput) / perMillion, true
}
