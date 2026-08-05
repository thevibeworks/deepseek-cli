package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/thevibeworks/deepseek-cli/internal/deepseek"
	"github.com/thevibeworks/deepseek-cli/internal/docs"
)

// `deepseek docs` — the CLI reading DeepSeek's own documentation.
//
// The point is not to be a documentation browser. It is that the tool
// which talks to an API should be able to answer questions about that
// API, from the vendor's own words, without a browser and without a
// search engine in between. `docs ask` closes the loop: the corpus goes
// to DeepSeek, DeepSeek answers questions about DeepSeek, and every claim
// carries the upstream URL it came from.
//
// That also makes it the honest demo of this CLI's cost accounting. The
// same doc pages lead every request, so the second question about the
// same area hits DeepSeek's context cache, and the usage line shows it.

// corpusURL is the mirror `docs sync` pulls from — the same repository
// the embedded snapshot is built from, so a sync is a newer copy of the
// same thing rather than a different source.
//
// This is the branch tarball rather than a release asset, because it
// always exists: no release pipeline to keep green, nothing to go stale
// between a docs change and a docs release. It carries both locales and
// the loader ignores the one it does not want, which costs about a
// megabyte on a command nobody runs in a loop.
const corpusURL = "https://github.com/thevibeworks/deepseek-docs/archive/refs/heads/main.tar.gz"

func newDocsCmd(o *Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "docs",
		Short: "Search, read and ask questions about DeepSeek's own API docs",
		Long: strings.TrimSpace(`
DeepSeek's API documentation, inside this binary.

Every page of api-docs.deepseek.com, plus the FAQ — which lives outside
that site as a JSON blob inside a JavaScript bundle and is not otherwise
readable as text. Roughly 85KB compressed, so it answers offline.

  deepseek docs search "context cache"
  deepseek docs show guides/thinking_mode
  deepseek docs ask "does FIM support thinking mode?"
  deepseek docs sync            # refresh from the mirror

A snapshot ages, so every command here prints how old it is and each
page carries the upstream URL it was converted from.`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDocsList(o)
		},
	}
	cmd.AddCommand(
		newDocsSearchCmd(o),
		newDocsShowCmd(o),
		newDocsAskCmd(o),
		newDocsChangelogCmd(o),
		newDocsSyncCmd(o),
	)
	return cmd
}

func runDocsList(o *Options) error {
	c, err := docs.Load()
	if err != nil {
		return err
	}
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PAGE\tTITLE")
	for i := range c.Pages {
		fmt.Fprintf(w, "%s\t%s\n", c.Pages[i].Path, c.Pages[i].Title)
	}
	w.Flush()
	fmt.Fprintf(&b, "\n%d pages · %s", len(c.Pages), corpusAge(c))
	return o.emitValue(c.Pages, b.String())
}

func newDocsSearchCmd(o *Options) *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Find the pages that cover something",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := docs.Load()
			if err != nil {
				return err
			}
			query := strings.Join(args, " ")
			hits := c.Search(query, limit)
			if len(hits) == 0 {
				return fmt.Errorf("nothing in the docs matches %q\n  Every term has to appear on the page; try fewer words", query)
			}

			var b strings.Builder
			for _, h := range hits {
				fmt.Fprintf(&b, "%s\n  %s\n", h.Path, h.Title)
				if h.Snippet != "" {
					fmt.Fprintf(&b, "  %s\n", h.Snippet)
				}
				b.WriteString("\n")
			}
			fmt.Fprintf(&b, "%s · read one with: deepseek docs show %s", corpusAge(c), hits[0].Path)
			return o.emitValue(hits, b.String())
		},
	}
	cmd.Flags().IntVarP(&limit, "limit", "n", 8, "how many results")
	return cmd
}

func newDocsShowCmd(o *Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <page>",
		Short: "Print a documentation page",
		Long: strings.TrimSpace(`
Print one page as markdown. The path may be given in full
(guides/thinking_mode) or by its last element (thinking_mode) when that
is unambiguous.`),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := docs.Load()
			if err != nil {
				return err
			}
			p, err := c.Get(args[0])
			if err != nil {
				return err
			}
			// The upstream URL goes to stderr, not into the markdown:
			// stdout stays the document, so it can be piped into a file
			// or another tool unchanged.
			fmt.Fprintln(o.stderr, o.dim(p.Source+" · fetched "+p.Fetched))
			return o.emitValue(p, pageMarkdown(p))
		},
	}
}

