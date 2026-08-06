package policy

import (
	"encoding/json"
	"strings"
	"testing"
)

func limits() Limits { return Limits{MaxTokens: 4096, Model: "deepseek-v4-flash"} }

func apply(t *testing.T, routeKey, body string) map[string]any {
	t.Helper()
	method, path, _ := strings.Cut(routeKey, " ")
	route, ok := Lookup(method, path)
	if !ok {
		t.Fatalf("no route for %s", routeKey)
	}
	d, err := Apply(route, []byte(body), "SUBJECT", limits())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(d.Body, &out); err != nil {
		t.Fatalf("rewritten body is not JSON: %v", err)
	}
	return out
}

func TestRoutesTheCLIActuallyUses(t *testing.T) {
	// Every path the deepseek CLI sends, plus the /v1 prefix that
	// OpenAI-compatible clients add on their own.
	for _, tc := range []struct{ method, path, want string }{
		{"POST", "/chat/completions", "chat"},
		{"POST", "/v1/chat/completions", "chat"},
		{"POST", "/beta/chat/completions", "chat"},
		{"POST", "/beta/completions", "fim"},
		{"POST", "/anthropic/v1/messages", "anthropic"},
		{"POST", "/responses", "responses"},
		{"GET", "/models", "models"},
		{"GET", "/v1/models", "models"},
	} {
		r, ok := Lookup(tc.method, tc.path)
		if !ok {
			t.Errorf("%s %s is not routed; the CLI sends it", tc.method, tc.path)
			continue
		}
		if r.Name != tc.want {
			t.Errorf("%s %s routed to %q, want %q", tc.method, tc.path, r.Name, tc.want)
		}
	}
}

func TestUnknownEndpointsAreRefused(t *testing.T) {
	// An endpoint we have not reasoned about is one whose cost we cannot
	// bound — including the one that would report our own balance.
	for _, tc := range [][2]string{
		{"GET", "/user/balance"},
		{"POST", "/admin/anything"},
		{"DELETE", "/chat/completions"},
		{"GET", "/chat/completions"},
		{"POST", "/../models"},
	} {
		if _, ok := Lookup(tc[0], tc[1]); ok {
			t.Errorf("%s %s is routed and should not be", tc[0], tc[1])
		}
	}
}

// The gateway serves one model. Asking for the expensive one is refused
// rather than silently downgraded: a user who asked for pro and got
// flash without being told would blame the model for the difference.
func TestProIsRefusedNotDowngraded(t *testing.T) {
	route, _ := Lookup("POST", "/chat/completions")
	_, err := Apply(route, []byte(`{"model":"deepseek-v4-pro","messages":[]}`), "S", limits())
	if err == nil {
		t.Fatal("a pro request was accepted by the free tier")
	}
	var rej *Reject
	if !asReject(err, &rej) {
		t.Fatalf("error is %T, want *Reject", err)
	}
	if !strings.Contains(rej.Message, "deepseek-v4-pro") {
		t.Errorf("refusal does not name the model asked for: %q", rej.Message)
	}
	if rej.Hint == "" {
		t.Error("refusal offers no way forward")
	}
}

func TestModelIsPinnedEvenWhenAbsent(t *testing.T) {
	got := apply(t, "POST /chat/completions", `{"messages":[]}`)
	if got["model"] != "deepseek-v4-flash" {
		t.Errorf("model = %v, want the free model pinned in", got["model"])
	}
}

// Claude names are remapped server-side by the Anthropic endpoint. The
// ones that land on flash have to keep working, or `deepseek anthropic`
// breaks against the free tier for no reason.
func TestClaudeNamesThatMapToFlashAreAllowed(t *testing.T) {
	route, _ := Lookup("POST", "/anthropic/v1/messages")
	for _, model := range []string{"claude-sonnet-4-5", "claude-haiku-4-5"} {
		if _, err := Apply(route, []byte(`{"model":"`+model+`","messages":[]}`), "S", limits()); err != nil {
			t.Errorf("%s was refused: %v", model, err)
		}
	}
	if _, err := Apply(route, []byte(`{"model":"claude-opus-4-1","messages":[]}`), "S", limits()); err == nil {
		t.Error("claude-opus maps to pro and should be refused")
	}
}

