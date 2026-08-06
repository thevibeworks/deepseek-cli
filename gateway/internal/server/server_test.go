package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thevibeworks/deepseek-cli/gateway/internal/mint"
	"github.com/thevibeworks/deepseek-cli/gateway/internal/quota"
	"github.com/thevibeworks/deepseek-cli/gateway/internal/token"
)

// upstream stands in for api.deepseek.com and records what reached it.
type upstream struct {
	mu       sync.Mutex
	requests []recorded
	handler  http.HandlerFunc
	server   *httptest.Server
}

type recorded struct {
	Path    string
	Headers http.Header
	Body    map[string]any
}

func newUpstream(t *testing.T, h http.HandlerFunc) *upstream {
	t.Helper()
	u := &upstream{handler: h}
	u.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(raw, &body)
		u.mu.Lock()
		u.requests = append(u.requests, recorded{Path: r.URL.Path, Headers: r.Header.Clone(), Body: body})
		u.mu.Unlock()
		u.handler(w, r)
	}))
	t.Cleanup(u.server.Close)
	return u
}

func (u *upstream) last(t *testing.T) recorded {
	t.Helper()
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.requests) == 0 {
		t.Fatal("nothing reached the upstream")
	}
	return u.requests[len(u.requests)-1]
}

func (u *upstream) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.requests)
}

const upstreamKey = "sk-the-real-key-nobody-should-see"

// chatReply is a plausible non-streamed chat completion.
func chatReply(in, out int) string {
	return fmt.Sprintf(`{"id":"x","object":"chat.completion","model":"deepseek-v4-flash",
	 "choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],
	 "usage":{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d,
	 "prompt_cache_hit_tokens":0,"prompt_cache_miss_tokens":%d}}`, in, out, in+out, in)
}

type harness struct {
	*Server
	client   *http.Client
	base     string
	ledger   *quota.Ledger
	upstream *upstream
}

func newHarness(t *testing.T, up *upstream, tune func(*Config, *quota.Limits)) *harness {
	t.Helper()

	limits := quota.Limits{
		DailyRequests:     5,
		DailyInputTokens:  10000,
		DailyOutputTokens: 5000,
		DailyBudgetUSD:    1,
		TotalBudgetUSD:    10,
	}
	cfg := Config{
		UpstreamBaseURL:          up.server.URL,
		UpstreamKeys:             []string{upstreamKey},
		Model:                    "deepseek-v4-flash",
		MaxBodyBytes:             4096,
		MaxTokens:                256,
		MaxInflight:              4,
		RequestsPerMinute:        1000,
		SubjectRequestsPerMinute: 1000,
		SubjectInflight:          4,
		TokenTTL:                 7 * 24 * time.Hour,
		Origins:                  []string{"https://deepseek-cli.example"},
	}
	if tune != nil {
		tune(&cfg, &limits)
	}

	signer, err := token.NewSigner([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := quota.Open(t.TempDir(), limits)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ledger.Close() })

	// A trivial puzzle with a large free allowance: these tests are about
	// the gateway, not about sha256.
	m := mint.New(signer, mint.Config{BaseBits: 4, FreeMints: 1 << 20, TTL: time.Minute})
	srv := New(cfg, signer, m, ledger)

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	return &harness{Server: srv, client: ts.Client(), base: ts.URL, ledger: ledger, upstream: up}
}

