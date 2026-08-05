package deepseek

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The proof-of-work is implemented twice — here, and in the gateway at
// gateway/internal/token — because the two are separate Go modules and
// neither imports the other. These vectors are the contract between
// them: the identical table appears in the gateway's own tests, so a
// change to either hash construction fails on both sides instead of
// silently making every enrolment attempt fail in production.
//
//	sha256("<challenge>:<nonce>") has >= difficulty leading zero bits,
//	nonce being the smallest such value.
var powVectors = []struct {
	Challenge  string
	Difficulty int
	Nonce      uint64
}{
	{"dsgate-protocol-vector.v1", 8, 148},
	{"dsgate-protocol-vector.v1", 12, 2601},
	{"dsgate-protocol-vector.v1", 16, 28337},
	{"abc.def", 8, 125},
	{"abc.def", 12, 1917},
}

func TestProofOfWorkVectors(t *testing.T) {
	for _, v := range powVectors {
		got := leadingZeroBits(powDigest(v.Challenge, v.Nonce))
		if got < v.Difficulty {
			t.Errorf("sha256(%q:%d) has %d leading zero bits, want >= %d",
				v.Challenge, v.Nonce, got, v.Difficulty)
		}
		// And it is the *smallest* such nonce, which is what the solver
		// searching upward from zero has to find.
		for n := uint64(0); n < v.Nonce; n++ {
			if leadingZeroBits(powDigest(v.Challenge, n)) >= v.Difficulty {
				t.Fatalf("%q at %d bits: nonce %d also solves it, before the vector's %d",
					v.Challenge, v.Difficulty, n, v.Nonce)
			}
		}
	}
}

func TestSolverFindsTheCanonicalNonce(t *testing.T) {
	for _, v := range powVectors {
		nonce, hashes, err := solvePoW(context.Background(), v.Challenge, v.Difficulty, nil)
		if err != nil {
			t.Fatalf("solvePoW(%q, %d): %v", v.Challenge, v.Difficulty, err)
		}
		// Workers stride, so the winner is not necessarily the smallest —
		// only that it is a valid solution.
		if got := leadingZeroBits(powDigest(v.Challenge, nonce)); got < v.Difficulty {
			t.Errorf("solver returned nonce %d with %d bits, want >= %d", nonce, got, v.Difficulty)
		}
		if hashes == 0 {
			t.Error("solver reported doing no work")
		}
	}
}

func TestSolverStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	// A difficulty nobody will reach by accident.
	done := make(chan error, 1)
	go func() {
		_, _, err := solvePoW(ctx, "unreachable", 60, nil)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a cancelled solve returned success")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("solvePoW ignored a cancelled context")
	}
}

