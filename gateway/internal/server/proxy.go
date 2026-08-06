package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/thevibeworks/deepseek-cli/gateway/internal/meter"
	"github.com/thevibeworks/deepseek-cli/gateway/internal/mint"
	"github.com/thevibeworks/deepseek-cli/gateway/internal/policy"
	"github.com/thevibeworks/deepseek-cli/gateway/internal/quota"
)

// Response headers carrying what is left of the caller's day. They are
// set from the snapshot taken just before the request is forwarded, so
// they do not include the request they are attached to.
const (
	headerRequestsLeft = "X-Free-Requests-Remaining"
	headerInputLeft    = "X-Free-Input-Tokens-Remaining"
	headerOutputLeft   = "X-Free-Output-Tokens-Remaining"
	headerResets       = "X-Free-Resets-At"
)

// maxMeterBuffer bounds how much of a non-streamed response is held in
// order to read its usage. A chat completion is a few kilobytes; nothing
// legitimate approaches this.
const maxMeterBuffer = 4 << 20

func (s *Server) handleProxy(w http.ResponseWriter, r *http.Request) {
	route, ok := policy.Lookup(r.Method, r.URL.Path)
	if !ok {
		writeError(w, http.StatusNotFound, typeRejected, fmt.Sprintf(
			"the free tier does not carry %s %s — it serves /chat/completions, /beta/completions, /anthropic/v1/messages, /responses and /models",
			r.Method, r.URL.Path))
		return
	}

	ip := s.clientIP(r)
	if ok, wait := s.limiter.Allow("api:" + mint.RequestBucket(ip)); !ok {
		retryAfter(w, wait)
		writeError(w, http.StatusTooManyRequests, typeQuota,
			fmt.Sprintf("slow down — retry in %s", wait.Round(time.Second)))
		return
	}

	tok, err := s.authenticate(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, typeAuth, err.Error())
		return
	}
	subject := tok.Subject.String()

	// The subject's own valves, independent of the address ones: a token
	// used from many addresses and many tokens behind one address are
	// different attacks.
	if ok, wait := s.subjLimiter.Allow(subject); !ok {
		retryAfter(w, wait)
		writeError(w, http.StatusTooManyRequests, typeQuota,
			fmt.Sprintf("this token is sending too fast — retry in %s", wait.Round(time.Second)))
		return
	}
	if !s.acquireSubject(subject) {
		w.Header().Set("Retry-After", "2")
		writeError(w, http.StatusTooManyRequests, typeQuota,
			fmt.Sprintf("this token already has %d request(s) in flight — wait for one to finish", s.cfg.SubjectInflight))
		return
	}
	defer s.releaseSubject(subject)

	// Counted here, once the caller is known to be real and admitted. The
	// country arrives already reduced to two letters by the edge; no
	// address reaches the collector. See package stats.
	s.stats.Seen(subject, edgeCountry(r), route.Name)

	// The model list barely changes and is deliberately uncharged, so it
	// is answered from a short cache when possible — otherwise the one
	// free endpoint would burn in-flight slots and upstream round trips.
	if route.Name == "models" {
		if body, ok := s.cachedModels(); ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write(body)
			return
		}
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.cfg.MaxBodyBytes))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, typeRejected, fmt.Sprintf(
			"request body is larger than the free tier's %d byte limit — bring your own key for prompts this size",
			s.cfg.MaxBodyBytes))
		return
	}

	decision, err := policy.Apply(route, body, subject, policy.Limits{
		MaxTokens: s.cfg.MaxTokens,
		Model:     s.cfg.Model,
	})
	if err != nil {
		var rej *policy.Reject
		msg := err.Error()
		if errors.As(err, &rej) && rej.Hint != "" {
			msg += " — " + rej.Hint
		}
		writeError(w, http.StatusBadRequest, typeRejected, msg)
		return
	}

	// Routes that cannot generate tokens are not charged. That keeps
	// `deepseek status` free and safe in a loop against the free tier,
	// exactly as it is against the real API.
	billable := route.Format != policy.FormatNone
	var reserve float64
	if billable {
		if s.upstreamDry.Load() {
			// DeepSeek says the account is unusable. The local ledger's
			// opinion is irrelevant; honest 402 beats a confusing relay of
			// upstream's insufficient-balance error.
			s.writeLimit(w, &quota.LimitError{Reason: quota.ReasonCredits})
			return
		}
		// The worst this request could cost is reserved before it is
		// forwarded. This is what makes the budget a ceiling: without the
		// reservation, every in-flight request is unbilled and the breaker
		// only notices after the money is spent.
		reserve = meter.Estimate(decision.Model, len(decision.Body), decision.MaxTokens)
		if err := s.ledger.Admit(subject, reserve); err != nil {
			s.writeLimit(w, err)
			return
		}
	}

	status := s.ledger.Status(subject, tok.Tier.String())
	setQuotaHeaders(w, status)

	if err := s.acquire(r); err != nil {
		if billable {
			s.ledger.Refund(subject, reserve)
		}
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusServiceUnavailable, typeQuota,
			"the free tier is busy; retry shortly")
		return
	}
	defer func() { <-s.inflight }()

	s.stats.InFlight(1)
	defer s.stats.InFlight(-1)

	s.forward(w, r, route, decision, subject, billable, reserve)
}

