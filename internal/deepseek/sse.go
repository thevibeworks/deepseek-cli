package deepseek

import (
	"bufio"
	"io"
	"strings"
)

// Event is one server-sent event. Name is empty for the OpenAI-format
// streams, which send bare `data:` lines; the Anthropic and Responses
// streams name every event.
type Event struct {
	Name string
	Data string
}

// sseScanner reads an SSE body into events.
//
// Two details of DeepSeek's stream are load-bearing here. First, while a
// request waits for capacity the server sends `: keep-alive` comment lines
// for up to ten minutes — those are not events and must not be surfaced.
// Second, the OpenAI-format streams terminate with a literal `data: [DONE]`
// sentinel while the Responses and Anthropic streams simply end; both are
// treated as end-of-stream here so callers do not have to care.
type sseScanner struct {
	sc *bufio.Scanner
}

func newSSEScanner(r io.Reader) *sseScanner {
	sc := bufio.NewScanner(r)
	// A single event can carry a whole response object (the terminal
	// response.completed event does). 1 MiB of line buffer is generous
	// enough that we never split one.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	return &sseScanner{sc: sc}
}

// Next returns the next event. ok is false at end of stream. The returned
// error is whatever the underlying reader failed with, if anything.
func (s *sseScanner) Next() (ev Event, ok bool, err error) {
	var name string
	var data strings.Builder

	for s.sc.Scan() {
		line := strings.TrimRight(s.sc.Text(), "\r")

		switch {
		case line == "":
			// Blank line dispatches the accumulated event. Ignore blanks
			// that dispatch nothing: non-streaming keep-alives are empty
			// lines, and consecutive separators are legal SSE.
			if data.Len() == 0 && name == "" {
				continue
			}
			payload := data.String()
			if payload == "[DONE]" {
				return Event{}, false, nil
			}
			return Event{Name: name, Data: payload}, true, nil

		case strings.HasPrefix(line, ":"):
			// Comment — the `: keep-alive` heartbeat lands here.
			continue

		case strings.HasPrefix(line, "event:"):
			name = strings.TrimSpace(line[len("event:"):])

		case strings.HasPrefix(line, "data:"):
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimSpace(line[len("data:"):]))
		}
	}

	if err := s.sc.Err(); err != nil {
		return Event{}, false, err
	}
	// End of body with a half-built event: dispatch it rather than drop it.
	if data.Len() > 0 && data.String() != "[DONE]" {
		return Event{Name: name, Data: data.String()}, true, nil
	}
	return Event{}, false, nil
}
