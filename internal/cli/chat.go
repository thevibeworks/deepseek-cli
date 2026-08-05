package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/thevibeworks/deepseek-cli/internal/deepseek"
	"github.com/thevibeworks/deepseek-cli/internal/session"
)

type chatFlags struct {
	model          string
	system         string
	think          string
	effort         string
	maxTokens      int
	temperature    float64
	topP           float64
	stop           []string
	responseFormat string
	tools          []string
	toolChoice     string
	logprobs       bool
	topLogprobs    int
	userID         string
	prefix         string
	files          []string
	stream         bool
	reasoning      bool
	sessionName    string
	continueLast   bool
}

func newChatCmd(o *Options) *cobra.Command {
	var f chatFlags

	cmd := &cobra.Command{
		Use:   "chat [prompt...]",
		Short: "Chat completion (OpenAI format, POST /chat/completions)",
		Long: strings.TrimSpace(`
Send a chat completion in the OpenAI format — the endpoint most tools and
SDKs expect, and the only one of the four that supports chat prefix
completion.

Thinking mode is on by DeepSeek's default. Its chain of thought goes to
stderr so that stdout stays pipeable; --json returns it in the response
under reasoning_content.

  deepseek chat "why is the sky blue"
  git diff | deepseek chat "write a commit message"
  deepseek chat "explain" --file server.go --model deepseek-v4-pro
  deepseek chat "and now in one line" --continue
  deepseek chat "list 3 colours as JSON" --response-format json_object --json`),
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runChat(cmd, o, &f, args)
		},
	}

	fl := cmd.Flags()
	fl.StringVarP(&f.model, "model", "m", deepseek.ModelFlash, "model: deepseek-v4-flash or deepseek-v4-pro")
	fl.StringVarP(&f.system, "system", "s", "", "system prompt, or @file")
	fl.StringVar(&f.think, "think", "", "thinking mode: on or off (default: the API's own default, on)")
	fl.StringVarP(&f.effort, "effort", "e", "", "reasoning effort: low, high, or max")
	fl.IntVar(&f.maxTokens, "max-tokens", 0, "cap generated tokens")
	fl.Float64Var(&f.temperature, "temperature", 0, "sampling temperature 0-2 (ignored in thinking mode)")
	fl.Float64Var(&f.topP, "top-p", 0, "nucleus sampling 0-1 (ignored in thinking mode)")
	fl.StringArrayVar(&f.stop, "stop", nil, "stop sequence (repeatable, max 16)")
	fl.StringVar(&f.responseFormat, "response-format", "", "text or json_object")
	fl.StringArrayVar(&f.tools, "tool", nil, "tool definition as JSON or @file (repeatable)")
	fl.StringVar(&f.toolChoice, "tool-choice", "", "none, auto, required, or a JSON tool-choice object")
	fl.BoolVar(&f.logprobs, "logprobs", false, "return log probabilities")
	fl.IntVar(&f.topLogprobs, "top-logprobs", 0, "return the N most likely tokens per position (0-20)")
	fl.StringVar(&f.userID, "user-id", "", "user_id for cache and scheduling isolation")
	fl.StringVar(&f.prefix, "prefix", "", "beta: force the answer to start with this text")
	fl.StringArrayVarP(&f.files, "file", "f", nil, "attach a file's contents to the prompt (repeatable)")
	fl.BoolVar(&f.stream, "stream", true, "stream the answer (default off when --json or --jq is used)")
	fl.BoolVar(&f.reasoning, "reasoning", true, "show the chain of thought on stderr (default off when stderr is not a terminal)")
	fl.StringVar(&f.sessionName, "session", "", "read and write this named conversation")
	fl.BoolVarP(&f.continueLast, "continue", "c", false, "continue the most recent conversation")

	return cmd
}

