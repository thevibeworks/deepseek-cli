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
	adm := quota.Admission{Search: decision.Search}
	if billable {
		// Whether this particular request could be carried for nothing.
		// It decides two things below: whether an empty credit pool is
		// fatal, and whether the money ceilings apply at all.
		freeLane := s.free != nil && s.free.serves(route, decision) && s.free.keys.Usable()

		if s.upstreamDry.Load() {
			// DeepSeek says the account is unusable. The local ledger's
			// opinion is irrelevant; honest 402 beats a confusing relay of
			// upstream's insufficient-balance error — unless the free lane
			// can carry this request, in which case it is bound to it, so
			// that nothing falls back onto the dead account.
			if !freeLane {
				s.writeLimit(w, &quota.LimitError{Reason: quota.ReasonCredits})
				return
			}
			adm.Free = true
		}
		// The worst this request could cost is reserved before it is
		// forwarded. This is what makes the budget a ceiling: without the
		// reservation, every in-flight request is unbilled and the breaker
		// only notices after the money is spent.
		if !adm.Free {
			adm.ReserveUSD = meter.Estimate(decision.Model, len(decision.Body), decision.MaxTokens, decision.Search)
		}
		if err := s.ledger.Admit(subject, adm); err != nil {
			// Out of money, but this request need not cost any: retry the
			// admission bound to the free upstream, which cannot spend.
			// Only the money refusals are survivable this way — a per-user
			// limit or a revocation means no, on every lane.
			if !freeLane || !isMoneyLimit(err) {
				s.writeLimit(w, err)
				return
			}
			adm = quota.Admission{Search: decision.Search, Free: true}
			if err := s.ledger.Admit(subject, adm); err != nil {
				s.writeLimit(w, err)
				return
			}
		}
	}

	status := s.ledger.Status(subject, tok.Tier.String())
	setQuotaHeaders(w, status)

	if err := s.acquire(r); err != nil {
		if billable {
			s.ledger.Refund(subject, adm)
		}
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusServiceUnavailable, typeQuota,
			"the free tier is busy; retry shortly")
		return
	}
	defer func() { <-s.inflight }()

	s.stats.InFlight(1)
	defer s.stats.InFlight(-1)

	s.forward(w, r, route, decision, subject, billable, adm)
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

func (s *Server) forward(w http.ResponseWriter, r *http.Request, route policy.Route, d *policy.Decision, subject string, billable bool, adm quota.Admission) {
	reserve := adm.ReserveUSD

	lanes := s.lanesFor(route, d)
	if adm.Free {
		// This request was admitted past the money ceilings on the promise
		// that it cannot spend. The promise is kept here and nowhere else:
		// it is offered exactly one upstream, the free one.
		//
		// Admission only sets Free when the lane exists, so the nil check
		// is unreachable — and it is here anyway, because the failure it
		// would prevent is a panic in the proxy path and the failure it
		// replaces is a 402 nobody was going to be served past.
		if s.free == nil {
			s.ledger.Refund(subject, adm)
			s.writeLimit(w, &quota.LimitError{Reason: quota.ReasonCredits})
			return
		}
		lanes = []*lane{s.free}
	}

	var resp *http.Response
	var served *lane
	var lastErr error
	for i, ln := range lanes {
		last := i == len(lanes)-1
		got, err := s.attempt(r, ln, route, d)
		if err != nil {
			lastErr = err
			// A caller who hung up is not a lane failure, and retrying on
			// their behalf would spend money on an answer nobody will read.
			if last || r.Context().Err() != nil {
				break
			}
			s.freeFellBack.Add(1)
			continue
		}
		// Falling back has to happen here, before a single byte of this
		// response reaches the client — past that point the status line is
		// spent and the only honest thing left is to relay what came back.
		// Any non-2xx from the free lane is worth one paid retry: its 429
		// is its own rate limiter rather than the caller's fault, and a
		// route or a parameter it does not implement is exactly the
		// difference the paid lane exists to cover.
		if !last && (got.StatusCode < 200 || got.StatusCode > 299) {
			drain(got)
			s.freeFellBack.Add(1)
			continue
		}
		resp, served = got, ln
		break
	}
	if resp == nil {
		s.forwardFailed(w, r, lastErr, subject, route, d, billable, adm)
		return
	}
	defer resp.Body.Close()
	if served.free {
		s.freeServed.Add(1)
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
				s.ledger.Refund(subject, adm)
			} else {
				// The caller's own 4xx keeps its request debit, but the
				// money reserved for it goes back to the pool.
				s.ledger.Release(reserve)
			}
			return
		}
		// A 2xx we could not read. Charge the whole reservation — the most
		// it could have cost. Unbillable must never mean free, or it
		// becomes the way in. Unless the lane that answered charges us
		// nothing, in which case the most it could have cost is nothing:
		// the tokens are still recorded against the caller's day, and the
		// reservation goes back to the pool.
		s.ledger.Charge(subject, route.Name, model, len(d.Body)/4+1, 0, d.MaxTokens,
			laneCost(served, reserve), reserve, true)
		s.stats.Charged(len(d.Body)/4+1, d.MaxTokens)
		return
	}

	s.ledger.Charge(subject, route.Name, model,
		usage.InputTokens, usage.CacheHitTokens, usage.OutputTokens,
		laneCost(served, meter.Cost(model, usage)), reserve, false)
	s.stats.Charged(usage.InputTokens, usage.OutputTokens)
}

