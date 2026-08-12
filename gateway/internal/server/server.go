// Package server is the HTTP surface of the free tier: the mint
// endpoints, the quota endpoints, and the proxy that carries everything
// else to DeepSeek.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thevibeworks/deepseek-cli/gateway/internal/keyring"
	"github.com/thevibeworks/deepseek-cli/gateway/internal/mint"
	"github.com/thevibeworks/deepseek-cli/gateway/internal/policy"
	"github.com/thevibeworks/deepseek-cli/gateway/internal/quota"
	"github.com/thevibeworks/deepseek-cli/gateway/internal/stats"
	"github.com/thevibeworks/deepseek-cli/gateway/internal/token"
)

// Config is everything the server needs to run. It is filled from the
// environment in cmd/dsgate and validated there, so this package never
// reads os.Getenv and stays testable.
type Config struct {
	// UpstreamBaseURL is DeepSeek's API root.
	UpstreamBaseURL string
	// UpstreamKeys are the real API keys this service spends. They never
	// leave this process. More than one is a pool: requests rotate across
	// them and an emptied key retires itself, so a donation extends the
	// service without a restart.
	UpstreamKeys []string
	// KeyStatePath persists donated keys across restarts.
	KeyStatePath string
	// Model is the only model the free tier serves.
	Model string

	// FreeBaseURL, FreeKeys and FreeModel describe an upstream that costs
	// this service nothing — OpenCode Zen's free DeepSeek lane. When keys
	// are present it is tried first for every route it can serve, and a
	// refusal falls straight through to the paid upstream above, so the
	// worst case is one extra round trip and the best case is a request
	// the credit pool never pays for. Empty keys disable the lane and the
	// gateway behaves exactly as it did before it existed.
	FreeBaseURL string
	FreeKeys    []string
	// FreeModel is what that upstream calls the model. Bodies are
	// retargeted to it on the way out; nothing else in the service learns
	// the alias.
	FreeModel string
	// FreeKeyStatePath persists keys donated to the free lane.
	FreeKeyStatePath string

	// Version is the build, shown on the status page.
	Version string

	MaxBodyBytes int64
	MaxTokens    int
	MaxInflight  int

	// RequestsPerMinute is the per-address burst limit, applied before
	// quota so that a runaway loop is cheap to refuse.
	RequestsPerMinute int

	// SubjectRequestsPerMinute is the per-token burst limit. The address
	// limit alone is not enough: one subject spread across addresses, or
	// many subjects behind one address, are different attacks and need
	// separate valves.
	SubjectRequestsPerMinute int

	// SubjectInflight caps concurrent requests per token. Without it, one
	// token opening MaxInflight never-reading streams parks the whole
	// service behind the global cap.
	SubjectInflight int

	// TokenTTL is how long a minted token stays valid. Re-enrolment is a
	// second of CPU, so expiry costs honest users almost nothing — and
	// stops an attacker stockpiling identities for months and spending
	// them together.
	TokenTTL time.Duration

	// TrustProxy makes X-Forwarded-For authoritative. Set it only when
	// something we control terminates TLS in front of this process:
	// facing the internet directly, the header is attacker-supplied and
	// trusting it hands out unlimited identities.
	TrustProxy bool

	// Origins may call the mint and proxy endpoints from a browser. The
	// playground is the only intended caller.
	Origins []string

	// AdminToken gates the operator endpoints. Empty disables them.
	AdminToken string

	// TurnstileSecret enables the browser check on the mint's browser
	// lane: when set, a token redemption that carries an Origin header
	// must also carry a Cloudflare Turnstile token, verified against
	// siteverify. Empty disables the check entirely; the CLI's
	// no-Origin lane is never subject to it. See turnstile.go.
	TurnstileSecret string
	// TurnstileURL overrides the siteverify endpoint, for tests. Empty
	// means Cloudflare's real one.
	TurnstileURL string

	// Announce is the public URL of this gateway, used in the messages
	// that tell a user where their prompts are going.
	Announce string
}

