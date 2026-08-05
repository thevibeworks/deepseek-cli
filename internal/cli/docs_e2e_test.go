package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// End-to-end tests for the commands that read DeepSeek's own docs, the
// tokenizer, and the status probe. Same harness as e2e_test.go: real
// command tree, real flag parsing, stub API.

func TestDocsListsAndShowsPages(t *testing.T) {
	noAPI := func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("docs must answer offline; it called %s", r.URL.Path)
	}

	got := runCLI(t, noAPI, "docs")
	if got.err != nil {
		t.Fatalf("docs: %v", got.err)
	}
	for _, want := range []string{"guides/thinking_mode", "faq/category-4", "pages"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("docs listing missing %q", want)
		}
	}

	show := runCLI(t, noAPI, "docs", "show", "guides/kv_cache")
	if show.err != nil {
		t.Fatalf("docs show: %v", show.err)
	}
	if !strings.Contains(show.stdout, "Context Caching") {
		t.Error("docs show printed no page content")
	}
	// The source URL is status, not data: stdout must stay pipeable as
	// the document itself.
	if !strings.Contains(show.stderr, "api-docs.deepseek.com") {
		t.Error("docs show did not cite its upstream URL on stderr")
	}
	if strings.Contains(show.stdout, "api-docs.deepseek.com/guides/kv_cache\n") &&
		strings.HasPrefix(show.stdout, "source:") {
		t.Error("the source URL leaked into stdout")
	}
}

func TestDocsSearchRanksAndSuggests(t *testing.T) {
	noAPI := func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("docs search must answer offline; it called %s", r.URL.Path)
	}

	got := runCLI(t, noAPI, "docs", "search", "context cache")
	if got.err != nil {
		t.Fatalf("docs search: %v", got.err)
	}
	if !strings.Contains(got.stdout, "guides/kv_cache") {
		t.Errorf("search for the cache guide did not find it:\n%s", got.stdout)
	}
	if !strings.Contains(got.stdout, "docs show") {
		t.Error("search results should say how to read one")
	}

	miss := runCLI(t, noAPI, "docs", "search", "zzzznotarealterm")
	if miss.err == nil {
		t.Error("a search matching nothing should be an error, not an empty success")
	}
}

func TestDocsSearchJSONShape(t *testing.T) {
	got := runCLI(t, func(w http.ResponseWriter, r *http.Request) {}, "docs", "search", "thinking mode", "--json")
	if got.err != nil {
		t.Fatalf("docs search --json: %v", got.err)
	}
	var hits []struct {
		Path   string `json:"path"`
		Title  string `json:"title"`
		Source string `json:"source"`
		Score  int    `json:"score"`
	}
	if err := json.Unmarshal([]byte(got.stdout), &hits); err != nil {
		t.Fatalf("--json is not valid JSON: %v\n%s", err, got.stdout)
	}
	if len(hits) == 0 {
		t.Fatal("no hits")
	}
	if hits[0].Path == "" || hits[0].Source == "" || hits[0].Score == 0 {
		t.Errorf("incomplete hit: %+v", hits[0])
	}
	for i := 1; i < len(hits); i++ {
		if hits[i].Score > hits[i-1].Score {
			t.Errorf("results are not sorted by score: %d after %d", hits[i].Score, hits[i-1].Score)
		}
	}
}

// docs ask must send the documentation as a system message ahead of the
// question. That order is not cosmetic: it is what lets DeepSeek's
// context cache pay for the next question about the same area.
func TestDocsAskSendsDocsBeforeTheQuestion(t *testing.T) {
	got := runCLI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(chatOK))
	}, "docs", "ask", "how do I turn thinking off?", "--json")

	if got.err != nil {
		t.Fatalf("docs ask: %v", got.err)
	}
	if len(got.requests) != 1 {
		t.Fatalf("sent %d requests, want 1", len(got.requests))
	}

	msgs, _ := got.requests[0]["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("sent %d messages, want a system prompt then the question", len(msgs))
	}
	system, _ := msgs[0].(map[string]any)
	user, _ := msgs[1].(map[string]any)
	if system["role"] != "system" || user["role"] != "user" {
		t.Fatalf("roles are %v then %v, want system then user", system["role"], user["role"])
	}
	content, _ := system["content"].(string)
	if !strings.Contains(content, "Answer only from the supplied documentation") {
		t.Error("the grounding instruction is missing")
	}
	if !strings.Contains(content, "thinking") {
		t.Error("no documentation was retrieved into the system prompt")
	}
	if !strings.Contains(content, "source: https://api-docs.deepseek.com") {
		t.Error("retrieved pages carry no source URL, so answers cannot cite one")
	}
	if user["content"] != "how do I turn thinking off?" {
		t.Errorf("question = %v", user["content"])
	}

	// Provenance must always be reported, on stderr, with the age.
	if !strings.Contains(got.stderr, "answered from") || !strings.Contains(got.stderr, "docs built in") {
		t.Errorf("no provenance line on stderr:\n%s", got.stderr)
	}
}

