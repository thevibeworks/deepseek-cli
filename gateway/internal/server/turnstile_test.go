package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thevibeworks/deepseek-cli/gateway/internal/quota"
	"github.com/thevibeworks/deepseek-cli/gateway/internal/token"
)

// fakeSiteverify stands in for Cloudflare: "good-token" passes, anything
// else fails, and it counts calls so a test can prove the gateway never
// phoned out.
type fakeSiteverify struct {
	server *httptest.Server
	calls  atomic.Int64
	secret atomic.Value // last secret seen
}

func newFakeSiteverify() *fakeSiteverify {
	f := &fakeSiteverify{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		f.secret.Store(r.PostForm.Get("secret"))
		ok := r.PostForm.Get("response") == "good-token"
		out := map[string]any{"success": ok}
		if !ok {
			out["error-codes"] = []string{"invalid-input-response"}
		}
		json.NewEncoder(w).Encode(out)
	}))
	return f
}

// mint runs challenge -> solve -> redeem with an optional Origin header
// and Turnstile token, and returns the redemption response.
func (h *harness) mint(t *testing.T, origin, tsToken string) (*http.Response, string) {
	t.Helper()

	resp, err := h.client.Post(h.base+"/v1/anon/challenge", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var ch ChallengeResponse
	if err := json.NewDecoder(resp.Body).Decode(&ch); err != nil {
		t.Fatal(err)
	}
	nonce, err := token.Solve(ch.Challenge, ch.Difficulty, 1<<24)
	if err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(TokenRequest{
		Challenge:      ch.Challenge,
		Nonce:          strconv.FormatUint(nonce, 10),
		TurnstileToken: tsToken,
	})
	req, err := http.NewRequest(http.MethodPost, h.base+"/v1/anon/token", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	res, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	return res, string(raw)
}

const playgroundOrigin = "https://deepseek-cli.example"

// quietUpstream is an upstream these tests never reach: minting is the
// whole journey here.
func quietUpstream(t *testing.T) *upstream {
	t.Helper()
	return newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "the mint tests should never proxy upstream", http.StatusTeapot)
	})
}

func turnstileHarness(t *testing.T, f *fakeSiteverify) *harness {
	t.Helper()
	return newHarness(t, quietUpstream(t), func(c *Config, _ *quota.Limits) {
		c.TurnstileSecret = "test-secret"
		c.TurnstileURL = f.server.URL
	})
}

func TestTurnstileBrowserLaneNeedsToken(t *testing.T) {
	f := newFakeSiteverify()
	defer f.server.Close()
	h := turnstileHarness(t, f)

	res, body := h.mint(t, playgroundOrigin, "")
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("browser mint without turnstile: HTTP %d, want 403 (%s)", res.StatusCode, body)
	}
	if !strings.Contains(body, "browser check") {
		t.Fatalf("the refusal should say what to do, got: %s", body)
	}
	// The missing-token case must be refused locally: nobody may spend
	// our outbound calls without paying proof-of-work... and this caller
	// did pay, but the refusal happens before Redeem, so no call either.
	if n := f.calls.Load(); n != 0 {
		t.Fatalf("siteverify was called %d times for a missing token", n)
	}
}

func TestTurnstileBrowserLaneBadToken(t *testing.T) {
	f := newFakeSiteverify()
	defer f.server.Close()
	h := turnstileHarness(t, f)

	res, body := h.mint(t, playgroundOrigin, "forged")
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("browser mint with a bad turnstile token: HTTP %d, want 403 (%s)", res.StatusCode, body)
	}
	if f.calls.Load() != 1 {
		t.Fatalf("siteverify calls = %d, want 1", f.calls.Load())
	}
}

func TestTurnstileBrowserLaneGoodToken(t *testing.T) {
	f := newFakeSiteverify()
	defer f.server.Close()
	h := turnstileHarness(t, f)

	res, body := h.mint(t, playgroundOrigin, "good-token")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("browser mint with a good turnstile token: HTTP %d (%s)", res.StatusCode, body)
	}
	var tr TokenResponse
	if err := json.Unmarshal([]byte(body), &tr); err != nil || tr.Token == "" {
		t.Fatalf("no token in %s", body)
	}
	if got := f.secret.Load(); got != "test-secret" {
		t.Fatalf("siteverify saw secret %q", got)
	}
}

func TestTurnstileCLILaneUnaffected(t *testing.T) {
	f := newFakeSiteverify()
	defer f.server.Close()
	h := turnstileHarness(t, f)

	// No Origin header: the CLI lane. Pure proof-of-work, no widget.
	res, body := h.mint(t, "", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("CLI mint under turnstile config: HTTP %d (%s)", res.StatusCode, body)
	}
	if n := f.calls.Load(); n != 0 {
		t.Fatalf("the CLI lane reached siteverify %d times", n)
	}
}

func TestTurnstileOffByDefault(t *testing.T) {
	h := newHarness(t, quietUpstream(t), nil)

	// Browser-origin mint with no turnstile configured: unchanged.
	res, body := h.mint(t, playgroundOrigin, "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("browser mint with turnstile unconfigured: HTTP %d (%s)", res.StatusCode, body)
	}
}