// stubGateway implements the documented free-tier protocol, so the
// client half can be exercised without the server half being importable.
func stubGateway(t *testing.T, difficulty int) *httptest.Server {
	t.Helper()
	var issued string

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/anon/info", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"service":"dsgate","model":"deepseek-v4-flash",
		 "daily_limits":{"requests":30,"input_tokens":60000,"output_tokens":20000},
		 "max_tokens_per_request":4096,"proof_of_work_bits":`+strconv.Itoa(difficulty)+`,
		 "privacy":"not stored","service_exhausted":false}`)
	})
	mux.HandleFunc("POST /v1/anon/challenge", func(w http.ResponseWriter, r *http.Request) {
		issued = "stub-challenge-" + strconv.Itoa(difficulty)
		io.WriteString(w, `{"challenge":"`+issued+`","difficulty":`+strconv.Itoa(difficulty)+
			`,"algorithm":"sha256-leading-zero-bits","expires_in":300}`)
	})
	mux.HandleFunc("POST /v1/anon/token", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Challenge, Nonce string }
		json.NewDecoder(r.Body).Decode(&req)
		n, err := strconv.ParseUint(req.Nonce, 10, 64)
		if err != nil || req.Challenge != issued || leadingZeroBits(powDigest(req.Challenge, n)) < difficulty {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"message":"bad proof","type":"free_tier_rejected"}}`)
			return
		}
		io.WriteString(w, `{"token":"dsf_stub.token","subject":"SUBJ","tier":"anon"}`)
	})
	mux.HandleFunc("GET /v1/anon/quota", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer dsf_stub.token" {
			w.WriteHeader(401)
			io.WriteString(w, `{"error":{"message":"no","type":"free_tier_auth"}}`)
			return
		}
		io.WriteString(w, `{"subject":"SUBJ","tier":"anon",
		 "used":{"requests":3,"input_tokens":1200,"output_tokens":800,"spent_usd":0.0004},
		 "limits":{"requests":30,"input_tokens":60000,"output_tokens":20000},
		 "resets_at":"2030-01-01T00:00:00Z"}`)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestEnrolAgainstTheDocumentedProtocol(t *testing.T) {
	t.Setenv("DEEPSEEK_CONFIG_DIR", t.TempDir())
	srv := stubGateway(t, 12)

	gw := NewFreeGateway(srv.URL, 10*time.Second)
	info, err := gw.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Model != "deepseek-v4-flash" || info.Limits.Requests != 30 {
		t.Errorf("info did not decode: %+v", info)
	}

	free, err := gw.Enrol(context.Background(), nil)
	if err != nil {
		t.Fatalf("Enrol: %v", err)
	}
	if free.Token != "dsf_stub.token" || free.Subject != "SUBJ" {
		t.Errorf("enrolment did not decode: %+v", free)
	}

	if err := free.Save(); err != nil {
		t.Fatal(err)
	}
	got, ok := LoadFree()
	if !ok {
		t.Fatal("the saved enrolment did not load back")
	}
	if got.Token != free.Token || got.BaseURL != srv.URL {
		t.Errorf("round trip lost something: %+v", got)
	}

	// Mode 0600: it is a credential.
	fi, err := os.Stat(FreeFile())
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("%s has mode %o, want 600", FreeFile(), perm)
	}

	quota, err := gw.Quota(context.Background(), free.Token)
	if err != nil {
		t.Fatalf("Quota: %v", err)
	}
	if quota.Used.Requests != 3 || quota.Limits.Requests != 30 {
		t.Errorf("quota did not decode: %+v", quota)
	}

	if err := ForgetFree(); err != nil {
		t.Fatal(err)
	}
	if _, ok := LoadFree(); ok {
		t.Error("the enrolment survived being forgotten")
	}
	// Forgetting twice is not an error.
	if err := ForgetFree(); err != nil {
		t.Errorf("second ForgetFree: %v", err)
	}
}

// A gateway that changes its puzzle must produce a clear upgrade
// message, not a client that burns CPU on the wrong problem and is then
// rejected with no explanation.
func TestUnknownAlgorithmIsRefusedBeforeSpendingCPU(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/anon/challenge", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"challenge":"x","difficulty":8,"algorithm":"argon2id-v2"}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	_, err := NewFreeGateway(srv.URL, time.Second).Enrol(context.Background(), nil)
	if err == nil {
		t.Fatal("an unknown proof-of-work algorithm was attempted anyway")
	}
	if !strings.Contains(err.Error(), "argon2id-v2") || !strings.Contains(err.Error(), "go install") {
		t.Errorf("the error neither names the algorithm nor says how to upgrade: %v", err)
	}
}

// The gateway speaks DeepSeek's error envelope so that unmodified
// clients report its refusals properly. What must not happen is the CLI
// then bolting DeepSeek's own advice onto it — telling someone with no
// account to go top one up.
func TestFreeTierErrorsDoNotGetDeepSeekHints(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/anon/quota", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		io.WriteString(w, `{"error":{"message":"the free tier has run out of credit. Bring your own key","type":"free_tier_exhausted"}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	_, err := NewFreeGateway(srv.URL, time.Second).Quota(context.Background(), "dsf_x.y")
	var ae *APIError
	if !asAPIError(err, &ae) {
		t.Fatalf("error is %T, want *APIError", err)
	}
	if !ae.FromFreeTier() {
		t.Fatalf("type %q was not recognised as a gateway error", ae.Type)
	}
	if ae.Hint() != "" {
		t.Errorf("a free-tier 402 got DeepSeek's hint: %q", ae.Hint())
	}
	if !strings.Contains(ae.Error(), "run out of credit") {
		t.Errorf("the gateway's own message was lost: %q", ae.Error())
	}
	// A real DeepSeek 402 still gets its hint.
	real := &APIError{StatusCode: http.StatusPaymentRequired}
	if real.Hint() == "" {
		t.Error("a genuine 402 lost its top-up hint")
	}
}

func asAPIError(err error, target **APIError) bool {
	e, ok := err.(*APIError)
	if ok {
		*target = e
	}
	return ok
}