// pageMarkdown renders a page for stdout. The mirror's own H1 is already
// the first line of the body, so prepending the frontmatter title would
// print the heading twice — which it did, in v0.3.0.
func pageMarkdown(p *docs.Page) string {
	body := strings.TrimSpace(p.Body)
	if strings.HasPrefix(body, "# ") {
		return body
	}
	return "# " + p.Title + "\n\n" + body
}

func newDocsAskCmd(o *Options) *cobra.Command {
	var (
		model   string
		pages   int
		show    bool
		maxCtx  int
		think   string
		effort  string
		verbose bool
	)
	cmd := &cobra.Command{
		Use:   "ask <question>",
		Short: "Ask DeepSeek about the DeepSeek API, from the official docs",
		Long: strings.TrimSpace(`
Answer a question about the DeepSeek API using DeepSeek, reading only the
official documentation carried in this binary.

The relevant pages are selected locally, sent whole as context, and the
model is told to answer from them and cite the page. What comes back is
grounded in the vendor's own words rather than in what the model happens
to remember, and every claim can be checked against a URL.

  deepseek docs ask "how do I turn thinking off?"
  deepseek docs ask "what does insufficient_system_resource mean?"
  deepseek docs ask "which formats support web search?" --show-sources

Cheap by design: the same pages lead every request, so a follow-up
question about the same area hits the context cache. The usage line
shows how much of it was cached.`),
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDocsAsk(cmd, o, docsAskOpts{
				question: strings.Join(args, " "),
				model:    model, pages: pages, showSources: show,
				maxContext: maxCtx, think: think, effort: effort, verbose: verbose,
			})
		},
	}
	fl := cmd.Flags()
	fl.StringVarP(&model, "model", "m", deepseek.ModelFlash, "model to answer with")
	fl.IntVarP(&pages, "pages", "n", 4, "how many documentation pages to send")
	fl.BoolVar(&show, "show-sources", false, "list the pages sent before answering")
	fl.IntVar(&maxCtx, "max-context", 60_000, "cap on documentation bytes sent")
	fl.StringVar(&think, "think", "", "thinking mode: on or off (default: the API's own, on)")
	fl.StringVarP(&effort, "effort", "e", "", "reasoning effort: low, high, or max")
	fl.BoolVarP(&verbose, "reasoning", "r", false, "show the chain of thought")
	return cmd
}

type docsAskOpts struct {
	question    string
	model       string
	pages       int
	showSources bool
	maxContext  int
	think       string
	effort      string
	verbose     bool
}

// docsSystemPrompt is deliberately strict about the two failure modes
// that make a documentation assistant worse than useless: answering from
// the model's own memory of an API that has since changed, and answering
// confidently when the docs simply do not say.
const docsSystemPrompt = `You are answering questions about the DeepSeek API using the official documentation supplied below.

Rules:
- Answer only from the supplied documentation. Do not use prior knowledge about DeepSeek or any other API.
- If the documentation does not answer the question, say so plainly and name the closest thing it does cover. Do not guess.
- Cite the page you used by its path, like (guides/thinking_mode), after the claim it supports.
- Quote exact parameter names, values and limits rather than paraphrasing them.
- Be brief. A developer asked a question at a terminal; answer it and stop.

The documentation follows.`

