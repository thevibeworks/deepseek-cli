package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// End-to-end tests: real command tree, real flag parsing, real request
// building, against a stub API. This is the layer users touch — a flag
// that maps to the wrong field, or output landing on the wrong stream,
// is invisible to unit tests of the pieces.

type capture struct {
	stdout, stderr string
	err            error
	// requests holds every decoded request body the stub received, in
	// order, so a test can assert on what actually went on the wire.
	requests []map[string]any
	paths    []string
	headers  []http.Header
}

// runCLI executes argv against a stub server. It returns everything the
// command wrote and everything the server saw.
func runCLI(t *testing.T, handler http.HandlerFunc, argv ...string) capture {
	t.Helper()
	t.Setenv("DEEPSEEK_STATE_DIR", t.TempDir())
	t.Setenv("DEEPSEEK_API_KEY", "")

	var mu sync.Mutex
	cap := capture{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var decoded map[string]any
		if len(body) > 0 {
			json.Unmarshal(body, &decoded)
		}
		mu.Lock()
		cap.requests = append(cap.requests, decoded)
		cap.paths = append(cap.paths, r.URL.Path)
		cap.headers = append(cap.headers, r.Header.Clone())
		mu.Unlock()
		handler(w, r)
	}))
	defer srv.Close()

	var out, errBuf bytes.Buffer
	opts := &Options{stdout: &out, stderr: &errBuf}

	root := newRootCmd(opts, "test")
	root.SetOut(&errBuf)
	root.SetErr(&errBuf)
	root.SetArgs(append([]string{"--api-key", "sk-test", "--base-url", srv.URL}, argv...))

	cap.err = root.ExecuteContext(context.Background())
	cap.stdout = out.String()
	cap.stderr = errBuf.String()
	return cap
}

const chatOK = `{
  "id": "c1", "object": "chat.completion", "model": "deepseek-v4-flash",
  "choices": [{"index": 0, "message": {"role": "assistant", "content": "blue", "reasoning_content": "because scattering"}, "finish_reason": "stop"}],
  "usage": {"prompt_tokens": 100, "completion_tokens": 5, "total_tokens": 105, "prompt_cache_hit_tokens": 64, "prompt_cache_miss_tokens": 36}
}`

func serve(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }
}

func TestChatWritesAnswerToStdoutAndStatusToStderr(t *testing.T) {
	// The whole output contract in one assertion: redirecting stdout must
	// yield the answer and nothing else.
	got := runCLI(t, serve(chatOK), "chat", "why is the sky blue", "--stream=false")
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.stdout != "blue\n" {
		t.Errorf("stdout = %q, want just the answer", got.stdout)
	}
	if !strings.Contains(got.stderr, "in") || !strings.Contains(got.stderr, "cached") {
		t.Errorf("stderr should carry the usage line, got %q", got.stderr)
	}
	if strings.Contains(got.stdout, "cached") {
		t.Errorf("the usage line leaked into stdout: %q", got.stdout)
	}
}

func TestChatJSONPrintsTheAPIResponseUnwrapped(t *testing.T) {
	// No envelope, no injected fields: jq recipes written against the
	// OpenAI API have to keep working.
	got := runCLI(t, serve(chatOK), "chat", "hi", "--json")
	if got.err != nil {
		t.Fatal(got.err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(got.stdout), &decoded); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, got.stdout)
	}
	for _, key := range []string{"id", "object", "choices", "usage"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("response lost the %q field", key)
		}
	}
	for _, forbidden := range []string{"response", "stats", "cost_usd", "_meta"} {
		if _, ok := decoded[forbidden]; ok {
			t.Errorf("we wrapped the response with %q", forbidden)
		}
	}
}

