package deepseek

import (
	"context"
	"encoding/json"
	"fmt"
)

// Message is one entry in a chat conversation.
//
// ReasoningContent is DeepSeek's chain-of-thought channel, and its
// round-trip rule is the sharpest edge in the whole API: when a turn
// carries tools, every assistant message's reasoning_content MUST be sent
// back on every later request or the API answers 400. Without tools it is
// ignored, and dropping it saves tokens. Conversation.Append encodes that
// rule so callers never have to remember it.
type Message struct {
	Role             string     `json:"role"`
	Content          string     `json:"content"`
	Name             string     `json:"name,omitempty"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`

	// Prefix turns this assistant message into a chat prefix completion:
	// the model continues from Content instead of answering fresh. Beta —
	// requires the /beta base path.
	Prefix bool `json:"prefix,omitempty"`
}

// ToolCall is a function call emitted by the model.
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Index    *int   `json:"index,omitempty"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// Tool is a function the model may call. Parameters is held as raw JSON so
// a user-supplied JSON Schema reaches the API byte-for-byte; re-encoding a
// schema through Go types is how subtle validation bugs get introduced.
type Tool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters,omitempty"`
		Strict      bool            `json:"strict,omitempty"`
	} `json:"function"`
}

// Thinking toggles the chain of thought. DeepSeek defaults it to enabled;
// a nil *Thinking therefore means "leave the API's default alone", which
// is different from explicitly disabling it.
type Thinking struct {
	Type string `json:"type"` // enabled | disabled
}

// ResponseFormat selects plain text or guaranteed-valid JSON output.
type ResponseFormat struct {
	Type string `json:"type"` // text | json_object
}

// StreamOptions asks for a final usage-bearing chunk. Without it a
// streamed request reports no token counts at all, so the CLI always
// sets it — you cannot price what you cannot measure.
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// ChatRequest is POST /chat/completions.
type ChatRequest struct {
	Model           string          `json:"model"`
	Messages        []Message       `json:"messages"`
	Thinking        *Thinking       `json:"thinking,omitempty"`
	ReasoningEffort string          `json:"reasoning_effort,omitempty"`
	MaxTokens       *int            `json:"max_tokens,omitempty"`
	Temperature     *float64        `json:"temperature,omitempty"`
	TopP            *float64        `json:"top_p,omitempty"`
	Stop            []string        `json:"stop,omitempty"`
	ResponseFormat  *ResponseFormat `json:"response_format,omitempty"`
	Tools           []Tool          `json:"tools,omitempty"`
	ToolChoice      any             `json:"tool_choice,omitempty"`
	Logprobs        bool            `json:"logprobs,omitempty"`
	TopLogprobs     *int            `json:"top_logprobs,omitempty"`
	UserID          string          `json:"user_id,omitempty"`
	Stream          bool            `json:"stream,omitempty"`
	StreamOptions   *StreamOptions  `json:"stream_options,omitempty"`
}

// ChatResponse is the non-streaming chat completion object.
type ChatResponse struct {
	ID                string `json:"id"`
	Object            string `json:"object"`
	Created           int64  `json:"created"`
	Model             string `json:"model"`
	SystemFingerprint string `json:"system_fingerprint"`
	Choices           []struct {
		Index   int `json:"index"`
		Message struct {
			Role             string     `json:"role"`
			Content          string     `json:"content"`
			ReasoningContent string     `json:"reasoning_content"`
			ToolCalls        []ToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string          `json:"finish_reason"`
		Logprobs     json.RawMessage `json:"logprobs"`
	} `json:"choices"`
	Usage *ChatUsage `json:"usage"`
}

// ChatUsage is the OpenAI-format token accounting. prompt_tokens is the
// sum of the cache hit and miss counts, which are billed at prices that
// differ by a factor of fifty — hence the whole cost/cache reporting.
type ChatUsage struct {
	PromptTokens            int `json:"prompt_tokens"`
	CompletionTokens        int `json:"completion_tokens"`
	TotalTokens             int `json:"total_tokens"`
	PromptCacheHitTokens    int `json:"prompt_cache_hit_tokens"`
	PromptCacheMissTokens   int `json:"prompt_cache_miss_tokens"`
	CompletionTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

// Normalize converts to the format-independent Usage the CLI prices and
// logs. All four wire formats reduce to this shape.
func (u *ChatUsage) Normalize() Usage {
	if u == nil {
		return Usage{}
	}
	hit, miss := u.PromptCacheHitTokens, u.PromptCacheMissTokens
	// Older/edge responses omit the split; everything then counts as a miss,
	// which errs toward over-reporting cost rather than under-reporting it.
	if hit == 0 && miss == 0 {
		miss = u.PromptTokens
	}
	return Usage{
		InputTokens:     u.PromptTokens,
		CacheHitTokens:  hit,
		CacheMissTokens: miss,
		OutputTokens:    u.CompletionTokens,
		ReasoningTokens: u.CompletionTokensDetails.ReasoningTokens,
		TotalTokens:     u.TotalTokens,
	}
}

// Chat performs a non-streaming chat completion. The raw response bytes
// are returned alongside the decoded value so --json can print exactly
// what the API sent.
func (c *Client) Chat(ctx context.Context, req *ChatRequest, beta bool) (*ChatResponse, []byte, error) {
	req.Stream = false
	req.StreamOptions = nil
	raw, err := c.do(ctx, "POST", chatPath(beta), req, nil)
	if err != nil {
		return nil, nil, err
	}
	var out ChatResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, raw, fmt.Errorf("decoding chat response: %w", err)
	}
	return &out, raw, nil
}

// chatPath selects the beta prefix, which unlocks chat prefix completion.
func chatPath(beta bool) string {
	if beta {
		return "/beta/chat/completions"
	}
	return "/chat/completions"
}

// ChatChunk is one streamed delta.
type ChatChunk struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Role             string     `json:"role"`
			Content          string     `json:"content"`
			ReasoningContent string     `json:"reasoning_content"`
			ToolCalls        []ToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *ChatUsage `json:"usage"`
}

