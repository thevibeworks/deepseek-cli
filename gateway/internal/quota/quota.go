// Package quota is the money. It decides who may send a request and it
// records what every request cost.
//
// There is no database. Counters live in memory and every debit is
// appended to a JSONL journal that is replayed at boot — the same shape
// as the CLI's own usage ledger, and for the same reason: an operator
// can read the whole financial history of this service with `tail`.
// Hundreds of requests a day do not need Postgres, and a 1 GiB box that
// is already running someone else's production does not want it.
//
// The journal records counts and costs. It never records prompts,
// completions, IP addresses or headers. `deepseek free` promises that in
// so many words, which makes it this package's job to keep.
package quota

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Limits is the policy. Every field is a hard cap; see DESIGN.md for how
// the defaults were derived from flash's rate card.
type Limits struct {
	DailyRequests     int
	DailyInputTokens  int
	DailyOutputTokens int

	// DailyBudgetUSD is the circuit breaker: the total this service may
	// spend across all users in one UTC day. This is the number that
	// actually bounds our loss, because it does not care how many
	// identities an attacker managed to mint.
	DailyBudgetUSD float64

	// TotalBudgetUSD is the prepaid credit pool. When it empties the free
	// tier is over until someone tops it up.
	TotalBudgetUSD float64
}