func TestJSONImpliesNoStreaming(t *testing.T) {
	// --json asks for the API's own object; a streamed call could only
	// offer one we assembled. So streaming defaults off there.
	got := runCLI(t, serve(chatOK), "chat", "hi", "--json")
	if got.err != nil {
		t.Fatal(got.err)
	}
	if len(got.requests) != 1 {
		t.Fatalf("made %d requests", len(got.requests))
	}
	if stream, ok := got.requests[0]["stream"]; ok && stream == true {
		t.Error("--json should not stream unless asked")
	}
}

func TestChatStreamsByDefault(t *testing.T) {
	got := runCLI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`data: {"choices":[{"delta":{"content":"blue"}}]}` + "\n\n"))
		w.Write([]byte(`data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":1,"total_tokens":11,"prompt_cache_miss_tokens":10}}` + "\n\n"))
		w.Write([]byte("data: [DONE]\n\n"))
	}, "chat", "hi")

	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.requests[0]["stream"] != true {
		t.Error("chat should stream by default")
	}
	if got.stdout != "blue\n" {
		t.Errorf("stdout = %q", got.stdout)
	}
}

func TestChatFlagsReachTheWire(t *testing.T) {
	got := runCLI(t, serve(chatOK), "chat", "hi",
		"--stream=false",
		"--model", "deepseek-v4-pro",
		"--system", "be terse",
		"--think", "off",
		"--effort", "max",
		"--max-tokens", "512",
		"--temperature", "0.3",
		"--stop", "END",
		"--response-format", "json_object",
		"--user-id", "u1",
	)
	if got.err != nil {
		t.Fatal(got.err)
	}
	req := got.requests[0]

	if req["model"] != "deepseek-v4-pro" {
		t.Errorf("model = %v", req["model"])
	}
	if req["reasoning_effort"] != "max" {
		t.Errorf("reasoning_effort = %v", req["reasoning_effort"])
	}
	if req["max_tokens"] != float64(512) {
		t.Errorf("max_tokens = %v", req["max_tokens"])
	}
	if req["temperature"] != 0.3 {
		t.Errorf("temperature = %v", req["temperature"])
	}
	if req["user_id"] != "u1" {
		t.Errorf("user_id = %v", req["user_id"])
	}
	if think, _ := req["thinking"].(map[string]any); think["type"] != "disabled" {
		t.Errorf("thinking = %v", req["thinking"])
	}
	if rf, _ := req["response_format"].(map[string]any); rf["type"] != "json_object" {
		t.Errorf("response_format = %v", req["response_format"])
	}
	msgs, _ := req["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("sent %d messages, want system + user", len(msgs))
	}
	if first, _ := msgs[0].(map[string]any); first["role"] != "system" || first["content"] != "be terse" {
		t.Errorf("system message = %v", msgs[0])
	}
}

func TestUnsetFlagsAreNotSent(t *testing.T) {
	// Sending temperature 0 because a Go float defaulted to 0 would
	// silently change the model's behaviour.
	got := runCLI(t, serve(chatOK), "chat", "hi", "--stream=false")
	if got.err != nil {
		t.Fatal(got.err)
	}
	for _, key := range []string{"temperature", "top_p", "max_tokens", "thinking", "reasoning_effort", "response_format"} {
		if _, present := got.requests[0][key]; present {
			t.Errorf("%q was sent without being set", key)
		}
	}
}

func TestChatRejectsBadFlagValuesBeforeSpendingARequest(t *testing.T) {
	for _, argv := range [][]string{
		{"chat", "hi", "--think", "maybe"},
		{"chat", "hi", "--effort", "turbo"},
		{"chat", "hi", "--response-format", "yaml"},
		{"chat", "hi", "--tool-choice", "perhaps"},
	} {
		got := runCLI(t, serve(chatOK), argv...)
		if got.err == nil {
			t.Errorf("%v should have been rejected", argv)
		}
		if len(got.requests) != 0 {
			t.Errorf("%v reached the API before validation", argv)
		}
	}
}

