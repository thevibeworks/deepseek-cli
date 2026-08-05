package server

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thevibeworks/deepseek-cli/gateway/internal/quota"
)

// The two halves of the free tier are separate Go modules that share a
// documented wire format and no code. Every other test in this package
// drives the gateway with a client written in this file, which proves
// the gateway agrees with itself. This one runs the actual `deepseek`
// binary against it, which is the only thing that proves the CLI's
// independent implementation — its own proof-of-work solver, its own
// credential storage, its own auth resolution — actually interoperates.
//
// It needs the binary, so `make check` builds it first. Without one the
// test skips rather than failing: a fresh clone should not fail its
// tests for lack of a build artifact.
func findCLI(t *testing.T) string {
	t.Helper()
	if v := os.Getenv("DEEPSEEK_BIN"); v != "" {
		return v
	}
	// gateway/internal/server -> the repository root.
	for _, rel := range []string{"../../../deepseek", "../../../deepseek.exe"} {
		if abs, err := filepath.Abs(rel); err == nil {
			if fi, err := os.Stat(abs); err == nil && !fi.IsDir() {
				return abs
			}
		}
	}
	t.Skip("no deepseek binary to test against; run `make build` first, or set DEEPSEEK_BIN")
	return ""
}

type cliRunner struct {
	bin  string
	home string
	base string
}

func (c *cliRunner) run(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := exec.Command(c.bin, args...)
	// A clean environment. Inheriting DEEPSEEK_API_KEY from a developer's
	// shell would make this test pass by using a real key and never touch
	// the gateway at all.
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + c.home,
		"DEEPSEEK_CONFIG_DIR=" + filepath.Join(c.home, "config"),
		"DEEPSEEK_STATE_DIR=" + filepath.Join(c.home, "state"),
		"DEEPSEEK_FREE_URL=" + c.base,
		"NO_COLOR=1",
	}
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err = cmd.Run()
	return out.String(), errb.String(), err
}

