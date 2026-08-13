package deepseek

import "time"

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

// RepriceAt is when DeepSeek's dated repricing takes effect: 16:00 UTC
// on 2026-08-16 (midnight, Beijing), announced 2026-08-13 with the V4 GA
// release. From that instant the API bills peak/off-peak on a new,
// higher card, with off-peak at half the peak rate.
//
// TASTE.md's rule against applying announced-but-undated numbers does
// not apply here — these numbers carry their date, so the switch is
// encoded and gated on it, exactly as that scar's expiry clause says.
// Source: https://api-docs.deepseek.com/quick_start/pricing (2026-08-13).
var RepriceAt = time.Date(2026, time.August, 16, 16, 0, 0, 0, time.UTC)

// pricesFlat is the card published 2026-08-02, in force before RepriceAt.
var pricesFlat = map[string]Price{
	ModelFlash: {CacheHitInput: 0.0028, CacheMissInput: 0.14, Output: 0.28},
	ModelPro:   {CacheHitInput: 0.003625, CacheMissInput: 0.435, Output: 0.87},
}

// pricesOffPeak is the base card from RepriceAt on. During PeakWindows
// every billing item costs PeakMultiplier times these numbers; DeepSeek
// publishes the peak figures rather than the rule, and they are exactly
// double, so the multiplier is data, not interpretation.
var pricesOffPeak = map[string]Price{
	ModelFlash: {CacheHitInput: 0.007, CacheMissInput: 0.22, Output: 0.66},
	ModelPro:   {CacheHitInput: 0.022, CacheMissInput: 0.66, Output: 1.98},
}

// PeakMultiplier scales the off-peak card during PeakWindows.
const PeakMultiplier = 2.0

// Window is a daily time-of-day window in minutes of the UTC day, end
// exclusive. Upstream defines the boundaries in UTC, not Beijing.
type Window struct{ Start, End int }

// PeakWindows are the daily peak hours from RepriceAt on: 01:00-04:00
// and 06:00-10:00 UTC (09:00-12:00 and 14:00-18:00 Beijing).
var PeakWindows = []Window{{Start: 1 * 60, End: 4 * 60}, {Start: 6 * 60, End: 10 * 60}}

// Period names the pricing period one instant falls in.
type Period struct {
	// Label is "flat" before RepriceAt, then "peak" or "off-peak".
	Label string
	// Multiplier scales that era's base card. 1 except during peak.
	Multiplier float64
}

// PeriodAt reports the pricing period in force at one instant.
func PeriodAt(t time.Time) Period {
	if t.Before(RepriceAt) {
		return Period{Label: "flat", Multiplier: 1}
	}
	if inPeak(t) {
		return Period{Label: "peak", Multiplier: PeakMultiplier}
	}
	return Period{Label: "off-peak", Multiplier: 1}
}

func inPeak(t time.Time) bool {
	u := t.UTC()
	m := u.Hour()*60 + u.Minute()
	for _, w := range PeakWindows {
		if m >= w.Start && m < w.End {
			return true
		}
	}
	return false
}

// NextChange is the next instant after t at which the price of a call
// changes: the repricing instant while the flat card is in force, then
// the nearest peak-window boundary of the UTC day.
func NextChange(t time.Time) time.Time {
	if t.Before(RepriceAt) {
		return RepriceAt
	}
	u := t.UTC()
	m := u.Hour()*60 + u.Minute()
	day := time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
	for _, w := range PeakWindows {
		if m < w.Start {
			return day.Add(time.Duration(w.Start) * time.Minute)
		}
		if m < w.End {
			return day.Add(time.Duration(w.End) * time.Minute)
		}
	}
	return day.Add(24*time.Hour + time.Duration(PeakWindows[0].Start)*time.Minute)
}

// PriceAt returns the effective rate card for a model at one instant:
// that era's base card, scaled by the period's multiplier.
func PriceAt(model string, t time.Time) (Price, bool) {
	cards := pricesFlat
	if !t.Before(RepriceAt) {
		cards = pricesOffPeak
	}
	p, ok := cards[ResolveModel(model)]
	if !ok {
		return Price{}, false
	}
	if mult := PeriodAt(t).Multiplier; mult != 1 {
		p.CacheHitInput *= mult
		p.CacheMissInput *= mult
		p.Output *= mult
	}
	return p, true
}

// PriceFor returns the rate card in effect right now. Unknown models —
// including the Claude names the Anthropic endpoint remaps server-side —
// resolve through ResolveModel first.
func PriceFor(model string) (Price, bool) {
	return PriceAt(model, time.Now())
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

// Cost estimates what a request cost, in USD, from its token counts,
// priced at the card in force right now — the call being priced just
// happened. ok is false for a model with no published price, in which
// case callers should report tokens without a figure rather than print
// a zero.
func Cost(model string, u Usage) (usd float64, ok bool) {
	return CostAt(model, u, time.Now())
}

// CostAt prices token counts under the card in force at one instant,
// which is what makes ledger rows repriceable under any era.
func CostAt(model string, u Usage, t time.Time) (usd float64, ok bool) {
	p, ok := PriceAt(model, t)
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
// the cache-miss rate, minus what it did cost, under the card in force
// right now. This is the number that justifies structuring prompts for
// cache reuse, and no other DeepSeek tool surfaces it.
func CacheSavings(model string, u Usage) (usd float64, ok bool) {
	p, ok := PriceFor(model)
	if !ok {
		return 0, false
	}
	const perMillion = 1_000_000.0
	return float64(u.CacheHitTokens) * (p.CacheMissInput - p.CacheHitInput) / perMillion, true
}