// enrol runs the real client journey: challenge, proof of work, token.
func (h *harness) enrol(t *testing.T) string {
	t.Helper()

	resp, err := h.client.Post(h.base+"/v1/anon/challenge", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("challenge: HTTP %d", resp.StatusCode)
	}
	var ch ChallengeResponse
	if err := json.NewDecoder(resp.Body).Decode(&ch); err != nil {
		t.Fatal(err)
	}

	nonce, err := token.Solve(ch.Challenge, ch.Difficulty, 1<<24)
	if err != nil {
		t.Fatalf("solving the gateway's own challenge failed: %v", err)
	}

	body, _ := json.Marshal(TokenRequest{Challenge: ch.Challenge, Nonce: strconv.FormatUint(nonce, 10)})
	resp2, err := h.client.Post(h.base+"/v1/anon/token", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != 200 {
		raw, _ := io.ReadAll(resp2.Body)
		t.Fatalf("token: HTTP %d: %s", resp2.StatusCode, raw)
	}
	var tr TokenResponse
	if err := json.NewDecoder(resp2.Body).Decode(&tr); err != nil {
		t.Fatal(err)
	}
	if tr.Token == "" {
		t.Fatal("no token issued")
	}
	return tr.Token
}

func (h *harness) do(t *testing.T, method, path, tok, body string) *http.Response {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, h.base+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// settle waits for every in-flight request to be charged.
//
// Metering is genuinely after the fact: the answer is written, then the
// usage is read out of it, then the ledger is debited. A client can
// therefore hold a complete response before the charge for it exists.
// The in-flight slot is released only after the debit, so an empty
// semaphore is the one honest happens-after this design offers.
func (h *harness) settle(t *testing.T) {
	t.Helper()
	for i := 0; i < 4000; i++ {
		if len(h.inflight) == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("requests were still in flight after 4s")
}

func decodeError(t *testing.T, resp *http.Response) errorBody {
	t.Helper()
	var env errorEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("error response is not DeepSeek's envelope shape: %v", err)
	}
	return env.Error
}

// The whole point of the project, in one test: no API key, a working
// answer, and an accurate charge.
func TestEnrolAndChat(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, chatReply(85, 40))
	})
	h := newHarness(t, up, nil)

	tok := h.enrol(t)
	resp := h.do(t, "POST", "/chat/completions", tok,
		`{"messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("HTTP %d: %s", resp.StatusCode, raw)
	}
	raw, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(raw), `"content":"hi"`) {
		t.Errorf("the answer did not come back intact: %s", raw)
	}
	h.settle(t)

	// Charged what the response reported, not an estimate.
	if got := h.ledger.Health(); got.DaySpendUSD == 0 {
		t.Error("a completed request was not charged")
	}
	sub := subjectOf(t, tok)
	st := h.ledger.Status(sub, "anon")
	if st.Used.Requests != 1 || st.Used.InputTokens != 85 || st.Used.OutputTokens != 40 {
		t.Errorf("charged %+v, want 1 request / 85 in / 40 out", st.Used)
	}
}

// Our key must reach DeepSeek and the subject must arrive as user_id —
// the field their docs specify for one account fronting many users, and
// what buys KV cache isolation between strangers.
func TestUpstreamGetsOurKeyAndTheSubjectAsUserID(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, chatReply(10, 10))
	})
	h := newHarness(t, up, nil)
	tok := h.enrol(t)

	resp := h.do(t, "POST", "/chat/completions", tok, `{"messages":[],"user_id":"i-picked-this"}`)
	resp.Body.Close()

	got := up.last(t)
	if auth := got.Headers.Get("Authorization"); auth != "Bearer "+upstreamKey {
		t.Errorf("upstream Authorization = %q, want our key", auth)
	}
	sub := subjectOf(t, tok)
	if got.Body["user_id"] != sub {
		t.Errorf("upstream user_id = %v, want the subject %q", got.Body["user_id"], sub)
	}
	if got.Body["user_id"] == "i-picked-this" {
		t.Error("the client chose its own user_id; it could aim at another subject's cache")
	}
}

// The free-tier token must never be forwarded upstream, and neither must
// anything else the client sent in a header.
func TestClientCredentialsDoNotReachUpstream(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, chatReply(10, 10))
	})
	h := newHarness(t, up, nil)
	tok := h.enrol(t)

	req, _ := http.NewRequest("POST", h.base+"/chat/completions", strings.NewReader(`{"messages":[]}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("X-Sneaky", "should-not-travel")
	req.Header.Set("Cookie", "session=secret")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	got := up.last(t)
	if strings.Contains(got.Headers.Get("Authorization"), tok) {
		t.Error("the free-tier token was forwarded to DeepSeek")
	}
	for _, h := range []string{"X-Sneaky", "Cookie"} {
		if got.Headers.Get(h) != "" {
			t.Errorf("%s reached upstream; only headers the gateway chose should travel", h)
		}
	}
}