func TestPrefixUsesTheBetaPath(t *testing.T) {
	got := runCLI(t, serve(chatOK), "chat", "write code", "--prefix", "```python", "--stream=false")
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.paths[0] != "/beta/chat/completions" {
		t.Errorf("path = %q, want the beta path", got.paths[0])
	}
	msgs, _ := got.requests[0]["messages"].([]any)
	last, _ := msgs[len(msgs)-1].(map[string]any)
	if last["role"] != "assistant" || last["prefix"] != true {
		t.Errorf("last message = %v, want the assistant prefix", last)
	}
}

func TestSessionRoundTripsAcrossInvocations(t *testing.T) {
	// Two separate command runs sharing one state dir: the second must
	// replay the first turn.
	t.Setenv("DEEPSEEK_STATE_DIR", t.TempDir())

	srv := httptest.NewServer(serve(chatOK))
	defer srv.Close()

	var lastReq map[string]any
	run := func(prompt string) {
		var out, errBuf bytes.Buffer
		opts := &Options{stdout: &out, stderr: &errBuf}
		root := newRootCmd(opts, "test")
		root.SetOut(&errBuf)
		root.SetErr(&errBuf)
		root.SetArgs([]string{"--api-key", "k", "--base-url", srv.URL, "chat", prompt, "--session", "work", "--stream=false"})
		if err := root.ExecuteContext(context.Background()); err != nil {
			t.Fatal(err)
		}
	}

	// Capture the second request by wrapping the server.
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &lastReq)
		w.Write([]byte(chatOK))
	})

	run("first question")
	run("second question")

	msgs, _ := lastReq["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("second turn sent %d messages, want user+assistant+user", len(msgs))
	}
	if m, _ := msgs[0].(map[string]any); m["content"] != "first question" {
		t.Errorf("history lost the first turn: %v", msgs[0])
	}
	if m, _ := msgs[1].(map[string]any); m["role"] != "assistant" {
		t.Errorf("history lost the reply: %v", msgs[1])
	}
}