// laneCost is what a settled request actually costs the credit pool: what
// it was metered at, or nothing at all if the upstream that answered is
// free. The tokens are recorded either way — the per-user allowance is
// abuse control, and abuse is not free just because the tokens are.
func laneCost(ln *lane, metered float64) float64 {
	if ln != nil && ln.free {
		return 0
	}
	return metered
}

// errNoLaneKeys and errLaneRequest separate the two ways an attempt can
// fail before it reaches the network, because they mean different things
// to the caller: one is "we have nothing to spend", the other is a bug.
var (
	errNoLaneKeys  = errors.New("no usable key")
	errLaneRequest = errors.New("could not build the upstream request")
)

// attempt sends one request to one upstream and hands back whatever came
// out. It does not look at the status code beyond the bookkeeping a key
// pool needs: deciding what a status means is the caller's job, because
// only the caller knows whether another lane is left to try.
func (s *Server) attempt(r *http.Request, ln *lane, route policy.Route, d *policy.Decision) (*http.Response, error) {
	body := d.Body
	if ln.model != "" && route.Format != policy.FormatNone {
		b, err := policy.Retarget(d.Body, ln.model)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", errLaneRequest, err)
		}
		body = b
	}

	var payload io.Reader
	if route.Method == http.MethodPost {
		payload = bytes.NewReader(body)
	}
	url := strings.TrimRight(ln.baseURL, "/") + route.Upstream
	up, err := http.NewRequestWithContext(r.Context(), route.Method, url, payload)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errLaneRequest, err)
	}

	// Only headers this gateway chose reach upstream. Nothing the client
	// sent is forwarded except the media types, because a header we did
	// not think about is a header we cannot vouch for — and because our
	// key is on this request.
	if payload != nil {
		up.Header.Set("Content-Type", "application/json")
	}
	if accept := r.Header.Get("Accept"); accept != "" {
		up.Header.Set("Accept", accept)
	}
	secret, fingerprint, err := ln.keys.Next()
	if err != nil {
		return nil, errNoLaneKeys
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

	sent := time.Now()
	resp, err := s.http.Do(up)

	// The upstream health series is about DeepSeek specifically — it
	// exists to answer "is this you or them" — so the free lane stays out
	// of it. Mixing the two would report a shared free pool's ordinary
	// 429s as a DeepSeek outage. The free lane's own health is its
	// served/fell-back counters, which say the thing that matters about
	// it: how much of the work it is actually taking.
	if !ln.free {
		switch {
		case err != nil:
			// A caller who hung up is not an upstream fault and is not
			// recorded as one; anything else is.
			if r.Context().Err() == nil {
				s.stats.Upstream("unreachable", true, 0)
			}
		default:
			// One observation per round trip, and only upstream's own
			// failures count as faults: a 4xx is the caller's request
			// coming back.
			s.stats.Upstream(strconv.Itoa(resp.StatusCode),
				resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500,
				time.Since(sent))
		}
	}
	if err != nil {
		return nil, err
	}

	// A key upstream refuses for money or validity is done, and leaves the
	// rotation now rather than after it has failed everyone else's request
	// too. Other 4xx are about the request, not the key — including 429,
	// which on the free lane is the normal state of a shared pool and
	// would otherwise retire a perfectly good key on its first busy
	// minute.
	switch resp.StatusCode {
	case http.StatusPaymentRequired:
		ln.keys.MarkDry(fingerprint, ln.label()+" answered 402: out of credit")
		s.invalidateStatus()
	case http.StatusUnauthorized, http.StatusForbidden:
		ln.keys.MarkDry(fingerprint, ln.label()+" rejected this key: "+resp.Status)
		s.invalidateStatus()
	}
	return resp, nil
}