// ChatStream streams a chat completion, invoking fn for every chunk. It
// returns the assembled final message so a streamed turn can be appended
// to a conversation exactly like a non-streamed one.
func (c *Client) ChatStream(ctx context.Context, req *ChatRequest, beta bool, fn func(*ChatChunk) error) (*ChatResponse, error) {
	req.Stream = true
	req.StreamOptions = &StreamOptions{IncludeUsage: true}

	body, err := c.stream(ctx, "POST", chatPath(beta), req, nil)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	assembled := &ChatResponse{Object: "chat.completion", Model: req.Model}
	assembled.Choices = make([]struct {
		Index   int `json:"index"`
		Message struct {
			Role             string     `json:"role"`
			Content          string     `json:"content"`
			ReasoningContent string     `json:"reasoning_content"`
			ToolCalls        []ToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string          `json:"finish_reason"`
		Logprobs     json.RawMessage `json:"logprobs"`
	}, 1)
	assembled.Choices[0].Message.Role = "assistant"

	var toolCalls []ToolCall
	scanner := newSSEScanner(body)
	for {
		ev, ok, err := scanner.Next()
		if err != nil {
			return assembled, fmt.Errorf("reading stream: %w", err)
		}
		if !ok {
			break
		}

		var chunk ChatChunk
		if err := json.Unmarshal([]byte(ev.Data), &chunk); err != nil {
			// A malformed frame mid-stream is worth reporting but not worth
			// discarding the text already received.
			return assembled, fmt.Errorf("decoding stream chunk: %w", err)
		}
		if chunk.ID != "" {
			assembled.ID = chunk.ID
		}
		if chunk.Model != "" {
			assembled.Model = chunk.Model
		}
		if chunk.Usage != nil {
			assembled.Usage = chunk.Usage
		}
		for _, ch := range chunk.Choices {
			assembled.Choices[0].Message.Content += ch.Delta.Content
			assembled.Choices[0].Message.ReasoningContent += ch.Delta.ReasoningContent
			if ch.FinishReason != "" {
				assembled.Choices[0].FinishReason = ch.FinishReason
			}
			toolCalls = mergeToolCalls(toolCalls, ch.Delta.ToolCalls)
		}
		if err := fn(&chunk); err != nil {
			return assembled, err
		}
	}
	assembled.Choices[0].Message.ToolCalls = toolCalls
	return assembled, nil
}

// mergeToolCalls reassembles tool calls that arrive as fragments. The
// model streams a call's arguments across many chunks, keyed by index;
// only the first fragment carries the id and name.
func mergeToolCalls(acc, deltas []ToolCall) []ToolCall {
	for _, d := range deltas {
		idx := 0
		if d.Index != nil {
			idx = *d.Index
		}
		for len(acc) <= idx {
			acc = append(acc, ToolCall{Type: "function"})
		}
		if d.ID != "" {
			acc[idx].ID = d.ID
		}
		if d.Type != "" {
			acc[idx].Type = d.Type
		}
		if d.Function.Name != "" {
			acc[idx].Function.Name = d.Function.Name
		}
		acc[idx].Function.Arguments += d.Function.Arguments
	}
	return acc
}
