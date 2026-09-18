package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/thevibeworks/deepseek-cli/internal/deepseek"
)

// The docs an agent reads before it runs this tool. They restate things
// the code decides — the default model, which flags work — and nothing
// else keeps them honest: on 2026-09-10 the default changed name and a
// flag stopped doing anything, and every check stayed green while the
// docs went on saying the old thing.
var claimDocs = []string{"README.md", "AGENTS.md", "skill/SKILL.md", "llms.txt", "site/llms.txt"}

func readClaimDoc(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// Every sentence that names the default model names the one the flags
// actually default to. Every occurrence, not "somewhere": a stale second
// copy must not hide behind a fresh first one.
func TestPublishedDefaultModelIsTheFlagDefault(t *testing.T) {
	claims := []*regexp.Regexp{
		regexp.MustCompile("`(deepseek-[a-z0-9.-]+)` \\(default\\)"),
		regexp.MustCompile("default \\(`(deepseek-[a-z0-9.-]+)`"),
	}
	found := map[string]int{}
	for _, doc := range claimDocs {
		text := readClaimDoc(t, doc)
		for _, re := range claims {
			for _, m := range re.FindAllStringSubmatch(text, -1) {
				found[doc]++
				if m[1] != deepseek.ModelFlash {
					t.Errorf("%s says the default model is %s; the flags default to %s", doc, m[1], deepseek.ModelFlash)
				}
			}
		}
	}
	// The two agent-facing docs state it; if they stop, the claim moved
	// somewhere this test cannot see, which is its own kind of drift.
	for _, doc := range []string{"AGENTS.md", "skill/SKILL.md"} {
		if found[doc] == 0 {
			t.Errorf("%s no longer states the default model in a form this test reads", doc)
		}
	}
}

// DeepSeek removed server-side web_search on 2026-09-10 and `respond
// --web-search` now exits 1. A doc that still shows it as a command to run
// is teaching a failure.
func TestNoDocOffersWebSearchAsACommand(t *testing.T) {
	run := regexp.MustCompile(`^\s*(\$ )?(deepseek|ds|dscli) respond\b.*--web-search`)
	for _, doc := range claimDocs {
		for i, line := range strings.Split(readClaimDoc(t, doc), "\n") {
			if run.MatchString(line) {
				t.Errorf("%s:%d offers --web-search, which DeepSeek removed on 2026-09-10: %s", doc, i+1, strings.TrimSpace(line))
			}
		}
	}
}