func TestSessionStripsReasoningWithoutTools(t *testing.T) {
	// The API ignores reasoning_content when no tools are sent, so
	// replaying it would spend input tokens on discarded text.
	t.Setenv("DEEPSEEK_STATE_DIR", t.TempDir())

	var lastReq map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &lastReq)
		w.Write([]byte(chatOK))
	}))
	defer srv.Close()

	run := func(prompt string) {
		var out, errBuf bytes.Buffer
		opts := &Options{stdout: &out, stderr: &errBuf}
		root := newRootCmd(opts, "test")
		root.SetOut(&errBuf)
		root.SetErr(&errBuf)
		root.SetArgs([]string{"--api-key", "k", "--base-url", srv.URL, "chat", prompt, "--session", "s", "--stream=false"})
		if err := root.ExecuteContext(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	run("one")
	run("two")

	msgs, _ := lastReq["messages"].([]any)
	for _, m := range msgs {
		msg, _ := m.(map[string]any)
		if msg["role"] == "assistant" {
			if _, present := msg["reasoning_content"]; present {
				t.Errorf("reasoning_content replayed without tools: %v", msg)
			}
		}
	}
}

func TestAnthropicUsesAPIKeyHeaderAndRequiresMaxTokens(t *testing.T) {
	got := runCLI(t, serve(`{"id":"m1","type":"message","role":"assistant","model":"deepseek-v4-flash","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":2,"cache_read_input_tokens":0}}`),
		"anthropic", "hello", "--stream=false")
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.paths[0] != "/anthropic/v1/messages" {
		t.Errorf("path = %q", got.paths[0])
	}
	if got.headers[0].Get("x-api-key") != "sk-test" {
		t.Errorf("x-api-key = %q", got.headers[0].Get("x-api-key"))
	}
	// The format makes max_tokens required, so one is always supplied.
	if got.requests[0]["max_tokens"] == nil {
		t.Error("max_tokens must always be sent for this format")
	}
	if got.stdout != "hi\n" {
		t.Errorf("stdout = %q", got.stdout)
	}
}

func TestRespondFailedStatusIsAnError(t *testing.T) {
	// A failed response arrives as HTTP 200 with status "failed"; without
	// catching it the command would exit 0 having printed nothing.
	got := runCLI(t, serve(`{"id":"r1","object":"response","status":"failed","model":"deepseek-v4-flash","output":[],"error":{"code":"x","message":"boom"}}`),
		"respond", "hi", "--stream=false")
	if got.err == nil {
		t.Fatal("a failed response should be an error")
	}
	if !strings.Contains(got.err.Error(), "failed") {
		t.Errorf("error = %v", got.err)
	}
}

func TestRespondSchemaImpliesJSONSchemaFormat(t *testing.T) {
	got := runCLI(t, serve(`{"id":"r1","object":"response","status":"completed","model":"deepseek-v4-flash","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"{}"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`),
		"respond", "hi", "--schema", `{"type":"object"}`, "--stream=false")
	if got.err != nil {
		t.Fatal(got.err)
	}
	text, _ := got.requests[0]["text"].(map[string]any)
	format, _ := text["format"].(map[string]any)
	if format["type"] != "json_schema" {
		t.Errorf("text.format = %v, want json_schema", format)
	}
}

func TestRespondWebSearchAddsTheServerSideTool(t *testing.T) {
	got := runCLI(t, serve(`{"id":"r1","object":"response","status":"completed","model":"deepseek-v4-flash","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`),
		"respond", "hi", "--web-search", "--stream=false")
	if got.err != nil {
		t.Fatal(got.err)
	}
	tools, _ := got.requests[0]["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("sent %d tools", len(tools))
	}
	if tool, _ := tools[0].(map[string]any); tool["type"] != "web_search" {
		t.Errorf("tool = %v", tools[0])
	}
}

func TestFIMSendsPromptAndSuffix(t *testing.T) {
	got := runCLI(t, serve(`{"id":"f1","object":"text_completion","model":"deepseek-v4-pro","choices":[{"index":0,"text":"    return a+b","finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_cache_miss_tokens":10}}`),
		"fim", "def add(a,b):", "--suffix", "# end", "--stream=false")
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.paths[0] != "/beta/completions" {
		t.Errorf("path = %q", got.paths[0])
	}
	if got.requests[0]["prompt"] != "def add(a,b):" || got.requests[0]["suffix"] != "# end" {
		t.Errorf("request = %v", got.requests[0])
	}
	if got.stdout != "    return a+b\n" {
		t.Errorf("stdout = %q", got.stdout)
	}
}

func TestModelsJoinsThePublishedRateCard(t *testing.T) {
	got := runCLI(t, serve(`{"object":"list","data":[{"id":"deepseek-v4-flash","object":"model","owned_by":"deepseek"}]}`), "models")
	if got.err != nil {
		t.Fatal(got.err)
	}
	if !strings.Contains(got.stdout, "deepseek-v4-flash") {
		t.Errorf("stdout = %q", got.stdout)
	}
	// Which figure depends on the hour: flash cache-miss input is $0.22
	// off-peak and $0.44 peak. Both are published numbers, so asserting
	// on either keeps this a real check without making it a time bomb
	// that fails whenever the suite runs inside a peak window.
	if !strings.Contains(got.stdout, "0.22") && !strings.Contains(got.stdout, "0.44") {
		t.Errorf("the price should sit next to the model, got %q", got.stdout)
	}
}

func TestBalanceExitsThreeWhenExhausted(t *testing.T) {
	got := runCLI(t, serve(`{"is_available":false,"balance_infos":[{"currency":"CNY","total_balance":"0.00","granted_balance":"0.00","topped_up_balance":"0.00"}]}`), "balance")
	if got.err == nil {
		t.Fatal("an exhausted balance should be an error")
	}
	if code := exitCodeOf(got.err); code != 3 {
		t.Errorf("exit code = %d, want 3", code)
	}
}

func TestBalanceListsEveryCurrency(t *testing.T) {
	// A real account holds more than one at once.
	got := runCLI(t, serve(`{"is_available":true,"balance_infos":[{"currency":"USD","total_balance":"1.00","granted_balance":"0","topped_up_balance":"1.00"},{"currency":"CNY","total_balance":"18.00","granted_balance":"0","topped_up_balance":"18.00"}]}`), "balance")
	if got.err != nil {
		t.Fatal(got.err)
	}
	for _, want := range []string{"USD", "CNY", "18.00"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("stdout missing %q: %s", want, got.stdout)
		}
	}
}

