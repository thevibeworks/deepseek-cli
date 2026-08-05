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

// defaultAnthropicMaxTokens is supplied because the Anthropic format makes
// max_tokens required while the other three make it optional. It is a cap,
// not a reservation — nothing is billed for tokens not generated — so it is
// set high enough that a thinking-mode answer is not cut off.
const defaultAnthropicMaxTokens = 32768

type anthropicFlags struct {
	model       string
	system      string
	think       string
	effort      string
	maxTokens   int
	temperature float64
	topP        float64
	stop        []string
	tools       []string
	toolChoice  string
	userID      string
	files       []string
	stream      bool
	reasoning   bool
}

func newAnthropicCmd(o *Options) *cobra.Command {
	var f anthropicFlags

	cmd := &cobra.Command{
		Use:     "anthropic [prompt...]",
		Aliases: []string{"messages"},
		Short:   "Message in the Anthropic format (POST /anthropic/v1/messages)",
		Long: strings.TrimSpace(`
Send a message in Anthropic's Messages format — the wire format Claude
Code, the Anthropic SDKs and the Claude desktop app speak. Use this to
check how your DeepSeek key behaves for those tools before pointing them
at it.

Claude model names are accepted and remapped server-side: claude-opus* to
deepseek-v4-pro, claude-sonnet*/claude-haiku* to deepseek-v4-flash, and
anything unrecognised to flash. The usage line shows both names so the
cost is traceable to the model that actually ran.

Text only: DeepSeek rejects image, document and search_result blocks.

  deepseek anthropic "hello"
  deepseek anthropic "hello" --model claude-opus-4-1 --json`),
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAnthropic(cmd, o, &f, args)
		},
	}

	fl := cmd.Flags()
	fl.StringVarP(&f.model, "model", "m", deepseek.ModelFlash, "model, or a Claude name to see how it is remapped")
	fl.StringVarP(&f.system, "system", "s", "", "system prompt, or @file")
	fl.StringVar(&f.think, "think", "", "thinking mode: on or off (default: the API's own default, on)")
	fl.StringVarP(&f.effort, "effort", "e", "", "reasoning effort: low, high, or max")
	fl.IntVar(&f.maxTokens, "max-tokens", defaultAnthropicMaxTokens, "cap generated tokens (required by this format)")
	fl.Float64Var(&f.temperature, "temperature", 0, "sampling temperature 0-2")
	fl.Float64Var(&f.topP, "top-p", 0, "nucleus sampling 0-1")
	fl.StringArrayVar(&f.stop, "stop", nil, "stop sequence (repeatable)")
	fl.StringArrayVar(&f.tools, "tool", nil, "tool definition as JSON or @file (repeatable)")
	fl.StringVar(&f.toolChoice, "tool-choice", "", "none, auto, any, or a JSON tool-choice object")
	fl.StringVar(&f.userID, "user-id", "", "metadata.user_id for cache and scheduling isolation")
	fl.StringArrayVarP(&f.files, "file", "f", nil, "attach a file's contents to the prompt (repeatable)")
	fl.BoolVar(&f.stream, "stream", true, "stream the answer (default off when --json or --jq is used)")
	fl.BoolVar(&f.reasoning, "reasoning", true, "show the chain of thought on stderr (default off when stderr is not a terminal)")

	return cmd
}