func runDocsAsk(cmd *cobra.Command, o *Options, a docsAskOpts) error {
	c, err := docs.Load()
	if err != nil {
		return err
	}

	hits := c.Search(a.question, a.pages)
	if len(hits) == 0 {
		// Falling back to the whole corpus would send half a megabyte to
		// answer a typo. Better to say the search found nothing.
		return fmt.Errorf("no documentation page matches %q\n"+
			"  Try naming a parameter or an endpoint: deepseek docs ask \"what is reasoning_effort\"\n"+
			"  Or browse: deepseek docs search %s", a.question, strings.Fields(a.question)[0])
	}
	context, used := docs.Context(hits, a.maxContext)

	if a.showSources {
		for _, h := range used {
			fmt.Fprintf(o.stderr, "%s\n", o.dim("· "+h.Path+" — "+h.Source))
		}
	}

	req := &deepseek.ChatRequest{
		Model: a.model,
		Messages: []deepseek.Message{
			// The stable part first, the question last. This is the exact
			// prompt shape the cost page recommends, and the reason a
			// second question about the same area is nearly free.
			{Role: "system", Content: docsSystemPrompt + "\n\n" + context},
			{Role: "user", Content: a.question},
		},
	}
	switch strings.ToLower(a.think) {
	case "":
	case "on", "enabled", "true":
		req.Thinking = &deepseek.Thinking{Type: "enabled"}
	case "off", "disabled", "false":
		req.Thinking = &deepseek.Thinking{Type: "disabled"}
	default:
		return fmt.Errorf("--think takes on or off, not %q", a.think)
	}
	if a.effort != "" {
		if err := validEffort(a.effort); err != nil {
			return err
		}
		req.ReasoningEffort = strings.ToLower(a.effort)
	}

	client, err := o.client()
	if err != nil {
		return err
	}

	start := time.Now()
	stream := !o.JSON && o.JQ == ""
	var resp *deepseek.ChatResponse
	var raw []byte
	if stream {
		resp, err = o.streamChat(ctxOf(cmd), client, req, false, a.verbose && o.stderrTTY)
	} else {
		resp, raw, err = client.Chat(ctxOf(cmd), req, false)
	}
	if err != nil {
		return err
	}
	elapsed := time.Since(start)

	msg, finish := chatMessage(resp)
	if !stream {
		if raw == nil {
			raw = nil
		}
		if err := o.emit(raw, msg.Content); err != nil {
			return err
		}
	}
	warnFinish(o, finish)

	// Say what the answer was built from, always. An answer from a
	// snapshot without its date is a claim about today made from an
	// unknown yesterday.
	sources := make([]string, len(used))
	for i, h := range used {
		sources[i] = h.Path
	}
	fmt.Fprintf(o.stderr, "%s\n", o.dim(fmt.Sprintf("answered from %s · %s",
		strings.Join(sources, ", "), corpusAge(c))))

	o.stats("docs", req.Model, resp.Usage.Normalize(), elapsed, stream, "")
	return nil
}

func newDocsSyncCmd(o *Options) *cobra.Command {
	var (
		url   string
		force bool
	)
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Refresh the documentation from the mirror",
		Long: strings.TrimSpace(`
Download the current corpus and cache it, superseding the snapshot built
into this binary.

DeepSeek ships model and pricing changes faster than this CLI ships
releases, so the embedded copy is a floor, not the truth. The mirror is
rebuilt from api-docs.deepseek.com by an agent running on the DeepSeek
API — see github.com/thevibeworks/deepseek-docs.`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			before, _ := docs.Load()

			o.verbosef("GET %s", url)
			req, err := http.NewRequestWithContext(ctxOf(cmd), "GET", url, nil)
			if err != nil {
				return err
			}
			req.Header.Set("User-Agent", "deepseek-cli/"+Version)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return fmt.Errorf("fetching the corpus: %w", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("fetching the corpus: HTTP %d from %s", resp.StatusCode, url)
			}
			// A corpus is under a megabyte. Anything far larger is not one,
			// and reading it into memory unbounded is how a redirect to the
			// wrong host becomes an out-of-memory kill.
			body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
			if err != nil {
				return fmt.Errorf("reading the corpus: %w", err)
			}

			after, err := docs.Inspect(body)
			if err != nil {
				return err
			}

			// A sync that installs an older corpus than the one already in
			// use is a downgrade, and it happens for a real reason: a
			// binary released the day the mirror updated carries a
			// snapshot newer than the mirror's last commit. Refuse it
			// rather than silently making the docs worse.
			if before != nil && after.Fetched < before.Fetched && !force {
				return fmt.Errorf("the mirror is older than the copy already in use (%s vs %s)\n"+
					"  Nothing was changed. Pass --force to install it anyway",
					after.Fetched, before.Fetched)
			}

			if _, err := docs.SaveCache(body); err != nil {
				return err
			}

			text := fmt.Sprintf("%d pages · fetched %s · cached at %s",
				len(after.Pages), after.Fetched, docs.CachePath())
			if before != nil && before.Fetched == after.Fetched {
				text += "\nsame date as the copy already in use — nothing changed"
			}
			return o.emitValue(map[string]any{
				"pages": len(after.Pages), "fetched": after.Fetched, "path": docs.CachePath(),
			}, text)
		},
	}
	cmd.Flags().StringVar(&url, "url", corpusURL, "where to fetch the corpus from")
	cmd.Flags().BoolVar(&force, "force", false, "install the fetched corpus even if it is older than the one in use")
	return cmd
}