func TestDocsAskWithNoMatchDoesNotCallTheAPI(t *testing.T) {
	got := runCLI(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("a question matching no page must not be billed")
	}, "docs", "ask", "zzzznotarealterm")
	if got.err == nil {
		t.Error("want an error when nothing matches")
	}
}

func TestDocsChangelogIsNewestFirst(t *testing.T) {
	got := runCLI(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("changelog must answer offline; it called %s", r.URL.Path)
	}, "docs", "changelog", "--json")
	if got.err != nil {
		t.Fatalf("docs changelog: %v", got.err)
	}
	var entries []struct {
		Path  string `json:"path"`
		Date  string `json:"date"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal([]byte(got.stdout), &entries); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if len(entries) < 5 {
		t.Fatalf("only %d releases; the corpus has more", len(entries))
	}
	for i := 1; i < len(entries); i++ {
		if entries[i].Date > entries[i-1].Date {
			t.Errorf("not newest first: %s before %s", entries[i-1].Date, entries[i].Date)
		}
	}
	// Both slug conventions must decode. The early posts are news<MMDD>,
	// the later ones news<YYMMDD>; a wrong parse silently reorders the
	// whole list.
	for _, e := range entries {
		if len(e.Date) != 10 || strings.Count(e.Date, "-") != 2 {
			t.Errorf("%s produced date %q, not YYYY-MM-DD", e.Path, e.Date)
		}
	}
}

func TestNewsDate(t *testing.T) {
	for path, want := range map[string]string{
		"news/news260424": "2026-04-24",
		"news/news251201": "2025-12-01",
		"news/news0802":   "2024-08-02",
		"news/news1226":   "2024-12-26",
	} {
		if got := newsDate(path); got != want {
			t.Errorf("newsDate(%q) = %q, want %q", path, got, want)
		}
	}
}

// The tokenizer counts via the FIM endpoint and subtracts the BOS token.
// The command must send the text unwrapped: a count of a file the CLI
// fenced and labelled would be a count of something the user never sends.
func TestTokensCountsViaFIMWithoutWrapping(t *testing.T) {
	got := runCLI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"c","object":"text_completion","model":"deepseek-v4-flash",
		  "choices":[{"text":"","index":0,"finish_reason":"length"}],
		  "usage":{"prompt_tokens":11,"completion_tokens":1,"total_tokens":12,
		           "prompt_cache_hit_tokens":0,"prompt_cache_miss_tokens":11}}`))
	}, "tokens", "The quick brown fox jumps over the lazy dog.")

	if got.err != nil {
		t.Fatalf("tokens: %v", got.err)
	}
	if len(got.paths) != 1 || got.paths[0] != "/beta/completions" {
		t.Fatalf("counted via %v, want the FIM path", got.paths)
	}
	if prompt := got.requests[0]["prompt"]; prompt != "The quick brown fox jumps over the lazy dog." {
		t.Errorf("prompt = %v; the text must go over the wire unwrapped", prompt)
	}
	if !strings.Contains(got.stdout, "10 tokens") {
		t.Errorf("want 10 tokens (11 reported minus the BOS), got:\n%s", got.stdout)
	}
	// The measurement costs money and must say so.
	if !strings.Contains(got.stderr, "·") {
		t.Error("no usage line for a billed measurement")
	}
}