// Someone with a real key pointing at the gateway is a likely mistake.
// Forwarding their key would be far worse than refusing it.
func TestARealKeyIsRefusedAndNotForwarded(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, chatReply(10, 10))
	})
	h := newHarness(t, up, nil)

	resp := h.do(t, "POST", "/chat/completions", "sk-somebody-elses-real-key", `{"messages":[]}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("HTTP %d, want 401", resp.StatusCode)
	}
	e := decodeError(t, resp)
	if !strings.Contains(e.Message, "api.deepseek.com") {
		t.Errorf("the refusal does not say where a real key should go: %q", e.Message)
	}
	if up.count() != 0 {
		t.Fatal("a stranger's API key was forwarded to DeepSeek")
	}
}

// A stream must arrive byte-identical, and still be metered.
func TestStreamingPassesThroughAndIsMetered(t *testing.T) {
	const final = `{"id":"x","object":"chat.completion.chunk","model":"deepseek-v4-flash","choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":120,"completion_tokens":33,"total_tokens":153,"prompt_cache_hit_tokens":64,"prompt_cache_miss_tokens":56}}`
	body := ": keep-alive\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"he\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"llo\"}}]}\n\n" +
		"data: " + final + "\n\n" +
		"data: [DONE]\n\n"

	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		for i := 0; i < len(body); i += 7 {
			end := min(i+7, len(body))
			io.WriteString(w, body[i:end])
			w.(http.Flusher).Flush()
		}
	})
	h := newHarness(t, up, nil)
	tok := h.enrol(t)

	resp := h.do(t, "POST", "/chat/completions", tok, `{"messages":[],"stream":true}`)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	if string(raw) != body {
		t.Errorf("the stream was altered in transit:\ngot  %q\nwant %q", raw, body)
	}
	h.settle(t)
	st := h.ledger.Status(subjectOf(t, tok), "anon")
	if st.Used.InputTokens != 120 || st.Used.OutputTokens != 33 {
		t.Errorf("streamed usage charged as %+v, want 120 in / 33 out", st.Used)
	}
}

// A 2xx whose usage cannot be read must cost more than it plausibly did,
// never zero — otherwise "return something unparseable" is the cheapest
// way to use the service.
func TestUnmeterableSuccessIsChargedAnEstimate(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"content":"no usage object here"}}]}`)
	})
	h := newHarness(t, up, nil)
	tok := h.enrol(t)

	resp := h.do(t, "POST", "/chat/completions", tok, `{"messages":[]}`)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	h.settle(t)

	st := h.ledger.Status(subjectOf(t, tok), "anon")
	if st.Used.SpentUSD <= 0 {
		t.Fatal("an unmeterable success was free")
	}
	if st.Used.OutputTokens != 256 {
		t.Errorf("estimated output = %d, want the full max_tokens ceiling of 256", st.Used.OutputTokens)
	}
}

// An upstream failure the caller could not have provoked costs them
// nothing. A 4xx is their own request coming back and is not refunded,
// or the request counter becomes a free retry loop.
func TestRefundsOnlyForFaultsTheCallerDidNotCause(t *testing.T) {
	var status int
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		io.WriteString(w, `{"error":{"message":"nope"}}`)
	})
	h := newHarness(t, up, nil)
	tok := h.enrol(t)
	sub := subjectOf(t, tok)

	status = http.StatusServiceUnavailable
	h.do(t, "POST", "/chat/completions", tok, `{"messages":[]}`).Body.Close()
	h.settle(t)
	if used := h.ledger.Status(sub, "anon").Used.Requests; used != 0 {
		t.Errorf("after a 503, requests used = %d, want 0 (refunded)", used)
	}

	status = http.StatusBadRequest
	h.do(t, "POST", "/chat/completions", tok, `{"messages":[]}`).Body.Close()
	h.settle(t)
	if used := h.ledger.Status(sub, "anon").Used.Requests; used != 1 {
		t.Errorf("after a 400, requests used = %d, want 1 (not refunded)", used)
	}
}

func TestQuotaExhaustionIsAnActionable429(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, chatReply(5, 5))
	})
	h := newHarness(t, up, func(c *Config, l *quota.Limits) { l.DailyRequests = 2 })
	tok := h.enrol(t)

	for i := 0; i < 2; i++ {
		h.do(t, "POST", "/chat/completions", tok, `{"messages":[]}`).Body.Close()
	}
	resp := h.do(t, "POST", "/chat/completions", tok, `{"messages":[]}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("HTTP %d, want 429", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Error("no Retry-After; a refusal with no horizon is not actionable")
	}
	e := decodeError(t, resp)
	if e.Type != typeQuota {
		t.Errorf("error type = %q, want %q", e.Type, typeQuota)
	}
	if !strings.Contains(e.Message, "00:00 UTC") || !strings.Contains(e.Message, "api_keys") {
		t.Errorf("the message says neither when it resets nor how to get around it: %q", e.Message)
	}
}

func TestExhaustedCreditIs402WithAWayForward(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, chatReply(5, 5))
	})
	h := newHarness(t, up, func(c *Config, l *quota.Limits) { l.TotalBudgetUSD = 0 })
	tok := h.enrol(t)

	resp := h.do(t, "POST", "/chat/completions", tok, `{"messages":[]}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("HTTP %d, want 402", resp.StatusCode)
	}
	e := decodeError(t, resp)
	if e.Type != typeExhausted {
		t.Errorf("error type = %q, want %q", e.Type, typeExhausted)
	}
	if !strings.Contains(e.Message, "api_keys") {
		t.Errorf("no way forward offered: %q", e.Message)
	}
	if up.count() != 0 {
		t.Error("a request was forwarded after the credit pool was empty")
	}
}

