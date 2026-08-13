// Package meter works out what a proxied request cost.
//
// It reads usage out of DeepSeek's responses in every wire format the
// gateway proxies, streaming or not, and prices it. Metering is the load
// bearing part of the whole design: the budget circuit breaker is only
// as good as this package, and a request that slips through unbilled is
// a hole straight to the credit pool. So the rule here is that a
// response we cannot measure is charged an over-estimate, never zero.
package meter

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"
)

// Usage is token accounting normalised across the four wire formats.
// InputTokens always means the whole prompt, cache hits included, which
// is the OpenAI convention — the Anthropic format reports it the other
// way and is converted on the way in.
type Usage struct {
	InputTokens    int
	CacheHitTokens int
	OutputTokens   int
	// Found is false when no usage object turned up at all, which is what
	// triggers the over-estimate.
	Found bool
}

// Price is the published per-million-token rate card, in USD.
//
// Source of truth is https://api-docs.deepseek.com/quick_start/pricing,
// mirrored in the CLI at internal/deepseek/pricing.go. These two copies
// exist because the CLI and the gateway are separate Go modules; `make
// price-check` fails if they drift.
type Price struct {
	CacheHitInput  float64
	CacheMissInput float64
	Output         float64
}

// RepriceAt is when DeepSeek's dated repricing takes effect: 16:00 UTC
// on 2026-08-16, announced 2026-08-13. From that instant billing is
// peak/off-peak on a new, higher card. Under-charging our own budget
// after the flip would drain the credit pool at yesterday's prices, so
// the switch is encoded here and gated on the date, exactly as the CLI's
// copy does.
var RepriceAt = time.Date(2026, time.August, 16, 16, 0, 0, 0, time.UTC)

// ratesFlat is the card published 2026-08-02, in force before RepriceAt.
var ratesFlat = map[string]Price{
	"deepseek-v4-flash": {CacheHitInput: 0.0028, CacheMissInput: 0.14, Output: 0.28},
	"deepseek-v4-pro":   {CacheHitInput: 0.003625, CacheMissInput: 0.435, Output: 0.87},
}

// ratesOffPeak is the base card from RepriceAt on; during peakWindows
// every billing item costs peakMultiplier times these numbers.
var ratesOffPeak = map[string]Price{
	"deepseek-v4-flash": {CacheHitInput: 0.007, CacheMissInput: 0.22, Output: 0.66},
	"deepseek-v4-pro":   {CacheHitInput: 0.022, CacheMissInput: 0.66, Output: 1.98},
}

const peakMultiplier = 2.0

// peakWindows are the daily peak hours from RepriceAt on, in minutes of
// the UTC day, end exclusive: 01:00-04:00 and 06:00-10:00 UTC.
var peakWindows = [][2]int{{1 * 60, 4 * 60}, {6 * 60, 10 * 60}}

func inPeak(t time.Time) bool {
	u := t.UTC()
	m := u.Hour()*60 + u.Minute()
	for _, w := range peakWindows {
		if m >= w[0] && m < w[1] {
			return true
		}
	}
	return false
}

// PriceFor returns the rate card in effect right now, defaulting to the
// more expensive model. Charging an unknown model at pro rates is
// deliberate: if DeepSeek ships a third model and we have not updated
// this table, we want to over-charge our own budget, not under-charge it.
func PriceFor(model string) Price {
	return PriceAt(model, time.Now())
}

// PriceAt is PriceFor at a chosen instant: that era's base card, doubled
// inside a peak window.
func PriceAt(model string, t time.Time) Price {
	if t.Before(RepriceAt) {
		return cardFor(ratesFlat, model)
	}
	p := cardFor(ratesOffPeak, model)
	if inPeak(t) {
		p = scale(p, peakMultiplier)
	}
	return p
}

func cardFor(cards map[string]Price, model string) Price {
	if p, ok := cards[model]; ok {
		return p
	}
	switch {
	case strings.Contains(model, "pro") || model == "":
		return cards["deepseek-v4-pro"]
	case strings.Contains(model, "flash"):
		return cards["deepseek-v4-flash"]
	default:
		return cards["deepseek-v4-pro"]
	}
}

func scale(p Price, mult float64) Price {
	p.CacheHitInput *= mult
	p.CacheMissInput *= mult
	p.Output *= mult
	return p
}

// Cost prices a usage record at the card in force right now — the
// response being settled just arrived.
func Cost(model string, u Usage) float64 {
	return CostAt(model, u, time.Now())
}

// CostAt prices a usage record under the card in force at one instant.
func CostAt(model string, u Usage, t time.Time) float64 {
	return costWith(PriceAt(model, t), u)
}

func costWith(p Price, u Usage) float64 {
	const perMillion = 1_000_000.0
	miss := u.InputTokens - u.CacheHitTokens
	if miss < 0 {
		miss = 0
	}
	return float64(u.CacheHitTokens)*p.CacheHitInput/perMillion +
		float64(miss)*p.CacheMissInput/perMillion +
		float64(u.OutputTokens)*p.Output/perMillion
}

