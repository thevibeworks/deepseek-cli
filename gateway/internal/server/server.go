// Package server is the HTTP surface of the free tier: the mint
// endpoints, the quota endpoints, and the proxy that carries everything
// else to DeepSeek.
package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/thevibeworks/deepseek-cli/gateway/internal/mint"
	"github.com/thevibeworks/deepseek-cli/gateway/internal/quota"
	"github.com/thevibeworks/deepseek-cli/gateway/internal/token"
)

// Config is everything the server needs to run. It is filled from the
// environment in cmd/dsgate and validated there, so this package never
// reads os.Getenv and stays testable.
type Config struct {
	// UpstreamBaseURL is DeepSeek's API root.
	UpstreamBaseURL string
	// UpstreamKey is our real API key. It never leaves this process.
	UpstreamKey string
	// Model is the only model the free tier serves.
	Model string

	MaxBodyBytes int64
	MaxTokens    int
	MaxInflight  int

	// RequestsPerMinute is the per-address burst limit, applied before
	// quota so that a runaway loop is cheap to refuse.
	RequestsPerMinute int

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

	http     *http.Client
	inflight chan struct{}
	limiter  *limiter

	origins map[string]bool
	started time.Time
}

func New(cfg Config, signer *token.Signer, m *mint.Mint, ledger *quota.Ledger) *Server {
	origins := map[string]bool{}
	for _, o := range cfg.Origins {
		origins[strings.TrimSuffix(strings.TrimSpace(o), "/")] = true
	}
	return &Server{
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
		},
		inflight: make(chan struct{}, cfg.MaxInflight),
		limiter:  newLimiter(cfg.RequestsPerMinute, time.Minute),
		origins:  origins,
		started:  time.Now(),
	}
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
	return t, nil
}

func (s *Server) clientIP(r *http.Request) string {
	return mint.ClientIP(r.RemoteAddr, r.Header.Get("X-Forwarded-For"), s.cfg.TrustProxy)
}

// --- simple endpoints ---------------------------------------------------

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"uptime_sec": int(time.Since(s.started).Seconds()),
	})
}

func (s *Server) handleAdminHealth(w http.ResponseWriter, r *http.Request) {
	if s.cfg.AdminToken == "" || r.Header.Get("X-Admin-Token") != s.cfg.AdminToken {
		writeError(w, http.StatusNotFound, typeRejected, "not found")
		return
	}
	writeJSON(w, http.StatusOK, s.ledger.Health())
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
		Privacy:   "prompts and completions are relayed to DeepSeek and are not stored or logged by this gateway; only token counts and cost are recorded",
		Exhausted: h.TotalSpendUSD >= h.TotalBudgetUSD,
	})
}