// Server is the gateway.
type Server struct {
	cfg    Config
	mint   *mint.Mint
	signer *token.Signer
	ledger *quota.Ledger
	keys   *keyring.Ring
	stats  *stats.Collector

	// paid and free are the upstreams, in the order they are tried. free
	// is nil unless a key was configured for it.
	paid *lane
	free *lane

	// freeServed and freeFellBack count how the free lane is doing. They
	// are the only way to tell "the free lane is carrying the service"
	// from "the free lane is refusing everything and the credit pool is
	// paying for it anyway", which look identical from the outside.
	freeServed   atomic.Int64
	freeFellBack atomic.Int64

	// statusDoc caches the public status document; see publicStatusTTL.
	statusMu  sync.Mutex
	statusDoc *PublicStatus
	statusAt  time.Time

	http        *http.Client
	inflight    chan struct{}
	limiter     *limiter
	subjLimiter *limiter

	subjMu       sync.Mutex
	subjInflight map[string]int

	// modelsCache holds the last filtered /models answer. The list changes
	// on the timescale of DeepSeek launches, and without a cache the one
	// deliberately-uncharged endpoint would consume an in-flight slot and
	// an upstream round trip per poll.
	modelsMu   sync.Mutex
	modelsBody []byte
	modelsAt   time.Time

	// upstreamDry is set when DeepSeek itself reports our account
	// unusable. The local ledger only knows what this gateway spent — the
	// account can empty underneath it (other spenders, a price change),
	// and a gateway that keeps promising credit it does not have would
	// fail every request with a confusing upstream error instead of an
	// honest 402.
	upstreamDry atomic.Bool

	origins map[string]bool
	started time.Time
}