// corpusAge describes how current the documentation is, in the terms a
// reader needs to decide whether to trust it.
func corpusAge(c *docs.Corpus) string {
	origin := "built in"
	if c.Origin != "embedded" {
		origin = "synced"
	}
	age, ok := c.Age()
	if !ok {
		return "docs " + origin
	}
	days := int(age.Hours() / 24)
	switch {
	case days <= 0:
		return fmt.Sprintf("docs %s, fetched today", origin)
	case days == 1:
		return fmt.Sprintf("docs %s, fetched yesterday", origin)
	case days < 30:
		return fmt.Sprintf("docs %s, fetched %d days ago", origin, days)
	default:
		return fmt.Sprintf("docs %s, fetched %s — %d days old, run: deepseek docs sync",
			origin, c.Fetched, days)
	}
}

// replDocs is /docs inside an interactive session: search first, because
// at a conversational prompt the question "where is this documented" is
// usually one step from "read it to me".
func (t *chatTurn) replDocs(ctx context.Context, query string) error {
	c, err := docs.Load()
	if err != nil {
		return err
	}
	hits := c.Search(query, 5)
	if len(hits) == 0 {
		return fmt.Errorf("nothing in the docs matches %q", query)
	}
	for _, h := range hits {
		fmt.Fprintf(t.o.stderr, "%s\n", t.o.dim("  "+h.Path+" — "+h.Title))
	}
	fmt.Fprintf(t.o.stderr, "%s\n", t.o.dim("  "+corpusAge(c)+" · read one: deepseek docs show "+hits[0].Path))
	return nil
}

func newDocsChangelogCmd(o *Options) *cobra.Command {
	var full bool
	cmd := &cobra.Command{
		Use:   "changelog",
		Short: "What DeepSeek has shipped, newest first",
		Long: strings.TrimSpace(`
DeepSeek's own change log and release notes, from the corpus in this
binary — the /updates page plus every /news post, newest first.

Useful for the question this CLI cannot answer on its own: whether the
behaviour you are seeing is new. Read one in full with docs show.`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := docs.Load()
			if err != nil {
				return err
			}
			if full {
				p, err := c.Get("updates")
				if err != nil {
					return err
				}
				return o.emitValue(p, "# "+p.Title+"\n\n"+strings.TrimSpace(p.Body))
			}

			type entry struct {
				Path        string `json:"path"`
				Date        string `json:"date"`
				Title       string `json:"title"`
				Description string `json:"description,omitempty"`
			}
			var entries []entry
			for i := range c.Pages {
				p := &c.Pages[i]
				if !strings.HasPrefix(p.Path, "news/") {
					continue
				}
				entries = append(entries, entry{
					Path: p.Path, Date: newsDate(p.Path), Title: p.Title, Description: p.Description,
				})
			}
			// Newest first. The slug encodes the date, in two different
			// shapes depending on when it was written — see newsDate.
			sort.Slice(entries, func(i, j int) bool { return entries[i].Date > entries[j].Date })

			var b strings.Builder
			for _, e := range entries {
				fmt.Fprintf(&b, "%s  %s\n", e.Date, e.Title)
				if e.Description != "" {
					fmt.Fprintf(&b, "            %s\n", truncate(e.Description, 92))
				}
				fmt.Fprintf(&b, "            deepseek docs show %s\n\n", e.Path)
			}
			fmt.Fprintf(&b, "%d releases · %s · full change log: deepseek docs changelog --full", len(entries), corpusAge(c))
			return o.emitValue(entries, b.String())
		},
	}
	cmd.Flags().BoolVar(&full, "full", false, "print the whole change log page instead of the index")
	return cmd
}

// newsDate recovers a sortable date from a news slug. DeepSeek changed
// the convention partway through: the early posts are news<MMDD> with the
// year implied, the later ones news<YYMMDD>. Six digits are read as
// written; four are 2024, which is when that shape was in use.
func newsDate(path string) string {
	slug := strings.TrimPrefix(path, "news/news")
	switch len(slug) {
	case 6:
		return "20" + slug[0:2] + "-" + slug[2:4] + "-" + slug[4:6]
	case 4:
		return "2024-" + slug[0:2] + "-" + slug[2:4]
	}
	return slug
}

func truncate(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}