func runChat(cmd *cobra.Command, o *Options, f *chatFlags, args []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	// --json asks for the API's own response object; a streamed call can
	// only offer one this CLI assembled. So streaming defaults off there,
	// while remaining available on request.
	stream := f.stream
	if !cmd.Flags().Changed("stream") && (o.JSON || o.JQ != "") {
		stream = false
	}
	showReasoning := f.reasoning && o.stderrTTY
	if cmd.Flags().Changed("reasoning") {
		showReasoning = f.reasoning
	}

	prompt, err := readPrompt(args, f.files, f.prefix == "")
	if err != nil {
		return err
	}

	sess, sessName, err := openSession(f.sessionName, f.continueLast)
	if err != nil {
		return err
	}

	tools, err := loadTools(f.tools)
	if err != nil {
		return err
	}

	req := &deepseek.ChatRequest{Model: f.model, Tools: tools}
	if err := applyChatFlags(cmd, f, req); err != nil {
		return err
	}

	// Build the message list: stored history, then this turn.
	if sess != nil {
		req.Messages = append(req.Messages, sess.History(len(tools) > 0)...)
	}
	if f.system != "" {
		sys, err := readMaybeFile(f.system)
		if err != nil {
			return err
		}
		req.Messages = prependSystem(req.Messages, sys)
	}
	var turn []deepseek.Message
	if strings.TrimSpace(prompt) != "" {
		turn = append(turn, deepseek.Message{Role: "user", Content: prompt})
	}
	// A prefix is an assistant message the model continues from. It must
	// be last, and it only works on the beta path.
	beta := false
	if f.prefix != "" {
		pfx, err := readMaybeFile(f.prefix)
		if err != nil {
			return err
		}
		turn = append(turn, deepseek.Message{Role: "assistant", Content: pfx, Prefix: true})
		beta = true
	}
	req.Messages = append(req.Messages, turn...)
	if len(req.Messages) == 0 {
		return fmt.Errorf("nothing to send — pass a prompt, --file, or pipe text in")
	}

	start := time.Now()
	client, err := o.client()
	if err != nil {
		return err
	}

	var resp *deepseek.ChatResponse
	var raw []byte
	if stream {
		resp, err = o.streamChat(ctx, client, req, beta, showReasoning)
	} else {
		resp, raw, err = client.Chat(ctx, req, beta)
	}
	if err != nil {
		return err
	}
	elapsed := time.Since(start)

	msg, finish := chatMessage(resp)
	if !stream {
		if showReasoning && msg.ReasoningContent != "" {
			fmt.Fprintln(o.stderr, o.dim(indent(msg.ReasoningContent)))
		}
		if raw == nil {
			raw, _ = json.Marshal(resp)
		}
		if err := o.emit(raw, msg.Content); err != nil {
			return err
		}
	} else if o.JSON || o.JQ != "" {
		// Streaming with --json: the assembled object, clearly marked as
		// ours by carrying the same shape the API would have returned.
		assembled, _ := json.Marshal(resp)
		if err := o.emit(assembled, ""); err != nil {
			return err
		}
	}

	if len(msg.ToolCalls) > 0 {
		o.printToolCalls(msg.ToolCalls)
	}
	warnFinish(o, finish)

	o.stats("chat", req.Model, resp.Usage.Normalize(), elapsed, stream, sessName)

	// Persist the turn only after a successful call, so a failed request
	// never corrupts the conversation the next one will replay.
	if sess != nil {
		// A prefix message is a control instruction, not a turn of its own,
		// and the API completes it without echoing it back. Fold its text
		// onto the front of the reply so the stored conversation reads as
		// the answer the user actually saw.
		var carried string
		for _, m := range turn {
			if m.Prefix {
				carried = m.Content
				continue
			}
			sess.Add(m)
		}
		sess.Add(deepseek.Message{
			Role:             "assistant",
			Content:          carried + msg.Content,
			ReasoningContent: msg.ReasoningContent,
			ToolCalls:        msg.ToolCalls,
		})
		sess.Model = req.Model
		if len(tools) > 0 {
			sess.UsedTools = true
		}
		if err := sess.Save(); err != nil {
			o.verbosef("could not save session %q: %v", sessName, err)
		}
	}
	return nil
}

// chatMessage extracts the first choice, tolerating an empty response.
func chatMessage(resp *deepseek.ChatResponse) (msg struct {
	Content          string
	ReasoningContent string
	ToolCalls        []deepseek.ToolCall
}, finish string) {
	if resp == nil || len(resp.Choices) == 0 {
		return msg, ""
	}
	c := resp.Choices[0]
	msg.Content = c.Message.Content
	msg.ReasoningContent = c.Message.ReasoningContent
	msg.ToolCalls = c.Message.ToolCalls
	return msg, c.FinishReason
}

