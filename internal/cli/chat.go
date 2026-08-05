package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
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
	interactive    bool
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
	fl.StringVarP(&f.effort, "effort", "e", "", "reasoning effort: low, high, max (pro promotes low to high)")
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
	fl.BoolVarP(&f.interactive, "interactive", "i", false, "keep the conversation open and prompt for follow-ups")

	return cmd
}

// chatTurn holds everything a single exchange needs, resolved once. A
// one-shot call sends one turn and exits; --interactive sends one per
// line the user types, against the same client, session and flags.
type chatTurn struct {
	o      *Options
	f      *chatFlags
	cmd    *cobra.Command
	client *deepseek.Client
	tools  []deepseek.Tool

	sess     *session.Session
	sessName string

	stream        bool
	showReasoning bool
	system        string
	prefix        string
	// pending is material attached by /file in interactive mode, spent on
	// the next turn. Files are attached to a question, not to a session.
	pending []string
}

func runChat(cmd *cobra.Command, o *Options, f *chatFlags, args []string) error {
	ctx := ctxOf(cmd)

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

	// A conversation needs somewhere to live, so --interactive implies a
	// session even when none was named. Without --continue it starts a
	// fresh one under the default name: `chat -i` means "start a chat",
	// not "silently resume yesterday's". `chat -ic` resumes.
	sess, sessName, err := openSession(f.sessionName, f.continueLast || f.interactive)
	if err != nil {
		return err
	}
	if f.interactive && f.sessionName == "" && !f.continueLast {
		sess = &session.Session{Name: session.Default}
		sessName = session.Default
	}

	tools, err := loadTools(f.tools)
	if err != nil {
		return err
	}
	if err := validateTools(tools); err != nil {
		return err
	}
	if err := validateUserID(f.userID); err != nil {
		return err
	}

	client, err := o.client()
	if err != nil {
		return err
	}

	t := &chatTurn{
		o: o, f: f, cmd: cmd, client: client, tools: tools,
		sess: sess, sessName: sessName,
		stream: stream, showReasoning: showReasoning,
	}
	if f.system != "" {
		if t.system, err = readMaybeFile(f.system); err != nil {
			return err
		}
	}
	if f.prefix != "" {
		if t.prefix, err = readMaybeFile(f.prefix); err != nil {
			return err
		}
	}

	// Refuse an impossible conversation before the first turn is billed,
	// not after.
	if f.interactive {
		if err := o.checkInteractive(); err != nil {
			return err
		}
	}

	// Interactive mode may start with nothing to say — the prompt is the
	// point — so an empty first turn is only an error without it.
	prompt, err := readPrompt(args, f.files, f.prefix == "" && !f.interactive)
	if err != nil {
		return err
	}

	if strings.TrimSpace(prompt) != "" || t.prefix != "" {
		if err := t.send(ctx, prompt); err != nil {
			return err
		}
	}
	if f.interactive {
		return t.repl(ctx)
	}
	return nil
}

