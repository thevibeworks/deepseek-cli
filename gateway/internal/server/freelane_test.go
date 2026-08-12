package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/thevibeworks/deepseek-cli/gateway/internal/quota"
)

const freeKey = "sk-the-free-lane-key"

// freeChatReply is what the free upstream sends back: the same wire
// format under its own model name, which is the one difference that has
// to survive the whole request path.
func freeChatReply(in, out int) string {
	return fmt.Sprintf(`{"id":"x","object":"chat.completion","model":"deepseek-v4-flash-free",
	 "choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],
	 "usage":{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d}}`, in, out, in+out)
}

// withFree wires a second upstream in front of the paid one.
func withFree(free *upstream) func(*Config, *quota.Limits) {
	return func(cfg *Config, _ *quota.Limits) {
		cfg.FreeBaseURL = free.server.URL
		cfg.FreeKeys = []string{freeKey}
		cfg.FreeModel = "deepseek-v4-flash-free"
	}
}

func chatBody() string {
	return `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}]}`
}

// The whole point: a chat request the free lane accepts never reaches the
// paid upstream, and never touches the credit pool.
func TestFreeLaneServesChatForNothing(t *testing.T) {
	paid := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the paid upstream was called for a request the free lane took")
		w.Write([]byte(chatReply(10, 5)))
	})
	free := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(freeChatReply(1000, 500)))
	})
	h := newHarness(t, paid, withFree(free))
	tok := h.enrol(t)

	resp := h.do(t, "POST", "/v1/chat/completions", tok, chatBody())
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("HTTP %d", resp.StatusCode)
	}
	h.settle(t)
	if free.count() != 1 {
		t.Fatalf("free upstream saw %d requests, want 1", free.count())
	}

	got := free.last(t)
	if got.Headers.Get("Authorization") != "Bearer "+freeKey {
		t.Error("the free lane was not authenticated with its own key")
	}
	// The client asked for deepseek-v4-flash and the allowlist approved
	// that name; only the lane knows the alias.
	if m, _ := got.Body["model"].(string); m != "deepseek-v4-flash-free" {
		t.Errorf("model reaching the free upstream = %q, want the alias", m)
	}

	// Tokens are recorded — the per-user allowance is abuse control, not
	// an accounting of money — but no money is.
	today := h.ledger.Today()
	if today.InputTokens != 1000 || today.OutputTokens != 500 {
		t.Errorf("tokens = %d/%d, want 1000/500", today.InputTokens, today.OutputTokens)
	}
	if today.SpentUSD != 0 {
		t.Errorf("a free-lane request charged $%f to the pool", today.SpentUSD)
	}
}

// A free lane that refuses is a detour, not an outage: the request is
// re-sent to the paid upstream and the caller never learns it happened.
func TestFreeLaneFallsBackOnRefusal(t *testing.T) {
	for _, code := range []int{429, 500, 404, 400} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			paid := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(chatReply(10, 5)))
			})
			free := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
				w.Write([]byte(`{"error":{"message":"no"}}`))
			})
			h := newHarness(t, paid, withFree(free))
			tok := h.enrol(t)

			resp := h.do(t, "POST", "/v1/chat/completions", tok, chatBody())
			defer resp.Body.Close()
			if resp.StatusCode != 200 {
				raw, _ := io.ReadAll(resp.Body)
				t.Fatalf("HTTP %d after fallback: %s", resp.StatusCode, raw)
			}
			h.settle(t)
			if paid.count() != 1 {
				t.Fatalf("paid upstream saw %d requests, want 1", paid.count())
			}
			if m, _ := paid.last(t).Body["model"].(string); m != "deepseek-v4-flash" {
				t.Errorf("model reaching the paid upstream = %q; the alias leaked", m)
			}
			if h.ledger.Today().SpentUSD <= 0 {
				t.Error("the paid upstream served this and was not charged for it")
			}
		})
	}
}

