package docs

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"strings"
	"testing"
)

// The corpus compiled into the binary is the thing most likely to break
// silently: a bad path in the embed directive, a repacking script that
// changes the tar layout, a frontmatter key renamed upstream. None of
// those fail the build.
func TestEmbeddedCorpusLoads(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(c.Pages) < 50 {
		t.Fatalf("corpus has %d pages; the mirror has 60+, so this is truncated", len(c.Pages))
	}
	if c.Fetched == "" {
		t.Error("corpus has no fetch date — every answer built on it would be undateable")
	}

	// Pages the rest of the CLI names directly, and the FAQ, which exists
	// only because it is extracted from a JS bundle.
	for _, want := range []string{
		"index", "updates",
		"guides/thinking_mode", "guides/kv_cache", "guides/tool_calls",
		"api/create-chat-completion", "quick_start/pricing",
		"quick_start/error_codes", "quick_start/rate_limit",
		"faq/category-4",
	} {
		p, err := c.Get(want)
		if err != nil {
			t.Errorf("Get(%q): %v", want, err)
			continue
		}
		if strings.TrimSpace(p.Body) == "" {
			t.Errorf("%s has no body", want)
		}
		if p.Source == "" {
			t.Errorf("%s has no source URL — a mirrored page must say where it came from", want)
		}
	}
}

// Ranking regressions. Both of these shipped as wrong answers from
// `docs ask` before the scoring was fixed, so they are pinned here
// against the real corpus rather than a fixture.
func TestSearchRanking(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	cases := []struct {
		name    string
		query   string
		want    string
		withinN int
		why     string
	}{{
		name:    "partial term coverage still competes",
		query:   "what is the max output token limit for FIM?",
		want:    "guides/fim_completion",
		withinN: 4,
		why:     "this page states the 4K cap but never uses the word 'output'",
	}, {
		name:    "a long page cannot win on raw term counts",
		query:   "when must I send reasoning_content back?",
		want:    "guides/thinking_mode",
		withinN: 1,
		why:     "the 82KB codex page outscored it before length normalisation",
	}, {
		name:    "exact subject wins",
		query:   "context caching",
		want:    "guides/kv_cache",
		withinN: 1,
	}, {
		name:    "the FAQ is reachable",
		query:   "invoice billing",
		want:    "faq/category-4",
		withinN: 2,
		why:     "only the FAQ covers invoices; api-docs does not mention them",
	}, {
		name:    "error codes",
		query:   "402 insufficient balance",
		want:    "quick_start/error_codes",
		withinN: 3,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hits := c.Search(tc.query, 8)
			for i, h := range hits {
				if h.Path == tc.want {
					if i >= tc.withinN {
						t.Errorf("%q ranked %s at %d, want within the top %d. %s",
							tc.query, tc.want, i+1, tc.withinN, tc.why)
					}
					return
				}
			}
			var got []string
			for _, h := range hits {
				got = append(got, h.Path)
			}
			t.Errorf("%q did not return %s at all; got %v. %s", tc.query, tc.want, got, tc.why)
		})
	}
}

func TestSearchEmptyAndStopwordOnlyQueries(t *testing.T) {
	c, _ := Load()
	for _, q := range []string{"", "   ", "the and of", "a"} {
		if hits := c.Search(q, 5); hits != nil {
			t.Errorf("Search(%q) returned %d hits; a query with no real terms should match nothing", q, len(hits))
		}
	}
}

func TestGetAcceptsSuffixAndRejectsAmbiguity(t *testing.T) {
	c, _ := Load()

	// Last element only, when unique.
	if p, err := c.Get("thinking_mode"); err != nil || p.Path != "guides/thinking_mode" {
		t.Errorf("Get(thinking_mode) = %v, %v; want guides/thinking_mode", p, err)
	}
	// A trailing .md is what someone reading a path would type.
	if p, err := c.Get("guides/kv_cache.md"); err != nil || p.Path != "guides/kv_cache" {
		t.Errorf("Get with .md suffix = %v, %v", p, err)
	}
	if _, err := c.Get("no-such-page"); err == nil {
		t.Error("Get on a missing page should error")
	} else if !strings.Contains(err.Error(), "docs search") {
		t.Errorf("a miss should suggest search, got: %v", err)
	}
}

