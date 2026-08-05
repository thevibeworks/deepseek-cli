package deepseek

// The free tier: using the DeepSeek API before you have an API key.
//
// A gateway run by this project holds a real key and relays requests,
// metered and capped, to anyone who has solved a proof-of-work puzzle
// once. That puzzle is the whole enrolment — no account, no email, no
// card. See gateway/DESIGN.md for what the other end does with it.
//
// The client half is deliberately small: three HTTP calls and a hash
// loop, all stdlib. Nothing here is imported by the gateway and nothing
// from the gateway is imported here — the two halves share a documented
// wire format, not a package, so the CLI's dependency list stays what it
// claims to be.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math/bits"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultGatewayURL is the hosted free tier. Override it with
// DEEPSEEK_FREE_URL to point at your own deployment — the gateway is in
// this repository and is meant to be runnable by anyone.
const DefaultGatewayURL = "https://freeseek.1lm.io"

// FreeTier is a saved enrolment.
type FreeTier struct {
	Token   string `json:"token"`
	Subject string `json:"subject"`
	BaseURL string `json:"base_url"`
	Tier    string `json:"tier"`
	// Enrolled is when the token was minted, so `deepseek free status`
	// can say how old an enrolment is without asking the server.
	Enrolled time.Time `json:"enrolled"`
}

// FreeFile is where the enrolment lives. It sits beside the API key
// because it is the same kind of thing: a credential the user chose to
// store, not state the CLI generated.
func FreeFile() string { return filepath.Join(ConfigDir(), "free.json") }