// edgeCountry reads the two-letter country the CDN attached to this
// request. Cloudflare sets CF-IPCountry; other edges use the same idea
// under a different name.
//
// It is only meaningful behind a proxy we control — the same condition
// that makes X-Forwarded-For trustworthy — because a client can otherwise
// set it to anything. A wrong country on a histogram is harmless, but
// counting one we did not derive ourselves would be a lie about where the
// number came from, so it follows TrustProxy.
func edgeCountry(r *http.Request) string {
	c := r.Header.Get("CF-IPCountry")
	if c == "" {
		c = r.Header.Get("X-Country-Code")
	}
	c = strings.ToUpper(strings.TrimSpace(c))
	if len(c) != 2 || c == "XX" || c == "T1" {
		return ""
	}
	return c
}

// acquire takes an in-flight slot, or gives up.
//
// The cap exists twice over: it keeps a 1 GiB box from being asked to
// hold hundreds of ten-minute connections, and it bounds how far the
// budget can overshoot, since every admitted request is unbilled until
// it finishes.
func (s *Server) acquire(r *http.Request) error {
	select {
	case s.inflight <- struct{}{}:
		return nil
	case <-r.Context().Done():
		return r.Context().Err()
	case <-time.After(30 * time.Second):
		return errors.New("busy")
	}
}

