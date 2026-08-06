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
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	// ReasonUnavailable means the ledger cannot durably record spend right
	// now. Refusing is deliberate: admitting requests that cannot be
	// journalled means a restart silently refunds them, which is exactly
	// the fail-open hole a budget breaker must not have.
	ReasonUnavailable Reason = "ledger_unavailable"
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
	case ReasonUnavailable:
		return "the free tier cannot record spend right now"
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
	// priorTotals is lifetime token and request counts for every day
	// before today, scanned from the journals at boot. Spend is tracked
	// separately in priorSpend, which is persisted; these are not, because
	// re-deriving them is cheap and a counter that can drift from its own
	// journal is worse than one that cannot.
	priorTotals Totals
	// reserved is the projected worst-case cost of every admitted request
	// that has not yet been charged or refunded. It counts against both
	// budgets at admission, which is what makes them ceilings rather than
	// horizons: without it, MAX_INFLIGHT requests could all be admitted a
	// dollar before the breaker and each overshoot it.
	reserved float64
	revoked  map[string]bool
	journal  *os.File
	// journalErr is the last durability failure. While set, Admit refuses:
	// spend that cannot be journalled is spend a restart would refund.
	journalErr error

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
	if err := l.scanPastTotals(l.day); err != nil {
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

// scanPastTotals sums token and request counts from every journal before
// today, so the dashboard's lifetime figures survive a restart.
func (l *Ledger) scanPastTotals(today string) error {
	names, err := os.ReadDir(l.dir)
	if err != nil {
		return err
	}
	l.priorTotals = Totals{}
	for _, e := range names {
		day, ok := journalDay(e.Name())
		if !ok || day >= today {
			continue
		}
		f, err := os.Open(l.journalPath(day))
		if err != nil {
			continue
		}
		scanJournal(f, func(en entry) {
			l.priorTotals.Requests++
			l.priorTotals.InputTokens += en.InputTokens
			l.priorTotals.OutputTokens += en.OutputTokens
		})
		f.Close()
	}
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
	scanJournal(f, func(e entry) { total += e.USD })
	return total, nil
}

// scanJournal feeds every parseable entry to fn, line by line. A line
// that does not parse — a crash mid-write, a disk hiccup — is skipped
// rather than treated as the end of the file: every line after it is
// still real spend, and dropping it would refund that spend on restart.
func scanJournal(f *os.File, fn func(entry)) {
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var e entry
		if json.Unmarshal(line, &e) != nil {
			continue
		}
		fn(e)
	}
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

	scanJournal(f, func(e entry) {
		a := l.accountLocked(e.Subject)
		a.Requests++
		a.InputTokens += e.InputTokens
		a.OutputTokens += e.OutputTokens
		a.SpentUSD += e.USD
		l.daySpend += e.USD
	})
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
	for _, a := range l.accounts {
		l.priorTotals.Requests += a.Requests
		l.priorTotals.InputTokens += a.InputTokens
		l.priorTotals.OutputTokens += a.OutputTokens
	}
	l.accounts = map[string]*Account{}
	l.day = day
	l.through = day

	if l.journal != nil {
		l.journal.Close()
	}
	if f, err := os.OpenFile(l.journalPath(day), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		l.journal = f
		l.journalErr = nil
	} else {
		l.journal = nil
		l.journalErr = err
	}
	if err := l.saveStateLocked(nil); err != nil && l.journalErr == nil {
		l.journalErr = err
	}
}

// reopenLocked retries the journal after a durability failure, so a full
// disk that has been cleared heals without a restart.
func (l *Ledger) reopenLocked() {
	if l.journalErr == nil {
		return
	}
	if l.journal != nil {
		l.journal.Close()
	}
	f, err := os.OpenFile(l.journalPath(l.day), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		l.journal = nil
		return
	}
	// A failed write may have left a partial line behind. Terminating it
	// costs one blank line, which replay skips; not terminating it would
	// glue the next entry onto the partial one and lose both.
	if _, err := f.Write([]byte("\n")); err != nil {
		f.Close()
		l.journal = nil
		return
	}
	l.journal = f
	l.journalErr = nil
}