func New(cfg Config, signer *token.Signer, m *mint.Mint, ledger *quota.Ledger) *Server {
	origins := map[string]bool{}
	for _, o := range cfg.Origins {
		origins[strings.TrimSuffix(strings.TrimSpace(o), "/")] = true
	}
	keys := keyring.New(cfg.UpstreamKeys, cfg.KeyStatePath)
	s := &Server{
		cfg:    cfg,
		mint:   m,
		signer: signer,
		ledger: ledger,
		http: &http.Client{
			// No client timeout. DeepSeek documents holding a connection up
			// to ten minutes before inference starts, and a timeout shorter
			// than that would turn a normal slow start into a failure. The
			// request context, which follows the caller hanging up, is what
			// bounds this instead.
			Transport: &http.Transport{
				MaxIdleConnsPerHost: 16,
				IdleConnTimeout:     90 * time.Second,
				// Streaming is the common case, so buffering a response
				// before handing it to us would add latency to every token.
				DisableCompression: false,
				ForceAttemptHTTP2:  true,
			},
			// Never follow an upstream redirect. Our key rides on these
			// requests, and Go strips Authorization across hosts but not
			// x-api-key — a redirecting upstream would be handed the key.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		keys:         keys,
		stats:        stats.New(),
		inflight:     make(chan struct{}, cfg.MaxInflight),
		limiter:      newLimiter(cfg.RequestsPerMinute, time.Minute),
		subjLimiter:  newLimiter(cfg.SubjectRequestsPerMinute, time.Minute),
		subjInflight: map[string]int{},
		origins:      origins,
		started:      time.Now(),
	}

	s.paid = &lane{
		name:    LanePaid,
		baseURL: cfg.UpstreamBaseURL,
		keys:    keys,
	}
	if len(cfg.FreeKeys) > 0 {
		s.free = &lane{
			name:    LaneFree,
			baseURL: cfg.FreeBaseURL,
			keys:    keyring.New(cfg.FreeKeys, cfg.FreeKeyStatePath),
			model:   cfg.FreeModel,
			free:    true,
		}
	}
	return s
}

// Lane names, as they appear on the status page and in the logs.
const (
	LaneFree = "free"
	LanePaid = "deepseek"
)

// lane is one upstream this gateway can send a request to.
//
// The gateway was built around a single upstream, and for most of its
// life that was right: one API, one key pool, one rate card. A second
// lane earns the indirection because it is not a second copy of the
// first — it speaks a subset of the routes, calls the model by another
// name, and costs nothing, and each of those differences has to travel
// with the choice of where a request goes.
type lane struct {
	name    string
	baseURL string
	keys    *keyring.Ring
	// model overrides the body's model field for this lane. Empty leaves
	// the approved body untouched.
	model string
	// free means a request served here costs the credit pool nothing, so
	// its reservation is released and it is charged at zero.
	free bool
}

// label is how this lane is named in an operator-facing message.
func (l *lane) label() string {
	if l.free {
		return "the free upstream"
	}
	return "DeepSeek"
}

// serves reports whether this lane can carry a request at all.
//
// The free lane is deliberately narrow. Measured against OpenCode Zen on
// 2026-08-12: /chat/completions works and reports usage in both streamed
// and buffered form; /anthropic/v1/messages, /beta/completions and
// /user/balance are 404; /responses answers, but rejects a server-side
// web_search tool outright. So chat is the one route it is trusted with,
// which is also where nearly all of the volume is. Everything else goes
// to DeepSeek, exactly as before.
func (l *lane) serves(route policy.Route, d *policy.Decision) bool {
	if !l.free {
		return true
	}
	return route.Name == "chat" && !d.Search
}

// lanesFor is the order to try upstreams in for one request.
func (s *Server) lanesFor(route policy.Route, d *policy.Decision) []*lane {
	if s.free != nil && s.free.serves(route, d) && s.free.keys.Usable() {
		return []*lane{s.free, s.paid}
	}
	return []*lane{s.paid}
}

// Keys exposes the pool so an operator command can seed it at boot.
func (s *Server) Keys() *keyring.Ring { return s.keys }

// acquireSubject takes one of a token's concurrency slots, or reports
// that they are all in use.
func (s *Server) acquireSubject(subject string) bool {
	s.subjMu.Lock()
	defer s.subjMu.Unlock()
	if s.subjInflight[subject] >= s.cfg.SubjectInflight {
		return false
	}
	s.subjInflight[subject]++
	return true
}

func (s *Server) releaseSubject(subject string) {
	s.subjMu.Lock()
	defer s.subjMu.Unlock()
	if s.subjInflight[subject] <= 1 {
		// Deleting at zero keeps the map's size bounded by the subjects
		// actually in flight, which the global cap already bounds.
		delete(s.subjInflight, subject)
		return
	}
	s.subjInflight[subject]--
}

// Handler builds the routing table.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /v1/anon/challenge", s.handleChallenge)
	mux.HandleFunc("POST /v1/anon/token", s.handleToken)
	mux.HandleFunc("GET /v1/anon/quota", s.handleQuota)
	mux.HandleFunc("GET /v1/anon/info", s.handleInfo)
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /admin/health", s.handleAdminHealth)

	// The dashboard and what feeds it.
	mux.HandleFunc("GET /v1/status", s.handleStatus)
	mux.HandleFunc("GET /admin/status", s.handleAdminStatus)
	mux.HandleFunc("/admin/keys", s.handleAdminKeys)
	s.routeWeb(mux)

	// The balance endpoint is answered locally rather than proxied: the
	// upstream figure is our account's, and it is nobody else's business.
	mux.HandleFunc("GET /user/balance", s.handleBalance)
	mux.HandleFunc("GET /v1/user/balance", s.handleBalance)

	// Everything else is a candidate for proxying, and the allowlist in
	// package policy decides.
	mux.HandleFunc("/", s.handleProxy)

	return s.withCORS(mux)
}

// --- CORS ---------------------------------------------------------------

func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimSuffix(r.Header.Get("Origin"), "/")
		if origin != "" && s.origins[origin] {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Vary", "Origin")
			h.Set("Access-Control-Allow-Headers", "authorization, content-type, x-api-key, anthropic-version")
			h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			h.Set("Access-Control-Max-Age", "86400")
			// The playground shows remaining quota, which means it has to be
			// able to read these off a cross-origin response.
			h.Set("Access-Control-Expose-Headers", strings.Join([]string{
				headerRequestsLeft, headerInputLeft, headerOutputLeft, headerResets,
			}, ", "))
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// --- errors -------------------------------------------------------------

// errorEnvelope is DeepSeek's own error shape.
//
// Matching it exactly is what lets an unmodified client — ours or
// anyone's OpenAI-compatible tooling — report a gateway refusal as
// clearly as it reports an upstream one. A gateway that invented its own
// error format would surface as "unexpected response" in every client
// that ever pointed at it.
type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Param   string `json:"param,omitempty"`
	Code    string `json:"code,omitempty"`
}