func (s *Server) forward(w http.ResponseWriter, r *http.Request, route policy.Route, d *policy.Decision, subject string, billable bool, reserve float64) {
	url := strings.TrimRight(s.cfg.UpstreamBaseURL, "/") + route.Upstream

	var payload io.Reader
	if route.Method == http.MethodPost {
		payload = bytes.NewReader(d.Body)
	}
	up, err := http.NewRequestWithContext(r.Context(), route.Method, url, payload)
	if err != nil {
		if billable {
			s.ledger.Refund(subject, reserve)
		}
		writeError(w, http.StatusInternalServerError, typeInternal, "could not build the upstream request")
		return
	}

	// Only headers this gateway chose reach DeepSeek. Nothing the client
	// sent is forwarded except the media types, because a header we did
	// not think about is a header we cannot vouch for — and because our
	// key is on this request.
	if payload != nil {
		up.Header.Set("Content-Type", "application/json")
	}
	if accept := r.Header.Get("Accept"); accept != "" {
		up.Header.Set("Accept", accept)
	}
	secret, fingerprint, err := s.keys.Next()
	if err != nil {
		if billable {
			s.ledger.Refund(subject, reserve)
		}
		s.writeLimit(w, &quota.LimitError{Reason: quota.ReasonCredits})
		return
	}
	up.Header.Set("Authorization", "Bearer "+secret)
	if route.AnthropicAuth {
		up.Header.Set("x-api-key", secret)
		version := r.Header.Get("anthropic-version")
		if version == "" {
			version = "2023-06-01"
		}
		up.Header.Set("anthropic-version", version)
	}
	up.Header.Set("User-Agent", "dsgate")

	resp, err := s.http.Do(up)
	if err != nil {
		if r.Context().Err() != nil {
			// The caller hung up before upstream answered. The prompt very
			// likely reached DeepSeek, and whether they bill the aborted
			// prefill is undocumented — so this is charged as an input-side
			// estimate rather than refunded. Refunding here was a drain: an
			// attacker could send-and-abort in a loop and the ledger would
			// record nothing while our key paid for every prefill.
			if billable {
				cost := meter.Cost(d.Model, meter.Usage{InputTokens: len(d.Body) + 1})
				s.ledger.Charge(subject, route.Name, d.Model, len(d.Body)/4+1, 0, 0, cost, reserve, true)
				s.stats.Charged(len(d.Body)/4+1, 0)
			}
			return // there is nobody to tell
		}
		// Never reached the model, so it cost nothing and the caller keeps
		// their request allowance.
		if billable {
			s.ledger.Refund(subject, reserve)
		}
		writeError(w, http.StatusBadGateway, typeUpstream, "could not reach DeepSeek: "+err.Error())
		return
	}
	defer resp.Body.Close()

	// A key that upstream refuses for money or validity is done, and
	// leaves the rotation now rather than after it has failed everyone
	// else's request too. Other 4xx are about the request, not the key.
	switch resp.StatusCode {
	case http.StatusPaymentRequired:
		s.keys.MarkDry(fingerprint, "DeepSeek answered 402: out of credit")
		s.invalidateStatus()
	case http.StatusUnauthorized, http.StatusForbidden:
		s.keys.MarkDry(fingerprint, "DeepSeek rejected this key: "+resp.Status)
		s.invalidateStatus()
	}

	if route.Name == "models" && resp.StatusCode == http.StatusOK {
		s.relayModels(w, resp)
		return
	}

	streaming := strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream")

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	// Length is deliberately not copied: the body is relayed as it
	// arrives, and a declared length that a dropped upstream connection
	// then fails to deliver is worse than no length at all.
	w.WriteHeader(resp.StatusCode)

	sniffer := &meter.Sniffer{}
	var buffered bytes.Buffer
	var tee io.Writer = sniffer
	if !streaming {
		tee = &capWriter{buf: &buffered, limit: maxMeterBuffer}
	}
	relay(w, resp.Body, tee)

	if !billable {
		return
	}

	// Upstream refusals that the caller could not have provoked cost
	// nothing and are refunded. A 4xx is the caller's own request coming
	// back, and is not refunded: refunding on client-controlled failures
	// would turn the request counter into an unlimited retry loop.
	upstreamFault := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500

	var usage meter.Usage
	var model string
	if streaming {
		usage, model = sniffer.Result()
	} else {
		usage, model = meter.FromBody(buffered.Bytes())
	}
	if model == "" {
		model = d.Model
	}

	if !usage.Found {
		if upstreamFault || resp.StatusCode >= 400 {
			if upstreamFault {
				// No tokens were generated and the fault was not the
				// caller's: give everything back.
				s.ledger.Refund(subject, reserve)
			} else {
				// The caller's own 4xx keeps its request debit, but the
				// money reserved for it goes back to the pool.
				s.ledger.Release(reserve)
			}
			return
		}
		// A 2xx we could not read. Charge the whole reservation — the most
		// it could have cost. Unbillable must never mean free, or it
		// becomes the way in.
		s.ledger.Charge(subject, route.Name, model, len(d.Body)/4+1, 0, d.MaxTokens, reserve, reserve, true)
		s.stats.Charged(len(d.Body)/4+1, d.MaxTokens)
		return
	}

	s.ledger.Charge(subject, route.Name, model,
		usage.InputTokens, usage.CacheHitTokens, usage.OutputTokens,
		meter.Cost(model, usage), reserve, false)
	s.stats.Charged(usage.InputTokens, usage.OutputTokens)
}

// relayModels forwards the model list, minus the models this gateway
// will not serve.
//
// This is the one place the proxy edits a response, and it earns the
// exception: /models exists to answer "what can I use here", and through
// the free tier the honest answer is one model. Left unfiltered, any
// client that picks a model off this list has a 50% chance of choosing
// the one that is then refused.
//
// The call still goes upstream rather than being answered locally, so
// `deepseek status` keeps telling you whether DeepSeek itself is
// reachable — which is the question it exists to answer. Anything
// unparseable is passed through untouched.
func (s *Server) relayModels(w http.ResponseWriter, resp *http.Response) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadGateway, typeUpstream, "could not read the model list")
		return
	}

	var list struct {
		Object string           `json:"object"`
		Data   []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(raw, &list); err != nil || len(list.Data) == 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(raw)
		return
	}

	kept := list.Data[:0]
	for _, m := range list.Data {
		if id, _ := m["id"].(string); id == s.cfg.Model {
			kept = append(kept, m)
		}
	}
	list.Data = kept
	out, err := json.Marshal(list)
	if err != nil {
		out = raw
	} else {
		s.storeModels(out)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(out)
}

