package deepseek

import (
	"context"
	"encoding/json"
	"fmt"
)

// The OpenAI Responses format. DeepSeek added it for Codex, and it is the
// only one of the four formats with a server-side tool: web_search runs
// on DeepSeek's side and comes back as web_search_call output items.
//
// It is stateless. previous_response_id and conversation are rejected, so
// multi-turn means resending the whole input list every time.
const responsesPath = "/responses"

// ResponsesRequest is POST /responses.
type ResponsesRequest struct {
	Model           string          `json:"model"`
	Input           any             `json:"input,omitempty"` // string or []ResponseItem
	Instructions    string          `json:"instructions,omitempty"`
	Reasoning       *Reasoning      `json:"reasoning,omitempty"`
	MaxOutputTokens *int            `json:"max_output_tokens,omitempty"`
	Temperature     *float64        `json:"temperature,omitempty"`
	TopP            *float64        `json:"top_p,omitempty"`
	Text            *TextConfig     `json:"text,omitempty"`
	Tools           []ResponsesTool `json:"tools,omitempty"`
	ToolChoice      any             `json:"tool_choice,omitempty"`
	TopLogprobs     *int            `json:"top_logprobs,omitempty"`
	User            string          `json:"user,omitempty"`
	Stream          bool            `json:"stream,omitempty"`
}

// Reasoning controls thinking in this format. Unlike the OpenAI chat
// format, which has a separate on/off toggle, here effort carries both:
// "none" disables thinking entirely.
type Reasoning struct {
	Effort string `json:"effort,omitempty"` // none|minimal|low|medium|high|xhigh|max
}

// TextConfig selects the output format, including JSON Schema structured
// output — which this format supports and the OpenAI chat format does not.
type TextConfig struct {
	Format *TextFormat `json:"format,omitempty"`
}

// TextFormat is text, json_object, or json_schema.
type TextFormat struct {
	Type   string          `json:"type"`
	Name   string          `json:"name,omitempty"`
	Schema json.RawMessage `json:"schema,omitempty"`
}

// ResponsesTool is a function or the built-in server-side web_search.
type ResponsesTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// ResponseItem is one input or output item.
type ResponseItem struct {
	Type    string          `json:"type,omitempty"`
	ID      string          `json:"id,omitempty"`
	Status  string          `json:"status,omitempty"`
	Role    string          `json:"role,omitempty"`
	Content json.RawMessage `json:"content,omitempty"`

	// function_call / function_call_output
	CallID    string          `json:"call_id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments string          `json:"arguments,omitempty"`
	Output    string          `json:"output,omitempty"`
	Action    json.RawMessage `json:"action,omitempty"`
}

// ContentPart is one piece of an item's content.
type ContentPart struct {
	Type string `json:"type"` // output_text | reasoning_text | input_text
	Text string `json:"text"`
}

// Parts decodes the item's content parts, tolerating the bare-string form.
func (it ResponseItem) Parts() []ContentPart {
	if len(it.Content) == 0 {
		return nil
	}
	var parts []ContentPart
	if err := json.Unmarshal(it.Content, &parts); err == nil {
		return parts
	}
	var s string
	if err := json.Unmarshal(it.Content, &s); err == nil {
		return []ContentPart{{Type: "output_text", Text: s}}
	}
	return nil
}

// ResponsesResponse is the response object.
type ResponsesResponse struct {
	ID                string         `json:"id"`
	Object            string         `json:"object"`
	CreatedAt         int64          `json:"created_at"`
	Status            string         `json:"status"`
	Model             string         `json:"model"`
	Output            []ResponseItem `json:"output"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Error json.RawMessage `json:"error"`
	Usage *ResponsesUsage `json:"usage"`
}

// OutputText joins the assistant's visible text, the field the OpenAI
// SDKs expose as response.output_text.
func (r *ResponsesResponse) OutputText() string {
	var s string
	for _, item := range r.Output {
		if item.Type != "message" {
			continue
		}
		for _, p := range item.Parts() {
			if p.Type == "output_text" {
				s += p.Text
			}
		}
	}
	return s
}