func TestErrorExitCodes(t *testing.T) {
	cases := []struct {
		status int
		want   int
	}{
		{http.StatusUnauthorized, 2},
		{http.StatusPaymentRequired, 3},
		{http.StatusBadRequest, 1},
	}
	for _, tc := range cases {
		got := runCLI(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			w.Write([]byte(`{"error":{"message":"nope"}}`))
		}, "models")
		if got.err == nil {
			t.Fatalf("HTTP %d should be an error", tc.status)
		}
		if code := exitCodeOf(got.err); code != tc.want {
			t.Errorf("HTTP %d gave exit %d, want %d", tc.status, code, tc.want)
		}
	}
}

func TestLedgerRecordsTheCallAndUsageReportsIt(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEEPSEEK_STATE_DIR", dir)

	srv := httptest.NewServer(serve(chatOK))
	defer srv.Close()

	exec := func(argv ...string) string {
		var out, errBuf bytes.Buffer
		opts := &Options{stdout: &out, stderr: &errBuf}
		root := newRootCmd(opts, "test")
		root.SetOut(&errBuf)
		root.SetErr(&errBuf)
		root.SetArgs(append([]string{"--api-key", "k", "--base-url", srv.URL}, argv...))
		if err := root.ExecuteContext(context.Background()); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}

	exec("chat", "hi", "--stream=false")
	report := exec("usage", "--since", "all")

	if !strings.Contains(report, "deepseek-v4-flash") {
		t.Errorf("usage did not report the call: %s", report)
	}
	// 64 of 100 prompt tokens came from cache.
	if !strings.Contains(report, "64%") {
		t.Errorf("usage lost the cache rate: %s", report)
	}
}

func TestNoLedgerSkipsTheWrite(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEEPSEEK_STATE_DIR", dir)

	srv := httptest.NewServer(serve(chatOK))
	defer srv.Close()

	exec := func(argv ...string) string {
		var out, errBuf bytes.Buffer
		opts := &Options{stdout: &out, stderr: &errBuf}
		root := newRootCmd(opts, "test")
		root.SetOut(&errBuf)
		root.SetErr(&errBuf)
		root.SetArgs(append([]string{"--api-key", "k", "--base-url", srv.URL}, argv...))
		if err := root.ExecuteContext(context.Background()); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}

	exec("chat", "hi", "--stream=false", "--no-ledger", "--no-stats")
	if report := exec("usage", "--since", "all"); !strings.Contains(report, "no calls recorded") {
		t.Errorf("--no-ledger still wrote a row: %s", report)
	}
}

func TestNoStatsSuppressesTheUsageLine(t *testing.T) {
	got := runCLI(t, serve(chatOK), "chat", "hi", "--stream=false", "--no-stats")
	if got.err != nil {
		t.Fatal(got.err)
	}
	if strings.Contains(got.stderr, "cached") {
		t.Errorf("--no-stats still printed the usage line: %q", got.stderr)
	}
}

func TestRawSendsAnArbitraryRequest(t *testing.T) {
	got := runCLI(t, serve(`{"ok":true}`), "raw", "/some/new/endpoint", "--data", `{"a":1}`)
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.paths[0] != "/some/new/endpoint" {
		t.Errorf("path = %q", got.paths[0])
	}
	if got.requests[0]["a"] != float64(1) {
		t.Errorf("body = %v", got.requests[0])
	}
	if !strings.Contains(got.stdout, `"ok"`) {
		t.Errorf("stdout = %q", got.stdout)
	}
}