// modelsCacheTTL is short on purpose: /models doubles as `deepseek
// status`'s reachability probe, and a long cache would keep answering
// "up" after DeepSeek went down.
const modelsCacheTTL = 30 * time.Second

func (s *Server) cachedModels() ([]byte, bool) {
	s.modelsMu.Lock()
	defer s.modelsMu.Unlock()
	if s.modelsBody == nil || time.Since(s.modelsAt) > modelsCacheTTL {
		return nil, false
	}
	return s.modelsBody, true
}

func (s *Server) storeModels(body []byte) {
	s.modelsMu.Lock()
	s.modelsBody = body
	s.modelsAt = time.Now()
	s.modelsMu.Unlock()
}

// relay copies the upstream body to the client and to the meter,
// flushing as it goes.
//
// io.Copy would be shorter and wrong: it would let a streamed answer sit
// in a buffer until it filled, and DeepSeek's keep-alive traffic — empty
// lines on a non-streamed request, ": keep-alive" comments on a streamed
// one, for up to ten minutes before inference starts — exists precisely
// so that intermediaries do not time the connection out. An intermediary
// that then holds those bytes has defeated their purpose.
func relay(w http.ResponseWriter, src io.Reader, tee io.Writer) {
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}
	buf := make([]byte, 16<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			tee.Write(buf[:n])
			if _, werr := w.Write(buf[:n]); werr != nil {
				return // the caller hung up; upstream is still metered
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

// capWriter accumulates up to limit bytes and silently drops the rest.
// Dropping is safe here because the only reader is the meter, and a
// response too big to buffer is one whose usage object we will not find
// anyway — which the estimate path already handles.
type capWriter struct {
	buf   *bytes.Buffer
	limit int
}

func (c *capWriter) Write(p []byte) (int, error) {
	if room := c.limit - c.buf.Len(); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		c.buf.Write(p)
	}
	return len(p), nil
}

func (s *Server) writeLimit(w http.ResponseWriter, err error) {
	var lim *quota.LimitError
	if !errors.As(err, &lim) {
		writeError(w, http.StatusInternalServerError, typeInternal, err.Error())
		return
	}

	switch lim.Reason {
	case quota.ReasonCredits:
		// Setting a key is the whole fix: it takes precedence over the
		// enrolment automatically, so there is nothing to unset.
		writeError(w, http.StatusPaymentRequired, typeExhausted,
			"the free tier has run out of credit. Set DEEPSEEK_API_KEY to your own key — https://platform.deepseek.com/api_keys — and it will be used instead")
	case quota.ReasonRevoked:
		writeError(w, http.StatusForbidden, typeAuth,
			"this free-tier token has been revoked")
	case quota.ReasonUnavailable:
		// The ledger cannot record spend, so nothing is allowed to spend.
		// A 503 is honest: this is our outage, not the caller's quota.
		w.Header().Set("Retry-After", "30")
		writeError(w, http.StatusServiceUnavailable, typeInternal,
			"the free tier is temporarily unavailable; retry shortly")
	case quota.ReasonDailyBudget:
		retryAfter(w, lim.RetryAfter(time.Now()))
		writeError(w, http.StatusTooManyRequests, typeQuota,
			"the free tier has spent today's budget across all users. It resets at 00:00 UTC — or bring your own key: https://platform.deepseek.com/api_keys")
	default:
		retryAfter(w, lim.RetryAfter(time.Now()))
		writeError(w, http.StatusTooManyRequests, typeQuota, fmt.Sprintf(
			"%s. It resets at 00:00 UTC — or bring your own key: https://platform.deepseek.com/api_keys",
			lim.Error()))
	}
}

func setQuotaHeaders(w http.ResponseWriter, st quota.Status) {
	h := w.Header()
	h.Set(headerRequestsLeft, strconv.Itoa(max(0, st.Limits.Requests-st.Used.Requests)))
	h.Set(headerInputLeft, strconv.Itoa(max(0, st.Limits.InputTokens-st.Used.InputTokens)))
	h.Set(headerOutputLeft, strconv.Itoa(max(0, st.Limits.OutputTokens-st.Used.OutputTokens)))
	h.Set(headerResets, st.ResetsAt.UTC().Format(time.RFC3339))
}
