package deepseek

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The BOS constant is the whole trick, so pin it against the numbers
// measured on the live API. If DeepSeek changes the template, these fail
// and the count silently drifting by one is caught here rather than in
// somebody's budget.
func TestCountTokensSubtractsBOS(t *testing.T) {
	cases := []struct {
		name         string
		text         string
		promptTokens int
		want         int
	}{
		{"single char", "a", 2, 1},
		{"word", "hello", 2, 1},
		{"two words", "hello world", 3, 2},
		{"han", "你好", 2, 1},
		{"han punctuated", "你好，世界", 4, 3},
		{"sentence", "The quick brown fox jumps over the lazy dog.", 11, 10},
		{"long", "x", 402, 401},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/beta/completions" {
					t.Errorf("counted via %s, want the FIM path", r.URL.Path)
				}
				var req FIMRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Fatalf("decoding request: %v", err)
				}
				if req.Prompt != tc.text {
					t.Errorf("prompt = %q, want %q — the text must go over the wire unwrapped", req.Prompt, tc.text)
				}
				if req.MaxTokens == nil || *req.MaxTokens != 1 {
					t.Errorf("max_tokens = %v, want 1: a count should not pay to generate", req.MaxTokens)
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{
					"id": "x", "object": "text_completion",
					"choices": []any{map[string]any{"text": "", "finish_reason": "length"}},
					"usage": map[string]any{
						"prompt_tokens": tc.promptTokens, "completion_tokens": 1,
						"total_tokens":            tc.promptTokens + 1,
						"prompt_cache_hit_tokens": 0, "prompt_cache_miss_tokens": tc.promptTokens,
					},
				})
			}))
			defer srv.Close()

			c := New("k", srv.URL, 5*time.Second)
			got, u, err := c.CountTokens(context.Background(), ModelFlash, tc.text)
			if err != nil {
				t.Fatalf("CountTokens: %v", err)
			}
			if got != tc.want {
				t.Errorf("tokens = %d, want %d", got, tc.want)
			}
			if u.InputTokens != tc.promptTokens {
				t.Errorf("billed input = %d, want the full %d — the caller pays for the BOS too",
					u.InputTokens, tc.promptTokens)
			}
		})
	}
}

// Empty text is answered without a request: the API rejects an empty
// prompt outright, and a network round trip to learn that zero is zero
// would cost money and fail.
func TestCountTokensEmptyMakesNoRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("empty text must not reach the API")
	}))
	defer srv.Close()

	c := New("k", srv.URL, 5*time.Second)
	got, u, err := c.CountTokens(context.Background(), ModelFlash, "")
	if err != nil {
		t.Fatalf("CountTokens: %v", err)
	}
	if got != 0 {
		t.Errorf("tokens = %d, want 0", got)
	}
	if !u.Empty() {
		t.Errorf("usage = %+v, want nothing billed", u)
	}
}

func TestEstimateTokens(t *testing.T) {
	cases := []struct {
		name string
		text string
		want int
	}{
		{"empty", "", 0},
		// 44 latin characters at the published 0.3 -> 13.2, rounded to 13.
		// The true count is 10: the published ratio runs high on English,
		// which is why this is documented as an upper bound.
		{"english sentence", "The quick brown fox jumps over the lazy dog.", 13},
		// 5 CJK characters (the comma is fullwidth) at 0.6 -> 3, which is
		// exactly what the tokenizer returns.
		{"chinese", "你好，世界", 3},
		// Mixed: 2 Han at 0.6 plus 5 latin at 0.3 -> 1.2 + 1.5 = 2.7 -> 3.
		{"mixed", "你好 abcd", 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EstimateTokens(tc.text); got != tc.want {
				t.Errorf("EstimateTokens(%q) = %d, want %d", tc.text, got, tc.want)
			}
		})
	}
}

// The estimate must never undercount CJK, which is the case where a
// budget built on it would blow through a context window.
func TestEstimateTokensDoesNotUndercountCJK(t *testing.T) {
	for _, s := range []string{"你好", "こんにちは", "안녕하세요", "、。「」"} {
		if got, chars := EstimateTokens(s), len([]rune(s)); float64(got) < float64(chars)*0.6-0.5 {
			t.Errorf("EstimateTokens(%q) = %d for %d CJK runes, below the 0.6 published ratio", s, got, chars)
		}
	}
}