func TestOutputCapIsClamped(t *testing.T) {
	cases := []struct {
		name, route, body, field string
		want                     float64
	}{
		{"chat asks too much", "POST /chat/completions", `{"max_tokens":100000}`, "max_tokens", 4096},
		{"chat asks less", "POST /chat/completions", `{"max_tokens":100}`, "max_tokens", 100},
		{"chat asks nothing", "POST /chat/completions", `{}`, "max_tokens", 4096},
		{"chat asks zero", "POST /chat/completions", `{"max_tokens":0}`, "max_tokens", 4096},
		{"responses uses its own field", "POST /responses", `{"max_output_tokens":99999}`, "max_output_tokens", 4096},
		{"anthropic", "POST /anthropic/v1/messages", `{"max_tokens":50000}`, "max_tokens", 4096},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := apply(t, tc.route, tc.body)
			n, ok := got[tc.field].(float64)
			if !ok {
				t.Fatalf("%s = %v (%T)", tc.field, got[tc.field], got[tc.field])
			}
			if n != tc.want {
				t.Errorf("%s = %v, want %v", tc.field, n, tc.want)
			}
		})
	}
}

// user_id is not a nicety. DeepSeek documents it as the mechanism for one
// account fronting many end users: content-safety attribution, KV cache
// isolation between strangers, and per-user scheduling. Each format keeps
// it somewhere different.
func TestSubjectIsStampedWhereEachFormatReadsIt(t *testing.T) {
	t.Run("chat uses user_id", func(t *testing.T) {
		got := apply(t, "POST /chat/completions", `{"messages":[]}`)
		if got["user_id"] != "SUBJECT" {
			t.Errorf("user_id = %v", got["user_id"])
		}
	})

	t.Run("anthropic uses metadata.user_id", func(t *testing.T) {
		got := apply(t, "POST /anthropic/v1/messages", `{"messages":[]}`)
		meta, _ := got["metadata"].(map[string]any)
		if meta == nil || meta["user_id"] != "SUBJECT" {
			t.Errorf("metadata = %v", got["metadata"])
		}
		if _, leaked := got["user_id"]; leaked {
			t.Error("a top-level user_id was left on an Anthropic request")
		}
	})

	t.Run("responses uses user", func(t *testing.T) {
		// Measured 2026-08-05: the Responses endpoint echoes "user" back
		// and drops "user_id" and "metadata". Both names are set because
		// DeepSeek documents neither for this path.
		got := apply(t, "POST /responses", `{"input":"hi"}`)
		if got["user"] != "SUBJECT" {
			t.Errorf("user = %v", got["user"])
		}
		if got["user_id"] != "SUBJECT" {
			t.Errorf("user_id = %v", got["user_id"])
		}
	})
}

// A caller that could choose its own user_id could aim at another
// subject's KV cache namespace or poison their content-safety record.
func TestClientSuppliedIdentityIsOverwritten(t *testing.T) {
	got := apply(t, "POST /chat/completions", `{"user_id":"somebody-else","user":"also-them"}`)
	if got["user_id"] != "SUBJECT" {
		t.Errorf("client's user_id survived: %v", got["user_id"])
	}
	if _, ok := got["user"]; ok {
		t.Errorf("client's user survived on a chat request: %v", got["user"])
	}

	anth := apply(t, "POST /anthropic/v1/messages", `{"metadata":{"user_id":"somebody-else","note":"keep"}}`)
	meta := anth["metadata"].(map[string]any)
	if meta["user_id"] != "SUBJECT" {
		t.Errorf("client's metadata.user_id survived: %v", meta["user_id"])
	}
	if meta["note"] != "keep" {
		t.Error("rewriting the identity dropped an unrelated metadata field")
	}
}

