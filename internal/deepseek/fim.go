package deepseek

import (
	"context"
	"encoding/json"
	"fmt"
)

// FIM (fill in the middle) completion. Beta: it lives under the /beta
// path, caps output at 4K tokens, and runs in non-thinking mode only.
// Give it a prefix and an optional suffix; it writes what goes between.
const fimPath = "/beta/completions"

// FIMRequest is POST /beta/completions.
type FIMRequest struct {
	Model         string         `json:"model"`
	Prompt        string         `json:"prompt"`
	Suffix        string         `json:"suffix,omitempty"`
	MaxTokens     *int           `json:"max_tokens,omitempty"`
	Temperature   *float64       `json:"temperature,omitempty"`
	TopP          *float64       `json:"top_p,omitempty"`
	Stop          []string       `json:"stop,omitempty"`
	Echo          bool           `json:"echo,omitempty"`
	Logprobs      *int           `json:"logprobs,omitempty"`
	Stream        bool           `json:"stream,omitempty"`
	StreamOptions *StreamOptions `json:"stream_options,omitempty"`
}

// FIMResponse is the text_completion object.
type FIMResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int             `json:"index"`
		Text         string          `json:"text"`
		FinishReason string          `json:"finish_reason"`
		Logprobs     json.RawMessage `json:"logprobs"`
	} `json:"choices"`
	Usage *ChatUsage `json:"usage"`
}

// Text returns the completion text.
func (r *FIMResponse) Text() string {
	if len(r.Choices) == 0 {
		return ""
	}
	return r.Choices[0].Text
}

// FIM performs a non-streaming FIM completion.
func (c *Client) FIM(ctx context.Context, req *FIMRequest) (*FIMResponse, []byte, error) {
	req.Stream = false
	req.StreamOptions = nil
	raw, err := c.do(ctx, "POST", fimPath, req, nil)
	if err != nil {
		return nil, nil, err
	}
	var out FIMResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, raw, fmt.Errorf("decoding completion: %w", err)
	}
	return &out, raw, nil
}

// FIMStream streams a FIM completion, invoking fn with each text delta,
// and returns the assembled completion.
func (c *Client) FIMStream(ctx context.Context, req *FIMRequest, fn func(string) error) (*FIMResponse, error) {
	req.Stream = true
	req.StreamOptions = &StreamOptions{IncludeUsage: true}

	body, err := c.stream(ctx, "POST", fimPath, req, nil)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	assembled := &FIMResponse{Object: "text_completion", Model: req.Model}
	assembled.Choices = make([]struct {
		Index        int             `json:"index"`
		Text         string          `json:"text"`
		FinishReason string          `json:"finish_reason"`
		Logprobs     json.RawMessage `json:"logprobs"`
	}, 1)

	scanner := newSSEScanner(body)
	for {
		sse, ok, err := scanner.Next()
		if err != nil {
			return assembled, fmt.Errorf("reading stream: %w", err)
		}
		if !ok {
			break
		}
		var chunk FIMResponse
		if err := json.Unmarshal([]byte(sse.Data), &chunk); err != nil {
			return assembled, fmt.Errorf("decoding stream chunk: %w", err)
		}
		if chunk.ID != "" {
			assembled.ID = chunk.ID
		}
		if chunk.Usage != nil {
			assembled.Usage = chunk.Usage
		}
		for _, ch := range chunk.Choices {
			assembled.Choices[0].Text += ch.Text
			if ch.FinishReason != "" {
				assembled.Choices[0].FinishReason = ch.FinishReason
			}
			if ch.Text != "" {
				if err := fn(ch.Text); err != nil {
					return assembled, err
				}
			}
		}
	}
	return assembled, nil
}
