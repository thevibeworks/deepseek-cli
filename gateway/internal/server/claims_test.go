package server

import (
	"io/fs"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/thevibeworks/deepseek-cli/gateway/internal/policy"
)

// The pages this gateway serves about itself name the model it serves
// and say what it carries. Both are decided in code, and on 2026-09-10
// both changed upstream while every test stayed green: the model got a
// new name, and web_search stopped running. These pin the copy to the
// code so the next such change turns this red instead of the page stale.

// defaultModel is DSGATE_MODEL's default, read from the binary's source
// so the test and the flag cannot disagree.
func defaultModel(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile("../../cmd/dsgate/main.go")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`env\("DSGATE_MODEL", "([^"]+)"\)`).FindSubmatch(src)
	if m == nil {
		t.Fatal(`main.go no longer reads env("DSGATE_MODEL", "...") — update this test with it`)
	}
	return string(m[1])
}

func TestDefaultModelIsTheNameUpstreamServes(t *testing.T) {
	model := defaultModel(t)
	if canon := policy.Canonical(model); canon != model {
		t.Errorf("DSGATE_MODEL defaults to %s, a retired name; upstream serves it as %s", model, canon)
	}
}

// Every place a page names the served model, it names the default. The
// patterns are the phrasings the pages use to say "this is what runs
// here"; a retired name kept as history does not match them.
func TestPagesNameTheServedModel(t *testing.T) {
	model := defaultModel(t)
	claims := map[string][]*regexp.Regexp{
		"index.html": {
			regexp.MustCompile(`gateway to (deepseek-[a-z0-9.-]+)`),
			regexp.MustCompile(`<strong>(deepseek-[a-z0-9.-]+)</strong>`),
		},
		"pages/terms.html": {
			regexp.MustCompile(`the <code>(deepseek-[a-z0-9.-]+)</code> model only`),
		},
		"pages/economics.html": {
			regexp.MustCompile(`runs on <code>(deepseek-[a-z0-9.-]+)</code>`),
		},
	}
	for page, res := range claims {
		body, err := fs.ReadFile(webFS, "web/"+page)
		if err != nil {
			t.Fatalf("%s: %v", page, err)
		}
		n := 0
		for _, re := range res {
			for _, m := range re.FindAllStringSubmatch(string(body), -1) {
				n++
				if m[1] != model {
					t.Errorf("%s names %s as the served model; the gateway serves %s", page, m[1], model)
				}
			}
		}
		if n == 0 {
			t.Errorf("%s no longer names the served model in a form this test reads", page)
		}
	}
}

// DeepSeek removed web_search on 2026-09-10 and the gateway refuses it.
// A page may say so, and only so: every paragraph or list item that
// mentions web search has to say it was removed.
func TestNoPagePromisesWebSearch(t *testing.T) {
	blocks := regexp.MustCompile(`(?s)<(p|li|td)\b.*?</(p|li|td)>`)
	fs.WalkDir(webFS, "web", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".html") {
			return err
		}
		body, _ := fs.ReadFile(webFS, path)
		for _, b := range blocks.FindAllString(string(body), -1) {
			low := strings.ToLower(b)
			if (strings.Contains(low, "web_search") || strings.Contains(low, "web search")) && !strings.Contains(low, "removed") {
				t.Errorf("%s promises web search, which DeepSeek removed on 2026-09-10: %.160s", path, b)
			}
		}
		return nil
	})
}
