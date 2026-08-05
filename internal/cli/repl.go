package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/thevibeworks/deepseek-cli/internal/deepseek"
	"github.com/thevibeworks/deepseek-cli/internal/session"
	"golang.org/x/term"
)

// The interactive loop behind `deepseek chat --interactive`.
//
// The API is stateless and this CLI is one-shot by design, so a
// conversation is just a session replayed on every request. Interactive
// mode changes nothing about that — it only saves you from typing
// `deepseek chat --continue` between turns. Quitting and coming back with
// `deepseek chat -c` picks up exactly where the loop left off, because
// there is no other state.
//
// Terminal handling is deliberately narrow. The line editor owns the
// terminal only while a line is being typed; the moment a turn is sent,
// raw mode is restored and the answer streams to stdout through the same
// code path a one-shot call uses. That is what keeps the output contract
// intact — `deepseek chat -i > answers.txt` still captures answers and
// nothing else, because the prompt and the chrome go to stderr.

// replIO gives the line editor a terminal to read from and write to.
// Reads come from stdin; everything it echoes goes to stderr, so stdout
// stays reserved for answers.
type replIO struct {
	in  io.Reader
	out io.Writer
}

func (r replIO) Read(p []byte) (int, error)  { return r.in.Read(p) }
func (r replIO) Write(p []byte) (int, error) { return r.out.Write(p) }

// checkInteractive rejects a run that cannot become a conversation.
//
// It is called before the first request, not at the loop, because the
// first turn is billed: discovering that stdin is a pipe after paying
// for an answer nobody will follow up is a bad trade for the user.
func (o *Options) checkInteractive() error {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !o.stderrTTY {
		return fmt.Errorf("--interactive needs a terminal on stdin and stderr\n" +
			"  For scripted multi-turn use --session instead:\n" +
			"    deepseek chat \"first\"  --session work\n" +
			"    deepseek chat \"second\" --session work")
	}
	if o.JSON || o.JQ != "" {
		return fmt.Errorf("--interactive and --json do not combine — one is a conversation, the other is one response body\n" +
			"  For machine-readable multi-turn use --session and --json together")
	}
	return nil
}