func TestParseFrontmatter(t *testing.T) {
	body := "---\ntitle: \"Thinking Mode\"\ndescription: \"How to switch it\"\n" +
		"source: https://api-docs.deepseek.com/guides/thinking_mode\nfetched: 2026-08-05\n---\n\n# Thinking Mode\n\nBody text.\n"
	path, ok := corpusPath("en/guides/thinking_mode.md")
	if !ok {
		t.Fatal("corpusPath rejected a normal entry")
	}
	p := newPage(path, body)

	if p.Path != "guides/thinking_mode" {
		t.Errorf("Path = %q", p.Path)
	}
	if p.Title != "Thinking Mode" {
		t.Errorf("Title = %q", p.Title)
	}
	if p.Fetched != "2026-08-05" {
		t.Errorf("Fetched = %q", p.Fetched)
	}
	if strings.Contains(p.Body, "title:") {
		t.Error("frontmatter leaked into the body")
	}
	if !strings.Contains(p.Body, "Body text.") {
		t.Error("body was lost")
	}
}

// A page without frontmatter must still load. The mirror always writes
// it, but a corpus is an external input and a panic here would take the
// whole CLI down on an unrelated command.
func TestParseWithoutFrontmatter(t *testing.T) {
	path, _ := corpusPath("en/loose.md")
	p := newPage(path, "# Just a heading\n")
	if p.Path != "loose" || p.Title != "loose" {
		t.Errorf("got path=%q title=%q", p.Path, p.Title)
	}
	if p.Body == "" {
		t.Error("body dropped")
	}
}

func TestContextRespectsCap(t *testing.T) {
	c, _ := Load()
	hits := c.Search("thinking mode effort", 6)
	if len(hits) < 2 {
		t.Fatalf("need several hits for this test, got %d", len(hits))
	}

	text, used := Context(hits, 2000)
	if len(used) == 0 {
		t.Fatal("the cap dropped everything; at least the best hit must always go in")
	}
	if len(used) >= len(hits) {
		t.Errorf("a 2000-byte cap kept all %d pages; the cap does nothing", len(hits))
	}
	if used[0].Path != hits[0].Path {
		t.Errorf("the cap dropped the best hit (%s) and kept %s", hits[0].Path, used[0].Path)
	}
	for _, h := range used {
		if !strings.Contains(text, h.Source) {
			t.Errorf("context omitted the source URL for %s — answers could not cite it", h.Path)
		}
	}
}

func TestSaveCacheRejectsRubbish(t *testing.T) {
	t.Setenv("DEEPSEEK_STATE_DIR", t.TempDir())

	if _, err := SaveCache([]byte("not a gzip stream")); err == nil {
		t.Error("SaveCache accepted a non-archive; a bad sync must not replace working docs")
	}

	// A well-formed but empty archive is the other way a sync goes wrong.
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	tw.Close()
	zw.Close()
	if _, err := SaveCache(buf.Bytes()); err == nil {
		t.Error("SaveCache accepted an empty corpus")
	}
}

// A sync may be pointed at GitHub's repository tarball, which nests the
// same files under a commit-named directory and carries a second locale.
// Both layouts have to resolve to the same page paths, and zh-cn must
// never leak in — an answer half in Chinese from an English question
// would look like a model failure and be a loader bug.
func TestCorpusPathAcceptsBothArchiveLayouts(t *testing.T) {
	cases := []struct {
		entry string
		want  string
		ok    bool
	}{
		{"en/guides/thinking_mode.md", "guides/thinking_mode", true},
		{"en/index.md", "index", true},
		{"deepseek-docs-main/content/en/guides/thinking_mode.md", "guides/thinking_mode", true},
		{"deepseek-docs-abc1234/content/en/faq/category-4.md", "faq/category-4", true},
		{"zh-cn/guides/thinking_mode.md", "", false},
		{"deepseek-docs-main/content/zh-cn/index.md", "", false},
		{"deepseek-docs-main/README.md", "", false},
		{"en/guides/", "", false},
		{"deepseek-docs-main/llms-full.txt", "", false},
	}
	for _, tc := range cases {
		got, ok := corpusPath(tc.entry)
		if ok != tc.ok || got != tc.want {
			t.Errorf("corpusPath(%q) = %q, %v; want %q, %v", tc.entry, got, ok, tc.want, tc.ok)
		}
	}
}