// send performs one exchange: build the request from the stored
// conversation plus this prompt, print the answer, and persist the turn.
func (t *chatTurn) send(ctx context.Context, prompt string) error {
	o := t.o

	req := &deepseek.ChatRequest{Model: t.f.model, Tools: t.tools}
	if err := applyChatFlags(t.cmd, t.f, req); err != nil {
		return err
	}

	// Build the message list: stored history, then this turn.
	if t.sess != nil {
		req.Messages = append(req.Messages, t.sess.History(len(t.tools) > 0)...)
	}
	if t.system != "" {
		req.Messages = prependSystem(req.Messages, t.system)
	}
	if len(t.pending) > 0 {
		prompt = strings.Join(append(t.pending, prompt), "\n\n")
		t.pending = nil
	}
	var turn []deepseek.Message
	if strings.TrimSpace(prompt) != "" {
		turn = append(turn, deepseek.Message{Role: "user", Content: prompt})
	}
	// Two things move a request onto the beta path: a prefix message, and
	// any tool declaring strict mode.
	beta := anyStrict(t.tools)
	if t.prefix != "" {
		turn = append(turn, deepseek.Message{Role: "assistant", Content: t.prefix, Prefix: true})
		beta = true
	}
	req.Messages = append(req.Messages, turn...)
	if len(req.Messages) == 0 {
		return fmt.Errorf("nothing to send — pass a prompt, --file, or pipe text in")
	}

	start := time.Now()
	var resp *deepseek.ChatResponse
	var raw []byte
	var err error
	if t.stream {
		resp, err = o.streamChat(ctx, t.client, req, beta, t.showReasoning)
	} else {
		resp, raw, err = t.client.Chat(ctx, req, beta)
	}
	if err != nil {
		return err
	}
	elapsed := time.Since(start)

	msg, finish := chatMessage(resp)
	if !t.stream {
		if t.showReasoning && msg.ReasoningContent != "" {
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

	o.stats("chat", req.Model, resp.Usage.Normalize(), elapsed, t.stream, t.sessName)

	// Persist the turn only after a successful call, so a failed request
	// never corrupts the conversation the next one will replay.
	if t.sess != nil {
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
			t.sess.Add(m)
		}
		t.sess.Add(deepseek.Message{
			Role:             "assistant",
			Content:          carried + msg.Content,
			ReasoningContent: msg.ReasoningContent,
			ToolCalls:        msg.ToolCalls,
		})
		t.sess.Model = req.Model
		if len(t.tools) > 0 {
			t.sess.UsedTools = true
		}
		if err := t.sess.Save(); err != nil {
			o.verbosef("could not save session %q: %v", t.sessName, err)
		}
	}
	// A prefix applies to the turn it was given for, not to every later
	// turn in an interactive run.
	t.prefix = ""
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
		if len(f.stop) > maxStopSequences {
			return fmt.Errorf("--stop takes at most %d sequences, got %d", maxStopSequences, len(f.stop))
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
		if f.topLogprobs < 0 || f.topLogprobs > maxTopLogprobsBack {
			return fmt.Errorf("--top-logprobs takes 0-%d, got %d", maxTopLogprobsBack, f.topLogprobs)
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

// validEffort accepts what the API accepts.
//
// Three levels are real — low, high, max — and two more are taken for
// compatibility with clients written against other APIs. What they map
// to depends on the model, and the docs are explicit about it:
//
//	requested   flash    pro
//	low         low      high
//	medium      high     high
//	high        high     high
//	xhigh       high     max
//	max         max      max
//
// So on pro there is no way to ask for less than high. Passing `low`
// there is not an error and not ignored — it is silently promoted, which
// is worth knowing before concluding that effort has no effect.
//
// Source: guides/thinking_mode and api/create-chat-completion.
func validEffort(e string) error {
	switch strings.ToLower(e) {
	case "low", "high", "max":
		return nil
	case "none", "minimal", "medium", "xhigh":
		// Accepted by the API and verified live; none and minimal appear
		// in no documentation at all. The API rejects anything genuinely
		// unknown with "unknown variant", so this list is the API's, not
		// a guess.
		return nil
	}
	return fmt.Errorf("--effort takes none, minimal, low, medium, high, xhigh or max, not %q", e)
}

// userIDPattern is the character set the API documents for user_id.
var userIDPattern = regexp.MustCompile(`^[a-zA-Z0-9\-_]+$`)

// validateUserID checks the rules documented in quick_start/rate_limit
// before spending a request to learn them. The length cap is 512 and the
// character set is narrow; both produce a 400 that says less than this.
func validateUserID(id string) error {
	if id == "" {
		return nil
	}
	if len(id) > 512 {
		return fmt.Errorf("--user-id is %d characters; the API allows at most 512", len(id))
	}
	if !userIDPattern.MatchString(id) {
		return fmt.Errorf("--user-id may only contain letters, digits, - and _ (the API's rule)\n" +
			"  It is also not a place for personal data — DeepSeek uses it for cache and scheduling isolation")
	}
	return nil
}

// Limits the API documents for tools. Checked locally because a rejected
// request costs a round trip and returns a less specific message.
const (
	maxTools           = 128
	maxToolNameLength  = 64
	maxStopSequences   = 16
	maxTopLogprobsBack = 20
)

var toolNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func validateTools(tools []deepseek.Tool) error {
	if len(tools) > maxTools {
		return fmt.Errorf("%d tools declared; the API accepts at most %d", len(tools), maxTools)
	}
	for _, t := range tools {
		name := t.Function.Name
		if len(name) > maxToolNameLength {
			return fmt.Errorf("tool name %q is %d characters; the API allows at most %d",
				name, len(name), maxToolNameLength)
		}
		if !toolNamePattern.MatchString(name) {
			return fmt.Errorf("tool name %q may only contain letters, digits, _ and - (the API's rule)", name)
		}
	}
	return nil
}

// anyStrict reports whether any tool asked for strict mode, which the
// docs place behind the beta base path. Without this the request goes to
// the stable path, the server ignores strict, and the schema guarantee
// the caller asked for silently does not hold — the worst kind of
// failure, because the output usually still looks right.
func anyStrict(tools []deepseek.Tool) bool {
	for _, t := range tools {
		if t.Function.Strict {
			return true
		}
	}
	return false
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
