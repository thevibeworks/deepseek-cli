package quota

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testLimits() Limits {
	return Limits{
		DailyRequests:     3,
		DailyInputTokens:  1000,
		DailyOutputTokens: 500,
		DailyBudgetUSD:    0.01,
		TotalBudgetUSD:    0.10,
	}
}

func open(t *testing.T, dir string, lim Limits) (*Ledger, func()) {
	t.Helper()
	l, err := Open(dir, lim)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return l, func() { l.Close() }
}

func reasonOf(t *testing.T, err error) Reason {
	t.Helper()
	var lim *LimitError
	if !errors.As(err, &lim) {
		t.Fatalf("error %v is not a *LimitError", err)
	}
	return lim.Reason
}

func TestRequestsAreCappedPerDay(t *testing.T) {
	l, done := open(t, t.TempDir(), testLimits())
	defer done()

	for i := 0; i < 3; i++ {
		if err := l.Admit("alice", Admission{}); err != nil {
			t.Fatalf("request %d refused: %v", i+1, err)
		}
	}
	err := l.Admit("alice", Admission{})
	if err == nil {
		t.Fatal("a fourth request was admitted against a limit of three")
	}
	if got := reasonOf(t, err); got != ReasonRequests {
		t.Errorf("reason = %q, want %q", got, ReasonRequests)
	}

	// One subject hitting its limit must not affect another.
	if err := l.Admit("bob", Admission{}); err != nil {
		t.Errorf("bob was refused because alice ran out: %v", err)
	}
}

func TestTokenLimitsAreEnforcedAfterCharging(t *testing.T) {
	lim := testLimits()
	lim.DailyRequests = 100
	l, done := open(t, t.TempDir(), lim)
	defer done()

	if err := l.Admit("alice", Admission{}); err != nil {
		t.Fatal(err)
	}
	l.Charge("alice", "chat", "deepseek-v4-flash", 0, 0, 600, 0.0001, 0, false)

	err := l.Admit("alice", Admission{})
	if err == nil {
		t.Fatal("admitted after the output token cap was passed")
	}
	if got := reasonOf(t, err); got != ReasonOutputTokens {
		t.Errorf("reason = %q, want %q", got, ReasonOutputTokens)
	}
}

// The daily budget is the circuit breaker. It has to fire regardless of
// how many identities the spending was spread across, because that is
// the whole reason it exists.
func TestDailyBudgetStopsEveryone(t *testing.T) {
	lim := testLimits()
	lim.DailyRequests = 1000
	l, done := open(t, t.TempDir(), lim)
	defer done()

	// Spread $0.003 a head across distinct subjects. No one of them comes
	// close to a per-user limit; between them they pass the $0.01 budget.
	spenders := []string{"a", "b", "c", "d", "e"}
	tripped := ""
	for _, who := range spenders {
		if err := l.Admit(who, Admission{}); err != nil {
			tripped = who
			break
		}
		l.Charge(who, "chat", "deepseek-v4-flash", 100, 0, 100, 0.003, 0, false)
	}
	if tripped == "" {
		t.Fatal("five subjects spent $0.015 against a $0.01 budget and none was refused")
	}

	// And it holds for someone who has spent nothing at all: the breaker
	// is about the service's money, not the caller's behaviour.
	err := l.Admit("someone-brand-new", Admission{})
	if err == nil {
		t.Fatal("a fresh subject was admitted after the daily budget was spent")
	}
	if got := reasonOf(t, err); got != ReasonDailyBudget {
		t.Errorf("reason = %q, want %q", got, ReasonDailyBudget)
	}
}

func TestCreditsExhaustedOutranksTheDailyBudget(t *testing.T) {
	lim := testLimits()
	lim.DailyRequests = 1000
	lim.DailyBudgetUSD = 1000
	l, done := open(t, t.TempDir(), lim)
	defer done()

	l.Admit("a", Admission{})
	l.Charge("a", "chat", "deepseek-v4-flash", 0, 0, 0, 0.10, 0, false)

	err := l.Admit("b", Admission{})
	if got := reasonOf(t, err); got != ReasonCredits {
		t.Fatalf("reason = %q, want %q", got, ReasonCredits)
	}
	// Exhausted credit does not heal at midnight, so there is no retry
	// horizon to quote.
	var le *LimitError
	errors.As(err, &le)
	if d := le.RetryAfter(time.Now()); d != 0 {
		t.Errorf("RetryAfter = %v for exhausted credit; there is nothing to wait for", d)
	}
}

