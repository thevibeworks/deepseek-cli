package deepseek

import (
	"strings"
	"testing"
)

func collect(t *testing.T, body string) []Event {
	t.Helper()
	sc := newSSEScanner(strings.NewReader(body))
	var out []Event
	for {
		ev, ok, err := sc.Next()
		if err != nil {
			t.Fatalf("scan: %v", err)
		}
		if !ok {
			return out
		}
		out = append(out, ev)
	}
}

func TestSSEChatFormat(t *testing.T) {
	// The OpenAI-format stream: bare data lines, terminated by [DONE].
	body := "data: {\"a\":1}\n\ndata: {\"a\":2}\n\ndata: [DONE]\n\n"
	got := collect(t, body)
	if len(got) != 2 {
		t.Fatalf("want 2 events, got %d: %+v", len(got), got)
	}
	if got[0].Data != `{"a":1}` || got[1].Data != `{"a":2}` {
		t.Errorf("unexpected payloads: %+v", got)
	}
}

func TestSSENamedEvents(t *testing.T) {
	// The Anthropic and Responses streams name every event and end without
	// a [DONE] sentinel.
	body := "event: message_start\ndata: {\"type\":\"message_start\"}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\"}\n\n"
	got := collect(t, body)
	if len(got) != 2 {
		t.Fatalf("want 2 events, got %d", len(got))
	}
	if got[0].Name != "message_start" || got[1].Name != "content_block_delta" {
		t.Errorf("event names lost: %+v", got)
	}
}

func TestSSEKeepAliveIsNotAnEvent(t *testing.T) {
	// While a request waits for capacity DeepSeek sends `: keep-alive`
	// comments for up to ten minutes. Surfacing those as events would
	// feed junk to every decoder downstream.
	body := ": keep-alive\n\n: keep-alive\n\ndata: {\"a\":1}\n\ndata: [DONE]\n\n"
	got := collect(t, body)
	if len(got) != 1 {
		t.Fatalf("keep-alive leaked into the stream: %+v", got)
	}
	if got[0].Data != `{"a":1}` {
		t.Errorf("payload = %q", got[0].Data)
	}
}

func TestSSEBlankLinesAreNotEvents(t *testing.T) {
	// Non-streaming keep-alives are bare empty lines; consecutive event
	// separators are also legal SSE. Neither dispatches an event.
	got := collect(t, "\n\n\ndata: {\"a\":1}\n\n\n\n")
	if len(got) != 1 {
		t.Fatalf("want 1 event, got %d: %+v", len(got), got)
	}
}

func TestSSEMultiLineData(t *testing.T) {
	// Multiple data lines in one event concatenate with newlines.
	got := collect(t, "data: line one\ndata: line two\n\n")
	if len(got) != 1 || got[0].Data != "line one\nline two" {
		t.Fatalf("multi-line data mishandled: %+v", got)
	}
}

func TestSSEUnterminatedFinalEvent(t *testing.T) {
	// A body that ends without its trailing blank line still has to yield
	// the last event; dropping it would silently truncate an answer.
	got := collect(t, "data: {\"a\":1}")
	if len(got) != 1 || got[0].Data != `{"a":1}` {
		t.Fatalf("final event dropped: %+v", got)
	}
}

func TestSSECarriageReturns(t *testing.T) {
	got := collect(t, "event: ping\r\ndata: {\"a\":1}\r\n\r\n")
	if len(got) != 1 || got[0].Name != "ping" || got[0].Data != `{"a":1}` {
		t.Fatalf("CRLF mishandled: %+v", got)
	}
}