func TestTheCLIEnrolsAndChatsThroughTheGateway(t *testing.T) {
	bin := findCLI(t)

	// `deepseek chat` streams by default, so a stub that only answered in
	// one piece would test the wrong path — and would have been the
	// reason this test first came back with empty output.
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		for _, word := range []string{"the ", "sky ", "is ", "blue"} {
			io.WriteString(w, `data: {"choices":[{"index":0,"delta":{"content":"`+word+`"}}]}`+"\n\n")
			w.(http.Flusher).Flush()
		}
		io.WriteString(w, `data: {"id":"x","object":"chat.completion.chunk","model":"deepseek-v4-flash",`+
			`"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":91,"completion_tokens":17,"total_tokens":108,`+
			`"prompt_cache_hit_tokens":0,"prompt_cache_miss_tokens":91}}`+"\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	})
	h := newHarness(t, up, nil)
	cli := &cliRunner{bin: bin, home: t.TempDir(), base: h.base}

	// 1. Without enrolling, the CLI has no credential — and the error has
	//    to point at the way out.
	_, stderr, err := cli.run(t, "chat", "hi")
	if err == nil {
		t.Fatal("chat succeeded with no key and no enrolment")
	}
	if !strings.Contains(stderr, "deepseek free") {
		t.Errorf("the no-key error does not mention the free tier: %s", stderr)
	}

	// 2. Enrol. This runs the CLI's own proof-of-work solver against a
	//    challenge minted by the real gateway.
	_, stderr, err = cli.run(t, "free")
	if err != nil {
		t.Fatalf("deepseek free: %v\n%s", err, stderr)
	}
	if !strings.Contains(stderr, "Enrolled") {
		t.Errorf("enrolment did not report success: %s", stderr)
	}
	// The disclosure is the consent. It has to actually be printed.
	for _, want := range []string{"relays your prompts", "deepseek-v4-flash", "per day"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("enrolment output is missing %q:\n%s", want, stderr)
		}
	}

	// 3. Chat. No API key anywhere in the environment.
	stdout, stderr, err := cli.run(t, "chat", "why is the sky blue")
	if err != nil {
		t.Fatalf("deepseek chat: %v\n%s", err, stderr)
	}
	if strings.TrimSpace(stdout) != "the sky is blue" {
		t.Errorf("stdout = %q, want the answer and nothing else", stdout)
	}

	// The gateway stamped the subject on the upstream request, and the
	// CLI's token never went any further than the gateway.
	last := up.last(t)
	if last.Headers.Get("Authorization") != "Bearer "+upstreamKey {
		t.Error("upstream did not receive the gateway's key")
	}
	sub, _ := last.Body["user_id"].(string)
	if sub == "" {
		t.Error("upstream received no user_id")
	}

	h.settle(t)
	if st := h.ledger.Status(sub, "anon"); st.Used.InputTokens != 91 || st.Used.OutputTokens != 17 {
		t.Errorf("the CLI's request was charged as %+v, want 91 in / 17 out", st.Used)
	}

	// 4. Quota is visible to the user who spent it.
	stdout, stderr, err = cli.run(t, "free", "status", "--json")
	if err != nil {
		t.Fatalf("deepseek free status: %v\n%s", err, stderr)
	}
	var status struct {
		Enrolled bool `json:"enrolled"`
		Quota    struct {
			Used struct {
				Requests     int `json:"requests"`
				OutputTokens int `json:"output_tokens"`
			} `json:"used"`
			Limits struct {
				Requests int `json:"requests"`
			} `json:"limits"`
		} `json:"quota"`
	}
	if err := json.Unmarshal([]byte(stdout), &status); err != nil {
		t.Fatalf("free status --json is not JSON: %v\n%s", err, stdout)
	}
	if !status.Enrolled || status.Quota.Used.Requests != 1 || status.Quota.Used.OutputTokens != 17 {
		t.Errorf("status does not reflect the request just made: %s", stdout)
	}

	// 5. `deepseek balance` answers from the free tier rather than
	//    erroring or disclosing our account.
	stdout, stderr, err = cli.run(t, "balance", "--json")
	if err != nil {
		t.Fatalf("deepseek balance: %v\n%s", err, stderr)
	}
	if !strings.Contains(stdout, "x_free_tier") {
		t.Errorf("balance did not come from the free tier: %s", stdout)
	}

	// 6. Forgetting puts it back where it started.
	if _, stderr, err = cli.run(t, "free", "off"); err != nil {
		t.Fatalf("deepseek free off: %v\n%s", err, stderr)
	}
	if _, _, err = cli.run(t, "chat", "hi"); err == nil {
		t.Error("chat still worked after the enrolment was forgotten")
	}
}

// A quota refusal has to survive the trip back through the CLI as
// something a human can act on, not as "unexpected response".
func TestTheCLIReportsAQuotaRefusalUsefully(t *testing.T) {
	bin := findCLI(t)

	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, chatReply(5, 5))
	})
	h := newHarness(t, up, func(c *Config, l *quota.Limits) { l.DailyRequests = 1 })
	cli := &cliRunner{bin: bin, home: t.TempDir(), base: h.base}

	if _, stderr, err := cli.run(t, "free"); err != nil {
		t.Fatalf("enrol: %v\n%s", err, stderr)
	}
	if _, stderr, err := cli.run(t, "chat", "one"); err != nil {
		t.Fatalf("first chat: %v\n%s", err, stderr)
	}
	h.settle(t)

	_, stderr, err := cli.run(t, "chat", "two")
	if err == nil {
		t.Fatal("the second chat succeeded against a limit of one")
	}
	if !strings.Contains(stderr, "00:00 UTC") {
		t.Errorf("the refusal does not say when it lifts: %s", stderr)
	}
	if !strings.Contains(stderr, "api_keys") {
		t.Errorf("the refusal does not offer a way around it: %s", stderr)
	}
	// DeepSeek's own advice for a 429 is about account concurrency, which
	// would be nonsense here.
	if strings.Contains(stderr, "2500 flash") {
		t.Errorf("a gateway refusal picked up DeepSeek's unrelated hint: %s", stderr)
	}
	if code := exitCode(err); code != 4 {
		t.Errorf("exit code %d, want 4 (rate limited)", code)
	}
}

func exitCode(err error) int {
	var ee *exec.ExitError
	if asExitError(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

func asExitError(err error, target **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError)
	if ok {
		*target = e
	}
	return ok
}
