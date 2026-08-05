package meter

import (
	"math"
	"strings"
	"testing"
)

// The payloads below are verbatim from the live API on 2026-08-05, one
// per wire format, captured with `deepseek raw`. Hand-written fixtures
// would only prove this package agrees with my memory of the shapes;
// these prove it agrees with DeepSeek.
const (
	liveChatChunk = `{"id":"c3a1414d-4231-4fc0-8070-253992e7e197","object":"chat.completion.chunk","created":1785932694,"model":"deepseek-v4-flash","system_fingerprint":"fp_a18b46594c_prod0820_fp8_kvcache_20260402","choices":[{"index":0,"delta":{"content":"","reasoning_content":null},"logprobs":null,"finish_reason":"length"}],"usage":{"prompt_tokens":85,"completion_tokens":5,"total_tokens":90,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":5},"prompt_cache_hit_tokens":0,"prompt_cache_miss_tokens":85}}`

	liveAnthropicDelta = `{"type":"message_delta","delta":{"stop_reason":"max_tokens","stop_sequence":null},"usage":{"input_tokens":85,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":5,"service_tier":"standard"}}`

	liveResponsesEvent = `{"type":"response.completed","response":{"id":"resp_1","object":"response","model":"deepseek-v4-flash","usage":{"input_tokens":85,"input_tokens_details":{"cached_tokens":0},"output_tokens":16,"output_tokens_details":{"reasoning_tokens":16}}}}`
)

func TestUsageFromEveryWireFormat(t *testing.T) {
	cases := []struct {
		name         string
		body         string
		in, hit, out int
		model        string
	}{{
		name: "openai chat", body: liveChatChunk,
		in: 85, hit: 0, out: 5, model: "deepseek-v4-flash",
	}, {
		name: "anthropic messages", body: liveAnthropicDelta,
		// No model field on this event, which is why the proxy falls back
		// to the model it asked for.
		in: 85, hit: 0, out: 5, model: "",
	}, {
		name: "openai responses", body: liveResponsesEvent,
		in: 85, hit: 0, out: 16, model: "deepseek-v4-flash",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, model := FromBody([]byte(tc.body))
			if !u.Found {
				t.Fatal("no usage found in a live response body")
			}
			if u.InputTokens != tc.in || u.CacheHitTokens != tc.hit || u.OutputTokens != tc.out {
				t.Errorf("usage = in %d hit %d out %d; want in %d hit %d out %d",
					u.InputTokens, u.CacheHitTokens, u.OutputTokens, tc.in, tc.hit, tc.out)
			}
			if model != tc.model {
				t.Errorf("model = %q, want %q", model, tc.model)
			}
		})
	}
}

// The Anthropic format reports input_tokens EXCLUDING cache reads while
// every other format includes them. Getting this backwards would
// under-bill every cached Anthropic request — the cheap ones we most
// want people using.
func TestAnthropicCacheReadsAreAddedToInput(t *testing.T) {
	body := `{"usage":{"input_tokens":100,"cache_creation_input_tokens":30,"cache_read_input_tokens":900,"output_tokens":7}}`
	u, _ := FromBody([]byte(body))
	if u.InputTokens != 1030 {
		t.Errorf("input = %d, want 1030 (100 uncached + 900 read + 30 created)", u.InputTokens)
	}
	if u.CacheHitTokens != 900 {
		t.Errorf("cache hits = %d, want 900", u.CacheHitTokens)
	}
	// And the price must reflect that only 130 tokens were billed at the
	// full rate.
	want := 900*0.0028/1e6 + 130*0.14/1e6 + 7*0.28/1e6
	if got := Cost("deepseek-v4-flash", u); math.Abs(got-want) > 1e-12 {
		t.Errorf("cost = %v, want %v", got, want)
	}
}

func TestOpenAICacheHitsAreInsideInput(t *testing.T) {
	body := `{"usage":{"prompt_tokens":1000,"completion_tokens":10,"prompt_cache_hit_tokens":960,"prompt_cache_miss_tokens":40}}`
	u, _ := FromBody([]byte(body))
	if u.InputTokens != 1000 || u.CacheHitTokens != 960 {
		t.Fatalf("in %d hit %d, want 1000/960", u.InputTokens, u.CacheHitTokens)
	}
	want := 960*0.0028/1e6 + 40*0.14/1e6 + 10*0.28/1e6
	if got := Cost("deepseek-v4-flash", u); math.Abs(got-want) > 1e-12 {
		t.Errorf("cost = %v, want %v", got, want)
	}
}

