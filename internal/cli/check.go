package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/thevibeworks/deepseek-cli/internal/deepseek"
)

// probe is one endpoint's result.
type probe struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	OK      bool   `json:"ok"`
	Detail  string `json:"detail,omitempty"`
	Error   string `json:"error,omitempty"`
	Latency int64  `json:"ms"`

	// err keeps the typed failure so the command's exit code can carry
	// its meaning; Error above is the text for humans and --json.
	err error
}

type checkResult struct {
	BaseURL string  `json:"base_url"`
	KeySet  bool    `json:"key_set"`
	Probes  []probe `json:"probes"`
	OK      bool    `json:"ok"`
}

func newCheckCmd(o *Options) *cobra.Command {
	var model string

	cmd := &cobra.Command{
		Use:   "check",
		Short: "Verify the key and reach every endpoint",
		Long: strings.TrimSpace(`
Preflight: resolve the API key, then call all six endpoints once and
report which ones answered.

Run this when something is wrong and you do not yet know whether the
problem is your key, your balance, your network, a proxy in between, or
one specific endpoint. The four generation endpoints are called with a
one-token cap, so the whole check costs a fraction of a cent.

Exits 2 if the key is missing or rejected, 3 if the balance is
exhausted, 1 if any endpoint failed for another reason.`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCheck(cmd, o, model)
		},
	}
	cmd.Flags().StringVarP(&model, "model", "m", deepseek.ModelFlash, "model to probe the generation endpoints with")
	return cmd
}

func runCheck(cmd *cobra.Command, o *Options, model string) error {
	ctx := ctxOf(cmd)
	res := &checkResult{BaseURL: deepseek.DefaultBaseURL, OK: true}

	client, err := o.client()
	if err != nil {
		// No key at all: report it in the same shape as any other failure
		// so --json stays consistent, then exit with the auth code.
		res.Probes = append(res.Probes, probe{Name: "api key", OK: false, Error: err.Error()})
		res.OK = false
		if emitErr := o.emitValue(res, formatCheck(res)); emitErr != nil {
			return emitErr
		}
		return err
	}
	res.BaseURL = client.BaseURL
	res.KeySet = true

	// Ordered cheapest-first, so an auth failure shows up before anything
	// is spent on generation.
	res.Probes = append(res.Probes,
		run(ctx, "models", "GET /models", func(ctx context.Context) (string, error) {
			list, _, err := client.Models(ctx)
			if err != nil {
				return "", err
			}
			names := make([]string, 0, len(list.Data))
			for _, m := range list.Data {
				names = append(names, m.ID)
			}
			return strings.Join(names, ", "), nil
		}),
		run(ctx, "balance", "GET /user/balance", func(ctx context.Context) (string, error) {
			bal, _, err := client.Balance(ctx)
			if err != nil {
				return "", err
			}
			var parts []string
			for _, info := range bal.Funded() {
				parts = append(parts, info.TotalBalance+" "+info.Currency)
			}
			if len(parts) == 0 {
				return "", fmt.Errorf("no funded currency — top up at https://platform.deepseek.com/top_up")
			}
			return strings.Join(parts, ", "), nil
		}),
		run(ctx, "chat", "POST /chat/completions", func(ctx context.Context) (string, error) {
			resp, _, err := client.Chat(ctx, &deepseek.ChatRequest{
				Model:     model,
				Messages:  []deepseek.Message{{Role: "user", Content: "hi"}},
				Thinking:  &deepseek.Thinking{Type: "disabled"},
				MaxTokens: ptr(1),
			}, false)
			if err != nil {
				return "", err
			}
			return usageDetail(resp.Usage.Normalize()), nil
		}),
		run(ctx, "anthropic", "POST /anthropic/v1/messages", func(ctx context.Context) (string, error) {
			content, _ := json.Marshal("hi")
			resp, _, err := client.Anthropic(ctx, &deepseek.AnthropicRequest{
				Model:     model,
				MaxTokens: 1,
				Messages:  []deepseek.AnthropicMessage{{Role: "user", Content: content}},
				Thinking:  &deepseek.AnthropicThinking{Type: "disabled"},
			})
			if err != nil {
				return "", err
			}
			return usageDetail(resp.Usage.Normalize()), nil
		}),
		run(ctx, "responses", "POST /responses", func(ctx context.Context) (string, error) {
			resp, _, err := client.Responses(ctx, &deepseek.ResponsesRequest{
				Model:           model,
				Input:           "hi",
				Reasoning:       &deepseek.Reasoning{Effort: "none"},
				MaxOutputTokens: ptr(16),
			})
			if err != nil {
				return "", err
			}
			return usageDetail(resp.Usage.Normalize()), nil
		}),
		run(ctx, "fim", "POST /beta/completions", func(ctx context.Context) (string, error) {
			resp, _, err := client.FIM(ctx, &deepseek.FIMRequest{
				Model:     model,
				Prompt:    "def f():",
				MaxTokens: ptr(1),
			})
			if err != nil {
				return "", err
			}
			return usageDetail(resp.Usage.Normalize()), nil
		}),
	)

	var firstErr error
	for _, p := range res.Probes {
		if !p.OK {
			res.OK = false
			if firstErr == nil {
				firstErr = p.err
			}
		}
	}

	if err := o.emitValue(res, formatCheck(res)); err != nil {
		return err
	}
	if firstErr != nil {
		// Return the first failure so the exit code carries its meaning —
		// 2 for a rejected key, 3 for an exhausted balance.
		return firstErr
	}
	return nil
}

func run(ctx context.Context, name, path string, fn func(context.Context) (string, error)) probe {
	start := time.Now()
	detail, err := fn(ctx)
	p := probe{Name: name, Path: path, Latency: time.Since(start).Milliseconds()}
	if err != nil {
		p.Error = err.Error()
		p.err = err
		return p
	}
	p.OK = true
	p.Detail = detail
	return p
}

func usageDetail(u deepseek.Usage) string {
	return fmt.Sprintf("%d in / %d out", u.InputTokens, u.OutputTokens)
}

func formatCheck(res *checkResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", res.BaseURL)

	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	for _, p := range res.Probes {
		mark := "ok  "
		detail := p.Detail
		if !p.OK {
			mark = "FAIL"
			detail = p.Error
		}
		fmt.Fprintf(w, "%s\t%s\t%dms\t%s\n", mark, p.Path, p.Latency, detail)
	}
	w.Flush()

	if res.OK {
		fmt.Fprint(&b, "\nall endpoints reachable")
	}
	return strings.TrimRight(b.String(), "\n")
}

func ptr[T any](v T) *T { return &v }