// Estimate is the admission-time ceiling on what a request could cost,
// and the pessimistic charge when its usage could not be read.
//
// It has to be a true upper bound, because the budget breaker reserves it
// before the request is forwarded — an estimate a request could exceed
// would turn the hard ceiling back into a horizon. So:
//
//   - Input is one token per byte. DeepSeek's tokenizer averages 3–4
//     bytes per token, but adversarial text can approach one, and it
//     cannot go below: a token never encodes less than a byte.
//   - Output is maxTokens plus a reasoning allowance. Thinking is on by
//     default upstream, and DeepSeek does not document that reasoning
//     tokens respect max_tokens — they are billed as output either way,
//     so the bound assumes they do not.
//
// A search request breaks the first rule: the pages DeepSeek reads on the
// caller's behalf arrive as input tokens the body never contained, so
// searchInputAllowance is added to the input bound instead.
// A third rule joined them with the dated repricing: the reservation is
// priced at the dearest card the request could settle under, not the
// card of the admission instant. A request admitted just before a peak
// window (or just before the repricing flip) can settle inside it, and
// an estimate the clock can outrun is not a ceiling.
func Estimate(model string, requestBytes, maxTokens int, search bool) float64 {
	return EstimateAt(model, requestBytes, maxTokens, search, time.Now())
}

// EstimateAt is Estimate at a chosen instant.
func EstimateAt(model string, requestBytes, maxTokens int, search bool, t time.Time) float64 {
	input := requestBytes + 1
	if search {
		input += searchInputAllowance
	}
	return costWith(ceilingAt(model, t), Usage{
		InputTokens:  input,
		OutputTokens: maxTokens + reasoningAllowance,
		Found:        false,
	})
}

// ceilingAt is the dearest card a request admitted at t could settle
// under. Upstream holds a connection up to ten minutes before inference
// begins, so a request is given an hour of in-flight allowance: if that
// hour crosses the repricing flip or touches a peak window, the
// reservation is priced at the dearer side. Off-peak admissions far from
// any boundary still reserve at the off-peak card — a ceiling should be
// unbeatable, not double.
func ceilingAt(model string, t time.Time) Price {
	const inFlight = time.Hour
	if t.Add(inFlight).Before(RepriceAt) {
		return cardFor(ratesFlat, model)
	}
	if !t.Before(RepriceAt) && !peakTouches(t, inFlight) {
		return cardFor(ratesOffPeak, model)
	}
	return scale(cardFor(ratesOffPeak, model), peakMultiplier)
}

// peakTouches reports whether any instant of [t, t+d] falls in a peak
// window. The endpoint checks cover every span shorter than the gaps
// between windows; the start-of-window check keeps this correct even if
// a future card ships a window shorter than the span.
func peakTouches(t time.Time, d time.Duration) bool {
	if inPeak(t) || inPeak(t.Add(d)) {
		return true
	}
	u := t.UTC()
	m := u.Hour()*60 + u.Minute()
	span := int(d / time.Minute)
	for _, w := range peakWindows {
		for _, start := range []int{w[0], w[0] + 24*60} {
			if start > m && start < m+span {
				return true
			}
		}
	}
	return false
}

// reasoningAllowance is the output headroom reserved for chain-of-thought
// tokens on top of the caller's visible max_tokens. 32k covers the
// longest thinking runs measured live; at flash rates it prices at under
// a cent, so over-reserving costs headroom, not money.
const reasoningAllowance = 32 << 10

// searchInputAllowance is the input headroom reserved for a server-side
// web search, whose page reads land in input_tokens without ever passing
// through the request body.
//
// 256k is a judgement, not a proof. A search request measured live on
// 2026-08-07 reported 40,260 input tokens after eleven server-side calls,
// so this is roughly six times the observed case; the model's 1M context
// is the only true bound, and reserving 1M would price a single search at
// more than half a day's budget and make the feature unofferable.
//
// The honest statement of the trade: within this allowance the budget is
// still a hard ceiling, and beyond it a search request can overshoot by
// the difference. Two things keep that survivable — the per-subject
// in-flight cap means one caller cannot stack such requests, and searches
// are rationed per user per day, so the overshoot is bounded by the few
// distinct callers who can be mid-search at the same moment.
const searchInputAllowance = 256 << 10

