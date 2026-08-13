// Package cli assembles the deepseek command tree.
//
// Layout mirrors the API: one command per endpoint, named after what the
// endpoint does rather than after its path. Commands own their flags and
// share exactly two things — the Options below, and the output contract
// documented in output.go.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/thevibeworks/deepseek-cli/internal/deepseek"
)

// Options are the global flags, resolved once and handed to every command.
type Options struct {
	APIKey   string
	BaseURL  string
	JSON     bool
	JQ       string
	Timeout  time.Duration
	Verbose  int
	NoStats  bool
	NoLedger bool

	stdout    io.Writer
	stderr    io.Writer
	stderrTTY bool
}

// client builds an API client from the resolved options.
func (o *Options) client() (*deepseek.Client, error) {
	key, base, free, err := o.resolveAuth()
	if err != nil {
		return nil, err
	}
	c := deepseek.New(key, base, o.Timeout)
	if o.Verbose > 0 {
		c.Verbose = o.stderr
		// -vv adds bodies. They are off by default because a chat body is
		// the user's prompt, and prompts end up in scrollback and CI logs.
		c.VerboseBody = o.Verbose > 1
	}
	c.UserAgent = "deepseek-cli/" + Version
	if free != nil {
		// Worth saying out loud exactly once per run: this request is
		// leaving the machine by a different road than the user's own key
		// would have taken.
		o.verbosef("free tier via %s (subject %s)", free.BaseURL, free.Subject)
	}
	return c, nil
}

// resolveAuth decides which credential this run uses, and where it goes.
//
// A real key always wins. The free tier is the fallback for someone who
// has not got one yet, never an upgrade over one they have — silently
// routing a paying user's prompts through our relay would be a
// surprising thing for a CLI to do.
func (o *Options) resolveAuth() (key, base string, free *deepseek.FreeTier, err error) {
	key, err = deepseek.ResolveKey(o.APIKey)
	if err == nil {
		return key, o.baseURL(), nil, nil
	}
	if !errors.Is(err, deepseek.ErrNoKey) {
		return "", "", nil, err
	}

	// An explicitly chosen base URL is not somewhere to send a credential
	// minted for our gateway. Someone running their own gateway points
	// DEEPSEEK_FREE_URL at it and enrols there instead.
	if o.baseURL() != "" {
		return "", "", nil, err
	}
	f, ok := deepseek.LoadFree()
	if !ok {
		return "", "", nil, err
	}
	if time.Since(f.Enrolled) > freeRenewAfter {
		if renewed := o.renewFree(f); renewed != nil {
			f = renewed
		}
	}
	return f.Token, f.BaseURL, f, nil
}

// freeRenewAfter is the age at which an enrolment is quietly renewed.
// The hosted gateway expires tokens after 7 days; renewing at 6 means a
// regular user never sees the expiry error at all.
const freeRenewAfter = 6 * 24 * time.Hour

// renewFree re-runs the enrolment — about a second of CPU — and saves
// the fresh token. Failure is not an error: the old token is kept, and
// if it has actually expired the gateway's own 401 says what to do.
func (o *Options) renewFree(old *deepseek.FreeTier) *deepseek.FreeTier {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	f, err := deepseek.NewFreeGateway(old.BaseURL, 30*time.Second).Enrol(ctx, nil)
	if err != nil {
		return nil
	}
	o.verbosef("free-tier enrolment renewed (subject %s)", f.Subject)
	if err := f.Save(); err != nil {
		o.verbosef("could not save the renewed enrolment: %v", err)
	}
	return f
}

// usingFree reports whether this run would go through the free tier,
// without building a client. Commands use it to phrase their output for
// someone who has no key at all.
func (o *Options) usingFree() *deepseek.FreeTier {
	_, _, free, err := o.resolveAuth()
	if err != nil {
		return nil
	}
	return free
}

func (o *Options) baseURL() string {
	if o.BaseURL != "" {
		return o.BaseURL
	}
	return os.Getenv("DEEPSEEK_BASE_URL")
}

func (o *Options) verbosef(format string, args ...any) {
	if o.Verbose > 0 {
		fmt.Fprintf(o.stderr, o.dim("» "+format)+"\n", args...)
	}
}

// Version is set at build time via -ldflags.
var Version = "dev"

// aliases are the extra names the binary answers to, installed as
// symlinks beside it. `deepseek` is eight characters to type at a prompt
// many times a day; `ds` is two.
var aliases = []string{"deepseek", "ds", "dscli"}

// invokedAs returns the name the binary was actually called by, so usage
// lines say `ds ...` when the user typed `ds`. Anything unrecognised —
// including a test binary — falls back to the canonical name.
func invokedAs() string {
	base := filepath.Base(os.Args[0])
	if runtime.GOOS == "windows" {
		base = strings.TrimSuffix(base, ".exe")
	}
	for _, name := range aliases {
		if base == name {
			return base
		}
	}
	return "deepseek"
}

