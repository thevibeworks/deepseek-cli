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

// RepriceAt is when DeepSeek's repricing took effect: 16:00 UTC on
// 2026-08-16 (midnight, Beijing), announced 2026-08-13 with the V4 GA
// release. From that instant the API bills peak/off-peak on a new,
// higher card, with off-peak at half the peak rate.
//
// This is live, and measured, not just read off the page: a 188,542
// cache-miss-token call to pro in the off-peak window on 2026-08-17
// settled at 0.84 CNY, i.e. 4.46 CNY/1M against the new card's 4.5 and
// the old card's 3.0.
//
// TASTE.md's rule against applying announced-but-undated numbers does
// not apply here — these numbers carry their date, so the switch is
// encoded and gated on it, exactly as that scar's expiry clause says.
// Source: https://api-docs.deepseek.com/quick_start/pricing (2026-08-13).
var RepriceAt = time.Date(2026, time.August, 16, 16, 0, 0, 0, time.UTC)

// WeekendOffPeakAt is when weekends stopped billing peak at all: 16:00
// UTC on 2026-08-22 (00:00 Beijing, Sunday 2026-08-23). From that
// instant a Saturday or Sunday *on the Beijing calendar* is off-peak for
// all 24 hours, so peak is 35 hours a week rather than 49.
//
// DeepSeek put this only in the pricing-page footnote, and only for the
// few days before it took effect — there is no changelog entry, and the
// live page now carries just the settled rule. The announcement survives
// at web.archive.org/web/20260822141620/https://api-docs.deepseek.com/quick_start/pricing/
//
//	"Effective 00:00 (Beijing Time) on Sunday, August 23, 2026, we will
//	 adjust our peak/off-peak billing rules, with off-peak rates applying
//	 throughout the day on weekends (Saturdays and Sundays, Beijing Time)."
//
// Gated on its own instant rather than folded into RepriceAt because the
// ledger reprices history: a call made in a peak window on Sunday
// 2026-08-17 or Saturday 2026-08-22 really did bill peak.
var WeekendOffPeakAt = time.Date(2026, time.August, 22, 16, 0, 0, 0, time.UTC)

// V41At is when the Flash card dropped with the DeepSeek-V4.1-Flash
// release. Pro's card did not move. The changelog dates the release and
// the pricing page carries no instant, but DeepSeek's release note does:
// "New pricing takes effect at 04:00 UTC on Sept 10, 2026"
// (api-docs.deepseek.com/news/news260910, read 2026-09-18).
//
// Until that was read, this was 11:00 UTC, inferred from our docs mirror,
// which fetched the old card at 04:50 UTC that day and the new one at
// 11:27 UTC. The pricing page lagged the price; the note is the source.
var V41At = time.Date(2026, time.September, 10, 4, 0, 0, 0, time.UTC)

// beijing is the vendor's clock. China has observed no daylight saving
// since 1991, so a fixed offset is exact and needs no tzdata.
var beijing = time.FixedZone("CST", 8*60*60)

// The cards are keyed by ModelFlash and ModelPro as the two tiers, and
// ResolveModel maps every name the API accepts onto one of them. Each
// card is what that tier cost in its era, so ModelFlash's flat row is
// what deepseek-v4-flash cost then.
//
// Superseded cards stay because the ledger stores token counts rather
// than dollars, and a call must reprice under the card it was actually
// billed at.

// pricesFlat is the card published 2026-08-02, in force before RepriceAt.
var pricesFlat = map[string]Price{
	ModelFlash: {CacheHitInput: 0.0028, CacheMissInput: 0.14, Output: 0.28},
	ModelPro:   {CacheHitInput: 0.003625, CacheMissInput: 0.435, Output: 0.87},
}

// pricesV4OffPeak is the base card from RepriceAt until V41At. During
// PeakWindows every billing item costs PeakMultiplier times these
// numbers; DeepSeek publishes the peak figures rather than the rule, and
// they are exactly double, so the multiplier is data, not interpretation.
var pricesV4OffPeak = map[string]Price{
	ModelFlash: {CacheHitInput: 0.007, CacheMissInput: 0.22, Output: 0.66},
	ModelPro:   {CacheHitInput: 0.022, CacheMissInput: 0.66, Output: 1.98},
}