// rawUsage is permissive on purpose: it decodes the usage object of every
// format at once, using pointers so "absent" and "zero" stay distinct.
// Which fields are present is what identifies the format.
type rawUsage struct {
	// OpenAI chat and FIM.
	PromptTokens        *int `json:"prompt_tokens"`
	CompletionTokens    *int `json:"completion_tokens"`
	PromptCacheHit      *int `json:"prompt_cache_hit_tokens"`
	PromptTokensDetails *struct {
		Cached *int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`

	// Anthropic Messages and OpenAI Responses share these two names and
	// are told apart by the cache fields below.
	InputTokens  *int `json:"input_tokens"`
	OutputTokens *int `json:"output_tokens"`

	// Anthropic only. Note its input_tokens EXCLUDES cache reads, so the
	// prompt total is the sum of all three.
	CacheReadInput     *int `json:"cache_read_input_tokens"`
	CacheCreationInput *int `json:"cache_creation_input_tokens"`

	// Responses only.
	InputTokensDetails *struct {
		Cached *int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
}

func (r rawUsage) normalise() (Usage, bool) {
	switch {
	case r.PromptTokens != nil:
		u := Usage{InputTokens: *r.PromptTokens, Found: true}
		if r.CompletionTokens != nil {
			u.OutputTokens = *r.CompletionTokens
		}
		switch {
		case r.PromptCacheHit != nil:
			u.CacheHitTokens = *r.PromptCacheHit
		case r.PromptTokensDetails != nil && r.PromptTokensDetails.Cached != nil:
			u.CacheHitTokens = *r.PromptTokensDetails.Cached
		}
		return u, true

	case r.InputTokens != nil:
		u := Usage{InputTokens: *r.InputTokens, Found: true}
		if r.OutputTokens != nil {
			u.OutputTokens = *r.OutputTokens
		}
		if r.CacheReadInput != nil || r.CacheCreationInput != nil {
			// Anthropic: input_tokens is the uncached part only.
			if r.CacheReadInput != nil {
				u.CacheHitTokens = *r.CacheReadInput
				u.InputTokens += *r.CacheReadInput
			}
			if r.CacheCreationInput != nil {
				u.InputTokens += *r.CacheCreationInput
			}
		} else if r.InputTokensDetails != nil && r.InputTokensDetails.Cached != nil {
			u.CacheHitTokens = *r.InputTokensDetails.Cached
		}
		return u, true
	}
	return Usage{}, false
}

// envelope is the part of any response we care about: the usage, and the
// model that actually ran (which may differ from the one asked for).
type envelope struct {
	Model string    `json:"model"`
	Usage *rawUsage `json:"usage"`
	// Responses nests both inside a "response" object on its terminal
	// stream event.
	Response *struct {
		Model string    `json:"model"`
		Usage *rawUsage `json:"usage"`
	} `json:"response"`
}

// FromBody reads usage out of a complete, non-streamed response.
func FromBody(b []byte) (Usage, string) {
	var e envelope
	if err := json.Unmarshal(b, &e); err != nil {
		return Usage{}, ""
	}
	return fromEnvelope(e)
}

func fromEnvelope(e envelope) (Usage, string) {
	model := e.Model
	raw := e.Usage
	if raw == nil && e.Response != nil {
		raw = e.Response.Usage
		if model == "" {
			model = e.Response.Model
		}
	}
	if raw == nil {
		return Usage{}, model
	}
	u, ok := raw.normalise()
	if !ok {
		return Usage{}, model
	}
	return u, model
}

// maxSniffLine caps how much of one SSE line is buffered while looking
// for usage. Usage events are a few hundred bytes; anything past this is
// content, and content is not what we are reading.
const maxSniffLine = 256 << 10

// Sniffer extracts usage from a server-sent-event stream as it passes
// through, without buffering the stream or altering a byte of it.
//
// It keeps the last usage object it sees. All three streaming formats
// put theirs in the final event — measured against the live API on
// 2026-08-05 — so "last one wins" is both correct and robust to a format
// that starts reporting running totals.
type Sniffer struct {
	line     []byte
	skipping bool

	usage Usage
	model string
}

// Write consumes stream bytes. It never fails: a sniffer that errored
// would have to either break the user's stream or be ignored, and
// breaking the stream to protect our accounting is the wrong trade when
// the fallback is an over-estimate.
func (s *Sniffer) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			s.append(p)
			break
		}
		s.append(p[:i])
		s.flushLine()
		p = p[i+1:]
	}
	return n, nil
}

func (s *Sniffer) append(b []byte) {
	if s.skipping {
		return
	}
	if len(s.line)+len(b) > maxSniffLine {
		s.line = s.line[:0]
		s.skipping = true
		return
	}
	s.line = append(s.line, b...)
}

func (s *Sniffer) flushLine() {
	line := bytes.TrimSuffix(s.line, []byte("\r"))
	s.line = s.line[:0]
	s.skipping = false

	const prefix = "data:"
	if !bytes.HasPrefix(line, []byte(prefix)) {
		return
	}
	payload := bytes.TrimSpace(line[len(prefix):])
	// Cheap gate: parsing every content delta as JSON to find the one
	// event in a thousand that carries usage would be most of the CPU
	// this proxy spends.
	if !bytes.Contains(payload, []byte(`"usage"`)) {
		return
	}
	var e envelope
	if err := json.Unmarshal(payload, &e); err != nil {
		return
	}
	if u, model := fromEnvelope(e); u.Found {
		s.usage = u
		if model != "" {
			s.model = model
		}
	}
}

// Result returns what the stream reported. Call it after the body is
// fully copied.
func (s *Sniffer) Result() (Usage, string) {
	// A stream that ended without a trailing newline leaves its last line
	// unflushed, and that last line is exactly the one carrying usage.
	if len(s.line) > 0 {
		s.flushLine()
	}
	return s.usage, s.model
}
