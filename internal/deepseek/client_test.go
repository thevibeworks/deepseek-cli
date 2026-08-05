package deepseek

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(t *testing.T, h http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New("sk-test", srv.URL, 5*time.Second)
	c.Retries = 2
	return c
}

func TestChatSendsBearerAuth(t *testing.T) {
	var gotAuth, gotPath string
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		w.Write([]byte(`{"id":"x","choices":[{"message":{"content":"hi"}}]}`))
	}))

	if _, _, err := c.Chat(context.Background(), &ChatRequest{Model: ModelFlash}, false); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotPath != "/chat/completions" {
		t.Errorf("path = %q", gotPath)
	}
}

func TestAnthropicSendsAPIKeyHeader(t *testing.T) {
	// This format authenticates with x-api-key, which is what makes the
	// endpoint a faithful stand-in for the Anthropic ecosystem.
	var gotKey, gotAuth, gotPath string
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey, gotAuth, gotPath = r.Header.Get("x-api-key"), r.Header.Get("Authorization"), r.URL.Path
		w.Write([]byte(`{"id":"x","content":[{"type":"text","text":"hi"}]}`))
	}))

	content, _ := json.Marshal("hi")
	_, _, err := c.Anthropic(context.Background(), &AnthropicRequest{
		Model: ModelFlash, MaxTokens: 8,
		Messages: []AnthropicMessage{{Role: "user", Content: content}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotKey != "sk-test" {
		t.Errorf("x-api-key = %q", gotKey)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("the bearer token should still be sent as well, got %q", gotAuth)
	}
	if gotPath != "/anthropic/v1/messages" {
		t.Errorf("path = %q", gotPath)
	}
}

func TestBetaPathForPrefixCompletion(t *testing.T) {
	var gotPath string
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte(`{"id":"x"}`))
	}))
	if _, _, err := c.Chat(context.Background(), &ChatRequest{Model: ModelFlash}, true); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/beta/chat/completions" {
		t.Errorf("path = %q, want the beta path", gotPath)
	}
}

func TestAPIErrorCarriesMessageAndHint(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte(`{"error":{"message":"Insufficient Balance","type":"insufficient_balance","code":"invalid_request_error"}}`))
	}))

	_, _, err := c.Models(context.Background())
	if err == nil {
		t.Fatal("want an error")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("want *APIError, got %T", err)
	}
	if apiErr.Message != "Insufficient Balance" {
		t.Errorf("message = %q", apiErr.Message)
	}
	if !strings.Contains(err.Error(), "top up") {
		t.Errorf("a 402 should tell the user how to fix it, got %q", err.Error())
	}
	if got := ExitCode(err); got != ExitBalance {
		t.Errorf("exit code = %d, want %d", got, ExitBalance)
	}
}

func TestRetriesServerErrorsThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"error":{"message":"overloaded"}}`))
			return
		}
		w.Write([]byte(`{"object":"list","data":[{"id":"deepseek-v4-flash"}]}`))
	}))

	list, _, err := c.Models(context.Background())
	if err != nil {
		t.Fatalf("should have recovered: %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("made %d attempts, want 3", got)
	}
	if len(list.Data) != 1 {
		t.Errorf("decoded %d models", len(list.Data))
	}
}

func TestDoesNotRetryClientErrors(t *testing.T) {
	// A 400 retried is a 400. Retrying would triple the latency of every
	// malformed request for nothing.
	var calls atomic.Int32
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"bad model"}}`))
	}))

	if _, _, err := c.Models(context.Background()); err == nil {
		t.Fatal("want an error")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("made %d attempts, want exactly 1", got)
	}
}

func TestStreamIsNotRetriedOnceEstablished(t *testing.T) {
	// Output that already reached the user has already been billed;
	// re-running the request would charge twice for one answer.
	var calls atomic.Int32
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"))
	}))

	var chunks int
	resp, err := c.ChatStream(context.Background(), &ChatRequest{Model: ModelFlash}, false, func(*ChatChunk) error {
		chunks++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || chunks != 1 {
		t.Errorf("calls=%d chunks=%d, want 1 and 1", calls.Load(), chunks)
	}
	if got := resp.Choices[0].Message.Content; got != "hi" {
		t.Errorf("assembled content = %q", got)
	}
}

func TestStreamAlwaysAsksForUsage(t *testing.T) {
	// Without stream_options.include_usage a streamed call reports no
	// tokens at all, and nothing downstream could price it.
	var body map[string]any
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte("data: [DONE]\n\n"))
	}))

	if _, err := c.ChatStream(context.Background(), &ChatRequest{Model: ModelFlash}, false, func(*ChatChunk) error { return nil }); err != nil {
		t.Fatal(err)
	}
	opts, ok := body["stream_options"].(map[string]any)
	if !ok || opts["include_usage"] != true {
		t.Errorf("stream_options = %v, want include_usage true", body["stream_options"])
	}
}

