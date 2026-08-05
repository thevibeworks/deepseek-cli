package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/thevibeworks/deepseek-cli/internal/deepseek"
)

type fimFlags struct {
	model       string
	prefix      string
	suffix      string
	maxTokens   int
	temperature float64
	topP        float64
	stop        []string
	echo        bool
	logprobs    int
	stream      bool
}

func newFIMCmd(o *Options) *cobra.Command {
	var f fimFlags

	cmd := &cobra.Command{
		Use:     "fim [prefix...]",
		Aliases: []string{"complete"},
		Short:   "Fill in the middle (beta, POST /beta/completions)",
		Long: strings.TrimSpace(`
Give the model a prefix and an optional suffix; it writes what belongs
between them. This is the completion shape editors use for inline code
suggestions, and it is the one endpoint with no chat structure at all.

Beta, with two hard limits: output is capped at 4K tokens, and it runs in
non-thinking mode regardless of any effort setting.

  deepseek fim "def fib(n):" --suffix "    return fib(n-1) + fib(n-2)"
  deepseek fim --prefix @head.go --suffix @tail.go --max-tokens 200`),
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFIM(cmd, o, &f, args)
		},
	}

	fl := cmd.Flags()
	fl.StringVarP(&f.model, "model", "m", deepseek.ModelPro, "model: deepseek-v4-pro or deepseek-v4-flash")
	fl.StringVar(&f.prefix, "prefix", "", "text before the gap, or @file (defaults to the positional argument or stdin)")
	fl.StringVar(&f.suffix, "suffix", "", "text after the gap, or @file")
	fl.IntVar(&f.maxTokens, "max-tokens", 0, "cap generated tokens (the endpoint's own ceiling is 4096)")
	fl.Float64Var(&f.temperature, "temperature", 0, "sampling temperature 0-2")
	fl.Float64Var(&f.topP, "top-p", 0, "nucleus sampling 0-1")
	fl.StringArrayVar(&f.stop, "stop", nil, "stop sequence (repeatable, max 16)")
	fl.BoolVar(&f.echo, "echo", false, "include the prefix in the output")
	fl.IntVar(&f.logprobs, "logprobs", 0, "return log probabilities for the N most likely tokens (0-20)")
	fl.BoolVar(&f.stream, "stream", true, "stream the completion (default off when --json or --jq is used)")

	return cmd
}

func runFIM(cmd *cobra.Command, o *Options, f *fimFlags, args []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	// This is the one command whose default model is pro, which the free
	// tier does not carry.
	o.useFreeModel(cmd, &f.model)

	stream := f.stream
	if !cmd.Flags().Changed("stream") && (o.JSON || o.JQ != "") {
		stream = false
	}

	// The prefix comes from --prefix, or from the arguments and stdin —
	// so both `deepseek fim "def f():"` and `cat head.py | deepseek fim`
	// do the obvious thing.
	prefix := f.prefix
	if prefix != "" {
		var err error
		if prefix, err = readMaybeFile(prefix); err != nil {
			return fmt.Errorf("--prefix: %w", err)
		}
	} else {
		var err error
		if prefix, err = readPrompt(args, nil, true); err != nil {
			return err
		}
	}

	suffix, err := readMaybeFile(f.suffix)
	if err != nil {
		return fmt.Errorf("--suffix: %w", err)
	}

	req := &deepseek.FIMRequest{
		Model:  f.model,
		Prompt: prefix,
		Suffix: suffix,
		Stop:   f.stop,
		Echo:   f.echo,
	}
	if cmd.Flags().Changed("max-tokens") {
		req.MaxTokens = &f.maxTokens
	}
	if cmd.Flags().Changed("temperature") {
		req.Temperature = &f.temperature
	}
	if cmd.Flags().Changed("top-p") {
		req.TopP = &f.topP
	}
	if cmd.Flags().Changed("logprobs") {
		if f.logprobs < 0 || f.logprobs > 20 {
			return fmt.Errorf("--logprobs takes 0-20, got %d", f.logprobs)
		}
		req.Logprobs = &f.logprobs
	}

	client, err := o.client()
	if err != nil {
		return err
	}

	start := time.Now()
	var resp *deepseek.FIMResponse
	var raw []byte
	quiet := o.JSON || o.JQ != ""
	if stream {
		var wrote bool
		resp, err = client.FIMStream(ctx, req, func(delta string) error {
			if !quiet {
				fmt.Fprint(o.stdout, delta)
				wrote = true
			}
			return nil
		})
		if wrote {
			fmt.Fprintln(o.stdout)
		}
	} else {
		resp, raw, err = client.FIM(ctx, req)
	}
	if err != nil {
		return err
	}
	elapsed := time.Since(start)

	if !stream {
		if raw == nil {
			raw, _ = json.Marshal(resp)
		}
		if err := o.emit(raw, resp.Text()); err != nil {
			return err
		}
	} else if quiet {
		assembled, _ := json.Marshal(resp)
		if err := o.emit(assembled, ""); err != nil {
			return err
		}
	}

	if len(resp.Choices) > 0 {
		warnFinish(o, resp.Choices[0].FinishReason)
	}
	o.stats("fim", req.Model, resp.Usage.Normalize(), elapsed, stream, "")
	return nil
}
