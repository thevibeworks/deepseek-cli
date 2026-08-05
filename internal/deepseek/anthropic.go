package deepseek

import (
	"context"
	"encoding/json"
	"fmt"
)

// The Anthropic-format endpoint. DeepSeek serves the same two models
// through Anthropic's Messages wire format so that Claude Code, the
// Claude SDKs and anything else in that ecosystem can point at DeepSeek
// by changing two environment variables.
//
// Two differences from the OpenAI-format endpoint matter to callers:
// auth is x-api-key rather than a bearer token, and max_tokens is
// required rather than optional.
const anthropicPath = "/anthropic/v1/messages"

// AnthropicContent is one block of message content. Only text blocks are
// supported by DeepSeek — image, document and search_result blocks are
// rejected — so this deliberately models text and passes anything else
// through untouched as raw JSON.
type AnthropicContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`

	// Thinking blocks come back in thinking mode.
	Thinking string `json:"thinking,omitempty"`

	// tool_use / tool_result fields.
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
}

// AnthropicMessage is one turn. Content is raw JSON because the format
// permits both a bare string and an array of blocks, and forcing one
// shape would break round-tripping a conversation the API gave us.
type AnthropicMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// AnthropicThinking toggles the chain of thought. DeepSeek honours the
// type field and ignores budget_tokens.
type AnthropicThinking struct {
	Type         string `json:"type"` // enabled | disabled
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

// AnthropicOutputConfig carries reasoning effort in this format; the
// OpenAI format spells the same control reasoning_effort.
type AnthropicOutputConfig struct {
	Effort string `json:"effort,omitempty"` // low | high | max
}

// AnthropicRequest is POST /anthropic/v1/messages.
type AnthropicRequest struct {
	Model         string                 `json:"model"`
	MaxTokens     int                    `json:"max_tokens"`
	Messages      []AnthropicMessage     `json:"messages"`
	System        string                 `json:"system,omitempty"`
	Thinking      *AnthropicThinking     `json:"thinking,omitempty"`
	OutputConfig  *AnthropicOutputConfig `json:"output_config,omitempty"`
	Temperature   *float64               `json:"temperature,omitempty"`
	TopP          *float64               `json:"top_p,omitempty"`
	StopSequences []string               `json:"stop_sequences,omitempty"`
	Tools         []AnthropicTool        `json:"tools,omitempty"`
	ToolChoice    any                    `json:"tool_choice,omitempty"`
	Metadata      map[string]string      `json:"metadata,omitempty"`
	Stream        bool                   `json:"stream,omitempty"`
}

// AnthropicTool is a function declaration in Anthropic's spelling:
// input_schema where the OpenAI format says parameters.
type AnthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

// AnthropicResponse is the Messages object.
type AnthropicResponse struct {
	ID           string             `json:"id"`
	Type         string             `json:"type"`
	Role         string             `json:"role"`
	Model        string             `json:"model"`
	Content      []AnthropicContent `json:"content"`
	StopReason   string             `json:"stop_reason"`
	StopSequence string             `json:"stop_sequence"`
	Usage        *AnthropicUsage    `json:"usage"`
}

// Text joins the text blocks of the response.
func (r *AnthropicResponse) Text() string {
	var s string
	for _, blk := range r.Content {
		if blk.Type == "text" {
			s += blk.Text
		}
	}
	return s
}

// ThinkingText joins the thinking blocks of the response.
func (r *AnthropicResponse) ThinkingText() string {
	var s string
	for _, blk := range r.Content {
		if blk.Type == "thinking" {
			s += blk.Thinking
		}
	}
	return s
}

// AnthropicUsage is Anthropic-format token accounting.
//
// Note the different convention from the OpenAI format: here input_tokens
// counts only the tokens that were NOT served from cache, and the cached
// ones are reported separately. The full prompt is therefore the sum of
// all three input fields, which is what Normalize reconstructs.
type AnthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// Normalize converts to the format-independent Usage.
func (u *AnthropicUsage) Normalize() Usage {
	if u == nil {
		return Usage{}
	}
	hit := u.CacheReadInputTokens
	// Cache *creation* tokens were processed, not replayed, so they are
	// billed at the miss rate along with the plain input tokens.
	miss := u.InputTokens + u.CacheCreationInputTokens
	return Usage{
		InputTokens:     hit + miss,
		CacheHitTokens:  hit,
		CacheMissTokens: miss,
		OutputTokens:    u.OutputTokens,
		TotalTokens:     hit + miss + u.OutputTokens,
	}
}

func (c *Client) anthropicHeaders() map[string]string {
	return map[string]string{
		// The Anthropic ecosystem authenticates with x-api-key. DeepSeek
		// accepts the bearer token too, but sending the header this format
		// documents keeps the traffic faithful to what an Anthropic SDK
		// would produce — which is the point of testing against it.
		"x-api-key":         c.APIKey,
		"anthropic-version": "2023-06-01",
	}
}

// Anthropic performs a non-streaming Messages call.
func (c *Client) Anthropic(ctx context.Context, req *AnthropicRequest) (*AnthropicResponse, []byte, error) {
	req.Stream = false
	raw, err := c.do(ctx, "POST", anthropicPath, req, c.anthropicHeaders())
	if err != nil {
		return nil, nil, err
	}
	var out AnthropicResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, raw, fmt.Errorf("decoding messages response: %w", err)
	}
	return &out, raw, nil
}

// AnthropicEvent is one decoded stream event. The format names every
// event; the payload shape depends on the name.
type AnthropicEvent struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	ContentBlock *AnthropicContent  `json:"content_block"`
	Message      *AnthropicResponse `json:"message"`
	Usage        *AnthropicUsage    `json:"usage"`
}

// AnthropicStream streams a Messages call, invoking fn per event, and
// returns the assembled message.
func (c *Client) AnthropicStream(ctx context.Context, req *AnthropicRequest, fn func(*AnthropicEvent) error) (*AnthropicResponse, error) {
	req.Stream = true
	body, err := c.stream(ctx, "POST", anthropicPath, req, c.anthropicHeaders())
	if err != nil {
		return nil, err
	}
	defer body.Close()

	assembled := &AnthropicResponse{Type: "message", Role: "assistant", Model: req.Model}
	var text, thinking string

	scanner := newSSEScanner(body)
	for {
		sse, ok, err := scanner.Next()
		if err != nil {
			return assembled, fmt.Errorf("reading stream: %w", err)
		}
		if !ok {
			break
		}

		var ev AnthropicEvent
		if err := json.Unmarshal([]byte(sse.Data), &ev); err != nil {
			return assembled, fmt.Errorf("decoding stream event: %w", err)
		}
		switch ev.Type {
		case "message_start":
			if ev.Message != nil {
				assembled.ID = ev.Message.ID
				assembled.Model = ev.Message.Model
				assembled.Usage = ev.Message.Usage
			}
		case "content_block_delta":
			text += ev.Delta.Text
			thinking += ev.Delta.Thinking
		case "message_delta":
			if ev.Delta.StopReason != "" {
				assembled.StopReason = ev.Delta.StopReason
			}
			// The final usage lands here; it supersedes message_start's,
			// which knows the input count but not yet the output count.
			if ev.Usage != nil {
				assembled.Usage = ev.Usage
			}
		}
		if err := fn(&ev); err != nil {
			return assembled, err
		}
	}

	if thinking != "" {
		assembled.Content = append(assembled.Content, AnthropicContent{Type: "thinking", Thinking: thinking})
	}
	assembled.Content = append(assembled.Content, AnthropicContent{Type: "text", Text: text})
	return assembled, nil
}