// forwardFailed settles a request that never got a response out of any
// lane. It is the old single-upstream error path, unchanged in what it
// decides — only in how many upstreams had to fail first.
func (s *Server) forwardFailed(w http.ResponseWriter, r *http.Request, err error, subject string, route policy.Route, d *policy.Decision, billable bool, adm quota.Admission) {
	switch {
	case r.Context().Err() != nil:
		// The caller hung up before upstream answered. The prompt very
		// likely reached DeepSeek, and whether they bill the aborted
		// prefill is undocumented — so this is charged as an input-side
		// estimate rather than refunded. Refunding here was a drain: an
		// attacker could send-and-abort in a loop and the ledger would
		// record nothing while our key paid for every prefill.
		if billable {
			cost := meter.Cost(d.Model, meter.Usage{InputTokens: len(d.Body) + 1})
			if adm.Free {
				cost = 0
			}
			s.ledger.Charge(subject, route.Name, d.Model, len(d.Body)/4+1, 0, 0, cost, adm.ReserveUSD, true)
			s.stats.Charged(len(d.Body)/4+1, 0)
		}
		// There is nobody left to tell.
	case errors.Is(err, errNoLaneKeys):
		if billable {
			s.ledger.Refund(subject, adm)
		}
		s.writeLimit(w, &quota.LimitError{Reason: quota.ReasonCredits})
	case errors.Is(err, errLaneRequest):
		if billable {
			s.ledger.Refund(subject, adm)
		}
		writeError(w, http.StatusInternalServerError, typeInternal, errLaneRequest.Error())
	default:
		// Never reached the model, so it cost nothing and the caller keeps
		// their request allowance.
		if billable {
			s.ledger.Refund(subject, adm)
		}
		writeError(w, http.StatusBadGateway, typeUpstream, "could not reach DeepSeek: "+err.Error())
	}
}

// drain finishes with a response nobody will read, so the connection goes
// back to the pool instead of being torn down. The cap is there because
// this is an abandoned response: a lane that answers with a gigabyte
// should cost us one buffer, not a stall.
func drain(resp *http.Response) {
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
}

// isMoneyLimit reports whether a refusal is about the service's money
// rather than about this caller. Only these can be survived by an
// upstream that costs nothing.
func isMoneyLimit(err error) bool {
	var lim *quota.LimitError
	if !errors.As(err, &lim) {
		return false
	}
	return lim.Reason == quota.ReasonCredits || lim.Reason == quota.ReasonDailyBudget
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
	case quota.ReasonSearches:
		// A distinct message because the fix is distinct: the rest of the
		// tier still works, so "come back tomorrow" would be wrong.
		retryAfter(w, lim.RetryAfter(time.Now()))
		writeError(w, http.StatusTooManyRequests, typeQuota,
			"you have used today's web-search allowance. Ordinary requests still work — searches reset at 00:00 UTC, or bring your own key for unlimited search: https://platform.deepseek.com/api_keys")
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
