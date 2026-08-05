package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/thevibeworks/deepseek-cli/internal/deepseek"
)

type tokensFlags struct {
	model   string
	effort  string
	files   []string
	offline bool
	each    bool
}

// tokenCount is one measured input, and the JSON shape of this command.
type tokenCount struct {
	Source string `json:"source"`
	Tokens int    `json:"tokens"`
	Chars  int    `json:"chars"`
	Bytes  int    `json:"bytes"`
}

type tokensReport struct {
	Method string       `json:"method"` // "api" (exact) or "estimate"
	Model  string       `json:"model"`
	Total  tokenCount   `json:"total"`
	Items  []tokenCount `json:"items,omitempty"`
	// effortLabel names the effort the surcharge was computed for, since
	// the surcharge is not the same at every level.
	effortLabel string
	// Chat is what one chat request carrying this text would be billed for
	// on the input side, before the model generates anything.
	Chat struct {
		Input         int `json:"input"`
		InputThinking int `json:"input_thinking"`
	} `json:"chat"`
}

func newTokensCmd(o *Options) *cobra.Command {
	var f tokensFlags

	cmd := &cobra.Command{
		Use:   "tokens [text...]",
		Short: "Count tokens exactly, using the model's own tokenizer",
		Long: strings.TrimSpace(`
Count the tokens in text, a file, or a pipe.

DeepSeek publishes no count-tokens endpoint and no Go tokenizer — only a
Python demo and two rules of thumb. So this asks the model. The FIM
endpoint takes a raw prompt with no chat template around it and reports
prompt_tokens for exactly the bytes sent, plus one BOS token; subtract
the one and the count is exact for the tokenizer that will bill you.

That measurement is a real request: your text is sent to DeepSeek and
billed as input at the cache-miss rate, the same as sending it would
have been. The cost is printed on stderr with every count. Use --offline
for a free local estimate from DeepSeek's published character ratios,
which is an upper bound and says so.

  deepseek tokens "why is the sky blue"
  deepseek tokens --file main.go --file main_test.go --each
  git diff | deepseek tokens
  deepseek tokens --offline --file huge.log`),
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTokens(cmd, o, &f, args)
		},
	}

	fl := cmd.Flags()
	fl.StringVarP(&f.model, "model", "m", deepseek.ModelFlash, "model whose tokenizer to use")
	fl.StringArrayVarP(&f.files, "file", "f", nil, "count a file's contents (repeatable)")
	fl.BoolVar(&f.offline, "offline", false, "estimate locally from published character ratios; no request, no cost")
	fl.StringVarP(&f.effort, "effort", "e", "", "effort level to price the thinking template at")
	fl.BoolVar(&f.each, "each", false, "break the total down per input")

	return cmd
}

func runTokens(cmd *cobra.Command, o *Options, f *tokensFlags, args []string) error {
	inputs, err := collectTokenInputs(args, f.files)
	if err != nil {
		return err
	}
	if len(inputs) == 0 {
		return fmt.Errorf("nothing to count — pass text, --file, or pipe it in\n  Example: deepseek tokens \"why is the sky blue\"")
	}

	rep := &tokensReport{Method: "api", Model: f.model, effortLabel: f.effort}
	if rep.effortLabel == "" {
		rep.effortLabel = "default"
	}
	if f.offline {
		rep.Method = "estimate"
		rep.Model = ""
	}

	client, err := o.client()
	if err != nil && !f.offline {
		return err
	}

	start := time.Now()
	var billed deepseek.Usage
	for _, in := range inputs {
		item := tokenCount{
			Source: in.name,
			Chars:  utf8.RuneCountInString(in.text),
			Bytes:  len(in.text),
		}
		if f.offline {
			item.Tokens = deepseek.EstimateTokens(in.text)
		} else {
			n, u, err := client.CountTokens(ctxOf(cmd), f.model, in.text)
			if err != nil {
				return err
			}
			item.Tokens = n
			billed.InputTokens += u.InputTokens
			billed.CacheHitTokens += u.CacheHitTokens
			billed.CacheMissTokens += u.CacheMissTokens
			billed.OutputTokens += u.OutputTokens
			billed.TotalTokens += u.TotalTokens
		}
		rep.Items = append(rep.Items, item)
		rep.Total.Tokens += item.Tokens
		rep.Total.Chars += item.Chars
		rep.Total.Bytes += item.Bytes
	}
	rep.Total.Source = "total"
	rep.Chat.Input = rep.Total.Tokens + deepseek.ChatEnvelopeTokens
	rep.Chat.InputThinking = rep.Chat.Input + deepseek.ThinkingTemplate(f.model, f.effort)

	if err := o.emitValue(rep, renderTokens(rep, f.each || len(rep.Items) > 1)); err != nil {
		return err
	}

	// The count is data; what it cost to obtain is status. Recorded under
	// its own api name so `usage --json` can tell measurement calls apart
	// from work.
	o.stats("tokens", f.model, billed, time.Since(start), false, "")
	if f.offline {
		fmt.Fprintln(o.stderr, o.dim("estimate from DeepSeek's published character ratios — an upper bound, not the tokenizer"))
	}
	return nil
}