func TestRawDefaultsToGETWithoutABody(t *testing.T) {
	var method string
	got := runCLI(t, func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		w.Write([]byte(`{"ok":true}`))
	}, "raw", "/models")
	if got.err != nil {
		t.Fatal(got.err)
	}
	if method != "GET" {
		t.Errorf("method = %q, want GET", method)
	}
}

// TestCheckCoversEveryEndpoint is the guard behind the "6/6 endpoints"
// claim: if an endpoint is added to the client without being added to
// check, this fails and the badge stops being true.
func TestCheckCoversEveryEndpoint(t *testing.T) {
	got := runCLI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Write([]byte(`{"object":"list","data":[{"id":"deepseek-v4-flash"}]}`))
		case "/user/balance":
			w.Write([]byte(`{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"1.00"}]}`))
		case "/chat/completions":
			w.Write([]byte(chatOK))
		case "/anthropic/v1/messages":
			w.Write([]byte(`{"id":"m","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
		case "/responses":
			w.Write([]byte(`{"id":"r","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
		case "/beta/completions":
			w.Write([]byte(`{"id":"f","choices":[{"text":"x"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
		default:
			t.Errorf("check hit an unexpected path: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}, "check")

	if got.err != nil {
		t.Fatalf("check failed: %v\n%s", got.err, got.stdout)
	}

	want := []string{"/models", "/user/balance", "/chat/completions", "/anthropic/v1/messages", "/responses", "/beta/completions"}
	if len(got.paths) != len(want) {
		t.Fatalf("check probed %d endpoints (%v), want %d", len(got.paths), got.paths, len(want))
	}
	for _, path := range want {
		var found bool
		for _, got := range got.paths {
			if got == path {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("check never probed %s", path)
		}
	}
	if !strings.Contains(got.stdout, "all endpoints reachable") {
		t.Errorf("stdout = %q", got.stdout)
	}
}

func TestCheckReportsEveryProbeEvenAfterAFailure(t *testing.T) {
	// The point of a preflight is the full picture: "all six rejected the
	// key" is a different diagnosis from "only /responses is unhappy".
	got := runCLI(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"Authentication Fails"}}`))
	}, "check")

	if got.err == nil {
		t.Fatal("want an error")
	}
	if code := exitCodeOf(got.err); code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
	if n := strings.Count(got.stdout, "FAIL"); n != 6 {
		t.Errorf("reported %d failures, want all 6 probes\n%s", n, got.stdout)
	}
	// The how-to-fix hint belongs once, at the end — not on every row.
	if n := strings.Count(got.stdout, "platform.deepseek.com"); n > 0 {
		t.Errorf("the hint leaked into the table %d times:\n%s", n, got.stdout)
	}
}

func TestCheckJSONShape(t *testing.T) {
	got := runCLI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Write([]byte(`{"object":"list","data":[{"id":"deepseek-v4-flash"}]}`))
		case "/user/balance":
			w.Write([]byte(`{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"1.00"}]}`))
		case "/chat/completions":
			w.Write([]byte(chatOK))
		case "/anthropic/v1/messages":
			w.Write([]byte(`{"id":"m","content":[],"usage":{"input_tokens":1,"output_tokens":1}}`))
		case "/responses":
			w.Write([]byte(`{"id":"r","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
		default:
			w.Write([]byte(`{"id":"f","choices":[{"text":"x"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
		}
	}, "check", "--json")

	if got.err != nil {
		t.Fatal(got.err)
	}
	var res struct {
		BaseURL string `json:"base_url"`
		KeySet  bool   `json:"key_set"`
		OK      bool   `json:"ok"`
		Probes  []struct {
			Name string `json:"name"`
			Path string `json:"path"`
			OK   bool   `json:"ok"`
		} `json:"probes"`
	}
	if err := json.Unmarshal([]byte(got.stdout), &res); err != nil {
		t.Fatalf("check --json is not valid JSON: %v\n%s", err, got.stdout)
	}
	if !res.OK || !res.KeySet || len(res.Probes) != 6 {
		t.Errorf("got %+v", res)
	}
}

func TestMissingKeyExitsTwoWithoutCallingTheAPI(t *testing.T) {
	t.Setenv("DEEPSEEK_STATE_DIR", t.TempDir())
	t.Setenv("DEEPSEEK_CONFIG_DIR", t.TempDir())
	t.Setenv("DEEPSEEK_API_KEY", "")

	var out, errBuf bytes.Buffer
	opts := &Options{stdout: &out, stderr: &errBuf}
	root := newRootCmd(opts, "test")
	root.SetOut(&errBuf)
	root.SetErr(&errBuf)
	root.SetArgs([]string{"models"})

	err := root.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("want an error")
	}
	if code := exitCodeOf(err); code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
	if !strings.Contains(err.Error(), "DEEPSEEK_API_KEY") {
		t.Errorf("the error should say how to fix it, got %q", err)
	}
}

func TestSessionCommands(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEEPSEEK_STATE_DIR", dir)

	srv := httptest.NewServer(serve(chatOK))
	defer srv.Close()

	exec := func(argv ...string) (string, error) {
		var out, errBuf bytes.Buffer
		opts := &Options{stdout: &out, stderr: &errBuf}
		root := newRootCmd(opts, "test")
		root.SetOut(&errBuf)
		root.SetErr(&errBuf)
		root.SetArgs(append([]string{"--api-key", "k", "--base-url", srv.URL}, argv...))
		err := root.ExecuteContext(context.Background())
		return out.String(), err
	}

	if _, err := exec("chat", "hi", "--session", "demo", "--stream=false"); err != nil {
		t.Fatal(err)
	}

	list, err := exec("session", "ls")
	if err != nil || !strings.Contains(list, "demo") {
		t.Errorf("ls = %q, err = %v", list, err)
	}

	show, err := exec("session", "show", "demo")
	if err != nil || !strings.Contains(show, "hi") {
		t.Errorf("show = %q, err = %v", show, err)
	}

	if _, err := exec("session", "rm", "demo"); err != nil {
		t.Fatal(err)
	}
	if list, _ := exec("session", "ls"); strings.Contains(list, "demo") {
		t.Errorf("rm left the session behind: %q", list)
	}
}

func TestTruncationIsWarnedAbout(t *testing.T) {
	// A truncated answer that prints silently is a bug report waiting to
	// happen.
	body := strings.Replace(chatOK, `"finish_reason": "stop"`, `"finish_reason": "length"`, 1)
	got := runCLI(t, serve(body), "chat", "hi", "--stream=false")
	if got.err != nil {
		t.Fatal(got.err)
	}
	if !strings.Contains(got.stderr, "truncated") {
		t.Errorf("stderr should warn about truncation, got %q", got.stderr)
	}
}

func TestToolCallsArePrintedNotExecuted(t *testing.T) {
	body := `{"id":"c1","object":"chat.completion","model":"deepseek-v4-flash",
	  "choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Hangzhou\"}"}}]},"finish_reason":"tool_calls"}],
	  "usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_cache_miss_tokens":10}}`

	got := runCLI(t, serve(body), "chat", "weather?", "--tool", `{"name":"get_weather"}`, "--stream=false")
	if got.err != nil {
		t.Fatal(got.err)
	}
	if !strings.Contains(got.stderr, "get_weather") {
		t.Errorf("the tool call should be reported on stderr, got %q", got.stderr)
	}
	if strings.Contains(got.stdout, "get_weather") {
		t.Errorf("the tool call leaked into stdout: %q", got.stdout)
	}
}