// A crash must not refund everybody. Every debit is fsynced, so a fresh
// process replaying the journal has to land on the same counters.
func TestCountersSurviveARestart(t *testing.T) {
	dir := t.TempDir()

	l, _ := open(t, dir, testLimits())
	l.Admit("alice", Admission{})
	l.Charge("alice", "chat", "deepseek-v4-flash", 400, 0, 200, 0.002, 0, false)
	l.Admit("alice", Admission{})
	l.Charge("alice", "chat", "deepseek-v4-flash", 100, 0, 50, 0.001, 0, false)
	l.Close()

	again, done := open(t, dir, testLimits())
	defer done()

	st := again.Status("alice", "anon")
	if st.Used.Requests != 2 {
		t.Errorf("requests after restart = %d, want 2", st.Used.Requests)
	}
	if st.Used.InputTokens != 500 || st.Used.OutputTokens != 250 {
		t.Errorf("tokens after restart = %d/%d, want 500/250", st.Used.InputTokens, st.Used.OutputTokens)
	}
	if h := again.Health(); h.DaySpendUSD < 0.0029 || h.DaySpendUSD > 0.0031 {
		t.Errorf("day spend after restart = %v, want ~0.003", h.DaySpendUSD)
	}
}

// A journal truncated mid-line is what a crash during a write looks
// like. Everything before the tear is still good and must be kept.
func TestTruncatedJournalKeepsWhatItCan(t *testing.T) {
	dir := t.TempDir()
	l, _ := open(t, dir, testLimits())
	l.Admit("alice", Admission{})
	l.Charge("alice", "chat", "deepseek-v4-flash", 400, 0, 200, 0.002, 0, false)
	day := l.day
	l.Close()

	path := filepath.Join(dir, "journal-"+day+".jsonl")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(b, []byte(`{"subject":"alice","input_`)...), 0o600); err != nil {
		t.Fatal(err)
	}

	again, done := open(t, dir, testLimits())
	defer done()
	if st := again.Status("alice", "anon"); st.Used.Requests != 1 || st.Used.InputTokens != 400 {
		t.Errorf("a torn journal lost the good entry before it: %+v", st.Used)
	}
}

// Lifetime spend must not be recoverable by deleting yesterday's
// journal, or the credit pool refills itself every time a log rotates.
func TestLifetimeSpendSurvivesTheDayRolling(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 5, 23, 59, 0, 0, time.UTC)

	l, _ := open(t, dir, testLimits())
	l.SetClock(func() time.Time { return now })
	l.Admit("alice", Admission{})
	l.Charge("alice", "chat", "deepseek-v4-flash", 100, 0, 100, 0.05, 0, false)

	now = now.Add(2 * time.Minute) // past midnight UTC

	st := l.Status("alice", "anon")
	if st.Used.Requests != 0 {
		t.Errorf("per-user counters did not reset at midnight: %+v", st.Used)
	}
	h := l.Health()
	if h.DaySpendUSD != 0 {
		t.Errorf("day spend = %v after rollover, want 0", h.DaySpendUSD)
	}
	if h.TotalSpendUSD < 0.05 {
		t.Errorf("lifetime spend = %v after rollover, want the 0.05 spent yesterday", h.TotalSpendUSD)
	}
	l.Close()

	// And it is still there for a process that starts tomorrow.
	fresh, done := open(t, dir, testLimits())
	defer done()
	if h := fresh.Health(); h.TotalSpendUSD < 0.05 {
		t.Errorf("lifetime spend = %v after restart, want 0.05", h.TotalSpendUSD)
	}
}

// A corrupt state file must not read as zero lifetime spend. That would
// hand out the whole credit pool a second time.
func TestCorruptStateFileIsFatalNotZero(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir, testLimits()); err == nil {
		t.Fatal("Open accepted a corrupt state file; the credit pool would silently reset")
	}
}

func TestRefundReturnsTheRequestAllowance(t *testing.T) {
	l, done := open(t, t.TempDir(), testLimits())
	defer done()

	l.Admit("alice", Admission{})
	l.Refund("alice", Admission{})
	if st := l.Status("alice", "anon"); st.Used.Requests != 0 {
		t.Errorf("requests after refund = %d, want 0", st.Used.Requests)
	}
	// Refunding more than was taken must not create allowance.
	l.Refund("alice", Admission{})
	l.Refund("alice", Admission{})
	if st := l.Status("alice", "anon"); st.Used.Requests != 0 {
		t.Errorf("over-refunding produced %d requests", st.Used.Requests)
	}
}