// pricesV41OffPeak is the base card from V41At on: Flash cut on every
// item, Pro unchanged. Same peak rule.
var pricesV41OffPeak = map[string]Price{
	ModelFlash: {CacheHitInput: 0.003, CacheMissInput: 0.15, Output: 0.6},
	ModelPro:   {CacheHitInput: 0.022, CacheMissInput: 0.66, Output: 1.98},
}

// Models are the priced models, in the order the rate card lists them.
// One list so a new model reaches every table at once. The retired names
// are not listed: they bill as ModelFlash, and the card says so once.
var Models = []string{ModelFlash, ModelPro}

// cardAt is the base card of the era in force at t.
func cardAt(t time.Time) map[string]Price {
	switch {
	case t.Before(RepriceAt):
		return pricesFlat
	case t.Before(V41At):
		return pricesV4OffPeak
	default:
		return pricesV41OffPeak
	}
}

// CardSince is the instant the base card in force at t took effect, and
// the zero time for the flat card, which has no start this schedule
// knows of.
func CardSince(t time.Time) time.Time {
	switch {
	case t.Before(RepriceAt):
		return time.Time{}
	case t.Before(V41At):
		return RepriceAt
	default:
		return V41At
	}
}

// PeakMultiplier scales the off-peak card during PeakWindows.
const PeakMultiplier = 2.0

// Window is a time-of-day window in minutes of the UTC day, end
// exclusive. Upstream defines the boundaries in UTC, not Beijing.
type Window struct{ Start, End int }

// PeakWindows are the peak hours from RepriceAt on: 01:00-04:00 and
// 06:00-10:00 UTC (09:00-12:00 and 14:00-18:00 Beijing). Daily until
// WeekendOffPeakAt, weekdays only after it — see isBeijingWeekend.
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
	if !t.Before(WeekendOffPeakAt) && isBeijingWeekend(t) {
		return false
	}
	u := t.UTC()
	m := u.Hour()*60 + u.Minute()
	for _, w := range PeakWindows {
		if m >= w.Start && m < w.End {
			return true
		}
	}
	return false
}

// isBeijingWeekend reports whether an instant falls on a Saturday or
// Sunday in Beijing. The weekday must be read on the vendor's clock
// because that is how the rule is published, which also means the
// weekend turns over at 16:00 UTC and not at midnight UTC.
func isBeijingWeekend(t time.Time) bool {
	switch t.In(beijing).Weekday() {
	case time.Saturday, time.Sunday:
		return true
	}
	return false
}

// NextChange is the next instant after t at which the price of a call
// changes: the repricing instant while the flat card is in force, then
// the nearest peak-window boundary or card change.
func NextChange(t time.Time) time.Time {
	if t.Before(RepriceAt) {
		return RepriceAt
	}
	// Every boundary this schedule has — a window edge, the weekend
	// turnover at 16:00 UTC, and the policy start dates — lands on a UTC
	// hour, so walking hours finds the next change exactly. Scanning a
	// week of them covers the longest run of one period, Friday 10:00
	// UTC to Monday 01:00 UTC, with room to spare. A new card is a change
	// even when the period's label is not.
	u := t.UTC()
	here, since := PeriodAt(u).Label, CardSince(u)
	at := u.Truncate(time.Hour).Add(time.Hour)
	for i := 0; i < 8*24; i++ {
		if PeriodAt(at).Label != here || !CardSince(at).Equal(since) {
			return at
		}
		at = at.Add(time.Hour)
	}
	return at
}

// BasePriceAt is a model's base card in the era in force at t, before
// any peak multiplier: the off-peak row once time-of-day billing began,
// the flat row before it.
func BasePriceAt(model string, t time.Time) (Price, bool) {
	p, ok := cardAt(t)[ResolveModel(model)]
	return p, ok
}

// PriceAt returns the effective rate card for a model at one instant:
// that era's base card, scaled by the period's multiplier.
func PriceAt(model string, t time.Time) (Price, bool) {
	p, ok := BasePriceAt(model, t)
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
// flash, and anything else unrecognised falls back to flash. The retired
// V4 Flash names are served and billed as flash.
func ResolveModel(model string) string {
	switch {
	case model == ModelFlash || model == ModelPro:
		return model
	case model == ModelFlashV4 || model == ModelFlashVision:
		return ModelFlash
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