func TestNoUsageIsNotSilentlyZero(t *testing.T) {
	for _, body := range []string{
		`{"choices":[{"message":{"content":"hi"}}]}`,
		`not json at all`,
		``,
		`{"usage":{}}`,
	} {
		if u, _ := FromBody([]byte(body)); u.Found {
			t.Errorf("FromBody(%q) claimed to have found usage", body)
		}
	}
}

// The sniffer sees the stream in whatever chunks the network delivers.
// A usage object split across a read must still be found, because the
// alternative is charging the estimate for every real request.
func TestSnifferAcrossChunkBoundaries(t *testing.T) {
	stream := "data: {\"choices\":[{\"delta\":{\"content\":\"hel\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n" +
		"data: " + liveChatChunk + "\n\n" +
		"data: [DONE]\n\n"

	for _, size := range []int{1, 3, 7, 64, 1000, len(stream)} {
		t.Run(sizeName(size), func(t *testing.T) {
			var s Sniffer
			for i := 0; i < len(stream); i += size {
				end := min(i+size, len(stream))
				s.Write([]byte(stream[i:end]))
			}
			u, model := s.Result()
			if !u.Found {
				t.Fatalf("usage missed at chunk size %d", size)
			}
			if u.InputTokens != 85 || u.OutputTokens != 5 {
				t.Errorf("in %d out %d, want 85/5", u.InputTokens, u.OutputTokens)
			}
			if model != "deepseek-v4-flash" {
				t.Errorf("model = %q", model)
			}
		})
	}
}

// DeepSeek sends ": keep-alive" comments and blank lines while it waits
// to start inference — documented in quick_start/rate_limit. They must
// not confuse the parser.
func TestSnifferIgnoresKeepAliveTraffic(t *testing.T) {
	stream := ": keep-alive\n\n\n: keep-alive\n\ndata: " + liveChatChunk + "\n\ndata: [DONE]\n\n"
	var s Sniffer
	s.Write([]byte(stream))
	if u, _ := s.Result(); !u.Found || u.InputTokens != 85 {
		t.Errorf("keep-alive traffic broke the sniffer: %+v", u)
	}
}

// A stream cut off before its final newline still has its usage on the
// last line, and that line is the one that matters.
func TestSnifferFlushesAnUnterminatedFinalLine(t *testing.T) {
	var s Sniffer
	s.Write([]byte("data: " + liveChatChunk))
	if u, _ := s.Result(); !u.Found {
		t.Error("usage on an unterminated final line was dropped")
	}
}

// A single enormous SSE line must not be buffered without bound. The
// stream keeps working; only the metering for it falls back to the
// estimate, which is the safe direction.
func TestSnifferBoundsMemory(t *testing.T) {
	var s Sniffer
	huge := strings.Repeat("x", 4<<20)
	s.Write([]byte("data: " + huge + "\n"))
	if len(s.line) > maxSniffLine {
		t.Errorf("sniffer buffered %d bytes for one line", len(s.line))
	}
	// And it recovers for the next line.
	s.Write([]byte("data: " + liveChatChunk + "\n"))
	if u, _ := s.Result(); !u.Found {
		t.Error("sniffer did not recover after an oversized line")
	}
}

// An unmeterable response must cost more than a measured one, never
// less. This is the property that keeps "we could not read the usage"
// from being the cheapest way to use the service.
func TestEstimateExceedsATypicalRealCharge(t *testing.T) {
	const body = 4000
	const maxTokens = 4096

	est := Estimate("deepseek-v4-flash", body, maxTokens)
	real := Cost("deepseek-v4-flash", Usage{InputTokens: body / 3, OutputTokens: 800, Found: true})
	if est <= real {
		t.Errorf("estimate %v is not above a realistic charge %v; unbillable would be cheaper than billable", est, real)
	}
}

// If DeepSeek ships a model we have not priced, the unknown must be
// charged at the higher rate. Guessing low would let a new model become
// a way to spend the budget faster than it is counted.
func TestUnknownModelsArePricedHigh(t *testing.T) {
	pro := PriceFor("deepseek-v4-pro")
	for _, model := range []string{"", "deepseek-v5", "something-else"} {
		if got := PriceFor(model); got != pro {
			t.Errorf("PriceFor(%q) = %+v, want the pro rate card %+v", model, got, pro)
		}
	}
	if PriceFor("deepseek-v4-flash") == pro {
		t.Error("flash is being priced as pro")
	}
}

func sizeName(n int) string {
	switch n {
	case 1:
		return "byte at a time"
	default:
		return "chunks of " + itoa(n)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
