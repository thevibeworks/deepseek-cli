package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/thevibeworks/deepseek-cli/internal/deepseek"
	"github.com/thevibeworks/deepseek-cli/internal/ledger"
	"golang.org/x/term"
)

// The output contract, which every command obeys:
//
//	stdout  the answer, and nothing else — the model's text, or with
//	        --json the API's response body byte-for-byte
//	stderr  everything about the answer — reasoning, the usage line,
//	        verbose HTTP, errors
//
// That split is what makes `deepseek chat ... > out.txt` and
// `deepseek chat ... | jq` both do the obvious thing.

// isTTY reports whether f is an interactive terminal.
//
// This asks the kernel rather than checking for a character device: the
// cheap mode check calls /dev/null a terminal, which would paint escape
// codes into `> /dev/null` and misread a redirected stdin.
func isTTY(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

const (
	ansiDim   = "\x1b[2m"
	ansiReset = "\x1b[0m"
)

// dim wraps s in a dim SGR sequence when stderr is a terminal, and leaves
// it alone when it is a pipe or a file. Colour that survives redirection
// is colour in someone's log file.
func (o *Options) dim(s string) string {
	if !o.stderrTTY {
		return s
	}
	return ansiDim + s + ansiReset
}

// emit writes a command's result to stdout in the requested form.
//
// raw is the response exactly as the API sent it and text is the plain
// human rendering. --json prints raw untouched: no wrapper, no injected
// fields, so existing jq recipes written against the OpenAI or Anthropic
// APIs keep working.
func (o *Options) emit(raw []byte, text string) error {
	switch {
	case o.JQ != "":
		return pipeJQ(o.stdout, raw, o.JQ)
	case o.JSON:
		return writeJSON(o.stdout, raw)
	default:
		if text == "" {
			return nil
		}
		_, err := fmt.Fprintln(o.stdout, strings.TrimRight(text, "\n"))
		return err
	}
}

// emitValue is emit for results the CLI computed itself (the usage report,
// the check results) rather than received from the API.
func (o *Options) emitValue(v any, text string) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return o.emit(raw, text)
}

func writeJSON(w io.Writer, raw []byte) error {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		// Not valid JSON — pass the bytes through rather than fail. The
		// API said something; showing it beats hiding it behind a parse
		// error of ours.
		_, err := w.Write(append(bytes.TrimRight(raw, "\n"), '\n'))
		return err
	}
	_, err := w.Write(append(buf.Bytes(), '\n'))
	return err
}

// pipeJQ shells out to jq. Reimplementing a jq engine to save an exec is
// a bad trade: jq is installed wherever people pipe JSON, and the real
// one has no dialect gaps.
func pipeJQ(w io.Writer, raw []byte, expr string) error {
	cmd := exec.Command("jq", expr)
	cmd.Stdin = bytes.NewReader(raw)
	cmd.Stdout = w
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if _, lookErr := exec.LookPath("jq"); lookErr != nil {
			return fmt.Errorf("--jq needs the jq binary on PATH (https://jqlang.org); or use --json and pipe it yourself")
		}
		return err
	}
	return nil
}

// stats prints the one-line accounting for a call and records it in the
// ledger. api is the wire format, requested the model the user asked for.
//
// It is one function because the two things must not drift: what the user
// is told a call cost and what gets written to the ledger are the same
// numbers from the same source.
func (o *Options) stats(api, requested string, u deepseek.Usage, dur time.Duration, stream bool, sessionName string) {
	if u.Empty() {
		return
	}
	if !o.NoLedger {
		if _, err := ledger.Record(api, requested, u, dur, stream, sessionName); err != nil {
			// Bookkeeping must never break the command that produced it.
			o.verbosef("ledger write failed: %v", err)
		}
	}
	if o.NoStats {
		return
	}

	var b strings.Builder
	fmt.Fprintf(&b, "· %s", shortModel(requested))
	fmt.Fprintf(&b, " · %s in", humanTokens(u.InputTokens))
	if u.CacheHitTokens > 0 {
		fmt.Fprintf(&b, " (%.0f%% cached)", u.CacheHitRate()*100)
	}
	fmt.Fprintf(&b, " · %s out", humanTokens(u.OutputTokens))
	if u.ReasoningTokens > 0 {
		fmt.Fprintf(&b, " (%s think)", humanTokens(u.ReasoningTokens))
	}
	if cost, ok := deepseek.Cost(requested, u); ok {
		fmt.Fprintf(&b, " · ~%s", money(cost))
	}
	fmt.Fprintf(&b, " · %s", dur.Round(10*time.Millisecond))

	fmt.Fprintln(o.stderr, o.dim(b.String()))
}

// shortModel trims the common prefix for the status line. The full name
// is always in --json and in the ledger; this is for the human glance.
func shortModel(model string) string {
	resolved := deepseek.ResolveModel(model)
	short := strings.TrimPrefix(resolved, "deepseek-v4-")
	if resolved != model {
		// The Anthropic endpoint remapped a Claude name; show both so the
		// cost is traceable to the model that actually ran.
		return model + "→" + short
	}
	return short
}

// humanTokens renders a token count compactly: 940, 1.2k, 34k, 1.1M.
func humanTokens(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 10_000:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1000), ".0") + "k"
	case n < 1_000_000:
		return fmt.Sprintf("%.0fk", float64(n)/1000)
	default:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1_000_000), ".0") + "M"
	}
}

// money renders a USD figure at a precision that stays useful for the
// very small numbers this API produces. A single flash call can cost a
// few millionths of a dollar; printing $0.00 would say nothing.
func money(usd float64) string {
	switch {
	case usd == 0:
		return "$0"
	case usd < 0.01:
		return fmt.Sprintf("$%.6f", usd)
	case usd < 1:
		return fmt.Sprintf("$%.4f", usd)
	default:
		return fmt.Sprintf("$%.2f", usd)
	}
}