// Execute runs the CLI and returns the process exit code.
func Execute(version string) int {
	Version = version

	opts := &Options{
		stdout:    os.Stdout,
		stderr:    os.Stderr,
		stderrTTY: isTTY(os.Stderr),
	}

	ctx, stop := interruptContext()
	defer stop()

	root := newRootCmd(opts, version)
	root.Use = invokedAs() + " [command]"
	if err := root.ExecuteContext(ctx); err != nil {
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			fmt.Fprintln(os.Stderr, opts.dim("interrupted"))
			return 130 // 128 + SIGINT, the shell convention
		}
		// Cobra has already printed usage errors it recognises; everything
		// else is ours to report, with the API's own hint attached.
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		return exitCodeOf(err)
	}
	return deepseek.ExitOK
}

// interruptContext cancels on SIGINT or SIGTERM so an in-flight request
// unwinds cleanly: a partially streamed answer stays on screen and the
// usage line still prints.
//
// Catching a signal means taking responsibility for still dying. If the
// work does not stop — blocked on a read, or on a write to a pipe nobody
// is draining — a caught SIGTERM would leave the process unkillable by
// `timeout` or `kill`, which is worse than not catching it at all. So the
// second signal, or two seconds, force-exits regardless.
//
// The exception is an interactive session, which holds the signal for the
// duration of a turn — see holdInterrupt.
func interruptContext() (context.Context, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)

	go func() {
		for {
			<-ch
			if h := interruptHolder.Load(); h != nil {
				// A REPL is mid-answer. Abandon that answer, keep the
				// process: returning to the prompt is the whole point of
				// an interactive session.
				(*h)()
				continue
			}
			cancel()
			select {
			case <-ch:
			case <-time.After(2 * time.Second):
			}
			os.Exit(130) // 128 + SIGINT, the shell convention
		}
	}()

	return ctx, func() {
		signal.Stop(ch)
		cancel()
	}
}

// interruptHolder is the interactive loop's claim on SIGINT, set only
// while a turn is in flight.
var interruptHolder atomic.Pointer[func()]

// holdInterrupt redirects the next SIGINT to cancel, instead of killing
// the process, until the returned function is called. Used by the REPL
// around each turn so ^C abandons one answer rather than the session.
func holdInterrupt(cancel func()) (release func()) {
	interruptHolder.Store(&cancel)
	return func() { interruptHolder.Store(nil) }
}

// exitCodeOf lets a command name its own exit code, falling back to the
// classification of the API error behind it.
func exitCodeOf(err error) int {
	var coder interface{ ExitCode() int }
	if errors.As(err, &coder) {
		return coder.ExitCode()
	}
	return deepseek.ExitCode(err)
}

func newRootCmd(opts *Options, version string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "deepseek",
		Short: "The DeepSeek API from the command line",
		Long: strings.TrimSpace(`
DeepSeek serves the same two models through four different wire formats.
This talks to all of them, keeps the multi-turn bookkeeping straight, and
tells you what each call cost.

  deepseek chat "explain this diff" --file diff.patch
  deepseek chat -c "now write the commit message"
  deepseek anthropic "hello"          # the format Claude Code speaks
  deepseek respond "hello"            # the format Codex speaks
  deepseek usage --since 7d

stdout is the answer; reasoning, usage and errors go to stderr.`),
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version,
	}

	f := cmd.PersistentFlags()
	f.StringVar(&opts.APIKey, "api-key", "", "API key (default $DEEPSEEK_API_KEY, then "+deepseek.KeyFile()+")")
	f.StringVar(&opts.BaseURL, "base-url", "", "API base URL (default $DEEPSEEK_BASE_URL, then "+deepseek.DefaultBaseURL+")")
	f.BoolVar(&opts.JSON, "json", false, "print the raw API response instead of the text")
	f.StringVar(&opts.JQ, "jq", "", "filter the raw API response through a jq expression")
	f.DurationVar(&opts.Timeout, "timeout", 10*time.Minute, "request timeout (the API holds a connection up to 10m before inference starts)")
	f.CountVarP(&opts.Verbose, "verbose", "v", "log HTTP to stderr; -vv includes request and response bodies")
	f.BoolVar(&opts.NoStats, "no-stats", false, "suppress the token/cost line on stderr")
	f.BoolVar(&opts.NoLedger, "no-ledger", false, "do not record this call in the usage ledger")

	cmd.AddCommand(
		newChatCmd(opts),
		newAnthropicCmd(opts),
		newRespondCmd(opts),
		newFIMCmd(opts),
		newTokensCmd(opts),
		newModelsCmd(opts),
		newBalanceCmd(opts),
		newPricingCmd(opts),
		newUsageCmd(opts),
		newSessionCmd(opts),
		newDocsCmd(opts),
		newStatusCmd(opts),
		newCheckCmd(opts),
		newFreeCmd(opts),
		newRawCmd(opts),
	)
	cmd.SetVersionTemplate("deepseek {{.Version}}\n")
	return cmd
}