func TestStreamAssemblesToolCallFragments(t *testing.T) {
	// The model streams one call's arguments across many chunks; only the
	// first fragment carries the id and the name.
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, frag := range []string{
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"ci"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ty\": \"Hangzhou\"}"}}]}}]}`,
		} {
			w.Write([]byte("data: " + frag + "\n\n"))
		}
		w.Write([]byte("data: [DONE]\n\n"))
	}))

	resp, err := c.ChatStream(context.Background(), &ChatRequest{Model: ModelFlash}, false, func(*ChatChunk) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	calls := resp.Choices[0].Message.ToolCalls
	if len(calls) != 1 {
		t.Fatalf("assembled %d tool calls, want 1", len(calls))
	}
	if calls[0].ID != "call_1" || calls[0].Function.Name != "get_weather" {
		t.Errorf("identity lost across fragments: %+v", calls[0])
	}
	if got := calls[0].Function.Arguments; got != `{"city": "Hangzhou"}` {
		t.Errorf("arguments = %q", got)
	}
}

func TestStreamSplitsReasoningFromContent(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`data: {"choices":[{"delta":{"content":null,"reasoning_content":"think"}}]}` + "\n\n"))
		w.Write([]byte(`data: {"choices":[{"delta":{"content":"answer"}}]}` + "\n\n"))
		w.Write([]byte("data: [DONE]\n\n"))
	}))

	resp, err := c.ChatStream(context.Background(), &ChatRequest{Model: ModelFlash}, false, func(*ChatChunk) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	msg := resp.Choices[0].Message
	if msg.Content != "answer" || msg.ReasoningContent != "think" {
		t.Errorf("content=%q reasoning=%q", msg.Content, msg.ReasoningContent)
	}
}

func TestResponsesStreamKeepsTerminalResponse(t *testing.T) {
	// This stream has no [DONE]; the terminal event carries the complete
	// object, usage included.
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n"))
		w.Write([]byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"usage\":{\"input_tokens\":5,\"output_tokens\":2,\"total_tokens\":7}}}\n\n"))
	}))

	resp, err := c.ResponsesStream(context.Background(), &ResponsesRequest{Model: ModelFlash}, func(*ResponsesEvent) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "r1" || resp.Status != "completed" {
		t.Errorf("terminal response lost: %+v", resp)
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 7 {
		t.Errorf("usage lost: %+v", resp.Usage)
	}
}

func TestAnthropicStreamPrefersFinalUsage(t *testing.T) {
	// message_start knows the input count but not the output count;
	// message_delta carries the real total and must win.
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n"))
		w.Write([]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n"))
		w.Write([]byte("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":10,\"output_tokens\":7}}\n\n"))
		w.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))

	content, _ := json.Marshal("hi")
	resp, err := c.AnthropicStream(context.Background(), &AnthropicRequest{
		Model: ModelFlash, MaxTokens: 8,
		Messages: []AnthropicMessage{{Role: "user", Content: content}},
	}, func(*AnthropicEvent) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text() != "hi" {
		t.Errorf("text = %q", resp.Text())
	}
	if resp.Usage.OutputTokens != 7 {
		t.Errorf("output tokens = %d, want the message_delta value 7", resp.Usage.OutputTokens)
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("stop reason = %q", resp.StopReason)
	}
}

func TestContextCancellationStopsRetrying(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := c.Models(ctx); err == nil {
		t.Fatal("want an error from a cancelled context")
	}
	if got := calls.Load(); got > 1 {
		t.Errorf("kept retrying a cancelled request: %d attempts", got)
	}
}

func TestEndpointJoinsPathsCleanly(t *testing.T) {
	c := New("k", "https://proxy.example.com/deepseek/", time.Second)
	if got := c.Endpoint("/chat/completions"); got != "https://proxy.example.com/deepseek/chat/completions" {
		t.Errorf("got %q", got)
	}
}
