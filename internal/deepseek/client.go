// Package deepseek is a hand-rolled client for the whole DeepSeek API:
// the OpenAI-format chat and FIM endpoints, the OpenAI Responses format,
// the Anthropic Messages format, plus models and balance.
//
// It is hand-rolled on purpose. The public API is six endpoints; an SDK
// dependency would cover at most one of the four wire formats and would
// hide the very thing this tool exists to show — the bytes on the wire.
package deepseek

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// DefaultBaseURL is the OpenAI-format root. The Anthropic format lives
// under /anthropic and the beta features (FIM, chat prefix) under /beta;
// both are derived from this one value so a proxy only has to be set once.
const DefaultBaseURL = "https://api.deepseek.com"

// Models. DeepSeek exposes exactly two; everything else the API accepts
// (Claude model names on the Anthropic endpoint) is mapped server-side.
const (
	ModelFlash = "deepseek-v4-flash"
	ModelPro   = "deepseek-v4-pro"
)

// Client talks to one DeepSeek deployment. The zero value is not usable;
// build one with New.
type Client struct {
	APIKey  string
	BaseURL string
	HTTP    *http.Client

	// Verbose writes request/response metadata to this writer when set.
	// Bodies are included only when VerboseBody is true, because a chat
	// body is the user's prompt and often does not belong in a log.
	Verbose     io.Writer
	VerboseBody bool

	// UserAgent identifies this CLI to the API.
	UserAgent string

	// Retries is the number of additional attempts made for transport
	// failures and 429/5xx responses. Requests that reached the model are
	// never retried — a second call would be billed twice.
	Retries int
}

// New builds a client. An empty baseURL falls back to DefaultBaseURL.
func New(apiKey, baseURL string, timeout time.Duration) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		APIKey:    apiKey,
		BaseURL:   strings.TrimRight(baseURL, "/"),
		HTTP:      &http.Client{Timeout: timeout},
		UserAgent: "deepseek-cli",
		Retries:   2,
	}
}

// Endpoint resolves a path against the base URL. Paths are given exactly
// as the API documents them ("/chat/completions", "/anthropic/v1/messages",
// "/beta/completions") so that grepping the docs finds the call site.
func (c *Client) Endpoint(path string) string {
	return c.BaseURL + "/" + strings.TrimLeft(path, "/")
}

// do performs one API call. A non-nil body is marshalled as JSON; a nil
// body makes a GET. The response body is returned raw so callers can both
// decode it and hand the untouched bytes to --json output.
func (c *Client) do(ctx context.Context, method, path string, body any, hdr map[string]string) ([]byte, error) {
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encoding request: %w", err)
		}
	}

	var lastErr error
	for attempt := 0; ; attempt++ {
		resp, err := c.attempt(ctx, method, path, payload, hdr)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if attempt >= c.Retries || !Retryable(err) || ctx.Err() != nil {
			return nil, lastErr
		}
		// Exponential backoff, honouring Retry-After when the API sent one.
		delay := backoff(attempt)
		if apiErr, ok := err.(*APIError); ok && apiErr.RetryAfter > 0 {
			delay = apiErr.RetryAfter
		}
		c.logf("retrying after %s (attempt %d/%d): %v", delay, attempt+1, c.Retries, err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
}

func (c *Client) attempt(ctx context.Context, method, path string, payload []byte, hdr map[string]string) ([]byte, error) {
	req, err := c.request(ctx, method, path, payload, hdr)
	if err != nil {
		return nil, err
	}

	start := time.Now()
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, &TransportError{Op: method + " " + path, Err: err}
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &TransportError{Op: "reading " + path, Err: err}
	}
	c.logf("%s %s -> %d (%s, %d bytes)", method, req.URL.Path, resp.StatusCode, time.Since(start).Round(time.Millisecond), len(raw))
	if c.VerboseBody && len(raw) > 0 {
		c.logf("response body: %s", truncate(string(raw), 4096))
	}

	if resp.StatusCode >= 400 {
		return nil, newAPIError(resp, raw)
	}
	return raw, nil
}

// stream opens an SSE response. The caller owns the returned body and must
// close it. Unlike do, a stream is never retried once the connection is
// established: partial output has already been shown and billed.
func (c *Client) stream(ctx context.Context, method, path string, body any, hdr map[string]string) (io.ReadCloser, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encoding request: %w", err)
	}

	var lastErr error
	for attempt := 0; ; attempt++ {
		req, err := c.request(ctx, method, path, payload, hdr)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "text/event-stream")

		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = &TransportError{Op: method + " " + path, Err: err}
		} else if resp.StatusCode >= 400 {
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			c.logf("%s %s -> %d (stream refused)", method, req.URL.Path, resp.StatusCode)
			lastErr = newAPIError(resp, raw)
		} else {
			c.logf("%s %s -> %d (streaming)", method, req.URL.Path, resp.StatusCode)
			return resp.Body, nil
		}

		if attempt >= c.Retries || !Retryable(lastErr) || ctx.Err() != nil {
			return nil, lastErr
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff(attempt)):
		}
	}
}

func (c *Client) request(ctx context.Context, method, path string, payload []byte, hdr map[string]string) (*http.Request, error) {
	endpoint := c.Endpoint(path)
	if _, err := url.Parse(endpoint); err != nil {
		return nil, fmt.Errorf("bad endpoint %q: %w", endpoint, err)
	}

	var rdr io.Reader
	if payload != nil {
		rdr = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, rdr)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", c.UserAgent)
	// Default auth is the OpenAI-format bearer token. The Anthropic
	// endpoint takes x-api-key instead and overrides this via hdr.
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}

	if c.Verbose != nil {
		c.logf("%s %s", method, endpoint)
		if c.VerboseBody && payload != nil {
			c.logf("request body: %s", truncate(string(payload), 4096))
		}
	}
	return req, nil
}

func (c *Client) logf(format string, args ...any) {
	if c.Verbose == nil {
		return
	}
	fmt.Fprintf(c.Verbose, "» "+format+"\n", args...)
}

func backoff(attempt int) time.Duration {
	d := time.Duration(1<<attempt) * 500 * time.Millisecond
	if d > 8*time.Second {
		d = 8 * time.Second
	}
	return d
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("… (%d bytes total)", len(s))
}

// ResolveKey finds an API key in the usual places, in precedence order:
// an explicit flag, DEEPSEEK_API_KEY, then a key file. The key file exists
// so agents and cron jobs do not have to keep a secret in the environment.
func ResolveKey(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if v := os.Getenv("DEEPSEEK_API_KEY"); v != "" {
		return v, nil
	}
	if path := KeyFile(); path != "" {
		if b, err := os.ReadFile(path); err == nil {
			if key := strings.TrimSpace(string(b)); key != "" {
				return key, nil
			}
		}
	}
	return "", ErrNoKey
}
