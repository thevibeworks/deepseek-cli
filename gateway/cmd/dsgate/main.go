// Command dsgate is the free-tier gateway for the deepseek CLI: a
// policy-enforcing proxy that lets people use the DeepSeek API before
// they have an API key.
//
// It holds one real key, meters every request, and stops when the day's
// budget is gone. See gateway/DESIGN.md for why it is shaped this way.
//
// Run it with nothing but an upstream key:
//
//	DSGATE_UPSTREAM_KEY=sk-... dsgate
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/thevibeworks/deepseek-cli/gateway/internal/mint"
	"github.com/thevibeworks/deepseek-cli/gateway/internal/quota"
	"github.com/thevibeworks/deepseek-cli/gateway/internal/server"
	"github.com/thevibeworks/deepseek-cli/gateway/internal/token"
)

// version is set at build time via -ldflags.
var version = "dev"

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	log.SetPrefix("dsgate ")

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "-v", "--version", "version":
			fmt.Println("dsgate " + version)
			return
		case "-h", "--help", "help":
			fmt.Print(usage)
			return
		}
	}

	if err := run(); err != nil {
		log.Fatalf("fatal: %v", err)
	}
}

const usage = `dsgate — the free tier for the deepseek CLI

Configuration is entirely environment variables:

  DSGATE_UPSTREAM_KEY        DeepSeek API key to spend        (required)
  DSGATE_UPSTREAM_KEYS       more keys, comma separated; the pool rotates
  DSGATE_UPSTREAM_BASE_URL   upstream root                    (https://api.deepseek.com)
  DSGATE_ADDR                listen address                   (:8787)
  DSGATE_STATE_DIR           journal, secret, revocations     (./state)
  DSGATE_SECRET              token signing secret, hex        (generated and persisted)
  DSGATE_MODEL               the only model served            (deepseek-v4-flash)
  DSGATE_ANNOUNCE            public URL, shown to clients

Per-user daily limits:

  DSGATE_ANON_DAILY_REQUESTS       (30)
  DSGATE_ANON_DAILY_INPUT_TOKENS   (60000)
  DSGATE_ANON_DAILY_OUTPUT_TOKENS  (20000)
  DSGATE_ANON_DAILY_SEARCHES       (3)      server-side web searches per user
  DSGATE_ANON_MAX_TOKENS           (4096)    per-request output cap
  DSGATE_MAX_BODY_BYTES            (131072)  per-request body cap
  DSGATE_REQUESTS_PER_MINUTE       (20)      per-address burst
  DSGATE_SUBJECT_REQUESTS_PER_MINUTE (6)     per-token burst
  DSGATE_SUBJECT_INFLIGHT          (2)       per-token concurrency
  DSGATE_TOKEN_TTL_DAYS            (7)       token lifetime; 0 = never expires

Service limits — these are what actually bound the spend:

  DSGATE_DAILY_BUDGET_USD    (1.00)   circuit breaker, resets 00:00 UTC
  DSGATE_TOTAL_BUDGET_USD    (20.00)  the credit pool
  DSGATE_MAX_INFLIGHT        (8)
  DSGATE_BALANCE_CHECK_MINUTES (15)   poll upstream /user/balance; 0 = off

Anti-abuse:

  DSGATE_POW_BITS            (20)  first mint costs ~1s of one core
  DSGATE_MINT_DAILY_PER_IP   (3)   beyond this, difficulty escalates
  DSGATE_TRUST_PROXY         (false)  set only behind a proxy you control

Operations:

  DSGATE_ORIGINS       comma-separated CORS allowlist for the playground
  DSGATE_ADMIN_TOKEN   enables GET /admin/health with X-Admin-Token

SIGHUP re-reads <state>/revoked.txt without dropping connections.
`

