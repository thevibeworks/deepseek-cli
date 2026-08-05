package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/thevibeworks/deepseek-cli/internal/deepseek"
)

// runFree drives the real command tree with no API key anywhere, against
// a stub gateway. The point is the resolution path: a run with no key
// has to find the free-tier enrolment on its own, and a run with a key
// has to ignore it.
func runFree(t *testing.T, gateway string, argv ...string) capture {
	t.Helper()
	var out, errBuf bytes.Buffer
	opts := &Options{stdout: &out, stderr: &errBuf, Timeout: 10 * time.Second}

	root := newRootCmd(opts, "test")
	root.SetOut(&errBuf)
	root.SetErr(&errBuf)
	root.SetArgs(argv)

	t.Setenv("DEEPSEEK_FREE_URL", gateway)

	cap := capture{}
	cap.err = root.ExecuteContext(context.Background())
	cap.stdout = out.String()
	cap.stderr = errBuf.String()
	return cap
}

// freeGateway is a stub that implements the enrolment protocol at a
// difficulty low enough not to slow the suite down.
func freeGateway(t *testing.T) (url string, seen *[]string) {
	t.Helper()
	var paths []string

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/anon/info", func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		io.WriteString(w, `{"service":"dsgate","model":"deepseek-v4-flash",
		 "daily_limits":{"requests":30,"input_tokens":60000,"output_tokens":20000},
		 "max_tokens_per_request":4096,"proof_of_work_bits":8,
		 "privacy":"not stored","service_exhausted":false}`)
	})
	mux.HandleFunc("POST /v1/anon/challenge", func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		io.WriteString(w, `{"challenge":"abc.def","difficulty":8,
		 "algorithm":"sha256-leading-zero-bits","expires_in":300}`)
	})
	mux.HandleFunc("POST /v1/anon/token", func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		var req struct{ Challenge, Nonce string }
		json.NewDecoder(r.Body).Decode(&req)
		if req.Nonce == "" {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"message":"no nonce","type":"free_tier_rejected"}}`)
			return
		}
		io.WriteString(w, `{"token":"dsf_stub.token","subject":"SUBJECT1","tier":"anon"}`)
	})
	mux.HandleFunc("GET /v1/anon/quota", func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer dsf_stub.token" {
			w.WriteHeader(401)
			io.WriteString(w, `{"error":{"message":"nope","type":"free_tier_auth"}}`)
			return
		}
		io.WriteString(w, `{"subject":"SUBJECT1","tier":"anon",
		 "used":{"requests":7,"input_tokens":1200,"output_tokens":800,"spent_usd":0.00042},
		 "limits":{"requests":30,"input_tokens":60000,"output_tokens":20000},
		 "resets_at":"2099-01-01T00:00:00Z"}`)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL, &paths
}

func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("DEEPSEEK_CONFIG_DIR", t.TempDir())
	t.Setenv("DEEPSEEK_STATE_DIR", t.TempDir())
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("DEEPSEEK_BASE_URL", "")
}

func TestFreeEnrolThenStatusThenOff(t *testing.T) {
	isolate(t)
	url, _ := freeGateway(t)

	// The disclosure has to be printed before anything is minted: the act
	// of running the command is the consent, so it cannot be silent about
	// what it is consenting to.
	enrol := runFree(t, url, "free")
	if enrol.err != nil {
		t.Fatalf("free: %v\n%s", enrol.err, enrol.stderr)
	}
	for _, want := range []string{"relays your prompts", "deepseek-v4-flash", "per day", "privacy", "Enrolled"} {
		if !strings.Contains(enrol.stderr, want) {
			t.Errorf("enrolment output is missing %q:\n%s", want, enrol.stderr)
		}
	}

	status := runFree(t, url, "free", "status")
	if status.err != nil {
		t.Fatalf("free status: %v\n%s", status.err, status.stderr)
	}
	for _, want := range []string{"SUBJECT1", "7/30 requests", "resets"} {
		if !strings.Contains(status.stdout, want) {
			t.Errorf("status is missing %q:\n%s", want, status.stdout)
		}
	}

	off := runFree(t, url, "free", "off")
	if off.err != nil {
		t.Fatalf("free off: %v", off.err)
	}
	if _, ok := deepseek.LoadFree(); ok {
		t.Error("the enrolment survived `free off`")
	}
	// And saying so is more useful than a second removal.
	again := runFree(t, url, "free", "off")
	if !strings.Contains(again.stderr, "Not enrolled") {
		t.Errorf("a second `free off` did not say there was nothing to do: %s", again.stderr)
	}
}

// Enrolling twice should not burn a mint allowance for nothing.
func TestFreeEnrolIsIdempotent(t *testing.T) {
	isolate(t)
	url, paths := freeGateway(t)

	runFree(t, url, "free")
	before := len(*paths)
	second := runFree(t, url, "free")

	if second.err != nil {
		t.Fatalf("second enrol: %v", second.err)
	}
	if !strings.Contains(second.stderr, "already enrolled") {
		t.Errorf("a second enrol did not notice the first: %s", second.stderr)
	}
	for _, p := range (*paths)[before:] {
		if strings.Contains(p, "token") || strings.Contains(p, "challenge") {
			t.Errorf("a second enrol minted again: %s", p)
		}
	}
}