func TestOversizedBodyIsRefusedBeforeUpstream(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, chatReply(5, 5))
	})
	h := newHarness(t, up, nil)
	tok := h.enrol(t)

	big := `{"messages":[{"role":"user","content":"` + strings.Repeat("x", 8192) + `"}]}`
	resp := h.do(t, "POST", "/chat/completions", tok, big)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("HTTP %d, want 413", resp.StatusCode)
	}
	if up.count() != 0 {
		t.Error("an oversized body was forwarded")
	}
}

func TestProIsRefused(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, chatReply(5, 5))
	})
	h := newHarness(t, up, nil)
	tok := h.enrol(t)

	resp := h.do(t, "POST", "/chat/completions", tok, `{"model":"deepseek-v4-pro","messages":[]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("HTTP %d, want 400", resp.StatusCode)
	}
	if up.count() != 0 {
		t.Error("a pro request was forwarded and billed at 3x")
	}
}

// `deepseek status` and `deepseek check` poll /models. Charging for a
// call that cannot generate a token would make the free tier's own
// health check eat the day's allowance.
func TestModelsIsNotCharged(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"object":"list","data":[{"id":"deepseek-v4-flash"}]}`)
	})
	h := newHarness(t, up, nil)
	tok := h.enrol(t)

	for i := 0; i < 10; i++ {
		resp := h.do(t, "GET", "/models", tok, "")
		if resp.StatusCode != 200 {
			t.Fatalf("HTTP %d", resp.StatusCode)
		}
		resp.Body.Close()
	}
	h.settle(t)
	if used := h.ledger.Status(subjectOf(t, tok), "anon").Used.Requests; used != 0 {
		t.Errorf("ten /models calls consumed %d requests of quota", used)
	}
}

// /models answers "what can I use here". Through the free tier that is
// one model, and any client picking off an unfiltered list would have
// even odds of choosing the one that is then refused.
func TestModelsListsOnlyWhatIsServed(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"object":"list","data":[
		 {"id":"deepseek-v4-flash","object":"model","owned_by":"deepseek"},
		 {"id":"deepseek-v4-pro","object":"model","owned_by":"deepseek"}]}`)
	})
	h := newHarness(t, up, nil)
	tok := h.enrol(t)

	resp := h.do(t, "GET", "/models", tok, "")
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	if strings.Contains(string(raw), "deepseek-v4-pro") {
		t.Errorf("the free tier advertised a model it refuses to serve: %s", raw)
	}
	if !strings.Contains(string(raw), "deepseek-v4-flash") {
		t.Errorf("the served model was filtered out too: %s", raw)
	}
	// Still a real upstream call, so `deepseek status` keeps answering
	// whether DeepSeek itself is reachable.
	if up.count() != 1 {
		t.Errorf("upstream saw %d model requests, want 1", up.count())
	}
}

// A model list we cannot parse is forwarded rather than swallowed.
func TestUnparseableModelListIsPassedThrough(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"something":"unexpected"}`)
	})
	h := newHarness(t, up, nil)
	tok := h.enrol(t)

	resp := h.do(t, "GET", "/models", tok, "")
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(raw), "unexpected") {
		t.Errorf("an unrecognised model list was not passed through: %s", raw)
	}
}

