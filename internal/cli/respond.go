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

type respondFlags struct {
	model        string
	instructions string
	effort       string
	maxTokens    int
	temperature  float64
	topP         float64
	format       string
	schema       string
	schemaName   string
	tools        []string
	toolChoice   string
	webSearch    bool
	user         string
	files        []string
	stream       bool
	reasoning    bool
}

func newRespondCmd(o *Options) *cobra.Command {
	var f respondFlags

	cmd := &cobra.Command{
		Use:     "respond [prompt...]",
		Aliases: []string{"responses"},
		Short:   "Response in the OpenAI Responses format (POST /responses)",
		Long: strings.TrimSpace(`
Send a request in OpenAI's Responses format — the wire format Codex
speaks. Two things live here and nowhere else in the DeepSeek API:
JSON Schema structured output, and web_search, a tool DeepSeek runs
server-side.

Both models are accepted since V4-Pro's official release
(2026-08-12); the endpoint was flash-only before that.

  deepseek respond "what shipped in Go 1.26" --web-search
  deepseek respond "extract the versions" --schema @versions.json --json`),
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRespond(cmd, o, &f, args)
		},
	}

	fl := cmd.Flags()
	fl.StringVarP(&f.model, "model", "m", deepseek.ModelFlash, "model: deepseek-v4-flash or deepseek-v4-pro")
	fl.StringVarP(&f.instructions, "instructions", "s", "", "system-level instructions, or @file")
	fl.StringVarP(&f.effort, "effort", "e", "", "reasoning effort: none, low, high, or max (none disables thinking)")
	fl.IntVar(&f.maxTokens, "max-tokens", 0, "cap generated tokens, reasoning included")
	fl.Float64Var(&f.temperature, "temperature", 0, "sampling temperature 0-2")
	fl.Float64Var(&f.topP, "top-p", 0, "nucleus sampling 0-1")
	fl.StringVar(&f.format, "format", "", "output format: text, json_object, or json_schema")
	fl.StringVar(&f.schema, "schema", "", "JSON Schema for structured output, as JSON or @file (implies json_schema)")
	fl.StringVar(&f.schemaName, "schema-name", "response", "name for the JSON Schema")
	fl.StringArrayVar(&f.tools, "tool", nil, "tool definition as JSON or @file (repeatable)")
	fl.StringVar(&f.toolChoice, "tool-choice", "", "none, auto, required, or a JSON tool-choice object")
	fl.BoolVar(&f.webSearch, "web-search", false, "let the model search the web (runs on DeepSeek's servers)")
	fl.StringVar(&f.user, "user", "", "user identifier for cache and scheduling isolation")
	fl.StringArrayVarP(&f.files, "file", "f", nil, "attach a file's contents to the prompt (repeatable)")
	fl.BoolVar(&f.stream, "stream", true, "stream the answer (default off when --json or --jq is used)")
	fl.BoolVar(&f.reasoning, "reasoning", true, "show the chain of thought on stderr (default off when stderr is not a terminal)")

	return cmd
}

func runRespond(cmd *cobra.Command, o *Options, f *respondFlags, args []string) error {
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

	// This format accepts instructions alone, with no input at all.
	prompt, err := readPrompt(args, f.files, f.instructions == "")
	if err != nil {
		return err
	}

	req := &deepseek.ResponsesRequest{Model: f.model, User: f.user}
	if prompt != "" {
		req.Input = prompt
	}
	if f.instructions != "" {
		if req.Instructions, err = readMaybeFile(f.instructions); err != nil {
			return err
		}
	}
	if f.effort != "" {
		switch strings.ToLower(f.effort) {
		case "none", "minimal", "low", "medium", "high", "xhigh", "max":
			req.Reasoning = &deepseek.Reasoning{Effort: strings.ToLower(f.effort)}
		default:
			return fmt.Errorf("--effort takes none, low, high, or max, not %q", f.effort)
		}
	}
	if cmd.Flags().Changed("max-tokens") {
		req.MaxOutputTokens = &f.maxTokens
	}
	if cmd.Flags().Changed("temperature") {
		req.Temperature = &f.temperature
	}
	if cmd.Flags().Changed("top-p") {
		req.TopP = &f.topP
	}
	if req.Text, err = buildTextConfig(f); err != nil {
		return err
	}
	if req.Tools, err = loadResponsesTools(f.tools, f.webSearch); err != nil {
		return err
	}
	if f.toolChoice != "" {
		if req.ToolChoice, err = parseToolChoice(f.toolChoice); err != nil {
			return err
		}
	}

	client, err := o.client()
	if err != nil {
		return err
	}

	start := time.Now()
	var resp *deepseek.ResponsesResponse
	var raw []byte
	if stream {
		resp, err = o.streamRespond(ctx, client, req, showReasoning)
	} else {
		resp, raw, err = client.Responses(ctx, req)
	}
	if err != nil {
		return err
	}
	elapsed := time.Since(start)

	if !stream {
		if showReasoning {
			if t := resp.ReasoningText(); t != "" {
				fmt.Fprintln(o.stderr, o.dim(indent(t)))
			}
		}
		if raw == nil {
			raw, _ = json.Marshal(resp)
		}
		if err := o.emit(raw, resp.OutputText()); err != nil {
			return err
		}
	} else if o.JSON || o.JQ != "" {
		assembled, _ := json.Marshal(resp)
		if err := o.emit(assembled, ""); err != nil {
			return err
		}
	}

	// A failed response arrives as HTTP 200 with status "failed"; without
	// this the command would exit 0 having printed nothing.
	if resp.Status == "failed" {
		msg := strings.TrimSpace(string(resp.Error))
		if msg == "" || msg == "null" {
			msg = "no error detail returned"
		}
		return fmt.Errorf("response failed: %s", msg)
	}
	if resp.Status == "incomplete" && resp.IncompleteDetails != nil {
		fmt.Fprintln(o.stderr, o.dim("! incomplete: "+resp.IncompleteDetails.Reason))
	}
	o.printResponsesCalls(resp)
	o.stats("responses", req.Model, resp.Usage.Normalize(), elapsed, stream, "")
	return nil
}

