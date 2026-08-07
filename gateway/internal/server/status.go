package server

import (
	"net/http"
	"strings"
	"time"

	"github.com/thevibeworks/deepseek-cli/gateway/internal/keyring"
	"github.com/thevibeworks/deepseek-cli/gateway/internal/quota"
	"github.com/thevibeworks/deepseek-cli/gateway/internal/stats"
)

// The public status document.
//
// What it says and what it refuses to say are both deliberate. It carries
// enough for a stranger to decide whether to bother enrolling — is it up,
// is there credit, how busy is it — and enough for the dashboard to be
// worth looking at. It carries no dollar figures, no account balance, and
// no whole subject id.
//
// Dollars stay operator-only because publishing them turns the budget
// breaker into a progress bar for whoever wants to trip it: "$0.19 of
// $0.25 spent" tells an attacker exactly how much more to send. The same
// facts as percentages answer the honest question — how much is left —
// without handing over the target. Exact figures live behind
// /admin/status.
type PublicStatus struct {
	Service  string `json:"service"`
	Version  string `json:"version"`
	Model    string `json:"model"`
	Announce string `json:"announce,omitempty"`

	// State is the one word a status page leads with.
	State  string `json:"state"`
	Detail string `json:"detail"`

	Credit CreditStatus `json:"credit"`
	Usage  UsageStatus  `json:"usage"`
	Live   stats.Live   `json:"live"`

	Endpoints []stats.Count `json:"endpoints"`
	Countries []stats.Count `json:"countries"`
	Top       []TopSubject  `json:"top_subjects"`

	// History is the last 30 UTC days of traffic, oldest first. A service
	// this size serves a few requests an hour at best, which is why the
	// live five-minute view is nearly always zero and cannot be the whole
	// story — the daily series is where the traffic is actually visible.
	History []quota.Day `json:"history"`

	// Upstream is what DeepSeek has done for us lately, so a visitor can
	// tell our outage from theirs without leaving the page.
	Upstream stats.Upstream `json:"upstream"`

	Keys   PoolStatus     `json:"key_pool"`
	Limits quota.UserCaps `json:"daily_limits_per_user"`
	System stats.System   `json:"system"`

	ResetsAt time.Time `json:"resets_at"`
	Now      time.Time `json:"now"`
}

// Service states, coarsest first. A status page that only ever says "up"
// or "down" is useless on the day it matters; these are the four
// distinctions a user can actually act on.
const (
	StateOperational = "operational"   // everything works
	StateBusy        = "busy"          // at the concurrency cap, requests queue
	StateDayspent    = "day_exhausted" // today's budget is gone; back at 00:00 UTC
	StateDry         = "credit_exhausted"
	StateDegraded    = "degraded" // the ledger cannot record spend
)

// CreditStatus is the pool, in proportions rather than dollars.
type CreditStatus struct {
	DayRemainingPct  float64 `json:"day_remaining_pct"`
	PoolRemainingPct float64 `json:"pool_remaining_pct"`
	// Donated is how many keys strangers have added. It is the one number
	// that makes the donation ask concrete.
	Donated int `json:"donated_keys"`
}

// UsageStatus is what the service has served.
type UsageStatus struct {
	Today    quota.Totals `json:"today"`
	Lifetime quota.Totals `json:"lifetime"`
	// Subjects seen today, which is the closest honest thing to a user
	// count: no account exists, so "users" can only ever mean "distinct
	// anonymous identities that sent something".
	SubjectsToday int `json:"subjects_today"`
}

