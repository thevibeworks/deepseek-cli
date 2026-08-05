package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thevibeworks/deepseek-cli/internal/deepseek"
)

func TestHumanTokens(t *testing.T) {
	cases := map[int]string{
		0: "0", 940: "940", 1000: "1k", 1200: "1.2k",
		9999: "10k", 34_000: "34k", 999_000: "999k",
		1_000_000: "1M", 1_100_000: "1.1M",
	}
	for in, want := range cases {
		if got := humanTokens(in); got != want {
			t.Errorf("humanTokens(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestMoneyKeepsSmallFiguresLegible(t *testing.T) {
	// A single flash call costs millionths of a dollar. Printing $0.00
	// would say nothing at all.
	if got := money(0.0000025); got != "$0.000003" {
		t.Errorf("got %q, want six decimals for a sub-cent figure", got)
	}
	if got := money(0.42); got != "$0.4200" {
		t.Errorf("got %q", got)
	}
	if got := money(12.5); got != "$12.50" {
		t.Errorf("got %q", got)
	}
	if got := money(0); got != "$0" {
		t.Errorf("got %q", got)
	}
}

func TestShortModelShowsAnthropicRemapping(t *testing.T) {
	// When the endpoint silently swaps the model, the status line has to
	// show both names or the cost looks unattributable.
	if got := shortModel("deepseek-v4-flash"); got != "flash" {
		t.Errorf("got %q, want %q", got, "flash")
	}
	if got := shortModel("claude-opus-4-1"); got != "claude-opus-4-1→pro" {
		t.Errorf("got %q, want both names", got)
	}
}

func TestParseToolChoice(t *testing.T) {
	for _, mode := range []string{"none", "auto", "required"} {
		got, err := parseToolChoice(mode)
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		if got != mode {
			t.Errorf("got %v, want the bare string %q", got, mode)
		}
	}

	obj, err := parseToolChoice(`{"type":"function","function":{"name":"f"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := obj.(json.RawMessage); !ok {
		t.Errorf("a JSON object should pass through as raw JSON, got %T", obj)
	}

	if _, err := parseToolChoice("maybe"); err == nil {
		t.Error("an unknown mode should be rejected before it reaches the API")
	}
}

func TestParseAnthropicToolChoiceTranslatesRequired(t *testing.T) {
	// The two formats spell the same intent differently. Making the user
	// remember which is which would be a papercut for no gain.
	got, err := parseAnthropicToolChoice("required")
	if err != nil {
		t.Fatal(err)
	}
	m, ok := got.(map[string]string)
	if !ok || m["type"] != "any" {
		t.Errorf("got %v, want {type: any}", got)
	}

	if _, err := parseAnthropicToolChoice("nope"); err == nil {
		t.Error("an unknown mode should be rejected")
	}
}

func TestLoadToolsAcceptsBothShapes(t *testing.T) {
	dir := t.TempDir()

	wrapped := filepath.Join(dir, "wrapped.json")
	write(t, wrapped, `{"type":"function","function":{"name":"a","description":"A","parameters":{"type":"object"}}}`)

	bare := filepath.Join(dir, "bare.json")
	write(t, bare, `{"name":"b","description":"B","parameters":{"type":"object"}}`)

	got, err := loadTools([]string{"@" + wrapped, "@" + bare})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("loaded %d tools, want 2", len(got))
	}
	for i, want := range []string{"a", "b"} {
		if got[i].Function.Name != want {
			t.Errorf("tool %d name = %q, want %q", i, got[i].Function.Name, want)
		}
		if got[i].Type != "function" {
			t.Errorf("tool %d type = %q, want function", i, got[i].Type)
		}
	}
}

func TestLoadToolsAcceptsAnthropicInputSchema(t *testing.T) {
	// One tool file has to work against every format this CLI speaks, so
	// the Anthropic spelling of the schema field is accepted too.
	dir := t.TempDir()
	path := filepath.Join(dir, "anthropic.json")
	write(t, path, `{"name":"c","description":"C","input_schema":{"type":"object","properties":{"x":{"type":"string"}}}}`)

	got, err := loadTools([]string{"@" + path})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("loaded %d tools, want 1", len(got))
	}
	if !strings.Contains(string(got[0].Function.Parameters), "properties") {
		t.Errorf("input_schema was not carried over: %s", got[0].Function.Parameters)
	}
}

func TestLoadToolsAcceptsAnArray(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "many.json")
	write(t, path, `[{"name":"a"},{"name":"b"},{"name":"c"}]`)

	got, err := loadTools([]string{"@" + path})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("loaded %d tools, want 3", len(got))
	}
}

func TestLoadToolsRejectsMalformedJSONWithTheFileName(t *testing.T) {
	// The API's error for a bad schema is confusing; catching it here with
	// the filename attached is far more useful.
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.json")
	write(t, path, `{"name": "a",`)

	_, err := loadTools([]string{"@" + path})
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "broken.json") {
		t.Errorf("error should name the file, got %q", err)
	}
}

func TestReadMaybeFile(t *testing.T) {
	if got, err := readMaybeFile("inline text"); err != nil || got != "inline text" {
		t.Errorf("got %q, %v", got, err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "sys.txt")
	write(t, path, "you are a helpful assistant\n")

	got, err := readMaybeFile("@" + path)
	if err != nil {
		t.Fatal(err)
	}
	if got != "you are a helpful assistant" {
		t.Errorf("got %q — the trailing newline should be trimmed", got)
	}

	if _, err := readMaybeFile("@/no/such/file"); err == nil {
		t.Error("a missing file should error")
	}
}

func TestReadFileBlockNamesTheFile(t *testing.T) {
	// The model is told what it is reading rather than handed anonymous
	// text, and the extension picks the fence language.
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	write(t, path, "package main\n")

	got, err := readFileBlock(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "main.go:\n```go\n") {
		t.Errorf("block = %q", got)
	}
	if !strings.HasSuffix(got, "\n```") {
		t.Errorf("block is not fenced closed: %q", got)
	}
}

func TestPrependSystemReplacesRatherThanStacks(t *testing.T) {
	// --system on a continued conversation means what it says; two system
	// messages would leave the model reading contradictory instructions.
	msgs := prependSystem(nil, "first")
	msgs = append(msgs, msg("user", "q"))
	msgs = prependSystem(msgs, "second")

	var systems int
	for _, m := range msgs {
		if m.Role == "system" {
			systems++
			if m.Content != "second" {
				t.Errorf("system content = %q, want the newest", m.Content)
			}
		}
	}
	if systems != 1 {
		t.Errorf("found %d system messages, want 1", systems)
	}
	if msgs[0].Role != "system" {
		t.Errorf("the system message should stay first, got %q", msgs[0].Role)
	}
}

func TestOpenSessionOnlyWhenAsked(t *testing.T) {
	t.Setenv("DEEPSEEK_STATE_DIR", t.TempDir())

	// A plain one-shot call neither reads nor writes history.
	s, name, err := openSession("", false)
	if err != nil {
		t.Fatal(err)
	}
	if s != nil || name != "" {
		t.Errorf("got session %v named %q, want none", s, name)
	}

	// --continue resolves to the default session.
	s, name, err = openSession("", true)
	if err != nil {
		t.Fatal(err)
	}
	if s == nil || name != "last" {
		t.Errorf("got %v named %q, want the default session", s, name)
	}

	// An explicit name wins.
	if _, name, err = openSession("work", true); err != nil || name != "work" {
		t.Errorf("name = %q, err = %v", name, err)
	}
}

func TestBuildTextConfig(t *testing.T) {
	if got, err := buildTextConfig(&respondFlags{}); err != nil || got != nil {
		t.Errorf("no flags should mean no text config, got %v %v", got, err)
	}

	got, err := buildTextConfig(&respondFlags{format: "json_object"})
	if err != nil || got.Format.Type != "json_object" {
		t.Errorf("got %+v, %v", got, err)
	}

	// A schema implies json_schema without the user saying so twice.
	got, err = buildTextConfig(&respondFlags{schema: `{"type":"object"}`, schemaName: "out"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Format.Type != "json_schema" || got.Format.Name != "out" {
		t.Errorf("got %+v", got.Format)
	}

	// Asking for json_schema without one is a mistake worth naming.
	if _, err := buildTextConfig(&respondFlags{format: "json_schema"}); err == nil {
		t.Error("want an error telling the user to pass --schema")
	}
	if _, err := buildTextConfig(&respondFlags{format: "yaml"}); err == nil {
		t.Error("an unknown format should be rejected")
	}
}

func TestValidEffort(t *testing.T) {
	for _, ok := range []string{"low", "high", "max", "LOW", "medium", "xhigh", "minimal"} {
		if err := validEffort(ok); err != nil {
			t.Errorf("validEffort(%q) = %v", ok, err)
		}
	}
	if err := validEffort("turbo"); err == nil {
		t.Error("an unknown effort should be rejected before the request")
	}
}

func TestWriteJSONPassesThroughNonJSON(t *testing.T) {
	// The API said something; showing it beats hiding it behind a parse
	// error of ours.
	var b strings.Builder
	if err := writeJSON(&b, []byte("upstream proxy error")); err != nil {
		t.Fatal(err)
	}
	if got := b.String(); got != "upstream proxy error\n" {
		t.Errorf("got %q", got)
	}
}

func TestWriteJSONIndents(t *testing.T) {
	var b strings.Builder
	if err := writeJSON(&b, []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "\n  \"a\": 1") {
		t.Errorf("got %q, want indented output", b.String())
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func msg(role, content string) deepseek.Message {
	return deepseek.Message{Role: role, Content: content}
}

func TestBriefErrorIsOneLine(t *testing.T) {
	// Table rows carry the API's message; the how-to-fix hint is printed
	// once at the end, not stacked under every failed row.
	err := &deepseek.APIError{StatusCode: 401, Message: "Authentication Fails"}
	if strings.Contains(err.Error(), "\n") == false {
		t.Fatal("fixture should be a multi-line error with a hint")
	}

	got := briefError(err)
	if strings.Contains(got, "\n") {
		t.Errorf("briefError returned multiple lines: %q", got)
	}
	if !strings.Contains(got, "401") || !strings.Contains(got, "Authentication Fails") {
		t.Errorf("briefError dropped the useful part: %q", got)
	}
	if strings.Contains(got, "platform.deepseek.com") {
		t.Errorf("briefError should not repeat the hint: %q", got)
	}
}

func TestBriefErrorOnPlainError(t *testing.T) {
	if got := briefError(errors.New("line one\nline two")); got != "line one" {
		t.Errorf("got %q, want the first line", got)
	}
}