func renderTokens(rep *tokensReport, breakdown bool) string {
	var b strings.Builder
	if breakdown && len(rep.Items) > 1 {
		w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "TOKENS\tCHARS\tCHARS/TOK\tSOURCE")
		for _, it := range rep.Items {
			fmt.Fprintf(w, "%d\t%d\t%s\t%s\n", it.Tokens, it.Chars, ratio(it.Chars, it.Tokens), it.Source)
		}
		fmt.Fprintf(w, "%d\t%d\t%s\t%s\n",
			rep.Total.Tokens, rep.Total.Chars, ratio(rep.Total.Chars, rep.Total.Tokens), "total")
		w.Flush()
	} else {
		fmt.Fprintf(&b, "%d tokens · %d chars · %s chars/token\n",
			rep.Total.Tokens, rep.Total.Chars, ratio(rep.Total.Chars, rep.Total.Tokens))
	}

	// The number people actually want next: what a request carrying this
	// costs on the input side before the model writes a word.
	surcharge := rep.Chat.InputThinking - rep.Chat.Input
	fmt.Fprintf(&b, "\nas a chat request: %d in (+%d envelope)", rep.Chat.Input, deepseek.ChatEnvelopeTokens)
	if surcharge > 0 {
		fmt.Fprintf(&b, ", %d with thinking at %s effort (+%d template)",
			rep.Chat.InputThinking, rep.effortLabel, surcharge)
	} else {
		fmt.Fprintf(&b, " — %s effort adds no thinking template", rep.effortLabel)
	}

	if p, ok := deepseek.PriceFor(deepseek.ModelFlash); ok {
		miss := float64(rep.Chat.Input) * p.CacheMissInput / 1_000_000
		hit := float64(rep.Chat.Input) * p.CacheHitInput / 1_000_000
		fmt.Fprintf(&b, "\nflash input cost: %s uncached, %s fully cached", money(miss), money(hit))
	}
	return b.String()
}

func ratio(chars, tokens int) string {
	if tokens == 0 {
		return "-"
	}
	return fmt.Sprintf("%.2f", float64(chars)/float64(tokens))
}

// tokenInput is one thing to count, carrying the label it will be
// reported under.
type tokenInput struct {
	name string
	text string
}

// collectTokenInputs gathers arguments, files and stdin as separate
// items. Unlike readPrompt, which fuses them into one message with file
// fences, counting wants each source kept whole and unadorned: the answer
// must be the tokens in *your* file, not in a wrapper this CLI invented.
func collectTokenInputs(args []string, files []string) ([]tokenInput, error) {
	var out []tokenInput

	if text := strings.Join(args, " "); strings.TrimSpace(text) != "" {
		out = append(out, tokenInput{name: "argument", text: text})
	}
	for _, path := range files {
		text, err := readFileRaw(path)
		if err != nil {
			return nil, err
		}
		out = append(out, tokenInput{name: path, text: text})
	}
	if text, ok, err := readPipedStdin(args); err != nil {
		return nil, err
	} else if ok {
		out = append(out, tokenInput{name: "stdin", text: text})
	}
	return out, nil
}