// TopSubject is one row of the leaderboard, with the id cut short.
type TopSubject struct {
	Subject      string `json:"subject"`
	Requests     int    `json:"requests"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
}

// PoolStatus is the key pool without the keys.
type PoolStatus struct {
	Active  int `json:"active"`
	Dry     int `json:"dry"`
	Retired int `json:"retired"`
	Total   int `json:"total"`
}

// withoutMoney blanks the cost field before a total is published.
//
// The token counts are the interesting half and they stay; the dollars
// are what turn this document into a countdown for whoever wants to empty
// the pool. The field itself is kept rather than dropped so the shape of
// the JSON does not change between the public and operator views.
func withoutMoney(t quota.Totals) quota.Totals {
	t.SpentUSD = 0
	return t
}

// historyDays is how far the daily series goes back. Thirty days is one
// screen of bars at a readable width, and long enough that a week of
// growth or a quiet stretch is legible rather than a rounding error.
const historyDays = 30

// publicStatusTTL caches the document. The dashboard polls, several
// people may have it open, and every field is a five-minute rolling
// figure — recomputing per request would spend more CPU on watching the
// service than on running it.
const publicStatusTTL = 3 * time.Second

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if !s.limitMeta(w, r) {
		return
	}
	s.statusMu.Lock()
	if s.statusDoc != nil && time.Since(s.statusAt) < publicStatusTTL {
		doc := s.statusDoc
		s.statusMu.Unlock()
		writeJSON(w, http.StatusOK, doc)
		return
	}
	s.statusMu.Unlock()

	doc := s.buildStatus()

	s.statusMu.Lock()
	s.statusDoc, s.statusAt = doc, time.Now()
	s.statusMu.Unlock()

	writeJSON(w, http.StatusOK, doc)
}

func (s *Server) buildStatus() *PublicStatus {
	h := s.ledger.Health()
	snap := s.stats.Snapshot()
	pool := s.keys.Status(false)

	dayLeft := pct(h.DailyBudgetUSD-h.DaySpendUSD-h.ReservedUSD, h.DailyBudgetUSD)
	poolLeft := pct(h.TotalBudgetUSD-h.TotalSpendUSD-h.ReservedUSD, h.TotalBudgetUSD)

	state, detail := s.state(h, pool, snap)

	top := s.ledger.TopSubjects(10)
	rows := make([]TopSubject, 0, len(top))
	for _, u := range top {
		rows = append(rows, TopSubject{
			Subject:      shortSubject(u.Subject),
			Requests:     u.Requests,
			InputTokens:  u.InputTokens,
			OutputTokens: u.OutputTokens,
		})
	}

	return &PublicStatus{
		Service: "dsgate", Version: s.cfg.Version, Model: s.cfg.Model,
		Announce: s.cfg.Announce,
		State:    state, Detail: detail,
		Credit: CreditStatus{
			DayRemainingPct:  dayLeft,
			PoolRemainingPct: poolLeft,
			Donated:          donatedCount(s.keys),
		},
		Usage: UsageStatus{
			Today:         withoutMoney(s.ledger.Today()),
			Lifetime:      withoutMoney(s.ledger.Lifetime()),
			SubjectsToday: h.Subjects,
		},
		Live:      snap.Live,
		History:   s.ledger.History(historyDays),
		Upstream:  snap.Upstream,
		Endpoints: snap.Endpoints,
		Countries: snap.Countries,
		Top:       rows,
		Keys:      PoolStatus{Active: pool.Active, Dry: pool.Dry, Retired: pool.Retired, Total: pool.Total},
		Limits:    s.ledger.Status("", "anon").Limits,
		System:    snap.System,
		ResetsAt:  midnightUTC(time.Now()),
		Now:       time.Now().UTC(),
	}
}

// state reduces everything to the one word the page leads with, in the
// order a user cares about: can I use it at all, then is it degraded,
// then is it merely busy.
func (s *Server) state(h quota.Health, pool keyring.Status, snap stats.Snapshot) (string, string) {
	switch {
	case pool.Active == 0:
		return StateDry, "no upstream key in the pool has credit left — bring your own key, or donate one"
	case s.upstreamDry.Load():
		return StateDry, "DeepSeek reports the funding account is out of credit"
	case h.TotalSpendUSD >= h.TotalBudgetUSD:
		return StateDry, "the shared credit pool is spent — bring your own key, or donate one"
	case !h.JournalOK:
		return StateDegraded, "the spend journal is not writable, so requests are refused until it is"
	case h.DaySpendUSD >= h.DailyBudgetUSD:
		return StateDayspent, "today's shared budget is spent; it resets at 00:00 UTC"
	case snap.Live.InFlight >= int64(s.cfg.MaxInflight):
		return StateBusy, "every slot is in use right now; requests may queue briefly"
	default:
		return StateOperational, "free DeepSeek access is working — no key, no account"
	}
}

// shortSubject cuts an id down to something that identifies a row on a
// leaderboard without identifying the holder. A whole subject can be
// matched against the one in someone's free.json; six characters of
// base64url cannot be, and still reads as a name.
func shortSubject(sub string) string {
	if len(sub) <= 6 {
		return sub
	}
	return sub[:6] + "…"
}

func donatedCount(r *keyring.Ring) int {
	n := 0
	for _, k := range r.Status(true).Keys {
		if k.Source == keyring.SourceDonor {
			n++
		}
	}
	return n
}

// pct is what is left, as a percentage, clamped. A negative remainder
// (reservations can briefly exceed the balance) reads as zero rather than
// as a negative bar.
func pct(remaining, total float64) float64 {
	if total <= 0 {
		return 0
	}
	p := remaining / total * 100
	switch {
	case p < 0:
		return 0
	case p > 100:
		return 100
	}
	return float64(int64(p*10+0.5)) / 10
}

func midnightUTC(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC).Add(24 * time.Hour)
}

// --- operator view -------------------------------------------------------

// AdminStatus is everything the public document withholds: exact money,
// per-key health, journal durability.
type AdminStatus struct {
	*PublicStatus
	Health            quota.Health         `json:"ledger"`
	UpstreamAvailable bool                 `json:"upstream_available"`
	KeyDetail         []keyring.Key        `json:"keys"`
	Subjects          []quota.SubjectUsage `json:"subjects_today"`
}

func (s *Server) handleAdminStatus(w http.ResponseWriter, r *http.Request) {
	if !s.adminOK(r) {
		writeError(w, http.StatusNotFound, typeRejected, "not found")
		return
	}
	writeJSON(w, http.StatusOK, AdminStatus{
		PublicStatus:      s.buildStatus(),
		Health:            s.ledger.Health(),
		UpstreamAvailable: !s.upstreamDry.Load(),
		KeyDetail:         s.keys.Status(true).Keys,
		Subjects:          s.ledger.TopSubjects(200),
	})
}

func (s *Server) adminOK(r *http.Request) bool {
	if s.cfg.AdminToken == "" {
		return false
	}
	return subtleEqual(r.Header.Get("X-Admin-Token"), s.cfg.AdminToken)
}

// --- key donation --------------------------------------------------------

// donateRequest is an operator handing the pool another key.
type donateRequest struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// handleAdminKeys lists, adds, retires, revives and removes keys without
// a restart, so a donation lands in seconds and a compromised key leaves
// in seconds.
//
// It is admin-gated rather than public on purpose. A public "paste your
// API key here" form would be a service that collects other people's
// credentials over the open internet, and teaching users that habit is
// worse than the friction it saves — the donation path is a private
// message to a human, who adds it here.
func (s *Server) handleAdminKeys(w http.ResponseWriter, r *http.Request) {
	if !s.adminOK(r) {
		writeError(w, http.StatusNotFound, typeRejected, "not found")
		return
	}

	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.keys.Status(true))

	case http.MethodPost:
		var req donateRequest
		if err := decodeJSON(r, 8<<10, &req); err != nil {
			writeError(w, http.StatusBadRequest, typeRejected, err.Error())
			return
		}
		if strings.TrimSpace(req.Key) == "" {
			writeError(w, http.StatusBadRequest, typeRejected, "no key given")
			return
		}
		fp, added := s.keys.Donate(req.Key, req.Label)
		s.invalidateStatus()
		writeJSON(w, http.StatusOK, map[string]any{
			"fingerprint": fp, "added": added,
			"pool": s.keys.Status(false),
		})

	case http.MethodDelete:
		fp := r.URL.Query().Get("fingerprint")
		action := r.URL.Query().Get("action")
		var ok bool
		switch action {
		case "retire":
			s.keys.Retire(fp, "retired by operator")
			ok = true
		case "revive":
			ok = s.keys.Revive(fp)
		default:
			ok = s.keys.Remove(fp)
		}
		s.invalidateStatus()
		writeJSON(w, http.StatusOK, map[string]any{"ok": ok, "pool": s.keys.Status(false)})

	default:
		writeError(w, http.StatusMethodNotAllowed, typeRejected, "GET, POST or DELETE")
	}
}

func (s *Server) invalidateStatus() {
	s.statusMu.Lock()
	s.statusDoc = nil
	s.statusMu.Unlock()
}
