package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/thevibeworks/deepseek-cli/gateway/internal/meter"
	"github.com/thevibeworks/deepseek-cli/gateway/internal/mint"
	"github.com/thevibeworks/deepseek-cli/gateway/internal/quota"
)

// limitMeta throttles the read-only endpoints — info, quota, balance.
// They cost no money, but unthrottled they are still free CPU and a
// probe surface, and every other endpoint already pays a toll.
func (s *Server) limitMeta(w http.ResponseWriter, r *http.Request) bool {
	ok, wait := s.limiter.Allow("meta:" + mint.RequestBucket(s.clientIP(r)))
	if !ok {
		retryAfter(w, wait)
		writeError(w, http.StatusTooManyRequests, typeQuota,
			fmt.Sprintf("slow down — retry in %s", wait.Round(time.Second)))
	}
	return ok
}

// challengeTTL must match the value the mint was built with; it is
// echoed to the client so a solver knows how long it has.
const challengeTTL = 5 * time.Minute

// ChallengeResponse is the puzzle.
//
// It describes its own rules rather than assuming the client already
// knows them. That is not politeness: the CLI and the browser playground
// are two independent solvers, and a wire format that carries its own
// spec is one that cannot drift out of sync with half its clients.
type ChallengeResponse struct {
	Challenge  string `json:"challenge"`
	Difficulty uint8  `json:"difficulty"`
	Algorithm  string `json:"algorithm"`
	Input      string `json:"input"`
	Rule       string `json:"rule"`
	ExpiresIn  int    `json:"expires_in"`
}

func (s *Server) handleChallenge(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	// Throttled by the same /48 bucket that difficulty escalates on. Keyed
	// on the raw address, one IPv6 /48 would be 65,536 independent
	// throttles — the exact fan-out MintBucket exists to prevent.
	if ok, wait := s.limiter.Allow("mint:" + mint.MintBucket(ip)); !ok {
		retryAfter(w, wait)
		writeError(w, http.StatusTooManyRequests, typeQuota,
			fmt.Sprintf("too many mint attempts; retry in %s", wait.Round(time.Second)))
		return
	}

	c, err := s.mint.Challenge(ip)
	if err != nil {
		writeError(w, http.StatusInternalServerError, typeInternal, "could not issue a challenge")
		return
	}
	writeJSON(w, http.StatusOK, ChallengeResponse{
		Challenge:  c.String,
		Difficulty: c.Difficulty,
		Algorithm:  "sha256-leading-zero-bits",
		Input:      "<challenge>:<nonce>",
		Rule:       "find a decimal nonce where sha256 of the ASCII string \"<challenge>:<nonce>\" begins with at least <difficulty> zero bits",
		ExpiresIn:  int(challengeTTL.Seconds()),
	})
}

// TokenRequest is a solved challenge.
type TokenRequest struct {
	Challenge string `json:"challenge"`
	// Nonce is a string because a uint64 does not survive a round trip
	// through JavaScript's number type, and the playground is a first
	// class client.
	Nonce string `json:"nonce"`
}

// TokenResponse is a minted credential.
type TokenResponse struct {
	Token   string       `json:"token"`
	Subject string       `json:"subject"`
	Tier    string       `json:"tier"`
	Quota   quota.Status `json:"quota"`
}

func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if ok, wait := s.limiter.Allow("mint:" + mint.MintBucket(ip)); !ok {
		retryAfter(w, wait)
		writeError(w, http.StatusTooManyRequests, typeQuota,
			fmt.Sprintf("too many mint attempts; retry in %s", wait.Round(time.Second)))
		return
	}

	var req TokenRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, typeRejected, "could not read the solution: "+err.Error())
		return
	}
	nonce, err := strconv.ParseUint(req.Nonce, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, typeRejected, "nonce must be a decimal integer")
		return
	}

	t, err := s.mint.Redeem(ip, req.Challenge, nonce)
	if err != nil {
		writeError(w, http.StatusBadRequest, typeRejected, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, TokenResponse{
		Token:   t.String,
		Subject: t.Subject.String(),
		Tier:    t.Tier.String(),
		Quota:   s.ledger.Status(t.Subject.String(), t.Tier.String()),
	})
}

func (s *Server) handleQuota(w http.ResponseWriter, r *http.Request) {
	if !s.limitMeta(w, r) {
		return
	}
	t, err := s.authenticate(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, typeAuth, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.ledger.Status(t.Subject.String(), t.Tier.String()))
}

// balanceResponse mirrors DeepSeek's GET /user/balance shape.
//
// The endpoint is answered locally, never proxied: upstream would report
// *our* account's balance, which is not the caller's business and would
// tell an attacker exactly how much there is left to drain.
//
// What it reports instead is what the caller's own remaining free quota
// is worth at the published rate card — a real number, in the shape every
// DeepSeek client already parses, so `deepseek balance` and `deepseek
// check` keep working against the free tier. The x_free_tier block
// carries the figures that actually govern, because a dollar value alone
// would imply a wallet the user does not have.
type balanceResponse struct {
	IsAvailable  bool          `json:"is_available"`
	BalanceInfos []balanceInfo `json:"balance_infos"`
	FreeTier     *quota.Status `json:"x_free_tier"`
	Note         string        `json:"x_note"`
}

type balanceInfo struct {
	Currency        string `json:"currency"`
	TotalBalance    string `json:"total_balance"`
	GrantedBalance  string `json:"granted_balance"`
	ToppedUpBalance string `json:"topped_up_balance"`
}

func (s *Server) handleBalance(w http.ResponseWriter, r *http.Request) {
	if !s.limitMeta(w, r) {
		return
	}
	t, err := s.authenticate(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, typeAuth, err.Error())
		return
	}
	st := s.ledger.Status(t.Subject.String(), t.Tier.String())

	// Value the remaining allowance at the model's published rates.
	remainingIn := max(0, st.Limits.InputTokens-st.Used.InputTokens)
	remainingOut := max(0, st.Limits.OutputTokens-st.Used.OutputTokens)
	worth := meter.Cost(s.cfg.Model, meter.Usage{InputTokens: remainingIn, OutputTokens: remainingOut})
	if st.Exhausted || st.Used.Requests >= st.Limits.Requests {
		worth = 0
	}
	amount := strconv.FormatFloat(worth, 'f', 4, 64)

	writeJSON(w, http.StatusOK, balanceResponse{
		IsAvailable: worth > 0,
		BalanceInfos: []balanceInfo{{
			Currency: "USD", TotalBalance: amount,
			GrantedBalance: amount, ToppedUpBalance: "0.00",
		}},
		FreeTier: &st,
		Note:     "free tier: this is what today's remaining quota is worth at published rates, not an account balance",
	})
}

func retryAfter(w http.ResponseWriter, d time.Duration) {
	if d <= 0 {
		return
	}
	secs := int(d.Seconds())
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
}