func run() error {
	// One key or many. The pool is what lets a donated key extend the
	// service without a restart, and what lets an emptied one retire
	// itself instead of taking the whole free tier down with it.
	keys := envList("DSGATE_UPSTREAM_KEYS")
	if k := env("DSGATE_UPSTREAM_KEY", os.Getenv("DEEPSEEK_API_KEY")); k != "" {
		keys = append([]string{k}, keys...)
	}
	if len(keys) == 0 {
		return errors.New("DSGATE_UPSTREAM_KEY is not set; there is nothing to spend")
	}

	stateDir := env("DSGATE_STATE_DIR", "./state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}

	secret, err := loadSecret(stateDir)
	if err != nil {
		return err
	}
	signer, err := token.NewSigner(secret)
	if err != nil {
		return err
	}

	limits := quota.Limits{
		DailyRequests:     envInt("DSGATE_ANON_DAILY_REQUESTS", 30),
		DailyInputTokens:  envInt("DSGATE_ANON_DAILY_INPUT_TOKENS", 60000),
		DailyOutputTokens: envInt("DSGATE_ANON_DAILY_OUTPUT_TOKENS", 20000),
		DailySearches:     envInt("DSGATE_ANON_DAILY_SEARCHES", 3),
		DailyBudgetUSD:    envFloat("DSGATE_DAILY_BUDGET_USD", 1.00),
		TotalBudgetUSD:    envFloat("DSGATE_TOTAL_BUDGET_USD", 20.00),
	}
	// A NaN budget compares false against everything, which would make
	// every admission check pass forever. Money limits have to be numbers.
	if math.IsNaN(limits.DailyBudgetUSD) || math.IsInf(limits.DailyBudgetUSD, 0) ||
		math.IsNaN(limits.TotalBudgetUSD) || math.IsInf(limits.TotalBudgetUSD, 0) ||
		limits.DailyBudgetUSD < 0 || limits.TotalBudgetUSD < 0 {
		return errors.New("DSGATE_DAILY_BUDGET_USD and DSGATE_TOTAL_BUDGET_USD must be finite, non-negative numbers")
	}
	ledger, err := quota.Open(filepath.Join(stateDir, "ledger"), limits)
	if err != nil {
		return err
	}
	defer ledger.Close()

	m := mint.New(signer, mint.Config{
		BaseBits:  uint8(envInt("DSGATE_POW_BITS", 20)),
		FreeMints: envInt("DSGATE_MINT_DAILY_PER_IP", 3),
		TTL:       5 * time.Minute,
		StatePath: filepath.Join(stateDir, "mint.json"),
	})

	cfg := server.Config{
		UpstreamBaseURL:          env("DSGATE_UPSTREAM_BASE_URL", "https://api.deepseek.com"),
		UpstreamKeys:             keys,
		KeyStatePath:             filepath.Join(stateDir, "donated-keys.json"),
		Model:                    env("DSGATE_MODEL", "deepseek-v4-flash"),
		Version:                  version,
		MaxBodyBytes:             int64(envInt("DSGATE_MAX_BODY_BYTES", 131072)),
		MaxTokens:                envInt("DSGATE_ANON_MAX_TOKENS", 4096),
		MaxInflight:              envInt("DSGATE_MAX_INFLIGHT", 8),
		RequestsPerMinute:        envInt("DSGATE_REQUESTS_PER_MINUTE", 20),
		SubjectRequestsPerMinute: envInt("DSGATE_SUBJECT_REQUESTS_PER_MINUTE", 6),
		SubjectInflight:          envInt("DSGATE_SUBJECT_INFLIGHT", 2),
		TokenTTL:                 time.Duration(envInt("DSGATE_TOKEN_TTL_DAYS", 7)) * 24 * time.Hour,
		TrustProxy:               envBool("DSGATE_TRUST_PROXY", false),
		Origins:                  envList("DSGATE_ORIGINS"),
		AdminToken:               os.Getenv("DSGATE_ADMIN_TOKEN"),
		Announce:                 os.Getenv("DSGATE_ANNOUNCE"),
	}

	gw := server.New(cfg, signer, m, ledger)
	srv := &http.Server{
		Addr:    env("DSGATE_ADDR", ":8787"),
		Handler: gw.Handler(),
		// DeepSeek documents holding a request up to ten minutes before
		// inference starts, so anything shorter here would manufacture
		// failures out of normal slow starts. The header timeout stays
		// short because a client that has not finished its headers is not
		// waiting on a model.
		ReadHeaderTimeout: 15 * time.Second,
		ReadTimeout:       15 * time.Minute,
		WriteTimeout:      15 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}

	// SIGHUP re-reads the revocation list. Doing it without a restart
	// matters: a restart drops every stream in flight, and cutting off
	// one abusive token should not interrupt everyone else's answer.
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for range hup {
			if err := ledger.LoadRevocations(); err != nil {
				log.Printf("reloading revocations: %v", err)
			} else {
				log.Print("revocations reloaded")
			}
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The local ledger only knows what this gateway spent; the account can
	// empty underneath it. Checking the real balance keeps "we have
	// credit" honest.
	if mins := envInt("DSGATE_BALANCE_CHECK_MINUTES", 15); mins > 0 {
		gw.StartBalanceWatch(ctx, time.Duration(mins)*time.Minute)
	}

	go func() {
		<-ctx.Done()
		log.Print("shutting down")
		shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()

	h := ledger.Health()
	log.Printf("version %s listening on %s", version, srv.Addr)
	log.Printf("upstream %s, model %s", cfg.UpstreamBaseURL, cfg.Model)
	log.Printf("budget: $%.2f/day, $%.2f total, $%.4f spent so far",
		limits.DailyBudgetUSD, limits.TotalBudgetUSD, h.TotalSpendUSD)
	if !cfg.TrustProxy {
		log.Print("X-Forwarded-For is NOT trusted; set DSGATE_TRUST_PROXY=1 only behind a proxy you control")
	}

	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// loadSecret reads the token signing secret, generating and persisting
// one on first run.
//
// Persisting matters more than it looks: the secret is the only thing
// that makes an outstanding token verifiable, so a gateway that
// regenerated it on restart would silently invalidate every user's
// enrolment on every deploy.
func loadSecret(stateDir string) ([]byte, error) {
	if v := os.Getenv("DSGATE_SECRET"); v != "" {
		b, err := hex.DecodeString(strings.TrimSpace(v))
		if err != nil {
			return nil, fmt.Errorf("DSGATE_SECRET is not hex: %w", err)
		}
		if len(b) < 32 {
			return nil, fmt.Errorf("DSGATE_SECRET is %d bytes, need at least 32", len(b))
		}
		return b, nil
	}

	path := filepath.Join(stateDir, "secret")
	if b, err := os.ReadFile(path); err == nil {
		if s, err := hex.DecodeString(strings.TrimSpace(string(b))); err == nil && len(s) >= 32 {
			return s, nil
		}
		return nil, fmt.Errorf("%s exists but is not a 32-byte hex secret; move it aside to rotate", path)
	}

	s := make([]byte, 32)
	if _, err := rand.Read(s); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(s)+"\n"), 0o600); err != nil {
		return nil, err
	}
	log.Printf("generated a new signing secret at %s", path)
	return s, nil
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
		log.Printf("%s=%q is not a number; using %d", key, v, fallback)
	}
	return fallback
}

func envFloat(key string, fallback float64) float64 {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
		log.Printf("%s=%q is not a number; using %.2f", key, v, fallback)
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		b, err := strconv.ParseBool(v)
		if err == nil {
			return b
		}
	}
	return fallback
}

func envList(key string) []string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