// Account is one subject's usage today.
type Account struct {
	Requests     int     `json:"requests"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	SpentUSD     float64 `json:"spent_usd"`
}

// Status is what a subject is told about its own standing. Only
// per-subject figures are exposed; the service's budget and credit
// balance are ours, not the caller's business, and publishing them would
// tell an attacker exactly how close they are to breaking it.
type Status struct {
	Subject  string    `json:"subject"`
	Tier     string    `json:"tier"`
	Used     Account   `json:"used"`
	Limits   UserCaps  `json:"limits"`
	ResetsAt time.Time `json:"resets_at"`
	// Exhausted is set when the service itself has run dry, which is not
	// the caller's fault and needs a different message.
	Exhausted bool `json:"service_exhausted,omitempty"`
}

// UserCaps is the per-subject half of Limits, the half a caller may see.
type UserCaps struct {
	Requests     int `json:"requests"`
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Reason classifies a refusal so the HTTP layer can pick a status code
// and the CLI can print something actionable.
type Reason string

const (
	ReasonRequests     Reason = "daily_requests"
	ReasonInputTokens  Reason = "daily_input_tokens"
	ReasonOutputTokens Reason = "daily_output_tokens"
	ReasonDailyBudget  Reason = "daily_budget"
	ReasonCredits      Reason = "credits_exhausted"
	ReasonRevoked      Reason = "revoked"
)

// LimitError is a refusal. It always says when the caller may try again,
// because "rate limited" with no horizon is the least useful thing a
// gateway can say.
type LimitError struct {
	Reason   Reason
	ResetsAt time.Time
}

func (e *LimitError) Error() string {
	switch e.Reason {
	case ReasonCredits:
		return "the free tier has run out of credit"
	case ReasonDailyBudget:
		return "the free tier has spent today's budget"
	case ReasonRevoked:
		return "this token has been revoked"
	default:
		return fmt.Sprintf("daily %s limit reached", string(e.Reason))
	}
}

// RetryAfter is how long until the refusal lifts. Zero means never, on
// this token: exhausted credit and revocation do not heal at midnight.
func (e *LimitError) RetryAfter(now time.Time) time.Duration {
	if e.Reason == ReasonCredits || e.Reason == ReasonRevoked {
		return 0
	}
	if d := e.ResetsAt.Sub(now); d > 0 {
		return d
	}
	return 0
}

// Ledger holds today's counters and the lifetime spend.
type Ledger struct {
	limits Limits
	dir    string

	mu       sync.Mutex
	day      string // UTC date the in-memory counters belong to
	accounts map[string]*Account
	daySpend float64
	// priorSpend is everything spent strictly before `through`, folded at
	// each rollover so lifetime spend never requires replaying the whole
	// history of journals.
	priorSpend float64
	// through is the day priorSpend is complete up to, exclusive. It is
	// persisted alongside priorSpend so a restart can tell which journals
	// have already been folded in and which still have to be.
	through string
	revoked map[string]bool
	journal *os.File

	now func() time.Time
}

// entry is one line of the journal.
type entry struct {
	Time         time.Time `json:"time"`
	Subject      string    `json:"subject"`
	Endpoint     string    `json:"endpoint"`
	Model        string    `json:"model,omitempty"`
	InputTokens  int       `json:"input_tokens"`
	CacheHit     int       `json:"cache_hit_tokens,omitempty"`
	OutputTokens int       `json:"output_tokens"`
	USD          float64   `json:"usd"`
	// Estimated marks a charge we could not measure and had to bound
	// from above. It exists so an operator can tell real spend from
	// defensive spend when the two diverge.
	Estimated bool `json:"estimated,omitempty"`
}

type state struct {
	PriorSpendUSD float64 `json:"prior_spend_usd"`
	Through       string  `json:"through_day"`
}

// Open loads a ledger from dir, replaying today's journal.
func Open(dir string, limits Limits) (*Ledger, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	l := &Ledger{
		limits:   limits,
		dir:      dir,
		accounts: map[string]*Account{},
		revoked:  map[string]bool{},
		now:      time.Now,
	}
	l.day = today(l.now())

	if err := l.loadState(); err != nil {
		return nil, err
	}
	// Days that ended while we were down have to be folded in before
	// today's journal is replayed, or their spend vanishes from the
	// lifetime pool.
	if err := l.foldPastLocked(l.day); err != nil {
		return nil, err
	}
	if err := l.replay(l.day); err != nil {
		return nil, err
	}
	if err := l.saveStateLocked(nil); err != nil {
		return nil, err
	}
	if err := l.LoadRevocations(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(l.journalPath(l.day), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	l.journal = f
	return l, nil
}

// SetClock replaces the time source, for tests that need to cross
// midnight without waiting for it.
func (l *Ledger) SetClock(now func() time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.now = now
}

func (l *Ledger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.journal == nil {
		return nil
	}
	err := l.journal.Close()
	l.journal = nil
	return l.saveStateLocked(err)
}

func (l *Ledger) journalPath(day string) string {
	return filepath.Join(l.dir, "journal-"+day+".jsonl")
}

func (l *Ledger) statePath() string { return filepath.Join(l.dir, "state.json") }

func (l *Ledger) loadState() error {
	b, err := os.ReadFile(l.statePath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var s state
	if err := json.Unmarshal(b, &s); err != nil {
		// A corrupt state file must not be silently treated as zero spend:
		// that would hand an attacker the whole credit pool a second time.
		return fmt.Errorf("reading %s: %w", l.statePath(), err)
	}
	l.priorSpend = s.PriorSpendUSD
	l.through = s.Through
	return nil
}

// foldPastLocked folds in every journal from days that ended while the
// process was not running.
//
// priorSpend only ever advanced at an in-process rollover, so a gateway
// stopped on one day and started on the next came back believing it had
// never spent that day's money — silently refunding the lifetime credit
// pool on every deploy or reboot that crossed midnight. The daily breaker
// was unaffected, but TotalBudgetUSD, the thing that is supposed to stop
// a bad invoice, was not enforced across restarts.
//
// Days in [through, today) are the ones priorSpend has not seen yet:
// priorSpend covers everything strictly before `through`, and today's
// journal is replayed separately into daySpend.
func (l *Ledger) foldPastLocked(today string) error {
	names, err := os.ReadDir(l.dir)
	if err != nil {
		return err
	}
	for _, e := range names {
		day, ok := journalDay(e.Name())
		if !ok || day >= today || day < l.through {
			continue
		}
		usd, err := l.sumJournal(day)
		if err != nil {
			return err
		}
		l.priorSpend += usd
	}
	l.through = today
	return nil
}

// journalDay pulls the date out of a journal filename, reporting whether
// the name was one of ours at all.
func journalDay(name string) (string, bool) {
	const prefix, suffix = "journal-", ".jsonl"
	if len(name) != len(prefix)+len("2006-01-02")+len(suffix) {
		return "", false
	}
	if name[:len(prefix)] != prefix || name[len(name)-len(suffix):] != suffix {
		return "", false
	}
	return name[len(prefix) : len(name)-len(suffix)], true
}

// sumJournal totals one day's spend without disturbing the live counters.
func (l *Ledger) sumJournal(day string) (float64, error) {
	f, err := os.Open(l.journalPath(day))
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer f.Close()

	var total float64
	dec := json.NewDecoder(f)
	for {
		var e entry
		if err := dec.Decode(&e); err != nil {
			// Same rule as replay: a truncated final line is a crash
			// mid-write, and everything before it still counts.
			break
		}
		total += e.USD
	}
	return total, nil
}

func (l *Ledger) saveStateLocked(prev error) error {
	b, err := json.Marshal(state{PriorSpendUSD: l.priorSpend, Through: l.through})
	if err != nil {
		return err
	}
	tmp := l.statePath() + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, l.statePath()); err != nil {
		return err
	}
	return prev
}

// replay rebuilds counters for one day from its journal.
func (l *Ledger) replay(day string) error {
	f, err := os.Open(l.journalPath(day))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	for {
		var e entry
		if err := dec.Decode(&e); err != nil {
			// A truncated final line is what a crash mid-write looks like.
			// Everything before it is still good, so stop rather than fail.
			break
		}
		a := l.accountLocked(e.Subject)
		a.Requests++
		a.InputTokens += e.InputTokens
		a.OutputTokens += e.OutputTokens
		a.SpentUSD += e.USD
		l.daySpend += e.USD
	}
	return nil
}

func (l *Ledger) accountLocked(subject string) *Account {
	a, ok := l.accounts[subject]
	if !ok {
		a = &Account{}
		l.accounts[subject] = a
	}
	return a
}

// rollLocked moves to a new UTC day when one has started.
func (l *Ledger) rollLocked() {
	day := today(l.now())
	if day == l.day {
		return
	}
	l.priorSpend += l.daySpend
	l.daySpend = 0
	l.accounts = map[string]*Account{}
	l.day = day
	l.through = day

	if l.journal != nil {
		l.journal.Close()
	}
	if f, err := os.OpenFile(l.journalPath(day), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		l.journal = f
	} else {
		l.journal = nil
	}
	l.saveStateLocked(nil)
}

// Admit debits one request against a subject and reports whether it may
// proceed.
//
// The request count is spent up front because it is knowable up front;
// token counts are only settled in Charge, once the model has answered.
// That leaves a bounded window where admitted-but-unbilled requests can
// overshoot a limit, which is why the server caps how many may be in
// flight at once.
func (l *Ledger) Admit(subject string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rollLocked()

	if l.revoked[subject] {
		return &LimitError{Reason: ReasonRevoked}
	}
	reset := midnight(l.now())

	// Service-wide limits first: when the service is out of money, the
	// caller's own remaining quota is irrelevant and saying "you have 28
	// requests left" would be a lie.
	if spent := l.priorSpend + l.daySpend; spent >= l.limits.TotalBudgetUSD {
		return &LimitError{Reason: ReasonCredits}
	}
	if l.daySpend >= l.limits.DailyBudgetUSD {
		return &LimitError{Reason: ReasonDailyBudget, ResetsAt: reset}
	}

	a := l.accountLocked(subject)
	switch {
	case a.Requests >= l.limits.DailyRequests:
		return &LimitError{Reason: ReasonRequests, ResetsAt: reset}
	case a.InputTokens >= l.limits.DailyInputTokens:
		return &LimitError{Reason: ReasonInputTokens, ResetsAt: reset}
	case a.OutputTokens >= l.limits.DailyOutputTokens:
		return &LimitError{Reason: ReasonOutputTokens, ResetsAt: reset}
	}

	a.Requests++
	return nil
}

// Refund returns a request allowance taken by Admit, for a call that
// never reached the model.
//
// Only failures the caller cannot provoke qualify — transport errors and
// upstream 429/5xx. Refunding on anything the client controls, such as a
// malformed body, would turn the request counter into a free retry loop.
func (l *Ledger) Refund(subject string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if a, ok := l.accounts[subject]; ok && a.Requests > 0 {
		a.Requests--
	}
}

// Charge records what a completed request cost.
func (l *Ledger) Charge(subject, endpoint, model string, in, cacheHit, out int, usd float64, estimated bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rollLocked()

	a := l.accountLocked(subject)
	a.InputTokens += in
	a.OutputTokens += out
	a.SpentUSD += usd
	l.daySpend += usd

	e := entry{
		Time: l.now().UTC(), Subject: subject, Endpoint: endpoint, Model: model,
		InputTokens: in, CacheHit: cacheHit, OutputTokens: out, USD: usd, Estimated: estimated,
	}
	if l.journal == nil {
		return
	}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	if _, err := l.journal.Write(append(b, '\n')); err != nil {
		return
	}
	// Durable per debit. At this service's volume the fsync is free, and
	// the alternative is that a crash refunds everybody.
	l.journal.Sync()
}

// Status reports one subject's standing.
func (l *Ledger) Status(subject, tier string) Status {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rollLocked()

	used := Account{}
	if a, ok := l.accounts[subject]; ok {
		used = *a
	}
	return Status{
		Subject: subject,
		Tier:    tier,
		Used:    used,
		Limits: UserCaps{
			Requests:     l.limits.DailyRequests,
			InputTokens:  l.limits.DailyInputTokens,
			OutputTokens: l.limits.DailyOutputTokens,
		},
		ResetsAt:  midnight(l.now()),
		Exhausted: l.priorSpend+l.daySpend >= l.limits.TotalBudgetUSD,
	}
}

// Health is the operator's view: the figures Status deliberately hides.
type Health struct {
	Day            string  `json:"day"`
	Subjects       int     `json:"subjects_today"`
	DaySpendUSD    float64 `json:"day_spend_usd"`
	TotalSpendUSD  float64 `json:"total_spend_usd"`
	DailyBudgetUSD float64 `json:"daily_budget_usd"`
	TotalBudgetUSD float64 `json:"total_budget_usd"`
}

func (l *Ledger) Health() Health {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rollLocked()
	return Health{
		Day: l.day, Subjects: len(l.accounts),
		DaySpendUSD: l.daySpend, TotalSpendUSD: l.priorSpend + l.daySpend,
		DailyBudgetUSD: l.limits.DailyBudgetUSD, TotalBudgetUSD: l.limits.TotalBudgetUSD,
	}
}

// LoadRevocations re-reads the revocation list, one subject per line.
// Called at boot and on SIGHUP, so a token can be cut off without a
// restart that would drop every stream in flight.
func (l *Ledger) LoadRevocations() error {
	path := filepath.Join(l.dir, "revoked.txt")
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	next := map[string]bool{}
	for _, line := range splitLines(string(b)) {
		if line != "" && line[0] != '#' {
			next[line] = true
		}
	}
	l.mu.Lock()
	l.revoked = next
	l.mu.Unlock()
	return nil
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '\n' {
			line := s[start:i]
			for len(line) > 0 && (line[len(line)-1] == '\r' || line[len(line)-1] == ' ' || line[len(line)-1] == '\t') {
				line = line[:len(line)-1]
			}
			for len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
				line = line[1:]
			}
			out = append(out, line)
			start = i + 1
		}
	}
	return out
}

func today(t time.Time) string { return t.UTC().Format("2006-01-02") }

func midnight(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC).Add(24 * time.Hour)
}
