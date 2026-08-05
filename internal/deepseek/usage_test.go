package deepseek

import (
	"encoding/json"
	"math"
	"testing"
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
	// 1M cache-miss input + 1M output on flash: 0.14 + 0.28.
	got, ok := Cost(ModelFlash, Usage{InputTokens: 1_000_000, CacheMissTokens: 1_000_000, OutputTokens: 1_000_000})
	if !ok {
		t.Fatal("flash should be priced")
	}
	if math.Abs(got-0.42) > 1e-9 {
		t.Errorf("got %v, want 0.42", got)
	}

	// The same prompt served entirely from cache costs 50x less on input.
	cached, _ := Cost(ModelFlash, Usage{InputTokens: 1_000_000, CacheHitTokens: 1_000_000})
	if math.Abs(cached-0.0028) > 1e-9 {
		t.Errorf("cached input = %v, want 0.0028", cached)
	}
}

func TestCacheSavings(t *testing.T) {
	// What the cached tokens would have cost at the miss rate, minus what
	// they did cost: 1M * (0.14 - 0.0028).
	got, ok := CacheSavings(ModelFlash, Usage{InputTokens: 1_000_000, CacheHitTokens: 1_000_000})
	if !ok {
		t.Fatal("flash should be priced")
	}
	if math.Abs(got-0.1372) > 1e-9 {
		t.Errorf("got %v, want 0.1372", got)
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
	opus, _ := Cost("claude-opus-4-1", u)
	pro, _ := Cost(ModelPro, u)
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