// n and best_of multiply the cost of a request that already passed the
// size check, which would let one admitted request cost many.
func TestFanOutIsRefused(t *testing.T) {
	route, _ := Lookup("POST", "/chat/completions")
	for _, body := range []string{`{"n":8}`, `{"best_of":4}`} {
		if _, err := Apply(route, []byte(body), "S", limits()); err == nil {
			t.Errorf("%s was accepted", body)
		}
	}
	// n=1 is what every ordinary client sends.
	if _, err := Apply(route, []byte(`{"n":1}`), "S", limits()); err != nil {
		t.Errorf("n=1 was refused: %v", err)
	}
}

// Everything not named in this package must survive untouched. The
// gateway's promise is that it forwards your request, not its idea of
// your request.
func TestUnrelatedFieldsAreForwardedVerbatim(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"hi"}],"temperature":0.7,"top_p":0.95,` +
		`"stop":["\n\n"],"tools":[{"type":"function"}],"stream":true,"frequency_penalty":-0.5,"seed":1234567890}`
	got := apply(t, "POST /chat/completions", body)

	for _, field := range []string{"messages", "temperature", "top_p", "stop", "tools", "frequency_penalty", "seed"} {
		if _, ok := got[field]; !ok {
			t.Errorf("%s was dropped", field)
		}
	}
	if got["temperature"] != 0.7 {
		t.Errorf("temperature = %v, want 0.7", got["temperature"])
	}
	if got["seed"] != float64(1234567890) {
		t.Errorf("seed = %v; a large integer was reshaped in transit", got["seed"])
	}
}

func TestStreamIsDetected(t *testing.T) {
	route, _ := Lookup("POST", "/chat/completions")
	for body, want := range map[string]bool{
		`{"stream":true}`:  true,
		`{"stream":false}`: false,
		`{}`:               false,
	} {
		d, err := Apply(route, []byte(body), "S", limits())
		if err != nil {
			t.Fatal(err)
		}
		if d.Stream != want {
			t.Errorf("Apply(%s).Stream = %v, want %v", body, d.Stream, want)
		}
	}
}

func TestGarbageBodyIsRejected(t *testing.T) {
	route, _ := Lookup("POST", "/chat/completions")
	for _, body := range []string{``, `not json`, `[]`, `null`, `"a string"`} {
		if _, err := Apply(route, []byte(body), "S", limits()); err == nil {
			t.Errorf("Apply accepted %q", body)
		}
	}
}

// GET /models has no body to rewrite and must not acquire one.
func TestBodylessRouteIsUntouched(t *testing.T) {
	route, _ := Lookup("GET", "/models")
	d, err := Apply(route, nil, "S", limits())
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Body) != 0 {
		t.Errorf("a GET grew a body: %q", d.Body)
	}
}

func asReject(err error, target **Reject) bool {
	r, ok := err.(*Reject)
	if ok {
		*target = r
	}
	return ok
}

// Server-side tools perform billed work that never appears in the usage
// object — outside every ceiling this gateway enforces. Client function
// tools only declare a schema and stay allowed.
func TestServerSideToolsAreRefused(t *testing.T) {
	route, _ := Lookup("POST", "/responses")
	lim := Limits{MaxTokens: 100, Model: "deepseek-v4-flash"}

	_, err := Apply(route, []byte(`{"input":"hi","tools":[{"type":"web_search"}]}`), "sub", lim)
	if err == nil {
		t.Fatal("web_search passed policy; its per-search cost has no ceiling")
	}

	if _, err := Apply(route, []byte(`{"input":"hi","tools":[{"type":"function","name":"f"}]}`), "sub", lim); err != nil {
		t.Errorf("a client function tool was refused: %v", err)
	}

	// The other formats have no server-side tools; their tools stay open.
	chat, _ := Lookup("POST", "/chat/completions")
	if _, err := Apply(chat, []byte(`{"messages":[],"tools":[{"type":"function"}]}`), "sub", lim); err != nil {
		t.Errorf("chat function tools were refused: %v", err)
	}
}