func TestRevocation(t *testing.T) {
	dir := t.TempDir()
	l, done := open(t, dir, testLimits())
	defer done()

	if err := l.Admit("spammer", Admission{}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "revoked.txt"),
		[]byte("# abuse\nspammer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := l.LoadRevocations(); err != nil {
		t.Fatal(err)
	}

	if got := reasonOf(t, l.Admit("spammer", Admission{})); got != ReasonRevoked {
		t.Errorf("reason = %q, want %q", got, ReasonRevoked)
	}
	if err := l.Admit("alice", Admission{}); err != nil {
		t.Errorf("revoking one subject blocked another: %v", err)
	}
}

// The journal is the record an operator reads. It must contain the
// counts and none of the content — `deepseek free` promises exactly that
// to the user before they enrol.
func TestJournalRecordsCountsAndNothingElse(t *testing.T) {
	dir := t.TempDir()
	l, _ := open(t, dir, testLimits())
	l.Admit("alice", Admission{})
	l.Charge("alice", "chat", "deepseek-v4-flash", 400, 120, 200, 0.002, 0, false)
	day := l.day
	l.Close()

	b, err := os.ReadFile(filepath.Join(dir, "journal-"+day+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	line := string(b)
	for _, want := range []string{`"subject":"alice"`, `"input_tokens":400`, `"cache_hit_tokens":120`, `"output_tokens":200`, `"endpoint":"chat"`} {
		if !strings.Contains(line, want) {
			t.Errorf("journal is missing %s: %s", want, line)
		}
	}
	for _, forbidden := range []string{"prompt", "message", "content", "ip", "header"} {
		if strings.Contains(strings.ToLower(line), forbidden) {
			t.Errorf("journal line contains %q, which it promises never to record: %s", forbidden, line)
		}
	}
}

func TestStatusDoesNotLeakServiceFinances(t *testing.T) {
	l, done := open(t, t.TempDir(), testLimits())
	defer done()
	l.Admit("alice", Admission{})
	l.Charge("alice", "chat", "deepseek-v4-flash", 10, 0, 10, 0.005, 0, false)

	st := l.Status("alice", "anon")
	if st.Used.SpentUSD == 0 {
		t.Error("a subject cannot see its own spend")
	}
	// Status is what goes over the wire to an anonymous caller. It must
	// not carry the service's budget or remaining credit: that tells an
	// attacker exactly how close they are to breaking it.
	if st.ResetsAt.IsZero() {
		t.Error("status has no reset horizon")
	}
}

// The reservation is what turns the budgets from horizons into ceilings:
// a request whose worst case does not fit is refused before it is
// forwarded, not billed after it overshoots.
func TestReservationIsACeiling(t *testing.T) {
	lim := testLimits() // $0.01/day
	lim.DailyRequests = 100
	l, done := open(t, t.TempDir(), lim)
	defer done()

	if got := reasonOf(t, l.Admit("a", Admission{ReserveUSD: 0.02})); got != ReasonDailyBudget {
		t.Fatalf("a $0.02 worst case fit under a $0.01 budget: %v", got)
	}
	if err := l.Admit("a", Admission{ReserveUSD: 0.005}); err != nil {
		t.Fatalf("a fitting reservation was refused: %v", err)
	}
	// A second request that fits the budget alone, but not alongside the
	// first one's reservation, must wait.
	if got := reasonOf(t, l.Admit("b", Admission{ReserveUSD: 0.006})); got != ReasonDailyBudget {
		t.Fatalf("overlapping reservations overshot the budget: %v", got)
	}
	// Settling the first request at its (small) real cost frees the room.
	l.Charge("a", "chat", "deepseek-v4-flash", 10, 0, 10, 0.001, 0.005, false)
	if err := l.Admit("b", Admission{ReserveUSD: 0.006}); err != nil {
		t.Fatalf("room was not released at Charge: %v", err)
	}
}

func TestRefundReleasesTheReservation(t *testing.T) {
	lim := testLimits()
	lim.DailyRequests = 100
	l, done := open(t, t.TempDir(), lim)
	defer done()

	if err := l.Admit("a", Admission{ReserveUSD: 0.009}); err != nil {
		t.Fatal(err)
	}
	l.Refund("a", Admission{ReserveUSD: 0.009})
	if err := l.Admit("b", Admission{ReserveUSD: 0.009}); err != nil {
		t.Fatalf("a refunded reservation still held the budget: %v", err)
	}
	l.Release(0.009)
	if err := l.Admit("c", Admission{ReserveUSD: 0.009}); err != nil {
		t.Fatalf("Release did not free the room: %v", err)
	}
}

// Spend that cannot be journalled is spend a restart refunds, so a
// broken journal must stop admissions rather than being shrugged off.
func TestJournalFailureFailsClosed(t *testing.T) {
	l, done := open(t, t.TempDir(), testLimits())
	defer done()

	if err := l.Admit("a", Admission{}); err != nil {
		t.Fatal(err)
	}

	// Break durability: close the fd out from under the ledger and make
	// the file unwritable so reopening cannot silently heal.
	path := l.journalPath(l.day)
	l.mu.Lock()
	l.journal.Close()
	l.mu.Unlock()
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}

	l.Charge("a", "chat", "deepseek-v4-flash", 10, 0, 10, 0.0001, 0, false)
	err := l.Admit("b", Admission{})
	if got := reasonOf(t, err); got != ReasonUnavailable {
		t.Fatalf("admissions continued with a dead journal: %v", err)
	}

	// And it heals without a restart once the disk does.
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := l.Admit("b", Admission{}); err != nil {
		t.Fatalf("the ledger did not recover after the journal came back: %v", err)
	}
}

// A corrupt line in the middle of a journal must cost us that line, not
// every line after it: dropping the tail would refund real spend.
func TestReplaySkipsACorruptLine(t *testing.T) {
	dir := t.TempDir()
	l, done := open(t, dir, testLimits())
	l.Admit("a", Admission{})
	l.Charge("a", "chat", "deepseek-v4-flash", 10, 0, 10, 0.002, 0, false)
	day := l.day
	done()

	f, err := os.OpenFile(filepath.Join(dir, "journal-"+day+".jsonl"), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"truncated`)
	f.WriteString("\n")
	f.Close()

	l2, done2 := open(t, dir, testLimits())
	l2.Admit("a", Admission{})
	l2.Charge("a", "chat", "deepseek-v4-flash", 10, 0, 10, 0.003, 0, false)
	done2()

	l3, done3 := open(t, dir, testLimits())
	defer done3()
	if got := l3.Health().DaySpendUSD; got < 0.0049 {
		t.Errorf("day spend replayed as $%.4f; the corrupt line ate the entries after it", got)
	}
}

// A search costs roughly ten times an ordinary turn, so it has its own
// ration. Two properties matter and neither is obvious: running out of
// searches must not touch the rest of the tier, and a search that never
// reached the model must give the ration back.
func TestSearchesAreRationedWithoutBlockingOrdinaryRequests(t *testing.T) {
	lim := testLimits()
	lim.DailyRequests = 100
	lim.DailySearches = 2
	l, done := open(t, t.TempDir(), lim)
	defer done()

	for i := 0; i < 2; i++ {
		if err := l.Admit("alice", Admission{Search: true}); err != nil {
			t.Fatalf("search %d refused: %v", i+1, err)
		}
	}

	err := l.Admit("alice", Admission{Search: true})
	if err == nil {
		t.Fatal("a third search was admitted against a ration of two")
	}
	if got := reasonOf(t, err); got != ReasonSearches {
		t.Errorf("reason = %q, want %q", got, ReasonSearches)
	}

	// The point of a separate ration: everything else still works.
	if err := l.Admit("alice", Admission{}); err != nil {
		t.Errorf("an ordinary request was refused because searches ran out: %v", err)
	}

	// And a refunded search is not a spent one.
	l.Refund("alice", Admission{Search: true})
	if err := l.Admit("alice", Admission{Search: true}); err != nil {
		t.Errorf("a refunded search ration was not returned: %v", err)
	}
}

func TestSearchRationIsPublishedWithTheOtherLimits(t *testing.T) {
	lim := testLimits()
	lim.DailySearches = 3
	l, done := open(t, t.TempDir(), lim)
	defer done()

	if got := l.Status("alice", "anon").Limits.Searches; got != 3 {
		t.Errorf("published search limit = %d, want 3 — a limit a caller cannot read is one they can only discover by hitting it", got)
	}
}
