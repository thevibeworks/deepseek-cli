package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Turnstile is the browser check on the mint's browser lane.
//
// Proof-of-work prices an identity in CPU, which is the right currency
// for the CLI — a shell has nothing else to offer. A browser has more:
// it can demonstrate it is a real browser under real use, which is what
// Cloudflare Turnstile attests, and a farm of headless enrollers fails
// that check long before it runs out of CPU. So the two lanes differ:
//
//   - No Origin header (the CLI, curl): proof-of-work alone, unchanged.
//   - Origin header present (a browser via CORS): proof-of-work AND a
//     Turnstile token, verified against Cloudflare before the solve is
//     honoured.
//
// The split is honest about what it defends: it hardens the playground
// path against browser automation without pretending to gate the API
// itself — a direct caller still pays proof-of-work into the same
// budgets, which remain the real boundary.
//
// The whole feature is off until TurnstileSecret is configured, so the
// gateway runs identically with an empty config.

// turnstileVerifyURL is Cloudflare's siteverify endpoint; Config may
// override it, which is how the tests stand in a fake Cloudflare.
const turnstileVerifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

// turnstileRequired says whether this request is on the browser lane.
// CORS preflight means every cross-origin browser call carries Origin;
// the CLI never sends one.
func (s *Server) turnstileRequired(r *http.Request) bool {
	return s.cfg.TurnstileSecret != "" && r.Header.Get("Origin") != ""
}

type turnstileVerdict struct {
	Success    bool     `json:"success"`
	ErrorCodes []string `json:"error-codes"`
}

// verifyTurnstile confirms a widget token with Cloudflare. It is called
// only after the proof-of-work has been redeemed, so an attacker cannot
// make this gateway spam Cloudflare without first paying CPU.
func (s *Server) verifyTurnstile(ctx context.Context, token, ip string) error {
	if token == "" {
		return errors.New("this browser did not complete the check")
	}
	endpoint := s.cfg.TurnstileURL
	if endpoint == "" {
		endpoint = turnstileVerifyURL
	}
	form := url.Values{
		"secret":   {s.cfg.TurnstileSecret},
		"response": {token},
		"remoteip": {ip},
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("could not build the verification request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	res, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach the verifier: %w", err)
	}
	defer res.Body.Close()

	var v turnstileVerdict
	if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
		return fmt.Errorf("unreadable verifier answer: %w", err)
	}
	if !v.Success {
		if len(v.ErrorCodes) > 0 {
			return fmt.Errorf("the browser check failed (%s)", strings.Join(v.ErrorCodes, ", "))
		}
		return errors.New("the browser check failed")
	}
	return nil
}