// Error types are prefixed "free_tier_" so a client can tell a refusal
// by this gateway from a refusal by DeepSeek. The CLI keys its hint text
// off exactly that prefix.
const (
	typeAuth      = "free_tier_auth"
	typeQuota     = "free_tier_quota"
	typeExhausted = "free_tier_exhausted"
	typeRejected  = "free_tier_rejected"
	typeUpstream  = "free_tier_upstream"
	typeInternal  = "free_tier_internal"
)

func writeError(w http.ResponseWriter, status int, kind, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(errorEnvelope{Error: errorBody{Message: msg, Type: kind}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// --- auth ---------------------------------------------------------------

// authenticate resolves the caller's token.
//
// Both header styles are accepted because the formats disagree: the
// OpenAI and Responses paths send Authorization, the Anthropic path
// sends x-api-key. A free-tier user should not have to know which.
func (s *Server) authenticate(r *http.Request) (*token.Token, error) {
	raw := strings.TrimSpace(r.Header.Get("Authorization"))
	if after, ok := strings.CutPrefix(raw, "Bearer "); ok {
		raw = strings.TrimSpace(after)
	}
	if raw == "" {
		raw = strings.TrimSpace(r.Header.Get("x-api-key"))
	}
	if raw == "" {
		return nil, fmt.Errorf("no credential — run `deepseek free` to get one")
	}
	if !token.IsToken(raw) {
		// Almost always a real DeepSeek key aimed at the wrong base URL.
		// Saying so is more useful than "unauthorized", and forwarding it
		// would be worse still: their key would end up in our upstream
		// request, which is not a thing this service should ever do.
		return nil, fmt.Errorf("that looks like a DeepSeek API key, not a free-tier token — a real key should go to https://api.deepseek.com directly, not through this gateway")
	}
	t, err := s.signer.ParseToken(raw)
	if err != nil {
		return nil, fmt.Errorf("token not valid: %w — run `deepseek free` to mint a new one", err)
	}
	if s.cfg.TokenTTL > 0 && time.Since(t.Issued) > s.cfg.TokenTTL {
		// Enforced here rather than in the codec: expiry is service
		// policy, and the codec's job is only to say whose token it is.
		return nil, fmt.Errorf("this free-tier token has expired — run `deepseek free` to mint a new one (about a second of CPU)")
	}
	return t, nil
}

func (s *Server) clientIP(r *http.Request) string {
	return mint.ClientIP(r.RemoteAddr, r.Header.Get("X-Forwarded-For"), s.cfg.TrustProxy)
}

// --- upstream balance ----------------------------------------------------

// StartBalanceWatch polls DeepSeek's balance endpoint so the gateway's
// idea of "we have credit" is checked against the account that actually
// pays. The poll costs nothing — /user/balance is unbilled.
func (s *Server) StartBalanceWatch(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	go func() {
		s.checkBalance(ctx)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.checkBalance(ctx)
			}
		}
	}()
}

// checkBalance asks DeepSeek about every key in the pool and retires the
// ones it says are done. Checking each key rather than a representative
// one is the point: with a pool, "are we out of money" is a question per
// key, and a single dry donation should not condemn the rest.
func (s *Server) checkBalance(ctx context.Context) {
	any := false
	// Dry keys are checked too, not skipped — that is how a donor who
	// topped their key up gets back into rotation without anyone noticing
	// by hand. Only an operator retirement is permanent.
	for _, fp := range s.keys.Fingerprints() {
		switch s.keyAvailable(ctx, fp) {
		case availYes:
			any = true
			if s.keys.MarkFunded(fp) {
				log.Printf("key %s has credit again and is back in rotation", fp)
			}
		case availNo:
			s.keys.MarkDry(fp, "DeepSeek reports no balance on this key")
		case availUnknown:
			// Network trouble is not "out of money". A key already in
			// rotation stays; one already dry stays dry until upstream
			// actually answers for it.
			any = true
		}
	}
	s.upstreamDry.Store(!any)
	s.invalidateStatus()
}

type availability int

const (
	availUnknown availability = iota
	availYes
	availNo
)

func (s *Server) keyAvailable(ctx context.Context, fingerprint string) availability {
	secret, ok := s.keys.Secret(fingerprint)
	if !ok {
		return availUnknown
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(s.cfg.UpstreamBaseURL, "/")+"/user/balance", nil)
	if err != nil {
		return availUnknown
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("User-Agent", "dsgate")

	resp, err := s.http.Do(req)
	if err != nil {
		return availUnknown
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return availNo // the key is not valid at all
	}
	if resp.StatusCode != http.StatusOK {
		return availUnknown
	}
	var b struct {
		IsAvailable bool `json:"is_available"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&b) != nil {
		return availUnknown
	}
	if b.IsAvailable {
		return availYes
	}
	return availNo
}

// --- simple endpoints ---------------------------------------------------

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"uptime_sec": int(time.Since(s.started).Seconds()),
	})
}

func (s *Server) handleAdminHealth(w http.ResponseWriter, r *http.Request) {
	if !s.adminOK(r) {
		writeError(w, http.StatusNotFound, typeRejected, "not found")
		return
	}
	writeJSON(w, http.StatusOK, struct {
		quota.Health
		UpstreamAvailable bool `json:"upstream_available"`
		Keys              int  `json:"keys_active"`
	}{s.ledger.Health(), !s.upstreamDry.Load(), s.keys.Status(false).Active})
}

// Info is the unauthenticated description of the service, so a client can
// show a user what they are about to opt into before they opt into it.
type Info struct {
	Service   string         `json:"service"`
	Announce  string         `json:"announce,omitempty"`
	Model     string         `json:"model"`
	Limits    quota.UserCaps `json:"daily_limits"`
	MaxTokens int            `json:"max_tokens_per_request"`
	MaxBody   int64          `json:"max_request_bytes"`
	PoWBits   uint8          `json:"proof_of_work_bits"`
	Endpoints []string       `json:"endpoints"`
	Privacy   string         `json:"privacy"`
	Exhausted bool           `json:"service_exhausted"`
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	if !s.limitMeta(w, r) {
		return
	}
	h := s.ledger.Health()
	writeJSON(w, http.StatusOK, Info{
		Service:   "dsgate",
		Announce:  s.cfg.Announce,
		Model:     s.cfg.Model,
		Limits:    s.ledger.Status("", "anon").Limits,
		MaxTokens: s.cfg.MaxTokens,
		MaxBody:   s.cfg.MaxBodyBytes,
		PoWBits:   s.mint.Difficulty(s.clientIP(r)),
		Endpoints: []string{
			"/chat/completions", "/beta/completions",
			"/anthropic/v1/messages", "/responses", "/models",
		},
		Privacy:   s.privacyNotice(),
		Exhausted: h.TotalSpendUSD >= h.TotalBudgetUSD || s.upstreamDry.Load(),
	})
}

// privacyNotice is what a client shows a user before they opt in. It is
// built rather than fixed because it stopped being true the day a second
// upstream appeared: a free lane is a third party the user did not
// choose, and at least one of them — OpenCode Zen — says outright that
// prompts on its free models may be used to improve them.
//
// The gateway saying this, rather than the CLI hardcoding it, is the
// point. The CLI cannot know which upstreams a given gateway uses, and a
// consent notice that is a guess about someone else's deployment is not
// consent.
func (s *Server) privacyNotice() string {
	const base = "prompts and completions are relayed to DeepSeek and are not stored or logged by this gateway; only token counts and cost are recorded"
	if s.free == nil {
		return base
	}
	return base + ". Chat requests are first offered to a third-party free upstream, whose provider may use them to improve its models; send nothing confidential, or bring your own key"
}