func runAnthropic(cmd *cobra.Command, o *Options, f *anthropicFlags, args []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	stream := f.stream
	if !cmd.Flags().Changed("stream") && (o.JSON || o.JQ != "") {
		stream = false
	}
	showReasoning := f.reasoning && o.stderrTTY
	if cmd.Flags().Changed("reasoning") {
		showReasoning = f.reasoning
	}

	prompt, err := readPrompt(args, f.files, true)
	if err != nil {
		return err
	}

	content, err := json.Marshal(prompt)
	if err != nil {
		return err
	}
	req := &deepseek.AnthropicRequest{
		Model:         f.model,
		MaxTokens:     f.maxTokens,
		Messages:      []deepseek.AnthropicMessage{{Role: "user", Content: content}},
		StopSequences: f.stop,
	}
	if f.system != "" {
		if req.System, err = readMaybeFile(f.system); err != nil {
			return err
		}
	}
	switch strings.ToLower(f.think) {
	case "":
	case "on", "enabled", "true":
		req.Thinking = &deepseek.AnthropicThinking{Type: "enabled"}
	case "off", "disabled", "false":
		req.Thinking = &deepseek.AnthropicThinking{Type: "disabled"}
	default:
		return fmt.Errorf("--think takes on or off, not %q", f.think)
	}
	if f.effort != "" {
		if err := validEffort(f.effort); err != nil {
			return err
		}
		req.OutputConfig = &deepseek.AnthropicOutputConfig{Effort: strings.ToLower(f.effort)}
	}
	if cmd.Flags().Changed("temperature") {
		req.Temperature = &f.temperature
	}
	if cmd.Flags().Changed("top-p") {
		req.TopP = &f.topP
	}
	if f.userID != "" {
		req.Metadata = map[string]string{"user_id": f.userID}
	}
	if req.Tools, err = loadAnthropicTools(f.tools); err != nil {
		return err
	}
	if f.toolChoice != "" {
		if req.ToolChoice, err = parseAnthropicToolChoice(f.toolChoice); err != nil {
			return err
		}
	}

	client, err := o.client()
	if err != nil {
		return err
	}

	start := time.Now()
	var resp *deepseek.AnthropicResponse
	var raw []byte
	if stream {
		resp, err = o.streamAnthropic(ctx, client, req, showReasoning)
	} else {
		resp, raw, err = client.Anthropic(ctx, req)
	}
	if err != nil {
		return err
	}
	elapsed := time.Since(start)

	if !stream {
		if showReasoning {
			if t := resp.ThinkingText(); t != "" {
				fmt.Fprintln(o.stderr, o.dim(indent(t)))
			}
		}
		if raw == nil {
			raw, _ = json.Marshal(resp)
		}
		if err := o.emit(raw, resp.Text()); err != nil {
			return err
		}
	} else if o.JSON || o.JQ != "" {
		assembled, _ := json.Marshal(resp)
		if err := o.emit(assembled, ""); err != nil {
			return err
		}
	}

	if resp.StopReason == "max_tokens" {
		fmt.Fprintln(o.stderr, o.dim("! truncated: hit max_tokens"))
	}
	o.stats("anthropic", req.Model, resp.Usage.Normalize(), elapsed, stream, "")
	return nil
}

func (o *Options) streamAnthropic(ctx context.Context, c *deepseek.Client, req *deepseek.AnthropicRequest, showReasoning bool) (*deepseek.AnthropicResponse, error) {
	quiet := o.JSON || o.JQ != ""
	var inThinking, wroteAnswer bool

	resp, err := c.AnthropicStream(ctx, req, func(ev *deepseek.AnthropicEvent) error {
		if ev.Type != "content_block_delta" || quiet {
			return nil
		}
		if t := ev.Delta.Thinking; t != "" && showReasoning {
			if !inThinking {
				fmt.Fprint(o.stderr, o.dim("thinking: "))
				inThinking = true
			}
			fmt.Fprint(o.stderr, o.dim(t))
		}
		if t := ev.Delta.Text; t != "" {
			if inThinking {
				fmt.Fprintln(o.stderr)
				inThinking = false
			}
			fmt.Fprint(o.stdout, t)
			wroteAnswer = true
		}
		return nil
	})

	if inThinking {
		fmt.Fprintln(o.stderr)
	}
	if wroteAnswer {
		fmt.Fprintln(o.stdout)
	}
	return resp, err
}

// loadAnthropicTools reuses the OpenAI-format tool reader and renames the
// schema field, so one --tool file works against every format.
func loadAnthropicTools(sources []string) ([]deepseek.AnthropicTool, error) {
	tools, err := loadTools(sources)
	if err != nil {
		return nil, err
	}
	var out []deepseek.AnthropicTool
	for _, t := range tools {
		out = append(out, deepseek.AnthropicTool{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			InputSchema: t.Function.Parameters,
		})
	}
	return out, nil
}

// parseAnthropicToolChoice builds this format's tool_choice, which is an
// object with a type rather than the OpenAI format's bare string.
func parseAnthropicToolChoice(v string) (any, error) {
	trimmed := strings.TrimSpace(v)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "@") {
		raw, err := readJSON(trimmed)
		if err != nil {
			return nil, fmt.Errorf("--tool-choice: %w", err)
		}
		return raw, nil
	}
	switch trimmed {
	case "none", "auto", "any":
		return map[string]string{"type": trimmed}, nil
	case "required":
		// The OpenAI format's word for the same thing; accept it rather
		// than make people remember which format calls it what.
		return map[string]string{"type": "any"}, nil
	}
	return nil, fmt.Errorf("--tool-choice takes none, auto, any, or a JSON object, not %q", v)
}