// streamChat prints deltas as they arrive: answer to stdout, chain of
// thought to stderr. Splitting the two streams is what lets a streamed
// call still be redirected into a file and get only the answer.
func (o *Options) streamChat(ctx context.Context, c *deepseek.Client, req *deepseek.ChatRequest, beta, showReasoning bool) (*deepseek.ChatResponse, error) {
	quiet := o.JSON || o.JQ != ""
	var inReasoning, wroteAnswer bool

	resp, err := c.ChatStream(ctx, req, beta, func(chunk *deepseek.ChatChunk) error {
		for _, ch := range chunk.Choices {
			if r := ch.Delta.ReasoningContent; r != "" && showReasoning && !quiet {
				if !inReasoning {
					fmt.Fprint(o.stderr, o.dim("thinking: "))
					inReasoning = true
				}
				fmt.Fprint(o.stderr, o.dim(r))
			}
			if t := ch.Delta.Content; t != "" && !quiet {
				if inReasoning {
					fmt.Fprintln(o.stderr)
					inReasoning = false
				}
				fmt.Fprint(o.stdout, t)
				wroteAnswer = true
			}
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

// applyChatFlags copies only the flags the user actually set. Sending a
// zero temperature because a Go float defaulted to 0 would silently change
// the model's behaviour.
func applyChatFlags(cmd *cobra.Command, f *chatFlags, req *deepseek.ChatRequest) error {
	fl := cmd.Flags()

	switch strings.ToLower(f.think) {
	case "":
	case "on", "enabled", "true":
		req.Thinking = &deepseek.Thinking{Type: "enabled"}
	case "off", "disabled", "false":
		req.Thinking = &deepseek.Thinking{Type: "disabled"}
	default:
		return fmt.Errorf("--think takes on or off, not %q", f.think)
	}

	if f.effort != "" {
		if err := validEffort(f.effort); err != nil {
			return err
		}
		req.ReasoningEffort = strings.ToLower(f.effort)
	}
	if fl.Changed("max-tokens") {
		req.MaxTokens = &f.maxTokens
	}
	if fl.Changed("temperature") {
		req.Temperature = &f.temperature
	}
	if fl.Changed("top-p") {
		req.TopP = &f.topP
	}
	if len(f.stop) > 0 {
		if len(f.stop) > 16 {
			return fmt.Errorf("--stop takes at most 16 sequences, got %d", len(f.stop))
		}
		req.Stop = f.stop
	}
	if f.responseFormat != "" {
		switch f.responseFormat {
		case "text", "json_object":
			req.ResponseFormat = &deepseek.ResponseFormat{Type: f.responseFormat}
		default:
			return fmt.Errorf("--response-format takes text or json_object, not %q", f.responseFormat)
		}
	}
	if f.toolChoice != "" {
		choice, err := parseToolChoice(f.toolChoice)
		if err != nil {
			return err
		}
		req.ToolChoice = choice
	}
	req.Logprobs = f.logprobs
	if fl.Changed("top-logprobs") {
		if f.topLogprobs < 0 || f.topLogprobs > 20 {
			return fmt.Errorf("--top-logprobs takes 0-20, got %d", f.topLogprobs)
		}
		req.TopLogprobs = &f.topLogprobs
		// The API requires logprobs when top_logprobs is set; setting one
		// without the other is always a mistake, so fix it rather than
		// let the request come back 400.
		req.Logprobs = true
	}
	req.UserID = f.userID
	return nil
}

func validEffort(e string) error {
	switch strings.ToLower(e) {
	case "low", "high", "max":
		return nil
	case "minimal", "medium", "xhigh":
		// Accepted by the API for compatibility and folded into the three
		// real levels; accepted here too rather than second-guessing it.
		return nil
	}
	return fmt.Errorf("--effort takes low, high, or max, not %q", e)
}

// parseToolChoice accepts either a mode word or a JSON object naming a
// specific tool.
func parseToolChoice(v string) (any, error) {
	trimmed := strings.TrimSpace(v)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "@") {
		raw, err := readJSON(trimmed)
		if err != nil {
			return nil, fmt.Errorf("--tool-choice: %w", err)
		}
		return raw, nil
	}
	switch trimmed {
	case "none", "auto", "required":
		return trimmed, nil
	}
	return nil, fmt.Errorf("--tool-choice takes none, auto, required, or a JSON object, not %q", v)
}

// loadTools reads tool definitions. Each source may hold a single tool or
// an array of them, in either the bare-function shape or the wrapped
// {"type":"function","function":{...}} shape, because both are in
// circulation and neither is worth making the user rewrite.
func loadTools(sources []string) ([]deepseek.Tool, error) {
	var out []deepseek.Tool
	for _, src := range sources {
		raw, err := readJSON(src)
		if err != nil {
			return nil, fmt.Errorf("--tool: %w", err)
		}
		if raw == nil {
			continue
		}
		var many []json.RawMessage
		if err := json.Unmarshal(raw, &many); err != nil {
			many = []json.RawMessage{raw}
		}
		for _, item := range many {
			tool, err := parseTool(item)
			if err != nil {
				return nil, fmt.Errorf("--tool %s: %w", src, err)
			}
			out = append(out, tool)
		}
	}
	return out, nil
}

func parseTool(raw json.RawMessage) (deepseek.Tool, error) {
	var wrapped deepseek.Tool
	if err := json.Unmarshal(raw, &wrapped); err == nil && wrapped.Function.Name != "" {
		if wrapped.Type == "" {
			wrapped.Type = "function"
		}
		return wrapped, nil
	}
	var bare struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
		InputSchema json.RawMessage `json:"input_schema"`
		Strict      bool            `json:"strict"`
	}
	if err := json.Unmarshal(raw, &bare); err != nil || bare.Name == "" {
		return deepseek.Tool{}, fmt.Errorf("no function name found")
	}
	var t deepseek.Tool
	t.Type = "function"
	t.Function.Name = bare.Name
	t.Function.Description = bare.Description
	t.Function.Parameters = bare.Parameters
	if t.Function.Parameters == nil {
		// Anthropic spells the same field input_schema; accept it so one
		// tool file works against every format this CLI speaks.
		t.Function.Parameters = bare.InputSchema
	}
	t.Function.Strict = bare.Strict
	return t, nil
}

// printToolCalls reports the calls the model wants made. This CLI does not
// execute them — running model-chosen commands is an agent's job, and a
// different, much larger set of safety questions. Showing them keeps the
// tool useful for developing and debugging tool schemas.
func (o *Options) printToolCalls(calls []deepseek.ToolCall) {
	if o.JSON || o.JQ != "" {
		return
	}
	for _, tc := range calls {
		fmt.Fprintf(o.stderr, "%s\n", o.dim(fmt.Sprintf("tool_call %s %s(%s)", tc.ID, tc.Function.Name, tc.Function.Arguments)))
	}
}

// warnFinish surfaces the finish reasons that mean the answer is not what
// it looks like. A truncated answer that prints silently is a bug report
// waiting to happen.
func warnFinish(o *Options, reason string) {
	switch reason {
	case "length":
		fmt.Fprintln(o.stderr, o.dim("! truncated: hit max_tokens or the context limit"))
	case "content_filter":
		fmt.Fprintln(o.stderr, o.dim("! truncated: content filter"))
	case "insufficient_system_resource":
		fmt.Fprintln(o.stderr, o.dim("! truncated: DeepSeek ran out of inference capacity mid-request — retry"))
	}
}

func prependSystem(msgs []deepseek.Message, system string) []deepseek.Message {
	sys := deepseek.Message{Role: "system", Content: system}
	// Replace an existing system message rather than stacking a second
	// one, so `--system` on a continued conversation means what it says.
	for i, m := range msgs {
		if m.Role == "system" {
			msgs[i] = sys
			return msgs
		}
	}
	return append([]deepseek.Message{sys}, msgs...)
}

// openSession resolves the session flags. Returning a nil session means
// this is a one-shot call that neither reads nor writes history.
func openSession(name string, continueLast bool) (*session.Session, string, error) {
	if name == "" && !continueLast {
		return nil, "", nil
	}
	if name == "" {
		name = session.Default
	}
	s, err := session.Load(name)
	if err != nil {
		return nil, "", err
	}
	return s, name, nil
}

func indent(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return strings.Join(lines, "\n")
}