func TestTokensOfflineMakesNoRequest(t *testing.T) {
	got := runCLI(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("--offline must not call the API")
	}, "tokens", "--offline", "hello world")
	if got.err != nil {
		t.Fatalf("tokens --offline: %v", got.err)
	}
	if !strings.Contains(got.stderr, "upper bound") {
		t.Error("an estimate must be labelled as one")
	}
}

func TestTokensReportsTheEffortSurcharge(t *testing.T) {
	fim := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"c","object":"text_completion","model":"deepseek-v4-flash",
		  "choices":[{"text":"","index":0,"finish_reason":"length"}],
		  "usage":{"prompt_tokens":6,"completion_tokens":1,"total_tokens":7,
		           "prompt_cache_hit_tokens":0,"prompt_cache_miss_tokens":6}}`))
	}

	// Default effort on flash carries the 79-token template.
	def := runCLI(t, fim, "tokens", "hello", "--json", "--no-stats")
	var rep struct {
		Chat struct {
			Input         int `json:"input"`
			InputThinking int `json:"input_thinking"`
		} `json:"chat"`
	}
	if err := json.Unmarshal([]byte(def.stdout), &rep); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, def.stdout)
	}
	if rep.Chat.InputThinking-rep.Chat.Input != 79 {
		t.Errorf("default surcharge = %d, measured 79", rep.Chat.InputThinking-rep.Chat.Input)
	}

	// At low effort there is none at all — the finding this command exists
	// to make visible.
	low := runCLI(t, fim, "tokens", "hello", "-e", "low", "--no-stats")
	if !strings.Contains(low.stdout, "adds no thinking template") {
		t.Errorf("low effort should report no surcharge:\n%s", low.stdout)
	}
}

func TestStatusReportsReachabilityAndLinksTheIncidentPage(t *testing.T) {
	got := runCLI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/models":
			w.Write([]byte(`{"object":"list","data":[
			  {"id":"deepseek-v4-flash","object":"model","owned_by":"deepseek"},
			  {"id":"deepseek-v4-pro","object":"model","owned_by":"deepseek"}]}`))
		case "/user/balance":
			w.Write([]byte(`{"is_available":true,"balance_infos":[
			  {"currency":"CNY","total_balance":"18.48","granted_balance":"0","topped_up_balance":"18.48"}]}`))
		default:
			t.Errorf("status called %s; it should cost no tokens", r.URL.Path)
		}
	}, "status")

	if got.err != nil {
		t.Fatalf("status: %v", got.err)
	}
	if !strings.HasPrefix(got.stdout, "ok") {
		t.Errorf("want an ok verdict first, got:\n%s", got.stdout)
	}
	for _, want := range []string{"flash", "pro", "18.48 CNY", "status.deepseek.com"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("status output missing %q:\n%s", want, got.stdout)
		}
	}
	// Neither call generates tokens, so nothing should reach the ledger.
	if strings.Contains(got.stderr, "in ·") {
		t.Errorf("status billed something:\n%s", got.stderr)
	}
}

func TestStatusFailsLoudlyWhenUnreachable(t *testing.T) {
	got := runCLI(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"Authentication Fails","type":"authentication_error"}}`))
	}, "status")

	if got.err == nil {
		t.Fatal("status should fail when the API rejects the key")
	}
	if exitCodeOf(got.err) != 2 {
		t.Errorf("exit code = %d, want 2 for auth", exitCodeOf(got.err))
	}
	if !strings.Contains(got.stdout, "unreachable") || !strings.Contains(got.stdout, "status.deepseek.com") {
		t.Errorf("a failure should still point at the incident page:\n%s", got.stdout)
	}
}

// v0.3.0 shipped `docs show` printing the heading twice: the mirror's own
// H1 is already the first line of the body, and the frontmatter title was
// being prepended to it.
func TestDocsShowDoesNotRepeatTheHeading(t *testing.T) {
	got := runCLI(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("docs show must answer offline; it called %s", r.URL.Path)
	}, "docs", "show", "guides/kv_cache")
	if got.err != nil {
		t.Fatalf("docs show: %v", got.err)
	}
	if n := strings.Count(got.stdout, "# Context Caching"); n != 1 {
		t.Errorf("the heading appears %d times, want 1:\n%s", n, firstLines(got.stdout, 6))
	}
}

func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