// The free lane is narrow on purpose. Everything it was not measured to
// carry goes straight to DeepSeek.
func TestFreeLaneOnlyCarriesChat(t *testing.T) {
	cases := []struct{ name, path, body string }{
		{"anthropic", "/v1/anthropic/v1/messages", `{"model":"deepseek-v4-flash","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`},
		{"fim", "/v1/completions", `{"model":"deepseek-v4-flash","prompt":"def f():"}`},
		{"search", "/v1/responses", `{"model":"deepseek-v4-flash","input":"hi","tools":[{"type":"web_search"}]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			paid := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(chatReply(10, 5)))
			})
			free := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
				t.Errorf("the free lane was offered %s, which it cannot serve", c.name)
				w.WriteHeader(500)
			})
			h := newHarness(t, paid, withFree(free))
			tok := h.enrol(t)

			resp := h.do(t, "POST", c.path, tok, c.body)
			resp.Body.Close()
			if free.count() != 0 {
				t.Fatalf("free upstream saw %d %s requests", free.count(), c.name)
			}
			if paid.count() != 1 {
				t.Fatalf("paid upstream saw %d requests, want 1", paid.count())
			}
		})
	}
}

// With the day's budget gone, chat keeps working on the free lane — and
// a free-lane refusal must not become a paid request, or the ceiling was
// never a ceiling.
func TestSpentBudgetDegradesToFreeLane(t *testing.T) {
	paid := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a request admitted past the budget reached the paid upstream")
		w.Write([]byte(chatReply(10, 5)))
	})
	served := true
	free := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if !served {
			w.WriteHeader(429)
			w.Write([]byte(`{"error":{"message":"busy"}}`))
			return
		}
		w.Write([]byte(freeChatReply(100, 50)))
	})
	// A budget of zero is "the day's money is gone" without having to
	// spend any of it first.
	h := newHarness(t, paid, func(cfg *Config, lim *quota.Limits) {
		withFree(free)(cfg, lim)
		lim.DailyBudgetUSD = 0
	})
	tok := h.enrol(t)

	resp := h.do(t, "POST", "/v1/chat/completions", tok, chatBody())
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("HTTP %d with a free lane available: %s", resp.StatusCode, raw)
	}
	h.settle(t)
	if h.ledger.Today().SpentUSD != 0 {
		t.Error("a request admitted past the budget spent money")
	}

	// Now the free lane refuses. There is nowhere left to go, and the
	// answer must be the quota refusal rather than a paid retry.
	served = false
	resp2 := h.do(t, "POST", "/v1/chat/completions", tok, chatBody())
	defer resp2.Body.Close()
	if resp2.StatusCode == 200 {
		t.Fatal("a refused free-lane request was served anyway")
	}
	if paid.count() != 0 {
		t.Fatalf("paid upstream saw %d requests while the budget was spent", paid.count())
	}
}

// Per-user limits are not money and are not survivable by a free lane:
// the tokens may be free, but the abuse is not.
func TestFreeLaneDoesNotBypassPerUserQuota(t *testing.T) {
	paid := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(chatReply(10, 5)))
	})
	free := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(freeChatReply(10, 5)))
	})
	h := newHarness(t, paid, func(cfg *Config, lim *quota.Limits) {
		withFree(free)(cfg, lim)
		lim.DailyRequests = 2
	})
	tok := h.enrol(t)

	for i := 0; i < 2; i++ {
		resp := h.do(t, "POST", "/v1/chat/completions", tok, chatBody())
		code := resp.StatusCode
		resp.Body.Close()
		if code != 200 {
			t.Fatalf("request %d: HTTP %d", i+1, code)
		}
	}
	resp := h.do(t, "POST", "/v1/chat/completions", tok, chatBody())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("the third request got HTTP %d, want 429", resp.StatusCode)
	}
}

// The status page has to distinguish "answering because we are paying"
// from "answering because someone else is".
func TestStatusReportsFreeLane(t *testing.T) {
	paid := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(chatReply(10, 5)))
	})
	first := true
	free := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if first {
			first = false
			w.Write([]byte(freeChatReply(10, 5)))
			return
		}
		w.WriteHeader(429)
	})
	h := newHarness(t, paid, withFree(free))
	tok := h.enrol(t)
	for i := 0; i < 2; i++ {
		h.do(t, "POST", "/v1/chat/completions", tok, chatBody()).Body.Close()
	}

	h.settle(t)
	resp := h.do(t, "GET", "/v1/status", "", "")
	defer resp.Body.Close()
	var st PublicStatus
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	if st.FreeLane == nil {
		t.Fatal("the status document does not mention the free lane")
	}
	if st.FreeLane.Served != 1 || st.FreeLane.FellBack != 1 {
		t.Errorf("free lane served/fell back = %d/%d, want 1/1", st.FreeLane.Served, st.FreeLane.FellBack)
	}
	if st.FreeLane.Model != "deepseek-v4-flash-free" {
		t.Errorf("free lane model = %q", st.FreeLane.Model)
	}
	if st.FreeLane.ServedPct != 50 {
		t.Errorf("free lane share = %v%%, want 50", st.FreeLane.ServedPct)
	}
}

// Without a free key the gateway is exactly what it was: one upstream,
// one key, no second round trip on anything.
func TestNoFreeLaneConfigured(t *testing.T) {
	paid := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(chatReply(10, 5)))
	})
	h := newHarness(t, paid, nil)
	tok := h.enrol(t)

	resp := h.do(t, "POST", "/v1/chat/completions", tok, chatBody())
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("HTTP %d", resp.StatusCode)
	}
	h.settle(t)
	if paid.count() != 1 {
		t.Fatalf("paid upstream saw %d requests, want 1", paid.count())
	}
	if st := h.buildStatus(); st.FreeLane != nil {
		t.Error("a gateway with no free lane advertises one")
	}
	if h.ledger.Today().SpentUSD <= 0 {
		t.Error("nothing was charged for a paid request")
	}
}

// A user cannot consent to a third party nobody told them about. The
// notice is the gateway's to make, because only it knows its upstreams.
func TestPrivacyNoticeNamesTheFreeLane(t *testing.T) {
	paid := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {})
	free := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {})

	plain := newHarness(t, paid, nil)
	if got := plain.privacyNotice(); strings.Contains(got, "third-party") {
		t.Errorf("a gateway with one upstream warns about a second: %q", got)
	}

	withLane := newHarness(t, paid, withFree(free))
	got := withLane.privacyNotice()
	for _, want := range []string{"third-party", "improve its models", "bring your own key"} {
		if !strings.Contains(got, want) {
			t.Errorf("privacy notice does not mention %q: %s", want, got)
		}
	}
}