func (t *chatTurn) repl(ctx context.Context) error {
	o := t.o
	if err := o.checkInteractive(); err != nil {
		return err
	}
	stdinFD := int(os.Stdin.Fd())

	line := term.NewTerminal(replIO{in: os.Stdin, out: o.stderr}, "")
	line.SetPrompt(o.dim("› "))

	o.replBanner()

	for {
		text, err := t.readLine(line, stdinFD)
		if err != nil {
			if errors.Is(err, io.EOF) {
				fmt.Fprintln(o.stderr, o.dim(t.replFarewell()))
				return nil
			}
			return err
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		if strings.HasPrefix(text, "/") {
			done, err := t.command(ctx, text)
			if err != nil {
				fmt.Fprintln(o.stderr, o.dim("! "+err.Error()))
			}
			if done {
				return nil
			}
			continue
		}

		// One cancellable context per turn. ^C during generation abandons
		// the answer and returns to the prompt; it does not kill the loop,
		// which is the difference between a REPL and a one-shot command.
		turnCtx, cancel := context.WithCancel(ctx)
		release := holdInterrupt(cancel)
		err = t.send(turnCtx, text)
		release()
		cancel()

		if err != nil {
			if errors.Is(err, context.Canceled) || turnCtx.Err() != nil {
				fmt.Fprintln(o.stderr, o.dim("^C — answer abandoned; the conversation is intact"))
				continue
			}
			// An interactive session outlives one bad request: report it
			// and keep the prompt, except when the key or the balance is
			// gone, where every later turn would fail the same way.
			fmt.Fprintf(o.stderr, "Error: %s\n", err)
			switch deepseek.ExitCode(err) {
			case deepseek.ExitAuth, deepseek.ExitBalance:
				return err
			}
		}
	}
}

// readLine puts the terminal in raw mode for exactly as long as it takes
// to read one line, then hands it back. Restoring between turns is what
// lets streamed output, signals and ^C behave normally the rest of the
// time.
func (t *chatTurn) readLine(line *term.Terminal, fd int) (string, error) {
	state, err := term.MakeRaw(fd)
	if err != nil {
		return "", fmt.Errorf("interactive mode needs raw terminal access: %w", err)
	}
	defer term.Restore(fd, state)

	if w, _, err := term.GetSize(fd); err == nil && w > 0 {
		line.SetSize(w, 1)
	}
	text, err := line.ReadLine()
	// A bracketed paste arrives as one line with this sentinel error. The
	// text is good; only the "was it typed" question was answered no.
	if errors.Is(err, term.ErrPasteIndicator) {
		return text, nil
	}
	return text, err
}

func (o *Options) replBanner() {
	fmt.Fprintln(o.stderr, o.dim("interactive — /help for commands, ^D to leave"))
}

func (t *chatTurn) replFarewell() string {
	if t.sess == nil || t.sess.Empty() {
		return "bye"
	}
	return fmt.Sprintf("bye — %d messages saved as %q; resume with: deepseek chat -c",
		len(t.sess.Messages), t.sessName)
}

// command runs a slash command. It returns done=true when the loop should
// end. Errors are reported and the loop continues: a typo in /model is
// not a reason to lose the conversation.
func (t *chatTurn) command(ctx context.Context, input string) (done bool, err error) {
	name, arg, _ := strings.Cut(strings.TrimPrefix(input, "/"), " ")
	arg = strings.TrimSpace(arg)
	o := t.o

	switch name {
	case "exit", "quit", "q":
		fmt.Fprintln(o.stderr, o.dim(t.replFarewell()))
		return true, nil

	case "help", "h", "?":
		fmt.Fprint(o.stderr, o.dim(replHelp))
		return false, nil

	case "new":
		t.sess = &session.Session{Name: t.sessName}
		fmt.Fprintln(o.stderr, o.dim("new conversation — history cleared"))
		return false, nil

	case "model", "m":
		if arg == "" {
			fmt.Fprintln(o.stderr, o.dim("model "+t.f.model))
			return false, nil
		}
		model := expandModel(arg)
		if _, ok := deepseek.PriceFor(model); !ok || deepseek.ResolveModel(model) != model {
			return false, fmt.Errorf("unknown model %q — try flash or pro", arg)
		}
		t.f.model = model
		fmt.Fprintln(o.stderr, o.dim("model "+model))
		return false, nil

	case "think":
		switch arg {
		case "on", "off":
			t.f.think = arg
			_ = t.cmd.Flags().Set("think", arg)
			fmt.Fprintln(o.stderr, o.dim("thinking "+arg))
		case "":
			state := t.f.think
			if state == "" {
				state = "on (the API's default)"
			}
			fmt.Fprintln(o.stderr, o.dim("thinking "+state))
		default:
			return false, fmt.Errorf("/think takes on or off")
		}
		return false, nil

	case "effort", "e":
		if arg == "" {
			effort := t.f.effort
			if effort == "" {
				effort = "high (the API's default)"
			}
			fmt.Fprintln(o.stderr, o.dim("effort "+effort))
			return false, nil
		}
		if err := validEffort(arg); err != nil {
			return false, err
		}
		t.f.effort = strings.ToLower(arg)
		fmt.Fprintln(o.stderr, o.dim("effort "+t.f.effort))
		return false, nil

	case "system", "s":
		if arg == "" {
			if t.system == "" {
				fmt.Fprintln(o.stderr, o.dim("no system prompt"))
			} else {
				fmt.Fprintln(o.stderr, o.dim("system: "+firstLine(t.system)))
			}
			return false, nil
		}
		text, err := readMaybeFile(arg)
		if err != nil {
			return false, err
		}
		t.system = text
		fmt.Fprintln(o.stderr, o.dim("system prompt set ("+plural(len(text), "byte")+")"))
		return false, nil

	case "file", "f":
		if arg == "" {
			return false, fmt.Errorf("/file needs a path")
		}
		block, err := readFileBlock(arg)
		if err != nil {
			return false, err
		}
		fmt.Fprintln(o.stderr, o.dim("attached "+arg+" — ask your question next"))
		t.pending = append(t.pending, block)
		return false, nil

	case "save":
		if arg == "" {
			return false, fmt.Errorf("/save needs a name")
		}
		if t.sess == nil {
			return false, fmt.Errorf("nothing to save")
		}
		t.sess.Name = arg
		t.sessName = arg
		if err := t.sess.Save(); err != nil {
			return false, err
		}
		fmt.Fprintf(o.stderr, "%s\n", o.dim(fmt.Sprintf("saved as %q — resume with: deepseek chat -i --session %s", arg, arg)))
		return false, nil

	case "session":
		if t.sess == nil {
			fmt.Fprintln(o.stderr, o.dim("no session"))
			return false, nil
		}
		fmt.Fprintf(o.stderr, "%s\n", o.dim(fmt.Sprintf("session %q · %d messages · model %s",
			t.sessName, len(t.sess.Messages), t.f.model)))
		return false, nil

	case "tokens":
		return false, t.replTokens(ctx, arg)

	case "docs":
		if arg == "" {
			return false, fmt.Errorf("/docs needs something to look up")
		}
		return false, t.replDocs(ctx, arg)

	default:
		return false, fmt.Errorf("unknown command /%s — /help lists them", name)
	}
}

const replHelp = `
  /help                 this list
  /new                  start over, same session name
  /model [flash|pro]    show or switch the model
  /think [on|off]       show or switch thinking mode
  /effort [low|high|max] show or set reasoning effort
  /system [text|@file]  show or set the system prompt
  /file <path>          attach a file to the next question
  /tokens [text]        count tokens — the conversation so far, or text
  /docs <query>         search DeepSeek's own API docs
  /session              what this conversation is called
  /save <name>          rename and save it
  /exit                 leave (so does ^D)

  ^C during an answer abandons it and keeps the conversation.
`

// replTokens counts either the given text or the conversation so far —
// "how big has this got" is the question a long session provokes.
func (t *chatTurn) replTokens(ctx context.Context, arg string) error {
	text := arg
	label := "text"
	if text == "" {
		if t.sess == nil || t.sess.Empty() {
			return fmt.Errorf("nothing to count yet")
		}
		var b strings.Builder
		for _, m := range t.sess.Messages {
			b.WriteString(m.Content)
			b.WriteString("\n")
		}
		text = b.String()
		label = fmt.Sprintf("%d messages", len(t.sess.Messages))
	}
	n, u, err := t.client.CountTokens(ctx, t.f.model, text)
	if err != nil {
		return err
	}
	t.o.stats("tokens", t.f.model, u, 0, false, "")
	fmt.Fprintf(t.o.stderr, "%s\n", t.o.dim(fmt.Sprintf("%d tokens · %s", n, label)))
	return nil
}

// expandModel accepts the short names people actually type.
func expandModel(name string) string {
	switch strings.ToLower(name) {
	case "flash", "f":
		return deepseek.ModelFlash
	case "pro", "p":
		return deepseek.ModelPro
	}
	return name
}

func plural(n int, unit string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, unit)
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// replDocs is the interactive face of `deepseek docs`. It is defined in
// docs.go, next to the index it searches.