// ReasoningText joins the chain-of-thought items.
func (r *ResponsesResponse) ReasoningText() string {
	var s string
	for _, item := range r.Output {
		if item.Type != "reasoning" {
			continue
		}
		for _, p := range item.Parts() {
			s += p.Text
		}
	}
	return s
}

// ResponsesUsage is Responses-format token accounting. Here input_tokens
// is the whole prompt and cached_tokens is the subset of it that hit the
// cache — the OpenAI convention, not the Anthropic one.
type ResponsesUsage struct {
	InputTokens        int `json:"input_tokens"`
	InputTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokens        int `json:"output_tokens"`
	OutputTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
	TotalTokens int `json:"total_tokens"`
}

// Normalize converts to the format-independent Usage.
func (u *ResponsesUsage) Normalize() Usage {
	if u == nil {
		return Usage{}
	}
	hit := u.InputTokensDetails.CachedTokens
	miss := u.InputTokens - hit
	if miss < 0 {
		miss = 0
	}
	return Usage{
		InputTokens:     u.InputTokens,
		CacheHitTokens:  hit,
		CacheMissTokens: miss,
		OutputTokens:    u.OutputTokens,
		ReasoningTokens: u.OutputTokensDetails.ReasoningTokens,
		TotalTokens:     u.TotalTokens,
	}
}

// Responses performs a non-streaming Responses call.
func (c *Client) Responses(ctx context.Context, req *ResponsesRequest) (*ResponsesResponse, []byte, error) {
	req.Stream = false
	raw, err := c.do(ctx, "POST", responsesPath, req, nil)
	if err != nil {
		return nil, nil, err
	}
	var out ResponsesResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, raw, fmt.Errorf("decoding response: %w", err)
	}
	return &out, raw, nil
}

// ResponsesEvent is one semantic stream event. This stream has no [DONE]
// sentinel; it ends with response.completed, .incomplete or .failed.
type ResponsesEvent struct {
	Type           string             `json:"type"`
	SequenceNumber int                `json:"sequence_number"`
	OutputIndex    int                `json:"output_index"`
	ItemID         string             `json:"item_id"`
	Delta          string             `json:"delta"`
	Text           string             `json:"text"`
	Item           *ResponseItem      `json:"item"`
	Response       *ResponsesResponse `json:"response"`
}

// Terminal reports whether this event ends the stream.
func (e *ResponsesEvent) Terminal() bool {
	switch e.Type {
	case "response.completed", "response.incomplete", "response.failed":
		return true
	}
	return false
}

// ResponsesStream streams a Responses call, invoking fn per event, and
// returns the final response object carried by the terminal event.
func (c *Client) ResponsesStream(ctx context.Context, req *ResponsesRequest, fn func(*ResponsesEvent) error) (*ResponsesResponse, error) {
	req.Stream = true
	body, err := c.stream(ctx, "POST", responsesPath, req, nil)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	final := &ResponsesResponse{Object: "response", Model: req.Model}
	var text, reasoning string

	scanner := newSSEScanner(body)
	for {
		sse, ok, err := scanner.Next()
		if err != nil {
			return final, fmt.Errorf("reading stream: %w", err)
		}
		if !ok {
			break
		}

		var ev ResponsesEvent
		if err := json.Unmarshal([]byte(sse.Data), &ev); err != nil {
			return final, fmt.Errorf("decoding stream event: %w", err)
		}
		switch ev.Type {
		case "response.output_text.delta":
			text += ev.Delta
		case "response.reasoning_text.delta":
			reasoning += ev.Delta
		}
		if ev.Terminal() && ev.Response != nil {
			final = ev.Response
		}
		if err := fn(&ev); err != nil {
			return final, err
		}
	}

	// The terminal event carries the complete output; only synthesize one
	// when the stream ended without it (a truncated connection).
	if len(final.Output) == 0 {
		if reasoning != "" {
			final.Output = append(final.Output, synthItem("reasoning", "reasoning_text", reasoning))
		}
		final.Output = append(final.Output, synthItem("message", "output_text", text))
	}
	return final, nil
}

func synthItem(itemType, partType, text string) ResponseItem {
	content, _ := json.Marshal([]ContentPart{{Type: partType, Text: text}})
	item := ResponseItem{Type: itemType, Status: "completed", Content: content}
	if itemType == "message" {
		item.Role = "assistant"
	}
	return item
}