func TestFreeStatusWithoutEnrolment(t *testing.T) {
	isolate(t)
	url, _ := freeGateway(t)

	got := runFree(t, url, "free", "status")
	if got.err != nil {
		t.Fatalf("free status: %v", got.err)
	}
	if !strings.Contains(got.stderr, "Not enrolled") {
		t.Errorf("status did not say it was not enrolled: %s", got.stderr)
	}

	js := runFree(t, url, "free", "status", "--json")
	var res struct {
		Enrolled bool   `json:"enrolled"`
		Gateway  string `json:"gateway"`
	}
	if err := json.Unmarshal([]byte(js.stdout), &res); err != nil {
		t.Fatalf("--json is not JSON: %v\n%s", err, js.stdout)
	}
	if res.Enrolled {
		t.Error("--json claims an enrolment that does not exist")
	}
}

// The credential precedence is the part most likely to surprise: a
// paying user's prompts must never start going through our relay.
func TestAPIKeyAlwaysBeatsTheFreeTier(t *testing.T) {
	isolate(t)
	url, _ := freeGateway(t)
	runFree(t, url, "free")

	o := &Options{stdout: io.Discard, stderr: io.Discard}
	if free := o.usingFree(); free == nil {
		t.Fatal("with no key, the free tier should be in use")
	}

	t.Setenv("DEEPSEEK_API_KEY", "sk-a-real-key")
	if free := o.usingFree(); free != nil {
		t.Error("an API key was set and the free tier was used anyway")
	}
	key, base, free, err := o.resolveAuth()
	if err != nil || key != "sk-a-real-key" || free != nil {
		t.Errorf("resolveAuth = %q %q %v %v; want the real key", key, base, free, err)
	}
}

// A token minted for our gateway is not something to send to an
// arbitrary host the user happened to configure.
func TestFreeTokenIsNotSentToAnExplicitBaseURL(t *testing.T) {
	isolate(t)
	url, _ := freeGateway(t)
	runFree(t, url, "free")

	o := &Options{stdout: io.Discard, stderr: io.Discard, BaseURL: "https://somewhere-else.example"}
	if _, _, free, err := o.resolveAuth(); free != nil || err == nil {
		t.Errorf("the free token was offered to an explicitly configured base URL (free=%v err=%v)", free, err)
	}

	// Via the environment, too.
	t.Setenv("DEEPSEEK_BASE_URL", "https://somewhere-else.example")
	plain := &Options{stdout: io.Discard, stderr: io.Discard}
	if _, _, free, err := plain.resolveAuth(); free != nil || err == nil {
		t.Errorf("DEEPSEEK_BASE_URL did not suppress the free tier (free=%v err=%v)", free, err)
	}
}

// fim is the one command whose own default is pro, which the free tier
// refuses. An explicit choice still has to survive.
func TestUseFreeModelOnlyMovesADefault(t *testing.T) {
	isolate(t)
	url, _ := freeGateway(t)
	runFree(t, url, "free")

	newCmd := func() *cobra.Command {
		c := &cobra.Command{Use: "x"}
		c.Flags().String("model", deepseek.ModelPro, "")
		return c
	}

	var buf bytes.Buffer
	o := &Options{stdout: io.Discard, stderr: &buf}

	model := deepseek.ModelPro
	o.useFreeModel(newCmd(), &model)
	if model != deepseek.ModelFlash {
		t.Errorf("an untouched pro default was not moved to flash: %q", model)
	}
	if !strings.Contains(buf.String(), "free tier serves") {
		t.Errorf("the model was changed silently: %q", buf.String())
	}

	// Explicitly asked for: left alone, so the gateway's refusal is what
	// the user sees.
	explicit := newCmd()
	explicit.Flags().Set("model", deepseek.ModelPro)
	chosen := deepseek.ModelPro
	o.useFreeModel(explicit, &chosen)
	if chosen != deepseek.ModelPro {
		t.Errorf("an explicit --model was overridden: %q", chosen)
	}
}

func TestUseFreeModelDoesNothingWithoutTheFreeTier(t *testing.T) {
	isolate(t)
	o := &Options{stdout: io.Discard, stderr: io.Discard}
	c := &cobra.Command{Use: "x"}
	c.Flags().String("model", deepseek.ModelPro, "")

	model := deepseek.ModelPro
	o.useFreeModel(c, &model)
	if model != deepseek.ModelPro {
		t.Errorf("the default moved with no free-tier enrolment: %q", model)
	}
}

func TestRoundDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		30 * time.Second:             "30s",
		90 * time.Second:             "1m",
		2 * time.Hour:                "2h",
		2*time.Hour + 31*time.Minute: "2h 31m",
		11*time.Hour + 3*time.Minute + 20*time.Second: "11h 3m",
	} {
		if got := roundDuration(d); got != want {
			t.Errorf("roundDuration(%s) = %q, want %q", d, got, want)
		}
	}
}