func (o *Options) streamRespond(ctx context.Context, c *deepseek.Client, req *deepseek.ResponsesRequest, showReasoning bool) (*deepseek.ResponsesResponse, error) {
	quiet := o.JSON || o.JQ != ""
	var inReasoning, wroteAnswer bool

	resp, err := c.ResponsesStream(ctx, req, func(ev *deepseek.ResponsesEvent) error {
		if quiet {
			return nil
		}
		switch ev.Type {
		case "response.reasoning_text.delta":
			if showReasoning && ev.Delta != "" {
				if !inReasoning {
					fmt.Fprint(o.stderr, o.dim("thinking: "))
					inReasoning = true
				}
				fmt.Fprint(o.stderr, o.dim(ev.Delta))
			}
		case "response.output_text.delta":
			if ev.Delta != "" {
				if inReasoning {
					fmt.Fprintln(o.stderr)
					inReasoning = false
				}
				fmt.Fprint(o.stdout, ev.Delta)
				wroteAnswer = true
			}
		case "response.web_search_call.searching":
			fmt.Fprintln(o.stderr, o.dim("· searching the web"))
		}
		return nil
	})

	if inReasoning {
		fmt.Fprintln(o.stderr)
	}
	if wroteAnswer {
		fmt.Fprintln(o.stdout)
	}
	return resp, err
}

func buildTextConfig(f *respondFlags) (*deepseek.TextConfig, error) {
	if f.schema != "" {
		schema, err := readJSON(f.schema)
		if err != nil {
			return nil, fmt.Errorf("--schema: %w", err)
		}
		return &deepseek.TextConfig{Format: &deepseek.TextFormat{
			Type:   "json_schema",
			Name:   f.schemaName,
			Schema: schema,
		}}, nil
	}
	switch f.format {
	case "":
		return nil, nil
	case "text", "json_object":
		return &deepseek.TextConfig{Format: &deepseek.TextFormat{Type: f.format}}, nil
	case "json_schema":
		return nil, fmt.Errorf("--format json_schema needs a schema — pass --schema @file.json")
	}
	return nil, fmt.Errorf("--format takes text, json_object, or json_schema, not %q", f.format)
}

func loadResponsesTools(sources []string, webSearch bool) ([]deepseek.ResponsesTool, error) {
	tools, err := loadTools(sources)
	if err != nil {
		return nil, err
	}
	var out []deepseek.ResponsesTool
	// This format flattens the function fields to the top level instead of
	// nesting them under "function".
	for _, t := range tools {
		out = append(out, deepseek.ResponsesTool{
			Type:        "function",
			Name:        t.Function.Name,
			Description: t.Function.Description,
			Parameters:  t.Function.Parameters,
		})
	}
	if webSearch {
		out = append(out, deepseek.ResponsesTool{Type: "web_search"})
	}
	return out, nil
}

// printResponsesCalls reports function calls and web searches to stderr.
func (o *Options) printResponsesCalls(resp *deepseek.ResponsesResponse) {
	if o.JSON || o.JQ != "" {
		return
	}
	for _, item := range resp.Output {
		switch item.Type {
		case "function_call":
			fmt.Fprintln(o.stderr, o.dim(fmt.Sprintf("tool_call %s %s(%s)", item.CallID, item.Name, item.Arguments)))
		case "web_search_call":
			if len(item.Action) > 0 {
				fmt.Fprintln(o.stderr, o.dim("web_search "+string(item.Action)))
			}
		}
	}
}