// Proxying this would report our account's balance to a stranger.
func TestBalanceIsAnsweredLocally(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"is_available":true,"balance_infos":[{"currency":"USD","total_balance":"9999.00"}]}`)
	})
	h := newHarness(t, up, nil)
	tok := h.enrol(t)

	resp := h.do(t, "GET", "/user/balance", tok, "")
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	if up.count() != 0 {
		t.Fatal("the balance request was proxied to DeepSeek")
	}
	if strings.Contains(string(raw), "9999") {
		t.Fatal("our account balance was disclosed")
	}
	var b balanceResponse
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatalf("not the documented balance shape: %v", err)
	}
	if len(b.BalanceInfos) == 0 || b.FreeTier == nil {
		t.Errorf("balance response is missing its free-tier detail: %s", raw)
	}
}

func TestQuotaHeadersAreSet(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, chatReply(5, 5))
	})
	h := newHarness(t, up, func(c *Config, l *quota.Limits) { l.DailyRequests = 5 })
	tok := h.enrol(t)

	resp := h.do(t, "POST", "/chat/completions", tok, `{"messages":[]}`)
	defer resp.Body.Close()

	if got := resp.Header.Get(headerRequestsLeft); got != "4" {
		t.Errorf("%s = %q, want 4", headerRequestsLeft, got)
	}
	if resp.Header.Get(headerResets) == "" {
		t.Errorf("%s is missing", headerResets)
	}
}

func TestUnknownEndpointExplainsWhatIsCarried(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {})
	h := newHarness(t, up, nil)
	tok := h.enrol(t)

	resp := h.do(t, "POST", "/some/other/thing", tok, `{}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("HTTP %d, want 404", resp.StatusCode)
	}
	if e := decodeError(t, resp); !strings.Contains(e.Message, "/chat/completions") {
		t.Errorf("the 404 does not list what is available: %q", e.Message)
	}
}

func TestAnthropicRouteUsesXAPIKeyAndMetadata(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"usage":{"input_tokens":12,"output_tokens":3}}`)
	})
	h := newHarness(t, up, nil)
	tok := h.enrol(t)

	// The Anthropic ecosystem authenticates with x-api-key, so the CLI
	// sends the free token that way too.
	req, _ := http.NewRequest("POST", h.base+"/anthropic/v1/messages", strings.NewReader(`{"messages":[]}`))
	req.Header.Set("x-api-key", tok)
	req.Header.Set("anthropic-version", "2023-06-01")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	h.settle(t)

	got := up.last(t)
	if got.Headers.Get("x-api-key") != upstreamKey {
		t.Errorf("upstream x-api-key = %q, want our key", got.Headers.Get("x-api-key"))
	}
	if got.Headers.Get("anthropic-version") == "" {
		t.Error("anthropic-version was dropped")
	}
	meta, _ := got.Body["metadata"].(map[string]any)
	if meta == nil || meta["user_id"] != subjectOf(t, tok) {
		t.Errorf("metadata.user_id = %v, want the subject", got.Body["metadata"])
	}
	if used := h.ledger.Status(subjectOf(t, tok), "anon").Used.InputTokens; used != 12 {
		t.Errorf("anthropic usage charged as %d input tokens, want 12", used)
	}
}

func TestCORSOnlyForConfiguredOrigins(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {})
	h := newHarness(t, up, nil)

	for origin, want := range map[string]bool{
		"https://deepseek-cli.example": true,
		"https://evil.example":         false,
	} {
		req, _ := http.NewRequest("OPTIONS", h.base+"/v1/anon/challenge", nil)
		req.Header.Set("Origin", origin)
		resp, err := h.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		got := resp.Header.Get("Access-Control-Allow-Origin") != ""
		if got != want {
			t.Errorf("origin %s allowed = %v, want %v", origin, got, want)
		}
	}
}

func TestNoCredentialIsAnActionable401(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {})
	h := newHarness(t, up, nil)

	resp := h.do(t, "POST", "/chat/completions", "", `{"messages":[]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("HTTP %d, want 401", resp.StatusCode)
	}
	if e := decodeError(t, resp); !strings.Contains(e.Message, "deepseek free") {
		t.Errorf("the 401 does not say how to get a credential: %q", e.Message)
	}
}

func TestInfoIsUnauthenticatedAndDescribesTheDeal(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {})
	h := newHarness(t, up, nil)

	resp, err := h.client.Get(h.base + "/v1/anon/info")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var info Info
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	if info.Model == "" || info.Limits.Requests == 0 || info.Privacy == "" {
		t.Errorf("info does not describe the deal: %+v", info)
	}
}

func TestAdminHealthIsGated(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {})
	h := newHarness(t, up, func(c *Config, l *quota.Limits) { c.AdminToken = "s3cret" })

	resp := h.do(t, "GET", "/admin/health", "", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unauthenticated admin health returned HTTP %d, want 404", resp.StatusCode)
	}

	req, _ := http.NewRequest("GET", h.base+"/admin/health", nil)
	req.Header.Set("X-Admin-Token", "s3cret")
	resp2, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Errorf("authenticated admin health returned HTTP %d", resp2.StatusCode)
	}
}

