package deepseek

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

// Fixed instants, one per pricing period, so these tests do not change
// meaning as the wall clock crosses RepriceAt.
var (
	atFlat    = time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC) // before the flip
	atOffPeak = time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC) // after, outside the windows
	atPeak    = time.Date(2026, 8, 17, 2, 0, 0, 0, time.UTC)  // after, inside 01:00-04:00 UTC
)

// The three wire formats report token usage with different conventions.
// These tests pin the difference, because getting it wrong silently
// misprices every call.
//
// The Anthropic numbers below are a real response: the same prompt sent
// to /chat/completions reported prompt_tokens 289, and the Anthropic
// endpoint reported input_tokens 33 with cache_read_input_tokens 256.
// 33 + 256 = 289 — that endpoint's input_tokens EXCLUDES cache reads.

func TestChatUsageNormalize(t *testing.T) {
	var u ChatUsage
	mustJSON(t, `{
		"prompt_tokens": 289,
		"completion_tokens": 56,
		"total_tokens": 345,
		"prompt_cache_hit_tokens": 256,
		"prompt_cache_miss_tokens": 33,
		"completion_tokens_details": {"reasoning_tokens": 20}
	}`, &u)

	got := u.Normalize()
	want := Usage{InputTokens: 289, CacheHitTokens: 256, CacheMissTokens: 33, OutputTokens: 56, ReasoningTokens: 20, TotalTokens: 345}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestChatUsageWithoutCacheSplit(t *testing.T) {
	// No split reported: everything counts as a miss, which over-reports
	// rather than under-reports cost.
	var u ChatUsage
	mustJSON(t, `{"prompt_tokens": 100, "completion_tokens": 10, "total_tokens": 110}`, &u)

	got := u.Normalize()
	if got.CacheMissTokens != 100 || got.CacheHitTokens != 0 {
		t.Errorf("got %+v, want all 100 prompt tokens as misses", got)
	}
}

func TestAnthropicUsageNormalize(t *testing.T) {
	var u AnthropicUsage
	mustJSON(t, `{
		"input_tokens": 33,
		"output_tokens": 56,
		"cache_read_input_tokens": 256,
		"cache_creation_input_tokens": 0
	}`, &u)

	got := u.Normalize()
	// The full prompt is the sum of all three input fields.
	if got.InputTokens != 289 {
		t.Errorf("InputTokens = %d, want 289 (33 uncached + 256 cache reads)", got.InputTokens)
	}
	if got.CacheHitTokens != 256 || got.CacheMissTokens != 33 {
		t.Errorf("cache split = %d hit / %d miss, want 256 / 33", got.CacheHitTokens, got.CacheMissTokens)
	}
}

func TestAnthropicCacheCreationIsBilledAsAMiss(t *testing.T) {
	// Cache creation tokens were processed, not replayed, so they belong
	// on the expensive side of the ledger.
	var u AnthropicUsage
	mustJSON(t, `{"input_tokens": 10, "output_tokens": 5, "cache_read_input_tokens": 0, "cache_creation_input_tokens": 90}`, &u)

	got := u.Normalize()
	if got.CacheMissTokens != 100 || got.CacheHitTokens != 0 {
		t.Errorf("got %d miss / %d hit, want 100 / 0", got.CacheMissTokens, got.CacheHitTokens)
	}
}

func TestResponsesUsageNormalize(t *testing.T) {
	// This format follows the OpenAI convention: input_tokens is the whole
	// prompt and cached_tokens is a subset of it.
	var u ResponsesUsage
	mustJSON(t, `{
		"input_tokens": 289,
		"input_tokens_details": {"cached_tokens": 256},
		"output_tokens": 56,
		"output_tokens_details": {"reasoning_tokens": 20},
		"total_tokens": 345
	}`, &u)

	got := u.Normalize()
	want := Usage{InputTokens: 289, CacheHitTokens: 256, CacheMissTokens: 33, OutputTokens: 56, ReasoningTokens: 20, TotalTokens: 345}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestNilUsageIsEmpty(t *testing.T) {
	var chat *ChatUsage
	var anth *AnthropicUsage
	var resp *ResponsesUsage
	for name, got := range map[string]Usage{
		"chat":      chat.Normalize(),
		"anthropic": anth.Normalize(),
		"responses": resp.Normalize(),
	} {
		if !got.Empty() {
			t.Errorf("%s: nil usage should normalize to empty, got %+v", name, got)
		}
	}
}

func TestCacheHitRate(t *testing.T) {
	if got := (Usage{InputTokens: 200, CacheHitTokens: 50}).CacheHitRate(); got != 0.25 {
		t.Errorf("got %v, want 0.25", got)
	}
	if got := (Usage{}).CacheHitRate(); got != 0 {
		t.Errorf("empty usage should not divide by zero, got %v", got)
	}
}

func TestCost(t *testing.T) {
	// 1M cache-miss input + 1M output on flash, flat card: 0.14 + 0.28.
	got, ok := CostAt(ModelFlash, Usage{InputTokens: 1_000_000, CacheMissTokens: 1_000_000, OutputTokens: 1_000_000}, atFlat)
	if !ok {
		t.Fatal("flash should be priced")
	}
	if math.Abs(got-0.42) > 1e-9 {
		t.Errorf("got %v, want 0.42", got)
	}

	// The same prompt served entirely from cache costs 50x less on input.
	cached, _ := CostAt(ModelFlash, Usage{InputTokens: 1_000_000, CacheHitTokens: 1_000_000}, atFlat)
	if math.Abs(cached-0.0028) > 1e-9 {
		t.Errorf("cached input = %v, want 0.0028", cached)
	}
}

// The dated repricing: flat until 16:00 UTC on 2026-08-16, then a new
// base card off-peak and exactly double during the two UTC peak windows.
// Wrong period arithmetic here misprices every estimate by 2x, which is
// precisely what the TASTE scar about undated multipliers was guarding
// against — these numbers are dated, so they are encoded and pinned.
func TestRepricingSwitchesOnItsEffectiveInstant(t *testing.T) {
	u := Usage{InputTokens: 1_000_000, CacheMissTokens: 1_000_000, OutputTokens: 1_000_000}

	before, _ := CostAt(ModelFlash, u, RepriceAt.Add(-time.Nanosecond))
	if math.Abs(before-0.42) > 1e-9 {
		t.Errorf("one instant before the flip: got %v, want the flat 0.42", before)
	}

	// 16:00 UTC is outside both peak windows, so the flip lands on the
	// off-peak card: 0.22 + 0.66.
	at, _ := CostAt(ModelFlash, u, RepriceAt)
	if math.Abs(at-0.88) > 1e-9 {
		t.Errorf("at the flip: got %v, want the off-peak 0.88", at)
	}

	peak, _ := CostAt(ModelFlash, u, atPeak)
	if math.Abs(peak-1.76) > 1e-9 {
		t.Errorf("in a peak window: got %v, want 1.76 (2x off-peak)", peak)
	}

	pro, _ := CostAt(ModelPro, u, atPeak)
	if math.Abs(pro-(1.32+3.96)) > 1e-9 {
		t.Errorf("pro in a peak window: got %v, want 5.28", pro)
	}
}

func TestPeriodBoundariesAreUTCAndEndExclusive(t *testing.T) {
	day := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	cases := map[time.Duration]string{
		0 * time.Hour:                  "off-peak", // midnight UTC
		1 * time.Hour:                  "peak",     // window start is inclusive
		4*time.Hour - time.Minute:      "peak",
		4 * time.Hour:                  "off-peak", // window end is exclusive
		6 * time.Hour:                  "peak",
		10*time.Hour - time.Nanosecond: "peak",
		10 * time.Hour:                 "off-peak",
		12 * time.Hour:                 "off-peak",
	}
	for d, want := range cases {
		if got := PeriodAt(day.Add(d)); got.Label != want {
			t.Errorf("PeriodAt(+%v) = %q, want %q", d, got.Label, want)
		}
	}
	if got := PeriodAt(atFlat); got.Label != "flat" || got.Multiplier != 1 {
		t.Errorf("before the flip: got %+v, want flat at 1x", got)
	}
}

func TestNextChange(t *testing.T) {
	if got := NextChange(atFlat); !got.Equal(RepriceAt) {
		t.Errorf("before the flip the next change is the flip, got %v", got)
	}
	day := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	cases := map[time.Duration]time.Duration{
		30 * time.Minute: 1 * time.Hour,  // off-peak, next: peak starts
		2 * time.Hour:    4 * time.Hour,  // peak, next: peak ends
		5 * time.Hour:    6 * time.Hour,  // gap between windows
		7 * time.Hour:    10 * time.Hour, // second window
		12 * time.Hour:   25 * time.Hour, // rest of day: tomorrow's first window
	}
	for at, want := range cases {
		if got := NextChange(day.Add(at)); !got.Equal(day.Add(want)) {
			t.Errorf("NextChange(+%v) = %v, want +%v", at, got, want)
		}
	}
}

func TestCacheSavings(t *testing.T) {
	// What the cached tokens would have cost at the miss rate, minus what
	// they did cost, under the card in force now. The exact figure moves
	// with the era, so pin the identity against PriceFor rather than a
	// constant that would silently change meaning at the flip.
	u := Usage{InputTokens: 1_000_000, CacheHitTokens: 1_000_000}
	got, ok := CacheSavings(ModelFlash, u)
	if !ok {
		t.Fatal("flash should be priced")
	}
	p, _ := PriceFor(ModelFlash)
	if want := p.CacheMissInput - p.CacheHitInput; math.Abs(got-want) > 1e-9 {
		t.Errorf("got %v, want %v", got, want)
	}
	if zero, _ := CacheSavings(ModelFlash, Usage{InputTokens: 100, CacheMissTokens: 100}); zero != 0 {
		t.Errorf("no cache hits should mean no savings, got %v", zero)
	}
}

func TestResolveModel(t *testing.T) {
	// The Anthropic endpoint remaps Claude names server-side; cost has to
	// land on the model that actually ran.
	cases := map[string]string{
		ModelFlash:          ModelFlash,
		ModelPro:            ModelPro,
		"claude-opus-4-1":   ModelPro,
		"claude-sonnet-4-5": ModelFlash,
		"claude-haiku-4-5":  ModelFlash,
		"something-unknown": ModelFlash,
		"claude-opusque":    ModelPro, // prefix match, as documented
	}
	for in, want := range cases {
		if got := ResolveModel(in); got != want {
			t.Errorf("ResolveModel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCostFollowsTheRemappedModel(t *testing.T) {
	// A Claude name billed at pro rates, not flash rates.
	u := Usage{InputTokens: 1_000_000, CacheMissTokens: 1_000_000}
	opus, _ := CostAt("claude-opus-4-1", u, atOffPeak)
	pro, _ := CostAt(ModelPro, u, atOffPeak)
	if opus != pro {
		t.Errorf("claude-opus cost %v, deepseek-v4-pro cost %v — should match", opus, pro)
	}
}

func mustJSON(t *testing.T, s string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(s), v); err != nil {
		t.Fatalf("fixture does not parse: %v", err)
	}
}