// Admit debits one request against a subject and reserves its worst-case
// cost, reporting whether it may proceed.
//
// The reservation is what makes the budgets hard ceilings: a request is
// admitted only if, priced at its absolute maximum, it still fits under
// both. Charge and Refund release the reservation, so the actual (almost
// always much smaller) cost is what sticks.
func (l *Ledger) Admit(subject string, reserveUSD float64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rollLocked()
	l.reopenLocked()

	if l.journalErr != nil {
		return &LimitError{Reason: ReasonUnavailable}
	}
	if l.revoked[subject] {
		return &LimitError{Reason: ReasonRevoked}
	}
	reset := midnight(l.now())

	// Service-wide limits first: when the service is out of money, the
	// caller's own remaining quota is irrelevant and saying "you have 28
	// requests left" would be a lie.
	// Refused when already spent out, and also when this request's
	// worst case would break the ceiling — both conditions, because a
	// pool that is exactly empty must refuse even a zero-cost admit.
	projected := l.reserved + reserveUSD
	if spent := l.priorSpend + l.daySpend; spent >= l.limits.TotalBudgetUSD ||
		spent+projected > l.limits.TotalBudgetUSD {
		return &LimitError{Reason: ReasonCredits}
	}
	if l.daySpend >= l.limits.DailyBudgetUSD ||
		l.daySpend+projected > l.limits.DailyBudgetUSD {
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
	l.reserved += reserveUSD
	return nil
}

// Refund returns everything Admit took — the request allowance and the
// reservation — for a call that never reached the model.
//
// Only failures the caller cannot provoke qualify — transport errors and
// upstream 429/5xx. Refunding on anything the client controls, such as a
// malformed body, would turn the request counter into a free retry loop.
func (l *Ledger) Refund(subject string, reserveUSD float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if a, ok := l.accounts[subject]; ok && a.Requests > 0 {
		a.Requests--
	}
	l.releaseLocked(reserveUSD)
}

// Release gives back a reservation while keeping the request debit, for
// a call that reached the model but generated nothing billable — an
// upstream 4xx that was the caller's own doing.
func (l *Ledger) Release(reserveUSD float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.releaseLocked(reserveUSD)
}

func (l *Ledger) releaseLocked(reserveUSD float64) {
	l.reserved -= reserveUSD
	if l.reserved < 0 {
		l.reserved = 0
	}
}

// Charge settles what a completed request actually cost, releasing its
// reservation.
func (l *Ledger) Charge(subject, endpoint, model string, in, cacheHit, out int, usd float64, reserveUSD float64, estimated bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rollLocked()
	l.releaseLocked(reserveUSD)

	a := l.accountLocked(subject)
	a.InputTokens += in
	a.OutputTokens += out
	a.SpentUSD += usd
	l.daySpend += usd

	e := entry{
		Time: l.now().UTC(), Subject: subject, Endpoint: endpoint, Model: model,
		InputTokens: in, CacheHit: cacheHit, OutputTokens: out, USD: usd, Estimated: estimated,
	}
	// The in-memory counters above are already debited, so a journal
	// failure here loses no money now — but it would on restart. Recording
	// the failure makes Admit refuse until the journal writes again.
	if l.journal == nil {
		if l.journalErr == nil {
			l.journalErr = fmt.Errorf("journal is not open")
		}
		return
	}
	b, err := json.Marshal(e)
	if err != nil {
		l.journalErr = err
		return
	}
	if _, err := l.journal.Write(append(b, '\n')); err != nil {
		l.journalErr = err
		return
	}
	// Durable per debit. At this service's volume the fsync is free, and
	// the alternative is that a crash refunds everybody.
	if err := l.journal.Sync(); err != nil {
		l.journalErr = err
	}
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

// Totals is aggregate usage over some period.
type Totals struct {
	Requests     int     `json:"requests"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	SpentUSD     float64 `json:"spent_usd"`
}

// Today totals everything charged since midnight UTC.
func (l *Ledger) Today() Totals {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rollLocked()

	var t Totals
	for _, a := range l.accounts {
		t.Requests += a.Requests
		t.InputTokens += a.InputTokens
		t.OutputTokens += a.OutputTokens
		t.SpentUSD += a.SpentUSD
	}
	return t
}

// Lifetime totals every journal this ledger has ever written.
//
// Unlike spend — which is folded into priorSpend precisely so lifetime
// money never needs a full replay — token counts are read by scanning
// the journals once at boot and kept live from there. The scan is a few
// hundred kilobytes at this service's volume, and the alternative was
// another persisted counter that could silently drift from the journals
// it claims to summarise.
func (l *Ledger) Lifetime() Totals {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rollLocked()

	t := l.priorTotals
	for _, a := range l.accounts {
		t.Requests += a.Requests
		t.InputTokens += a.InputTokens
		t.OutputTokens += a.OutputTokens
	}
	t.SpentUSD = l.priorSpend + l.daySpend
	return t
}

// SubjectUsage is one anonymous account's day, for the leaderboard.
type SubjectUsage struct {
	Subject string `json:"subject"`
	Account
}

// TopSubjects returns today's busiest subjects, most requests first.
//
// A subject is 16 random bytes with no person attached, but the caller
// still truncates it before publishing: an id that is whole is an id that
// can be matched against the one in someone's free.json.
func (l *Ledger) TopSubjects(n int) []SubjectUsage {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rollLocked()

	out := make([]SubjectUsage, 0, len(l.accounts))
	for sub, a := range l.accounts {
		out = append(out, SubjectUsage{Subject: sub, Account: *a})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Requests != out[j].Requests {
			return out[i].Requests > out[j].Requests
		}
		if out[i].OutputTokens != out[j].OutputTokens {
			return out[i].OutputTokens > out[j].OutputTokens
		}
		return out[i].Subject < out[j].Subject
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// Health is the operator's view: the figures Status deliberately hides.
type Health struct {
	Day            string  `json:"day"`
	Subjects       int     `json:"subjects_today"`
	DaySpendUSD    float64 `json:"day_spend_usd"`
	ReservedUSD    float64 `json:"reserved_usd"`
	TotalSpendUSD  float64 `json:"total_spend_usd"`
	DailyBudgetUSD float64 `json:"daily_budget_usd"`
	TotalBudgetUSD float64 `json:"total_budget_usd"`
	// JournalOK is false while the ledger is refusing admissions because
	// it cannot write. It is the first thing to check when everything is
	// suddenly 503.
	JournalOK bool `json:"journal_ok"`
}

func (l *Ledger) Health() Health {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rollLocked()
	return Health{
		Day: l.day, Subjects: len(l.accounts),
		DaySpendUSD: l.daySpend, ReservedUSD: l.reserved,
		TotalSpendUSD:  l.priorSpend + l.daySpend,
		DailyBudgetUSD: l.limits.DailyBudgetUSD, TotalBudgetUSD: l.limits.TotalBudgetUSD,
		JournalOK: l.journalErr == nil,
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
