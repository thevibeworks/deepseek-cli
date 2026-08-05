package deepseek

import (
	"context"
	"strings"
	"unicode"
)

// Token counting.
//
// DeepSeek ships no count-tokens endpoint and no Go tokenizer. What it
// publishes is a demo tokenizer as a Python zip and a pair of rules of
// thumb (1 English character ~ 0.3 tokens, 1 Chinese character ~ 0.6).
// Neither gives an exact answer from a Go binary.
//
// The API does, though — from an endpoint nobody uses for this. The FIM
// completion at /beta/completions takes a raw prompt with no chat
// template wrapped around it, and reports prompt_tokens for exactly the
// bytes you sent. One BOS token is added and nothing else, measured
// across inputs from one character to 1,800:
//
//	""                                             -> rejected, empty prompt
//	"a"                                            -> 2   (1 + BOS)
//	"hello"                                        -> 2   (1 + BOS)
//	"hello world"                                  -> 3   (2 + BOS)
//	"你好"                                          -> 2   (1 + BOS)
//	"你好，世界"                                     -> 4   (3 + BOS)
//	"The quick brown fox jumps over the lazy dog."  -> 11  (10 + BOS)
//	the same sentence x40 (1,800 chars)             -> 402 (401 + BOS)
//
// So: tokens(text) == prompt_tokens - 1. Exact, not estimated, for the
// tokenizer that will actually bill you.
//
// The measurement is a real request and costs real money — the text is
// billed as input at the cache-miss rate, the same as sending it would
// have. Callers surface that rather than hide it.
const fimBOSTokens = 1

// CountTokens returns the exact number of tokens in text, as counted by
// the model that would process it, plus the usage the measurement itself
// incurred so the caller can price and record it.
//
// Empty text is answered locally: the API rejects an empty prompt, and
// the answer is zero regardless.
func (c *Client) CountTokens(ctx context.Context, model, text string) (tokens int, u Usage, err error) {
	if text == "" {
		return 0, Usage{}, nil
	}
	one := 1
	resp, _, err := c.FIM(ctx, &FIMRequest{
		Model:     model,
		Prompt:    text,
		MaxTokens: &one,
	})
	if err != nil {
		return 0, Usage{}, err
	}
	u = resp.Usage.Normalize()
	tokens = u.InputTokens - fimBOSTokens
	if tokens < 0 {
		tokens = 0
	}
	return tokens, u, nil
}

// EstimateTokens is the offline fallback: DeepSeek's own published
// character ratios, applied per rune.
//
//	https://api-docs.deepseek.com/quick_start/token_usage
//	1 English character ~ 0.3 token · 1 Chinese character ~ 0.6 token
//
// It is a rule of thumb and the docs say so. Measured against the real
// tokenizer it runs high on English prose — the sentence above is 44
// characters, which this estimates at 13 against a true 10 — and lands
// close on Chinese. Treat it as an upper bound for budgeting, and use
// CountTokens when the number has to be right.
func EstimateTokens(text string) int {
	var cjk, other int
	for _, r := range text {
		if isCJK(r) {
			cjk++
		} else {
			other++
		}
	}
	return int(float64(cjk)*0.6 + float64(other)*0.3 + 0.5)
}

// isCJK reports whether a rune is counted at the Chinese ratio. The
// published rule says "a Chinese word", which in practice covers the Han
// ideographs plus the CJK punctuation that sits between them; Japanese
// kana and Hangul tokenize at a similar density, so they count here too.
func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r) ||
		(r >= 0x3000 && r <= 0x303F) || // CJK symbols and punctuation
		(r >= 0xFF00 && r <= 0xFFEF) // halfwidth and fullwidth forms
}

// ChatEnvelopeTokens is what a chat request costs before any of your text
// is counted: the template around a single user message. Measured against
// /chat/completions with thinking disabled — an empty user message
// reports prompt_tokens 4, and "hi" reports 5.
const ChatEnvelopeTokens = 4

// ThinkingTemplate is the input surcharge thinking mode adds before a
// single reasoning token is generated. It depends on the model and the
// effort, and not in the way the docs imply.
//
// Measured on 2026-08-05 against /chat/completions, two prompts (10 and
// 36 tokens), each level run twice. The surcharge is exactly constant
// across prompt length — 89-10 == 115-36 == 79 — so it is a fixed
// template, not a proportion:
//
//	                       flash    pro
//	reasoning_effort none    +0      +0     thinking off entirely
//	                minimal  +0      +0
//	                low      +0      +0
//	                medium  +79      +0
//	                high    +79      +0     <- flash default
//	                xhigh   +79      +0
//	                max     +92     +79
//
// Three things here are in no documentation:
//
//  1. `none` is documented only for the Responses API, but the chat
//     endpoint takes it too, and it disables thinking exactly as
//     thinking:{type:"disabled"} does — same 10 prompt tokens, no
//     reasoning_content.
//  2. `minimal` is accepted and undocumented anywhere.
//  3. low and minimal still reason, at no template cost at all. On short
//     factual work that is the whole surcharge removed while keeping the
//     chain of thought, which is a cost lever nobody mentions.
//
// The API rejects only genuinely unknown values, with "unknown variant".
func ThinkingTemplate(model, effort string) int {
	pro := ResolveModel(model) == ModelPro
	switch strings.ToLower(effort) {
	case "none":
		return 0
	case "minimal", "low":
		return 0
	case "max":
		if pro {
			return 79
		}
		return 92
	default: // medium, high, xhigh, and the unset default of high
		if pro {
			return 0
		}
		return 79
	}
}

// EffortDisablesThinking reports whether an effort value turns thinking
// off outright, rather than merely turning it down.
func EffortDisablesThinking(effort string) bool {
	return strings.EqualFold(effort, "none")
}