// GatewayURL is the free tier's base URL.
func GatewayURL() string {
	if v := strings.TrimSpace(os.Getenv("DEEPSEEK_FREE_URL")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return DefaultGatewayURL
}

// LoadFree reads the saved enrolment. A missing or unreadable file is
// not an error: it just means this machine is not enrolled.
func LoadFree() (*FreeTier, bool) {
	b, err := os.ReadFile(FreeFile())
	if err != nil {
		return nil, false
	}
	var f FreeTier
	if err := json.Unmarshal(b, &f); err != nil || f.Token == "" {
		return nil, false
	}
	if f.BaseURL == "" {
		f.BaseURL = GatewayURL()
	}
	return &f, true
}

// Save writes the enrolment, readable only by its owner.
func (f *FreeTier) Save() error {
	if err := os.MkdirAll(ConfigDir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(FreeFile(), append(b, '\n'), 0o600)
}

// ForgetFree removes the enrolment.
func ForgetFree() error {
	err := os.Remove(FreeFile())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// FreeInfo is the gateway's unauthenticated description of itself: what
// it serves, what it limits, and what it does with a prompt. The CLI
// shows this before enrolling, because "no signup" is not the same as
// "no informed consent".
type FreeInfo struct {
	Service   string `json:"service"`
	Announce  string `json:"announce"`
	Model     string `json:"model"`
	MaxTokens int    `json:"max_tokens_per_request"`
	MaxBody   int64  `json:"max_request_bytes"`
	PoWBits   int    `json:"proof_of_work_bits"`
	Privacy   string `json:"privacy"`
	Exhausted bool   `json:"service_exhausted"`
	Limits    struct {
		Requests     int `json:"requests"`
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"daily_limits"`
	Endpoints []string `json:"endpoints"`
}

// FreeQuota is one subject's standing, as the gateway reports it.
type FreeQuota struct {
	Subject string `json:"subject"`
	Tier    string `json:"tier"`
	Used    struct {
		Requests     int     `json:"requests"`
		InputTokens  int     `json:"input_tokens"`
		OutputTokens int     `json:"output_tokens"`
		SpentUSD     float64 `json:"spent_usd"`
	} `json:"used"`
	Limits struct {
		Requests     int `json:"requests"`
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"limits"`
	ResetsAt  time.Time `json:"resets_at"`
	Exhausted bool      `json:"service_exhausted"`
}

// challengeResponse is the puzzle. The gateway describes its own rules
// in these fields; this client only implements them.
type challengeResponse struct {
	Challenge  string `json:"challenge"`
	Difficulty int    `json:"difficulty"`
	Algorithm  string `json:"algorithm"`
	ExpiresIn  int    `json:"expires_in"`
}

type tokenResponse struct {
	Token   string `json:"token"`
	Subject string `json:"subject"`
	Tier    string `json:"tier"`
}

// EnrolProgress reports the proof-of-work search so a second of silence
// does not look like a hang.
type EnrolProgress struct {
	Difficulty int
	Hashes     uint64
	Elapsed    time.Duration
	Done       bool
}

// FreeGateway talks to a gateway.
type FreeGateway struct {
	BaseURL string
	HTTP    *http.Client
}

func NewFreeGateway(baseURL string, timeout time.Duration) *FreeGateway {
	if baseURL == "" {
		baseURL = GatewayURL()
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &FreeGateway{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP:    &http.Client{Timeout: timeout},
	}
}

// Info fetches the gateway's self-description.
func (g *FreeGateway) Info(ctx context.Context) (*FreeInfo, error) {
	var info FreeInfo
	if err := g.call(ctx, "GET", "/v1/anon/info", "", nil, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// Quota fetches one enrolment's standing.
func (g *FreeGateway) Quota(ctx context.Context, token string) (*FreeQuota, error) {
	var q FreeQuota
	if err := g.call(ctx, "GET", "/v1/anon/quota", token, nil, &q); err != nil {
		return nil, err
	}
	return &q, nil
}

// Enrol runs the whole journey: take a challenge, solve it, exchange the
// solution for a token.
func (g *FreeGateway) Enrol(ctx context.Context, progress func(EnrolProgress)) (*FreeTier, error) {
	var ch challengeResponse
	if err := g.call(ctx, "POST", "/v1/anon/challenge", "", map[string]any{}, &ch); err != nil {
		return nil, err
	}
	if ch.Challenge == "" || ch.Difficulty <= 0 {
		return nil, fmt.Errorf("the gateway issued a challenge this client cannot read")
	}
	if ch.Algorithm != "" && ch.Algorithm != powAlgorithm {
		// Refusing beats guessing: a client that solved the wrong puzzle
		// would burn CPU and then be rejected with no explanation.
		return nil, fmt.Errorf("the gateway asked for %q, which this version does not implement — upgrade with: go install github.com/thevibeworks/deepseek-cli/cmd/deepseek@latest", ch.Algorithm)
	}

	start := time.Now()
	nonce, hashes, err := solvePoW(ctx, ch.Challenge, ch.Difficulty, func(h uint64) {
		if progress != nil {
			progress(EnrolProgress{Difficulty: ch.Difficulty, Hashes: h, Elapsed: time.Since(start)})
		}
	})
	if err != nil {
		return nil, err
	}
	if progress != nil {
		progress(EnrolProgress{Difficulty: ch.Difficulty, Hashes: hashes, Elapsed: time.Since(start), Done: true})
	}

	var tr tokenResponse
	body := map[string]any{"challenge": ch.Challenge, "nonce": strconv.FormatUint(nonce, 10)}
	if err := g.call(ctx, "POST", "/v1/anon/token", "", body, &tr); err != nil {
		return nil, err
	}
	if tr.Token == "" {
		return nil, fmt.Errorf("the gateway accepted the proof of work but issued no token")
	}
	return &FreeTier{
		Token:    tr.Token,
		Subject:  tr.Subject,
		Tier:     tr.Tier,
		BaseURL:  g.BaseURL,
		Enrolled: time.Now().UTC(),
	}, nil
}

func (g *FreeGateway) call(ctx context.Context, method, path, token string, body any, out any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	// A nil body has to be a nil io.Reader, not a non-nil interface
	// wrapping one, or net/http sends a Content-Length of 0 on a GET.
	var rdr io.Reader
	if payload != nil {
		rdr = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.BaseURL+path, rdr)
	if err != nil {
		return err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("User-Agent", "deepseek-cli")

	resp, err := g.HTTP.Do(req)
	if err != nil {
		return &TransportError{Op: method + " " + g.BaseURL + path, Err: err}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return newAPIError(resp, raw)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("the gateway sent something this client could not read: %w", err)
	}
	return nil
}

// --- proof of work ------------------------------------------------------

// powAlgorithm is the only puzzle this client knows. The gateway names
// it in every challenge so that a future change is a clear error here
// rather than a silent failure to enrol.
const powAlgorithm = "sha256-leading-zero-bits"

// powLimit bounds the search. At the shipped difficulty the answer turns
// up in about a million hashes; this is enough headroom for the
// escalated difficulties an over-minting address is given, and low
// enough that a misconfigured gateway fails in seconds rather than
// spinning a laptop's fans forever.
const powLimit = 1 << 34

// solvePoW finds a nonce whose sha256("<challenge>:<nonce>") begins with
// difficulty zero bits.
//
// The search is spread across cores by striding: worker i tries
// i, i+n, i+2n… Every worker is doing independent work on the same
// problem, so the first to finish wins and the rest stop.
func solvePoW(ctx context.Context, challenge string, difficulty int, progress func(uint64)) (uint64, uint64, error) {
	workers := runtime.GOMAXPROCS(0)
	if workers < 1 {
		workers = 1
	}

	var (
		found    atomic.Bool
		answer   atomic.Uint64
		hashes   atomic.Uint64
		wg       sync.WaitGroup
		stopTick = make(chan struct{})
	)

	if progress != nil {
		go func() {
			t := time.NewTicker(200 * time.Millisecond)
			defer t.Stop()
			for {
				select {
				case <-stopTick:
					return
				case <-t.C:
					progress(hashes.Load())
				}
			}
		}()
	}

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(start uint64) {
			defer wg.Done()
			var local, published uint64
			// Publish the tail on the way out. Without this a puzzle
			// solved in under 4096 hashes reports having done none, and
			// the progress line says "0 hashes" for every fast solve.
			defer func() { hashes.Add(local - published) }()

			for nonce := start; nonce < powLimit; nonce += uint64(workers) {
				// Touching shared state on every iteration would cost more
				// than the hash does. Once every 4096 is prompt enough to
				// stop on and cheap enough to be invisible.
				if local++; local-published >= 4096 {
					hashes.Add(4096)
					published += 4096
					if found.Load() || ctx.Err() != nil {
						return
					}
				}
				if leadingZeroBits(powDigest(challenge, nonce)) >= difficulty {
					if !found.Swap(true) {
						answer.Store(nonce)
					}
					return
				}
			}
		}(uint64(w))
	}

	wg.Wait()
	close(stopTick)

	if err := ctx.Err(); err != nil {
		return 0, hashes.Load(), err
	}
	if !found.Load() {
		return 0, hashes.Load(), fmt.Errorf("no proof of work found for a difficulty of %d bits", difficulty)
	}
	return answer.Load(), hashes.Load(), nil
}

func powDigest(challenge string, nonce uint64) [32]byte {
	buf := make([]byte, 0, len(challenge)+21)
	buf = append(buf, challenge...)
	buf = append(buf, ':')
	buf = strconv.AppendUint(buf, nonce, 10)
	return sha256.Sum256(buf)
}

func leadingZeroBits(digest [32]byte) int {
	n := 0
	for _, b := range digest {
		if b != 0 {
			return n + bits.LeadingZeros8(b)
		}
		n += 8
	}
	return n
}