func subjectOf(t *testing.T, raw string) string {
	t.Helper()
	s, err := token.NewSigner([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := s.ParseToken(raw)
	if err != nil {
		t.Fatal(err)
	}
	return tok.Subject.String()
}

// One token must not be able to park the whole service behind the global
// in-flight cap.
func TestSubjectConcurrencyIsCapped(t *testing.T) {
	release := make(chan struct{})
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		<-release
		io.WriteString(w, chatReply(10, 10))
	})
	defer close(release)
	h := newHarness(t, up, func(cfg *Config, lim *quota.Limits) {
		cfg.SubjectInflight = 1
		lim.DailyRequests = 100
	})
	tok := h.enrol(t)

	started := make(chan *http.Response, 1)
	go func() {
		resp := h.do(t, "POST", "/chat/completions", tok, `{"messages":[]}`)
		started <- resp
	}()

	// Wait until the first request holds the subject's only slot.
	deadline := time.Now().Add(4 * time.Second)
	for h.upstream.count() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the first request never reached upstream")
		}
		time.Sleep(time.Millisecond)
	}

	resp := h.do(t, "POST", "/chat/completions", tok, `{"messages":[]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second concurrent request: HTTP %d, want 429", resp.StatusCode)
	}

	release <- struct{}{}
	(<-started).Body.Close()
	h.settle(t)

	// With the slot free again the same token proceeds.
	go func() { release <- struct{}{} }()
	resp2 := h.do(t, "POST", "/chat/completions", tok, `{"messages":[]}`)
	defer resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Errorf("request after the slot freed: HTTP %d, want 200", resp2.StatusCode)
	}
}

// A token past its TTL is a 401 pointing at re-enrolment, not a working
// credential. Stockpiled identities must age out.
func TestExpiredTokenIsRefused(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, chatReply(10, 10))
	})
	h := newHarness(t, up, func(cfg *Config, lim *quota.Limits) {
		cfg.TokenTTL = time.Millisecond
	})
	tok := h.enrol(t)
	time.Sleep(5 * time.Millisecond)

	resp := h.do(t, "POST", "/chat/completions", tok, `{"messages":[]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired token: HTTP %d, want 401", resp.StatusCode)
	}
	if e := decodeError(t, resp); !strings.Contains(e.Message, "deepseek free") {
		t.Errorf("the error does not say how to recover: %q", e.Message)
	}
	if up.count() != 0 {
		t.Error("an expired token's request reached upstream")
	}
}

// The second /models inside the cache window is answered locally: the
// endpoint is deliberately uncharged, so it must not cost upstream trips.
func TestModelsIsCached(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"object":"list","data":[{"id":"deepseek-v4-flash","object":"model"}]}`)
	})
	h := newHarness(t, up, nil)
	tok := h.enrol(t)

	for i := 0; i < 3; i++ {
		resp := h.do(t, "GET", "/models", tok, "")
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.Contains(string(raw), "deepseek-v4-flash") {
			t.Fatalf("models call %d: HTTP %d: %s", i, resp.StatusCode, raw)
		}
	}
	if got := up.count(); got != 1 {
		t.Errorf("3 /models calls made %d upstream trips, want 1", got)
	}
}

// When DeepSeek itself reports the account unusable, the gateway must
// say 402 — not relay a confusing upstream error while its own ledger
// still claims there is credit.
func TestUpstreamDryBalanceStopsAdmissions(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/user/balance" {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"is_available":false,"balance_infos":[]}`)
			return
		}
		io.WriteString(w, chatReply(10, 10))
	})
	h := newHarness(t, up, nil)
	tok := h.enrol(t)

	h.checkBalance(context.Background())

	resp := h.do(t, "POST", "/chat/completions", tok, `{"messages":[]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("HTTP %d with a dry upstream account, want 402", resp.StatusCode)
	}

	// And it heals when the account is topped up.
	up.mu.Lock()
	up.handler = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/user/balance" {
			io.WriteString(w, `{"is_available":true,"balance_infos":[]}`)
			return
		}
		io.WriteString(w, chatReply(10, 10))
	}
	up.mu.Unlock()
	h.checkBalance(context.Background())
	resp2 := h.do(t, "POST", "/chat/completions", tok, `{"messages":[]}`)
	defer resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Errorf("HTTP %d after the account recovered, want 200", resp2.StatusCode)
	}
}
